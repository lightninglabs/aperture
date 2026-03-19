package mpp

// ChargeRequest is the decoded request field for method="lightning",
// intent="charge" as defined in draft-lightning-charge-00 Section 7.
type ChargeRequest struct {
	// Amount is the invoice amount in base units (satoshis), encoded as a
	// decimal string.
	Amount string `json:"amount"`

	// Currency identifies the unit for Amount. Must be "sat" for
	// Lightning.
	Currency string `json:"currency"`

	// Description is an optional human-readable memo describing the
	// resource or service being paid for.
	Description string `json:"description,omitempty"`

	// Recipient is an optional payment recipient in method-native format.
	Recipient string `json:"recipient,omitempty"`

	// ExternalID is an optional merchant reference (e.g., order ID).
	ExternalID string `json:"externalId,omitempty"`

	// MethodDetails contains Lightning-specific fields nested under
	// methodDetails in the request JSON.
	MethodDetails ChargeMethodDetails `json:"methodDetails"`
}

// ChargeMethodDetails contains the Lightning-specific fields for a charge
// request per draft-lightning-charge-00 Section 7.2.
type ChargeMethodDetails struct {
	// Invoice is the full BOLT11-encoded payment request string. This
	// field is authoritative; all other payment parameters are derived
	// from it.
	Invoice string `json:"invoice"`

	// PaymentHash is an optional convenience field containing the payment
	// hash embedded in the invoice, as a lowercase hex-encoded string.
	PaymentHash string `json:"paymentHash,omitempty"`

	// Network identifies the Lightning Network the invoice is issued on.
	// Must be one of "mainnet", "regtest", or "signet". Defaults to
	// "mainnet" if omitted.
	Network string `json:"network,omitempty"`
}

// ChargePayload is the credential payload for the Lightning charge intent per
// draft-lightning-charge-00 Section 8.
type ChargePayload struct {
	// Preimage is the 32-byte payment preimage revealed upon successful
	// HTLC settlement, encoded as a lowercase hex string (64 characters).
	Preimage string `json:"preimage"`
}

// ChargeReceipt extends the base Receipt with Lightning charge-specific fields
// per draft-lightning-charge-00 Section 10.2.
type ChargeReceipt struct {
	// Method is always "lightning".
	Method string `json:"method"`

	// ChallengeID is the challenge identifier for audit correlation.
	ChallengeID string `json:"challengeId"`

	// Reference is the payment hash as a lowercase hex string.
	Reference string `json:"reference"`

	// Status is always "success".
	Status string `json:"status"`

	// Timestamp is the settlement time in RFC 3339 format.
	Timestamp string `json:"timestamp"`
}
