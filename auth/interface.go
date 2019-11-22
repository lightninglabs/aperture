package auth

import (
	"context"
	"net/http"

	"github.com/lightninglabs/kirin/mint"
	"github.com/lightninglabs/loop/lsat"
	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
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

// Minter is an entity that is able to mint and verify LSATs for a set of
// services.
type Minter interface {
	// MintLSAT mints a new LSAT for the target services.
	MintLSAT(context.Context, ...lsat.Service) (*macaroon.Macaroon, string, error)

	// VerifyLSAT attempts to verify an LSAT with the given parameters.
	VerifyLSAT(context.Context, *mint.VerificationParams) error
}
