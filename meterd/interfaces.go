package meterd

import "time"

// Store tracks the prepaid token bundles the server meters against, keyed by
// the hex-encoded L402 token ID. The JSONStore implementation persists the
// balances to a single JSON file; a daemon embedding meterd can supply a
// database-backed implementation instead.
type Store interface {
	// Book records a fresh bundle for the given token ID. Booking is
	// idempotent: a token that already has a bundle is left untouched,
	// and the boolean return value reports whether a new bundle was
	// created.
	Book(tokenID, model string, tokens, priceSats int64) (bool, error)

	// Authorize marks the token's bundle as used and, when the request
	// is going to be allowed, reserves the given estimated per-request
	// cost against its balance to bound concurrent overdraw. It returns
	// the tokens available after existing reservations and whether the
	// token is known.
	Authorize(tokenID string, estimate int64) (int64, bool, error)

	// Get returns a copy of the token's bundle.
	Get(tokenID string) (Bundle, bool)

	// Debit subtracts the actual tokens consumed from the token's
	// balance, clamping at zero, and releases the given reservation
	// estimate taken at authorization time. It returns a copy of the
	// bundle after the debit and whether the token is known.
	Debit(tokenID string, tokens, releaseEstimate int64) (Bundle, bool,
		error)

	// ExpireStale removes bundles that were never used by an authorized
	// request within the given TTL and returns the number of removed
	// bundles.
	ExpireStale(ttl time.Duration) int

	// Flush drains any pending state to durable storage. It is safe to
	// call on an unchanged store.
	Flush() error
}

// A compile-time check that JSONStore satisfies Store.
var _ Store = (*JSONStore)(nil)
