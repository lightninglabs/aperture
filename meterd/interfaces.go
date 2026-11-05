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

	// Authorize marks the token's bundle as used and atomically decides,
	// by AdmitReservation, whether a request reserving the estimate is
	// admitted, reserving it only if so. It returns the outcome and
	// whether the token is known.
	Authorize(tokenID string, estimate int64) (Authorization, bool, error)

	// Get returns a copy of the token's bundle.
	Get(tokenID string) (Bundle, bool)

	// Debit subtracts the actual tokens consumed from the token's
	// balance, clamping at zero, and releases the reservation of the
	// given estimate taken at authorization time, or nothing if none
	// matches. It returns a copy of the bundle after the debit and whether
	// the token is known.
	Debit(tokenID string, tokens, releaseEstimate int64) (Bundle, bool,
		error)

	// ExpireStale removes bundles that were never used by an authorized
	// request within the given TTL and returns the number of removed
	// bundles. It should also drop reservations held past their TTL.
	ExpireStale(ttl time.Duration) int

	// Flush drains any pending state to durable storage. It is safe to
	// call on an unchanged store.
	Flush() error
}

// ReservationExpirer is an optional interface a Store implements to report
// that it drops reservations whose usage report never arrives. Only then can
// a funded bundle whose balance is all reserved be told to retry later, since
// without expiry a leaked reservation would never clear and the buyer would
// retry forever. A store that does not implement it keeps the 402 quote.
type ReservationExpirer interface {
	// ReservationsExpire reports whether leaked reservations are
	// eventually dropped.
	ReservationsExpire() bool
}

// Authorization is the outcome of a Store.Authorize call.
type Authorization struct {
	// Remaining is the bundle's unspent balance at decision time.
	Remaining int64

	// Available is Remaining minus other in-flight reservations.
	Available int64

	// Admitted is whether the request may proceed, and so whether its
	// estimate was reserved.
	Admitted bool
}

// AdmitReservation reports whether a request reserving estimate tokens may
// proceed. It is admitted when the balance left after in-flight reservations
// covers it, or when nothing else is in flight and any balance is left, so a
// remainder is not stranded. Stores apply it under their own lock.
//
// The estimate only bounds a request's cost when it has a max_tokens the
// price server could read.
func AdmitReservation(remaining, reserved, estimate int64) bool {
	if remaining <= 0 {
		return false
	}

	if reserved <= 0 {
		return true
	}

	return remaining-reserved >= estimate
}

// Compile-time checks that JSONStore satisfies Store and reports whether its
// reservations expire.
var (
	_ Store              = (*JSONStore)(nil)
	_ ReservationExpirer = (*JSONStore)(nil)
)
