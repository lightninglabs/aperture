package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

const (
	// DefaultInvoiceLookupTimeout is the default maximum time we wait for
	// an invoice update to arrive.
	DefaultInvoiceLookupTimeout = 3 * time.Second
)

// Authenticator is the generic interface for validating client headers and
// returning new challenge headers.
type Authenticator interface {
	// Accept returns whether or not the header successfully authenticates
	// the user to a given backend service.
	Accept(*http.Header, string) bool

	// FreshChallengeHeader returns a header containing a challenge for the
	// user to complete.
	FreshChallengeHeader(*http.Request, string, int64) (http.Header, error)
}

// Minter is an entity that is able to mint and verify LSATs for a set of
// services.
type Minter interface {
	// MintLSAT mints a new LSAT for the target services.
	MintLSAT(context.Context, ...lsat.Service) (*macaroon.Macaroon, string, error)

	// VerifyLSAT attempts to verify an LSAT with the given parameters.
	VerifyLSAT(context.Context, *mint.VerificationParams) error
}

// InvoiceChecker is an entity that is able to check the status of an invoice,
// particularly whether it's been paid or not.
type InvoiceChecker interface {
	// VerifyInvoiceStatus checks that an invoice identified by a payment
	// hash has the desired status. To make sure we don't fail while the
	// invoice update is still on its way, we try several times until either
	// the desired status is set or the given timeout is reached.
	VerifyInvoiceStatus(lntypes.Hash, lnrpc.Invoice_InvoiceState,
		time.Duration) error
}
