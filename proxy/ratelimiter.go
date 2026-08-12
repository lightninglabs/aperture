package proxy

import (
	"bytes"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/netutil"
	"github.com/lightninglabs/neutrino/cache/lru"
	"golang.org/x/time/rate"
)

const (
	// DefaultMaxCacheSize is the default maximum number of rate limiter
	// entries to keep in the LRU cache.
	DefaultMaxCacheSize = 10_000
)

// limiterKey is a composite key for the rate limiter cache.
type limiterKey struct {
	// clientKey identifies the client (e.g., "ip:1.2.3.4" or "token:abc").
	clientKey string
	// ruleIndex identifies a rule within the service configuration. The
	// index, rather than PathRegexp, keeps rules with identical patterns
	// independent.
	ruleIndex int
}

// limiterEntry holds a rate.Limiter. Implements cache.Value interface.
type limiterEntry struct {
	limiter *rate.Limiter
}

// clientLock serializes admissions for one exact client key. refs includes the
// current holder and all waiters so the lock can be removed safely when the
// last user releases it.
type clientLock struct {
	mu   sync.Mutex
	refs int
}

// clientLockSet provides lifecycle-bounded per-client locks. The number of
// entries is bounded by concurrent admissions rather than historical client
// cardinality, and unrelated clients never block each other because of a hash
// collision.
type clientLockSet struct {
	mu    sync.Mutex
	locks map[string]*clientLock
}

// lock acquires and returns the lock for key. Callers must pass the returned
// pointer to unlock exactly once.
func (s *clientLockSet) lock(key string) *clientLock {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = make(map[string]*clientLock)
	}

	lock, ok := s.locks[key]
	if !ok {
		lock = &clientLock{}
		s.locks[key] = lock
	}
	lock.refs++
	s.mu.Unlock()

	lock.mu.Lock()

	return lock
}

// unlock releases a keyed lock after its final waiter leaves.
func (s *clientLockSet) unlock(key string, lock *clientLock) {
	// Unlock the client before dropping its reference. A new waiter that
	// arrives in between increments refs on this same lock, preventing it
	// from being removed while still in use.
	lock.mu.Unlock()

	s.mu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(s.locks, key)
	}
	s.mu.Unlock()
}

// ruleReservation records one rule's token reservation.
type ruleReservation struct {
	ruleIndex   int
	cfg         *RateLimitConfig
	reservation *rate.Reservation
}

// matchingRule identifies one configured rule that applies to a request.
type matchingRule struct {
	ruleIndex int
	cfg       *RateLimitConfig
}

// rateLimitAdmission is a successful, provisional multi-rule admission. The
// exact client's lock remains held until Commit or Cancel, making a later
// cancellation exact without blocking unrelated clients.
type rateLimitAdmission struct {
	now          time.Time
	reservations []ruleReservation
	clientLocks  *clientLockSet
	clientKey    string
	clientLock   *clientLock
	serviceName  string
	once         sync.Once
}

// Commit consumes the reserved tokens and records the allowed rule metrics.
func (a *rateLimitAdmission) Commit() {
	if a == nil {
		return
	}

	a.once.Do(func() {
		defer a.clientLocks.unlock(a.clientKey, a.clientLock)

		for _, rr := range a.reservations {
			rateLimitAllowed.WithLabelValues(
				a.serviceName, rr.cfg.PathRegexp,
			).Inc()
		}
	})
}

// Cancel refunds every rule reservation and releases the client lock.
func (a *rateLimitAdmission) Cancel() {
	if a == nil {
		return
	}

	a.once.Do(func() {
		defer a.clientLocks.unlock(a.clientKey, a.clientLock)

		for _, rr := range a.reservations {
			rr.reservation.CancelAt(a.now)
		}
	})
}

// Size implements cache.Value. Returns 1 so the LRU cache counts entries
// rather than bytes.
func (e *limiterEntry) Size() (uint64, error) {
	return 1, nil
}

// RateLimiter manages per-key rate limiters with LRU eviction.
type RateLimiter struct {
	// cacheMu protects the LRU cache which is not concurrency-safe.
	cacheMu sync.Mutex

	// clientLocks serialize admission across all rules for an exact client.
	// This makes multi-rule decisions atomic without allowing a slow
	// provisional admission to block a hash-colliding client.
	clientLocks clientLockSet

	// configs is the list of rate limit configurations for this limiter.
	configs []*RateLimitConfig

	// cache is the LRU cache of rate limiter entries.
	cache *lru.Cache[limiterKey, *limiterEntry]

	// maxSize is the maximum number of entries in the cache.
	maxSize int

	// serviceName is used for metrics labels.
	serviceName string
}

// RateLimiterOption is a functional option for configuring a RateLimiter.
type RateLimiterOption func(*RateLimiter)

// WithMaxCacheSize sets the maximum cache size.
func WithMaxCacheSize(size int) RateLimiterOption {
	return func(rl *RateLimiter) {
		rl.maxSize = size
	}
}

// NewRateLimiter creates a new RateLimiter with the given configurations.
func NewRateLimiter(serviceName string, configs []*RateLimitConfig,
	opts ...RateLimiterOption) *RateLimiter {

	rl := &RateLimiter{
		configs:     configs,
		maxSize:     DefaultMaxCacheSize,
		serviceName: serviceName,
	}

	for _, opt := range opts {
		opt(rl)
	}

	// Initialize the LRU cache with the configured max size.
	rl.cache = lru.NewCache[limiterKey, *limiterEntry](uint64(rl.maxSize))

	return rl
}

