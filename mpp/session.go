package mpp

// SessionAction enumerates the credential action types for the session intent.
type SessionAction string

const (
	// SessionActionOpen opens a new session with a deposit payment.
	SessionActionOpen SessionAction = "open"

	// SessionActionBearer authenticates a request against an existing
	// session without additional payment.
	SessionActionBearer SessionAction = "bearer"

	// SessionActionTopUp adds funds to an existing session via a new
	// deposit payment.
	SessionActionTopUp SessionAction = "topUp"

	// SessionActionClose terminates a session and triggers a refund of
	// unspent balance.
	SessionActionClose SessionAction = "close"
)

// SessionRequest is the decoded request field for method="lightning",
// intent="session" as defined in draft-lightning-session-00 Section 7.
type SessionRequest struct {
	// Amount is the cost per unit of service in base units (satoshis),
	// encoded as a decimal string.
	Amount string `json:"amount"`

	// Currency identifies the unit for Amount. Must be "sat".
	Currency string `json:"currency"`

	// Description is an optional human-readable description of the
	// service.
	Description string `json:"description,omitempty"`

	// UnitType is an optional human-readable label for the unit being
	// priced (e.g., "token", "chunk", "request").
	UnitType string `json:"unitType,omitempty"`

	// DepositInvoice is a BOLT11 invoice the client must pay to open or
	// top up the session. Required for open and topUp challenges.
	DepositInvoice string `json:"depositInvoice,omitempty"`

	// PaymentHash is the SHA-256 hash of the deposit invoice preimage, as
	// a lowercase hex string.
	PaymentHash string `json:"paymentHash"`

	// DepositAmount is the exact deposit amount in satoshis, as a decimal
	// string. When present, must equal the amount encoded in
	// DepositInvoice.
	DepositAmount string `json:"depositAmount,omitempty"`

	// IdleTimeout is the server's idle timeout policy for open sessions,
	// in seconds, as a decimal string.
	IdleTimeout string `json:"idleTimeout,omitempty"`
}

// SessionPayload is the credential payload for the Lightning session intent.
// The Action field discriminates the type of operation. Per
// draft-lightning-session-00 Section 8.
type SessionPayload struct {
	// Action is the session operation type: "open", "bearer", "topUp", or
	// "close".
	Action SessionAction `json:"action"`

	// Preimage is the hex-encoded payment preimage. Used for open, bearer,
	// and close actions. SHA-256(preimage) must equal the session's
	// paymentHash.
	Preimage string `json:"preimage,omitempty"`

	// SessionID is the paymentHash of the original deposit invoice,
	// identifying the session. Used for bearer, topUp, and close actions.
	SessionID string `json:"sessionId,omitempty"`

	// ReturnInvoice is a BOLT11 invoice with no encoded amount, created
	// by the client at session open. The server pays this invoice with the
	// unspent session balance on close. Used for open action only.
	ReturnInvoice string `json:"returnInvoice,omitempty"`

	// TopUpPreimage is the preimage of the top-up invoice. Used for topUp
	// action only. SHA-256(topUpPreimage) must equal the paymentHash of
	// the fresh invoice issued for this top-up.
	TopUpPreimage string `json:"topUpPreimage,omitempty"`
}

// SessionReceipt extends the base Receipt with session-specific fields per
// draft-lightning-session-00 Section 15.
type SessionReceipt struct {
	// Method is always "lightning".
	Method string `json:"method"`

	// Reference is the session ID (paymentHash).
	Reference string `json:"reference"`

	// Status is always "success".
	Status string `json:"status"`

	// Timestamp is the settlement time in RFC 3339 format.
	Timestamp string `json:"timestamp"`

	// RefundSats is the unspent balance that was refunded on close. Only
	// present for close actions.
	RefundSats int64 `json:"refundSats,omitempty"`

	// RefundStatus indicates the outcome of the refund attempt on close.
	// One of "succeeded", "failed", or "skipped". Only present for close
	// actions.
	RefundStatus string `json:"refundStatus,omitempty"`
}

// NeedTopUpEvent represents the SSE event data emitted when a streaming
// response exhausts the session balance per draft-lightning-session-00
// Section 13.1.
type NeedTopUpEvent struct {
	// SessionID is the session identifier (paymentHash of the deposit
	// invoice).
	SessionID string `json:"sessionId"`

	// BalanceSpent is the total satoshis spent from the current deposit at
	// the point of exhaustion.
	BalanceSpent int64 `json:"balanceSpent"`

	// BalanceRequired is the satoshis needed for the next unit of service.
	BalanceRequired int64 `json:"balanceRequired"`
}
