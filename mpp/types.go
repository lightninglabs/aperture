package mpp

import "encoding/json"

const (
	// AuthScheme is the HTTP authentication scheme name for the Payment
	// protocol as defined in draft-httpauth-payment-00.
	AuthScheme = "Payment"

	// MethodLightning is the payment method identifier for Lightning
	// Network payments.
	MethodLightning = "lightning"

	// IntentCharge is the intent identifier for one-time charge payments
	// as defined in draft-lightning-charge-00.
	IntentCharge = "charge"

	// IntentSession is the intent identifier for prepaid session payments
	// as defined in draft-lightning-session-00.
	IntentSession = "session"

	// CurrencySat is the currency identifier for satoshis, the base unit
	// used for Lightning/Bitcoin amounts.
	CurrencySat = "sat"

	// HeaderPaymentReceipt is the HTTP header field name for payment
	// receipts returned on successful payment verification.
	HeaderPaymentReceipt = "Payment-Receipt"

	// ReceiptStatusSuccess is the only valid receipt status value. Receipts
	// are only issued on successful payment responses.
	ReceiptStatusSuccess = "success"
)

// ChallengeParams represents the auth-params sent in the WWW-Authenticate:
// Payment header per draft-httpauth-payment-00 Section 5.1.
type ChallengeParams struct {
	// ID is the unique challenge identifier. Servers bind this value to
	// the challenge parameters via HMAC-SHA256 to enable stateless
	// verification.
	ID string

	// Realm is the protection space identifier per RFC 9110.
	Realm string

	// Method is the payment method identifier (e.g., "lightning").
	Method string

	// Intent is the payment intent type (e.g., "charge", "session").
	Intent string

	// Request is the base64url-encoded JCS-serialized JSON containing
	// payment-method-specific data needed to complete payment.
	Request string

	// Expires is an optional RFC 3339 timestamp indicating when this
	// challenge expires.
	Expires string

	// Digest is an optional content digest of the request body, formatted
	// per RFC 9530.
	Digest string

	// Description is an optional human-readable description of the
	// resource or payment purpose.
	Description string

	// Opaque is optional base64url-encoded JCS-serialized JSON containing
	// server-defined correlation data.
	Opaque string
}

// ChallengeEcho is the challenge object echoed back in the credential. The
// client returns all challenge parameters unchanged so the server can verify
// the binding.
type ChallengeEcho struct {
	// ID is the challenge identifier from the WWW-Authenticate header.
	ID string `json:"id"`

	// Realm is the protection space from the challenge.
	Realm string `json:"realm"`

	// Method is the payment method identifier from the challenge.
	Method string `json:"method"`

	// Intent is the payment intent type from the challenge.
	Intent string `json:"intent"`

	// Request is the base64url-encoded payment request from the challenge.
	Request string `json:"request"`

	// Expires is the challenge expiration timestamp, if present in the
	// original challenge.
	Expires string `json:"expires,omitempty"`

	// Description is the human-readable description, if present in the
	// original challenge.
	Description string `json:"description,omitempty"`

	// Opaque is the server correlation data, if present in the original
	// challenge.
	Opaque string `json:"opaque,omitempty"`

	// Digest is the content digest, if present in the original challenge.
	Digest string `json:"digest,omitempty"`
}

// Credential is the decoded Authorization: Payment token sent by the client.
// It contains the echoed challenge parameters and the payment-method-specific
// payload proving payment.
type Credential struct {
	// Challenge contains the echoed challenge parameters from the original
	// WWW-Authenticate header.
	Challenge ChallengeEcho `json:"challenge"`

	// Source is an optional payer identifier. The recommended format is a
	// DID per W3C-DID.
	Source string `json:"source,omitempty"`

	// Payload contains the payment-method-specific proof of payment. The
	// structure depends on the method and intent. We use json.RawMessage
	// to defer parsing until the method/intent is known.
	Payload json.RawMessage `json:"payload"`
}

// Receipt is the decoded Payment-Receipt header returned by the server on
// successful payment verification per draft-httpauth-payment-00 Section 5.3.
type Receipt struct {
	// Status is always "success". Receipts are only issued on successful
	// payment responses.
	Status string `json:"status"`

	// Method is the payment method used (e.g., "lightning").
	Method string `json:"method"`

	// Timestamp is the RFC 3339 settlement timestamp.
	Timestamp string `json:"timestamp"`

	// Reference is a method-specific reference (e.g., payment hash hex
	// for Lightning).
	Reference string `json:"reference"`

	// ChallengeID is the challenge identifier for audit and traceability.
	ChallengeID string `json:"challengeId,omitempty"`
}

// ToChallengeParams converts a ChallengeEcho back to ChallengeParams for HMAC
// verification. This is used when the server receives a credential and needs
// to verify the challenge binding.
func (e *ChallengeEcho) ToChallengeParams() *ChallengeParams {
	return &ChallengeParams{
		ID:          e.ID,
		Realm:       e.Realm,
		Method:      e.Method,
		Intent:      e.Intent,
		Request:     e.Request,
		Expires:     e.Expires,
		Description: e.Description,
		Opaque:      e.Opaque,
		Digest:      e.Digest,
	}
}
