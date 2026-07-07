package meterd

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// bundle tracks the prepaid token balance purchased with a single L402
// payment.
type Bundle struct {
	// Model is the configured model the bundle was booked for.
	Model string `json:"model"`

	// PriceSats is the price in satoshis the bundle was quoted at.
	PriceSats int64 `json:"price_sats"`

	// RemainingTokens is the number of unspent LLM tokens.
	RemainingTokens int64 `json:"remaining_tokens"`

	// CreatedAt is when the bundle was booked.
	CreatedAt time.Time `json:"created_at"`

	// Authorized is whether the bundle was ever used by an authorized
	// request. Bundles that are never authorized are expired by the
	// janitor, since their challenge was most likely never paid.
	Authorized bool `json:"authorized"`

	// Reserved is the number of tokens currently reserved by in-flight
	// requests that have been authorized but not yet reported. It is an
	// estimate that bounds concurrent overdraw, and is not persisted since
	// it only tracks live requests.
	Reserved int64 `json:"-"`
}

// storeState is the JSON serialization of the store.
type storeState struct {
	Bundles map[string]*Bundle `json:"bundles"`
}

// JSONStoreConfig holds the tunables that govern persistence coalescing and the
// bound on un-paid bundles.
type JSONStoreConfig struct {
	// StatePath is the path to the JSON file bundle balances are persisted
	// to. Persistence is disabled when empty.
	StatePath string

	// MaxUnauthorized is the maximum number of never-authorized (un-paid)
	// bundles retained at once. When a fresh booking would exceed the cap,
	// the oldest un-authorized bundle is evicted. A non-positive value
	// disables the bound.
	MaxUnauthorized int

	// MinEvictionAge is the age below which a never-authorized bundle is
	// immune to count-based eviction. Payment happens at the proxy, so a
	// just-paid bundle is indistinguishable from mint spam until its
	// first authorized request; the age floor keeps a spam burst from
	// evicting it in that window. The janitor still reaps bundles older
	// than the TTL, so with the floor set to the TTL the worst-case
	// retained state under spam is the mint rate times the TTL. A
	// non-positive value disables the floor.
	MinEvictionAge time.Duration
}

// store keeps the bundle balances, keyed by the hex-encoded L402 token ID, and
// optionally persists them to a JSON file. Persistence is coalesced: mutations
// set a dirty flag that a periodic flush and shutdown drain to disk, rather
// than rewriting the whole file synchronously on every change.
type JSONStore struct {
	mtx sync.Mutex

	bundles map[string]*Bundle

	cfg JSONStoreConfig

	// dirty is set when in-memory state has diverged from the persisted
	// file and a flush is pending.
	dirty bool
}

// newStore creates a store, loading existing state from the configured state
// path when the file exists. Persistence is disabled when the path is empty.
func newStore(statePath string) (*JSONStore, error) {
	return NewJSONStore(JSONStoreConfig{StatePath: statePath})
}

// NewJSONStore creates a store from the given configuration, loading any
// persisted bundle state.
func NewJSONStore(cfg JSONStoreConfig) (*JSONStore, error) {
	s := &JSONStore{
		bundles: make(map[string]*Bundle),
		cfg:     cfg,
	}
	if cfg.StatePath == "" {
		return s, nil
	}

	b, err := os.ReadFile(cfg.StatePath)
	switch {
	case err == nil:
		var state storeState
		if err := json.Unmarshal(b, &state); err != nil {
			return nil, fmt.Errorf("unable to parse state file "+
				"%s: %w", cfg.StatePath, err)
		}

		if state.Bundles != nil {
			s.bundles = state.Bundles
		}

	// A missing state file simply means a fresh start.
	case os.IsNotExist(err):

	default:
		return nil, fmt.Errorf("unable to read state file %s: %w",
			cfg.StatePath, err)
	}

	return s, nil
}

// persistLocked writes the current state to the state file and clears the
// dirty flag. The caller must hold the mutex. It is a no-op when persistence
// is disabled or no state is pending.
func (s *JSONStore) persistLocked() error {
	if s.cfg.StatePath == "" {
		s.dirty = false
		return nil
	}

	b, err := json.MarshalIndent(&storeState{Bundles: s.bundles}, "", "\t")
	if err != nil {
		return err
	}

	// Write to a temporary file and rename it into place, so a crash
	// mid-write cannot corrupt the previous state.
	tmpPath := s.cfg.StatePath + ".tmp"
	if err := os.WriteFile(tmpPath, b, 0644); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, s.cfg.StatePath); err != nil {
		return err
	}

	s.dirty = false

	return nil
}

// Flush persists the current state if it has diverged from the file since the
// last flush. It is safe to call on an unchanged store.
func (s *JSONStore) Flush() error {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if !s.dirty {
		return nil
	}

	return s.persistLocked()
}

