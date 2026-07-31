package pricer

import (
	"context"
	"net/http"
)

// SessionQuote is the pricer's answer for a request that will be, or was,
// served against a prepaid MPP session balance.
type SessionQuote struct {
	// UnitPriceSats is the estimated cost in satoshis of serving one
	// request of this shape. It is the per-unit amount a session challenge
	// advertises and the amount deducted from the session balance before
	// the request is proxied.
	UnitPriceSats int64

	// DepositSats is the deposit in satoshis to ask for when opening a
	// fresh session. Zero leaves the deposit to aperture's configured
	// deposit multiplier.
	DepositSats int64
}

// SessionUsage describes a response that was served against a session balance,
// as reported to the pricer once the response has completed.
type SessionUsage struct {
	// SessionID is the session the request drew against.
	SessionID string

	// Path is the URL path of the request.
	Path string

	// ServiceName is the name of the aperture service.
	ServiceName string

	// HTTPStatus is the status code the backend responded with.
	HTTPStatus int

	// ContentType is the Content-Type of the backend response.
	ContentType string

	// ContentEncoding is the Content-Encoding the backend response
	// declared. A non-identity value means the captured tail is compressed
	// and could not be costed.
	ContentEncoding string

	// Complete is whether the response body was read to completion.
	Complete bool

	// ResponseTail is a capped tail of the response body. For SSE streams
	// this contains the trailing chunks, including any final usage object.
	ResponseTail []byte

	// EstimateSats is the amount already deducted from the session balance
	// for this request, so the pricer can see what it is reconciling
	// against.
	EstimateSats int64

	// RequestText is the serialized HTTP request the response answered. A
	// session books no model the way an L402 token bundle does, so the
	// pricer needs the request back to know which model's rates to cost
	// the response at.
	RequestText string
}

// SessionPricer is an optional interface a Pricer can implement to price
// individual requests drawn against a prepaid MPP session balance.
//
// It is deliberately separate from MeteredPricer even though both cost the
// same responses, because the two answer different questions about different
// money. MeteredPricer sells a token bundle and holds the buyer's balance
// itself, so its AuthorizeRequest is an admission decision and its ReportUsage
// is a debit. A session's balance lives in aperture's own session store,
// because the deposit is a Lightning payment aperture received and will refund
// on close. So here the pricer only quotes and costs: aperture holds the funds
// and moves them.
type SessionPricer interface {
	Pricer

	// QuoteSession returns the estimated cost of serving the given request
	// against a session. The session ID is empty when the quote is for a
	// fresh challenge, which is minted before any session exists.
	QuoteSession(ctx context.Context, req *http.Request, sessionID string,
		serviceName string) (*SessionQuote, error)

	// SettleSession costs a completed response and returns what the
	// request should actually have cost in satoshis. Aperture reconciles
	// that against the estimate it already deducted.
	SettleSession(ctx context.Context, usage *SessionUsage) (int64, error)
}
