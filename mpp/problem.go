package mpp

import "encoding/json"

const (
	// ProblemBaseURI is the canonical base URI for Payment HTTP Auth
	// problem types per draft-httpauth-payment-00 Section 8.1.
	ProblemBaseURI = "https://paymentauth.org/problems/"

	// ProblemLightningBaseURI is the base URI for Lightning-specific
	// problem types.
	ProblemLightningBaseURI = ProblemBaseURI + "lightning/"

	// ProblemContentType is the MIME type for RFC 9457 Problem Details.
	ProblemContentType = "application/problem+json"
)

// Well-known problem type URIs per draft-httpauth-payment-00 Section 8.2.
const (
	ProblemPaymentRequired    = ProblemBaseURI + "payment-required"
	ProblemPaymentInsufficient = ProblemBaseURI + "payment-insufficient"
	ProblemPaymentExpired     = ProblemBaseURI + "payment-expired"
	ProblemVerificationFailed = ProblemBaseURI + "verification-failed"
	ProblemMethodUnsupported  = ProblemBaseURI + "method-unsupported"
	ProblemMalformedCredential = ProblemBaseURI + "malformed-credential"
	ProblemInvalidChallenge   = ProblemBaseURI + "invalid-challenge"
)

// Lightning-specific problem type URIs per draft-lightning-charge-00
// Section 11.
const (
	ProblemLightningMalformed   = ProblemLightningBaseURI + "malformed-credential"
	ProblemLightningUnknown     = ProblemLightningBaseURI + "unknown-challenge"
	ProblemLightningPreimage    = ProblemLightningBaseURI + "invalid-preimage"
	ProblemLightningExpired     = ProblemLightningBaseURI + "expired-invoice"
	ProblemLightningSessionNotFound = ProblemLightningBaseURI + "session-not-found"
	ProblemLightningSessionClosed   = ProblemLightningBaseURI + "session-closed"
	ProblemLightningInsufficient    = ProblemLightningBaseURI + "insufficient-balance"
	ProblemLightningChallengeExpired = ProblemLightningBaseURI + "challenge-expired"
	ProblemLightningReturnInvoice   = ProblemLightningBaseURI + "invalid-return-invoice"
)

// ProblemDetails represents an RFC 9457 Problem Details object for use in
// error responses.
type ProblemDetails struct {
	// Type is a URI reference that identifies the problem type.
	Type string `json:"type"`

	// Title is a short, human-readable summary of the problem.
	Title string `json:"title"`

	// Status is the HTTP status code.
	Status int `json:"status"`

	// Detail is a human-readable explanation specific to this occurrence.
	Detail string `json:"detail,omitempty"`

	// ChallengeID is the associated challenge identifier, if applicable.
	ChallengeID string `json:"challengeId,omitempty"`
}

// PaymentRequiredProblem returns the default Problem Details body for a 402
// Payment Required response.
func PaymentRequiredProblem() []byte {
	p := ProblemDetails{
		Type:   ProblemPaymentRequired,
		Title:  "Payment Required",
		Status: 402,
		Detail: "Payment required for access.",
	}
	data, _ := json.Marshal(p)
	return data
}
