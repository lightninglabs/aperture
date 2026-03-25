package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/lightninglabs/aperture/l402"
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
	FreshChallengeHeader(string, int64) (http.Header, error)
}

// Minter is an entity that is able to mint and verify L402s for a set of
// services.
type Minter interface {
	// MintL402 mints a new L402 for the target services.
	MintL402(context.Context, ...l402.Service) (*macaroon.Macaroon, string, error)

	// VerifyL402 attempts to verify an L402 with the given parameters.
	VerifyL402(context.Context, *mint.VerificationParams) error
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

// ReceiptProvider is an optional interface that authenticators can implement to
// provide response headers (e.g., Payment-Receipt) for successfully
// authenticated requests. This is used by the MPP authenticator to add
// Payment-Receipt headers to proxied responses.
type ReceiptProvider interface {
	// ReceiptHeader returns any response headers that should be added to
	// the proxied response for a successfully authenticated request. The
	// request headers and service name are provided for context. Returns
	// nil if no receipt headers are needed.
	ReceiptHeader(*http.Header, string) http.Header
}

// SessionStore persists MPP session state for the session intent. Sessions
// track prepaid balances that are decremented as services are consumed.
type SessionStore interface {
	// CreateSession creates a new session with the given initial state.
	CreateSession(ctx context.Context, session *Session) error

	// GetSession returns the session with the given session ID.
	GetSession(ctx context.Context, sessionID string) (*Session, error)

	// UpdateSessionBalance atomically adds the given amount to the
	// session's deposit balance.
	UpdateSessionBalance(ctx context.Context, sessionID string,
		addSats int64) error

	// DeductSessionBalance atomically adds the given amount to the
	// session's spent counter. Returns an error if the deduction would
	// exceed the deposit balance.
	DeductSessionBalance(ctx context.Context, sessionID string,
		amount int64) error

	// CloseSession marks the session as closed. No further operations are
	// accepted on a closed session.
	CloseSession(ctx context.Context, sessionID string) error
}

// Session represents an MPP prepaid session. The session is identified by the
// payment hash of the deposit invoice.
type Session struct {
	// SessionID is the payment hash of the deposit invoice, encoded as a
	// lowercase hex string. Serves as the unique session identifier.
	SessionID string

	// PaymentHash is the raw 32-byte payment hash of the deposit invoice.
	PaymentHash lntypes.Hash

	// DepositSats is the total satoshis deposited into the session.
	// Increases with each successful top-up.
	DepositSats int64

	// SpentSats is the running total of satoshis charged against the
	// session.
	SpentSats int64

	// ReturnInvoice is the BOLT11 return invoice for refunds on close.
	ReturnInvoice string

	// Status is either "open" or "closed".
	Status string

	// CreatedAt is the time the session was created.
	CreatedAt time.Time

	// UpdatedAt is the time the session was last updated.
	UpdatedAt time.Time
}

// TransactionRecorder records payment transactions for admin dashboard
// tracking. Both L402 and MPP payments use this to populate the transactions
// table.
type TransactionRecorder interface {
	// RecordMPPTransaction records a pending MPP charge or session
	// payment. The authType distinguishes between "mpp_charge" and
	// "mpp_session".
	RecordMPPTransaction(ctx context.Context, paymentHash []byte,
		serviceName string, priceSats int64,
		authType string) error
}

// PaymentSender is an interface for sending Lightning payments. This is used
// by the session authenticator to refund unspent balance when a session is
// closed.
type PaymentSender interface {
	// SendPayment sends a payment to the given invoice with the specified
	// amount in satoshis. Returns the payment preimage hex on success.
	SendPayment(ctx context.Context, invoice string,
		amtSats int64) (string, error)
}