// Allow checks if a request should be allowed based on all matching rate
// limits. Returns (allowed, retryAfter) where retryAfter is the suggested
// duration to wait if denied.
func (rl *RateLimiter) Allow(r *http.Request, key string) (bool,
	time.Duration) {
	admission, allowed, retryAfter := rl.reserve(r, key)
	if !allowed {
		return false, retryAfter
	}

	admission.Commit()

	return true, 0
}

// reserve provisionally reserves one token from every matching rule. A
// successful admission must be finalized with Commit or Cancel. This internal
// two-phase form lets callers refund capacity when a later prerequisite fails.
func (rl *RateLimiter) reserve(r *http.Request, key string) (
	*rateLimitAdmission, bool, time.Duration) {

	path := r.URL.Path

	// Find matching rules before acquiring the exact-client lock. Requests
	// that do not match any rule are unrestricted and should not contend on
	// the global lock map or allocate a per-client lock.
	var matches []matchingRule
	for ruleIndex, cfg := range rl.configs {
		if !cfg.Matches(path) {
			continue
		}

		matches = append(matches, matchingRule{
			ruleIndex: ruleIndex,
			cfg:       cfg,
		})
	}
	if len(matches) == 0 {
		return nil, true, 0
	}

	clientLock := rl.clientLocks.lock(key)
	lockTransferred := false
	defer func() {
		if !lockTransferred {
			rl.clientLocks.unlock(key, clientLock)
		}
	}()

	now := time.Now()

	// Collect all matching configs and their reservations. We need to check
	// all rules before consuming any tokens, so that if any rule denies we
	// can cancel all reservations.
	reservations := make([]ruleReservation, 0, len(matches))

	for _, match := range matches {
		// Create a composite key for independent limiting per rule. The
		// rule index is intentionally part of the identity because
		// multiple limits with different time horizons can legitimately
		// use the same path pattern.
		cacheKey := limiterKey{
			clientKey: key,
			ruleIndex: match.ruleIndex,
		}

		limiter := rl.getOrCreateLimiter(cacheKey, match.cfg)
		reservation := limiter.ReserveN(now, 1)

		reservations = append(reservations, ruleReservation{
			ruleIndex:   match.ruleIndex,
			cfg:         match.cfg,
			reservation: reservation,
		})
	}

	// Check if all reservations can proceed immediately. If any rule
	// denies, we must cancel ALL reservations to avoid consuming tokens
	// unfairly.
	var maxWait time.Duration
	allAllowed := true

	for _, rr := range reservations {
		if !rr.reservation.OK() {
			// The request exceeds the limiter's burst. Service validation
			// prevents this for configured rules, but keep the direct
			// RateLimiter API fail closed.
			allAllowed = false
			maxWait = time.Second
			continue
		}

		delay := rr.reservation.DelayFrom(now)
		if delay > 0 {
			allAllowed = false
			if delay > maxWait {
				maxWait = delay
			}
		}
	}

	// If any rule denied, cancel all reservations and return denied.
	if !allAllowed {
		for _, rr := range reservations {
			rr.reservation.CancelAt(now)
			rateLimitDenied.WithLabelValues(
				rl.serviceName, rr.cfg.PathRegexp,
			).Inc()
		}

		return nil, false, maxWait
	}

	lockTransferred = true

	return &rateLimitAdmission{
		now:          now,
		reservations: reservations,
		clientLocks:  &rl.clientLocks,
		clientKey:    key,
		clientLock:   clientLock,
		serviceName:  rl.serviceName,
	}, true, 0
}

// getOrCreateLimiter retrieves an existing limiter or creates a new one.
func (rl *RateLimiter) getOrCreateLimiter(key limiterKey,
	cfg *RateLimitConfig) *rate.Limiter {

	rl.cacheMu.Lock()
	defer rl.cacheMu.Unlock()

	// Try to get existing entry from cache (also updates LRU order).
	if entry, err := rl.cache.Get(key); err == nil {
		return entry.limiter
	}

	// Create a new limiter.
	limiter := rate.NewLimiter(
		rate.Limit(cfg.Rate()), cfg.EffectiveBurst(),
	)

	entry := &limiterEntry{
		limiter: limiter,
	}

	// Put handles eviction automatically when cache is full.
	evicted, _ := rl.cache.Put(key, entry)
	if evicted {
		rateLimitEvictions.WithLabelValues(rl.serviceName).Inc()
	}

	rateLimitCacheSize.WithLabelValues(rl.serviceName).Set(
		float64(rl.cache.Len()),
	)

	return limiter
}

// Size returns the current number of entries in the cache.
func (rl *RateLimiter) Size() int {
	rl.cacheMu.Lock()
	defer rl.cacheMu.Unlock()

	return rl.cache.Len()
}

// ExtractRateLimitKey extracts the rate-limiting key from a request.
// For authenticated requests, it uses the L402 token ID. For unauthenticated
// requests, it falls back to the client IP address.
//
// IMPORTANT: The authenticated parameter should only be true if the L402 token
// has been validated by the authenticator. Using unvalidated L402 tokens as
// keys is a DoS vector since attackers can flood the cache with garbage tokens.
func ExtractRateLimitKey(r *http.Request, remoteIP net.IP,
	authenticated bool) string {

	// Only use L402 token ID if the request has been authenticated.
	// This prevents DoS attacks where garbage L402 tokens flood the cache.
	if authenticated {
		mac, _, err := l402.FromHeader(&r.Header)
		if err == nil && mac != nil {
			identifier, err := l402.DecodeIdentifier(
				bytes.NewBuffer(mac.Id()),
			)
			if err == nil {
				return "token:" + identifier.TokenID.String()
			}
		}
	}

	// Fall back to IP address for unauthenticated requests.
	// Mask the IP to group clients from the same network segment.
	return "ip:" + netutil.MaskIP(remoteIP).String()
}
