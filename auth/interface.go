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

// CreditOutcome says what a credit attempt did to a session's deposit. A
// settled Lightning invoice stays settled forever, so a preimage proves only
// that the payment happened at some point, never that it has not already been
// spent. The store is the only thing that knows the difference, and this is how
// it reports it.
type CreditOutcome uint8

const (
	// CreditApplied means the payment hash had never funded anything, so
	// the deposit grew by the full amount.
	CreditApplied CreditOutcome = iota

	// CreditReplayed means the payment hash had already funded this very
	// session, so nothing was added. This is what an honest client looks
	// like when its top-up response was lost and it resent the request:
	// the balance it is asking for is already there, and the right answer
	// is to let it proceed rather than charge it a second time.
	CreditReplayed

	// CreditForeign means the payment hash had already funded a different
	// session. Nothing was added, and no honest client does this: it is a
	// credential paid for one session being pointed at another.
	CreditForeign
)

// String returns a human readable name for the outcome.
func (c CreditOutcome) String() string {
	switch c {
	case CreditApplied:
		return "applied"

	case CreditReplayed:
		return "replayed"

	case CreditForeign:
		return "foreign"

	default:
		return "unknown"
	}
}

// SessionStore persists MPP session state for the session intent. Sessions
// track prepaid balances that are decremented as services are consumed.
type SessionStore interface {
	// CreateSession creates a new session with the given initial state,
	// claiming the deposit payment hash for it in the same atomic step.
	// The session is refused if that hash has already funded a session,
	// which is what stops one deposit payment from opening a session and
	// then being presented again as a top-up somewhere else.
	CreateSession(ctx context.Context, session *Session) error

	// GetSession returns the session with the given session ID.
	GetSession(ctx context.Context, sessionID string) (*Session, error)

	// CreditSession adds the given amount to the session's deposit if and
	// only if the given payment hash has never been credited before, and
	// reports which of those two happened. The claim on the payment hash
	// and the balance increment are one atomic step, so concurrent replays
	// of the same credential credit exactly once between them.
	//
	// This is deliberately the only way to grow a balance. A separate "has
	// this hash been seen" query would leave the once-only property resting
	// on every caller remembering to ask.
	CreditSession(ctx context.Context, sessionID string,
		paymentHash lntypes.Hash, addSats int64) (CreditOutcome, error)

	// DeductSessionBalance atomically adds the given amount to the
	// session's spent counter. Returns an error if the deduction would
	// exceed the deposit balance.
	DeductSessionBalance(ctx context.Context, sessionID string,
		amount int64) error

	// CloseSession marks the session as closed. No further operations are
	// accepted on a closed session.
	CloseSession(ctx context.Context, sessionID string) error

	// SettleSessionBalance atomically adjusts the session's spent counter
	// by a signed amount, clamped so the spend can neither go below zero
	// nor above the deposit, and returns the resulting spend. It is how a
	// request charged an estimate before its response existed is
	// reconciled against what it turned out to cost.
	SettleSessionBalance(ctx context.Context, sessionID string,
		deltaSats int64) (int64, error)

	// CloseSessionAndGetBalance atomically closes the session and returns
	// the remaining balance (deposit_sats - spent_sats). This prevents
	// the TOCTOU race where a concurrent bearer request could deduct
	// balance between a separate read and close.
	CloseSessionAndGetBalance(ctx context.Context,
		sessionID string) (int64, error)
}

// ChallengePrices carries the prices a fresh 402 challenge should quote. A
// single number no longer suffices, because the intents a challenge can carry
// ask genuinely different questions of the pricer: L402 and the MPP charge
// intent quote a whole one-shot purchase, which for a metered service is a
// token bundle, while the MPP session intent quotes the cost of one request
// drawn against a prepaid balance. Quoting a bundle price as a session's
// per-unit amount would make every bearer request cost as much as a bundle.
type ChallengePrices struct {
	// Charge is the price in satoshis of a single one-shot purchase. It is
	// what the L402 and MPP charge challenges quote, and it is the value
	// the plain FreshChallengeHeader receives.
	Charge int64

	// SessionUnit is the estimated price in satoshis of one request served
	// against a prepaid session. Zero means no session-aware price was
	// available, and the session challenge falls back to Charge.
	SessionUnit int64

	// SessionDeposit is the deposit in satoshis to ask for when opening a
	// session. Zero leaves the deposit to the authenticator's configured
	// deposit multiplier.
	SessionDeposit int64
}

// PricedChallenger is an optional interface an Authenticator implements when
// the challenge it mints depends on more than one price. Authenticators that
// do not implement it are driven through FreshChallengeHeader with the charge
// price, which is what they have always received.
type PricedChallenger interface {
	// FreshChallengeHeaderWithPrices returns a challenge header quoting the
	// given set of prices.
	FreshChallengeHeaderWithPrices(serviceName string,
		prices ChallengePrices) (http.Header, error)
}

// SessionSettler is an optional interface an Authenticator implements when it
// holds prepaid session balances that are drawn down per request.
//
// It exists because the two halves of metered session pricing sit on opposite
// sides of the authenticator's boundary. An Authenticator only ever sees
// request headers, so it cannot ask a pricer what the request in hand should
// cost, and it never sees a response at all, so it cannot learn what the
// request did cost. The proxy sees both, but the balance lives behind the
// authenticator's session store. This interface is the narrow seam between
// them: the proxy costs a completed request and hands the reconciliation back
// to whoever owns the balance.
type SessionSettler interface {
	// BearerSessionID returns the session a bearer credential in the header
	// draws against, along with the amount that credential's challenge
	// quoted and that the authenticator has already deducted. It reports
	// false for a header that is not a bearer credential the implementation
	// itself would accept.
	//
	// The implementation must re-verify the credential rather than trust
	// that authentication already passed. A request can carry credentials
	// for several schemes at once, and only one of them needs to have been
	// the one that authenticated it.
	BearerSessionID(ctx context.Context, header *http.Header) (
		sessionID string, chargedSats int64, ok bool)

	// SettleSessionRequest reconciles a request that was charged
	// chargedSats against the actualSats it turned out to cost.
	SettleSessionRequest(ctx context.Context, sessionID string,
		chargedSats, actualSats int64) error
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
