package pricer

import (
	"context"
	"net/http"
)

// AuthorizeResult is the pricer's decision for an authenticated request to a
// metered service.
type AuthorizeResult struct {
	// Allowed is whether the request may proceed to the backend.
	Allowed bool

	// PriceSats is the price in satoshis for the fresh challenge that
	// should be minted when the request is not allowed. A value of zero
	// means the price should be looked up through GetPrice instead.
	PriceSats int64

	// Reason optionally describes why the request was denied.
	Reason string

	// ReservedEstimate is the per-request token estimate the pricer
	// reserved against the token's balance when allowing the request.
	// It is echoed back in the matching usage report so the pricer can
	// release the exact reservation it took.
	ReservedEstimate int64
}

// Usage describes the outcome of a proxied request to a metered service, as
// reported to the pricer once the response has completed.
type Usage struct {
	// TokenID is the hex-encoded L402 token ID the request authenticated
	// with.
	TokenID string

	// Path is the URL path of the request.
	Path string

	// ServiceName is the name of the aperture service.
	ServiceName string

	// HTTPStatus is the status code the backend responded with.
	HTTPStatus int

	// ContentType is the Content-Type of the backend response.
	ContentType string

	// ContentEncoding is the Content-Encoding the backend response
	// declared. Metered requests strip the client's Accept-Encoding so the
	// observed tail is plaintext; a non-identity value here signals that
	// the observer captured bytes it could not decode.
	ContentEncoding string

	// Complete is whether the response body was read to completion.
	Complete bool

	// ResponseTail is a capped tail of the response body. For SSE streams
	// this contains the trailing chunks, including any final usage object.
	ResponseTail []byte

	// ReservedEstimate is the ReservedEstimate value of the authorization
	// that admitted this request, echoed back verbatim so the pricer can
	// release the exact reservation it took. Zero when the authorization
	// carried none.
	ReservedEstimate int64
}

// MeteredPricer is an optional interface a Pricer can implement to support
// metered draw-down against prepaid L402 tokens. Aperture consults the
// metered pricer on every authenticated request so the pricer can decide when
// a token's purchased balance is exhausted and a fresh challenge is due, and
// reports response usage back so the pricer can perform the cost analysis and
// debit the balance.
type MeteredPricer interface {
	Pricer

	// ChallengeMinted notifies the pricer that a fresh challenge macaroon
	// with the given token ID was minted at the given price.
	ChallengeMinted(ctx context.Context, req *http.Request, tokenID string,
		serviceName string, priceSats int64) error

	// AuthorizeRequest decides whether an authenticated request may
	// proceed, based on the token's remaining balance.
	AuthorizeRequest(ctx context.Context, req *http.Request,
		tokenID string, serviceName string) (*AuthorizeResult, error)

	// ReportUsage reports the outcome of a proxied request so the pricer
	// can debit the token's balance.
	ReportUsage(ctx context.Context, usage *Usage) error
}
