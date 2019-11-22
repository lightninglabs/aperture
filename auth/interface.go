package auth

import (
	"net/http"

	"github.com/lightningnetwork/lnd/lntypes"
)

// Authenticator is the generic interface for validating client headers and
// returning new challenge headers.
type Authenticator interface {
	// Accept returns whether or not the header successfully authenticates
	// the user to a given backend service.
	Accept(*http.Header, string) bool

	// FreshChallengeHeader returns a header containing a challenge for the
	// user to complete.
	FreshChallengeHeader(*http.Request, string) (http.Header, error)
}

// Challenger is an interface for generating new payment challenges.
type Challenger interface {
	// NewChallenge creates a new LSAT payment challenge, returning a
	// payment request (invoice) and the corresponding payment hash.
	NewChallenge() (string, lntypes.Hash, error)
}