// evictOldestUnauthorizedLocked removes never-authorized bundles until at most
// maxUnauthorized of them remain, evicting the oldest first. Bundles younger
// than the configured eviction age floor are immune, so a mint-spam burst
// cannot push out a just-paid bundle before its first authorized request. The
// caller must hold the mutex. It returns the number of bundles evicted.
func (s *JSONStore) evictOldestUnauthorizedLocked() int {
	cap := s.cfg.MaxUnauthorized
	if cap <= 0 {
		return 0
	}

	var protectCutoff time.Time
	if s.cfg.MinEvictionAge > 0 {
		protectCutoff = time.Now().Add(-s.cfg.MinEvictionAge)
	}

	// Count all un-authorized bundles against the cap, but only the ones
	// past the age floor are candidates for eviction.
	type entry struct {
		tokenID string
		created time.Time
	}
	var total int
	unauthorized := make([]entry, 0, len(s.bundles))
	for tokenID, b := range s.bundles {
		if b.Authorized {
			continue
		}
		total++

		if !protectCutoff.IsZero() &&
			b.CreatedAt.After(protectCutoff) {

			continue
		}

		unauthorized = append(unauthorized, entry{
			tokenID: tokenID,
			created: b.CreatedAt,
		})
	}

	excess := total - cap
	if excess <= 0 {
		return 0
	}
	if excess > len(unauthorized) {
		excess = len(unauthorized)
	}

	// Sort oldest first, so the oldest un-paid bundles are evicted.
	for i := 1; i < len(unauthorized); i++ {
		for j := i; j > 0 &&
			unauthorized[j].created.Before(
				unauthorized[j-1].created,
			); j-- {

			unauthorized[j], unauthorized[j-1] =
				unauthorized[j-1], unauthorized[j]
		}
	}

	var evicted int
	for i := 0; i < excess; i++ {
		delete(s.bundles, unauthorized[i].tokenID)
		evicted++
	}

	return evicted
}

// Book records a fresh bundle for the given token ID. Booking is idempotent: a
// token that already has a bundle is left untouched. The boolean return value
// indicates whether a new bundle was created. Booking only marks the store
// dirty rather than persisting synchronously, so a burst of un-paid challenge
// mints cannot amplify into a full-file rewrite per request.
func (s *JSONStore) Book(tokenID, model string, tokens, priceSats int64) (bool,
	error) {

	s.mtx.Lock()
	defer s.mtx.Unlock()

	if _, ok := s.bundles[tokenID]; ok {
		return false, nil
	}

	s.bundles[tokenID] = &Bundle{
		Model:           model,
		PriceSats:       priceSats,
		RemainingTokens: tokens,
		CreatedAt:       time.Now().UTC(),
	}

	// Bound the number of never-authorized bundles so unauthenticated
	// challenge-mint spam cannot grow the map without limit.
	if evicted := s.evictOldestUnauthorizedLocked(); evicted > 0 {
		log.Debugf("Evicted %d oldest un-authorized bundle(s) over "+
			"the cap of %d", evicted, s.cfg.MaxUnauthorized)
	}

	s.dirty = true

	return true, nil
}

// Authorize marks the token's bundle as used and, when the request is going to
// be allowed, reserves the given estimated per-request cost against its balance
// to bound concurrent overdraw. It returns the tokens available after existing
// reservations (remaining minus the total reserved) and whether the token is
// known. The estimate is not applied to the returned availability, so a single
// request against a bundle with any positive balance is still authorized.
func (s *JSONStore) Authorize(tokenID string, estimate int64) (int64, bool, error) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	b, ok := s.bundles[tokenID]
	if !ok {
		return 0, false, nil
	}

	// The available balance already nets out the tokens reserved by other
	// in-flight requests, so N concurrent requests on a near-empty bundle
	// cannot all authorize.
	available := b.RemainingTokens - b.Reserved

	if !b.Authorized {
		b.Authorized = true
		s.dirty = true
	}

	// Reserve the estimate only when the request is going to be allowed, so
	// a denied request does not tie up balance.
	if available > 0 && estimate > 0 {
		b.Reserved += estimate
	}

	return available, true, nil
}

// Get returns a copy of the token's bundle.
func (s *JSONStore) Get(tokenID string) (Bundle, bool) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	b, ok := s.bundles[tokenID]
	if !ok {
		return Bundle{}, false
	}

	return *b, true
}

// Debit subtracts the actual tokens consumed from the token's balance,
// clamping at zero, and releases the given reservation estimate taken at
// authorization time. It returns a copy of the bundle after the debit and
// whether the token is known. A real debit is flushed to disk immediately,
// since it moves money.
//
// The proto does not correlate a report with the authorization that preceded
// it, so the release is approximate: it subtracts the estimate the caller
// passes, clamped at zero, rather than the exact amount this request reserved.
func (s *JSONStore) Debit(tokenID string, tokens, releaseEstimate int64) (Bundle,
	bool, error) {

	s.mtx.Lock()
	defer s.mtx.Unlock()

	b, ok := s.bundles[tokenID]
	if !ok {
		return Bundle{}, false, nil
	}

	// Release the reservation taken on authorization.
	if releaseEstimate > 0 {
		b.Reserved -= releaseEstimate
		if b.Reserved < 0 {
			b.Reserved = 0
		}
	}

	var err error
	if tokens > 0 {
		b.RemainingTokens -= tokens
		if b.RemainingTokens < 0 {
			b.RemainingTokens = 0
		}

		s.dirty = true

		// A debit moves money, so it is flushed immediately rather than
		// left for the periodic flush.
		err = s.persistLocked()
	}

	return *b, true, err
}

// ExpireStale removes bundles that were never used by an authorized request
// within the given TTL and returns the number of removed bundles.
func (s *JSONStore) ExpireStale(ttl time.Duration) int {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	cutoff := time.Now().Add(-ttl)

	var removed int
	for tokenID, b := range s.bundles {
		if !b.Authorized && b.CreatedAt.Before(cutoff) {
			delete(s.bundles, tokenID)
			removed++
		}
	}

	if removed > 0 {
		s.dirty = true
		if err := s.persistLocked(); err != nil {
			log.Errorf("Unable to persist state after expiring "+
				"stale bundles: %v", err)
		}
	}

	return removed
}
