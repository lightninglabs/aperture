package mpp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSessionRequestEncodeDecode verifies round-trip encoding of session
// request objects via the EncodeRequest/DecodeRequest helpers.
func TestSessionRequestEncodeDecode(t *testing.T) {
	req := &SessionRequest{
		Amount:         "2",
		Currency:       CurrencySat,
		Description:    "LLM token stream",
		UnitType:       "token",
		DepositInvoice: "lnbcrt1p5mzfsa...",
		PaymentHash:    "7f3a1b2c4d5e6f",
		DepositAmount:  "300",
		IdleTimeout:    "300",
	}

	encoded, err := EncodeRequest(req)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	var decoded SessionRequest
	err = DecodeRequest(encoded, &decoded)
	require.NoError(t, err)
	require.Equal(t, req.Amount, decoded.Amount)
	require.Equal(t, req.Currency, decoded.Currency)
	require.Equal(t, req.Description, decoded.Description)
	require.Equal(t, req.UnitType, decoded.UnitType)
	require.Equal(t, req.DepositInvoice, decoded.DepositInvoice)
	require.Equal(t, req.PaymentHash, decoded.PaymentHash)
	require.Equal(t, req.DepositAmount, decoded.DepositAmount)
	require.Equal(t, req.IdleTimeout, decoded.IdleTimeout)
}

// TestSessionPayloadOpenAction verifies encoding/decoding of the open action
// payload.
func TestSessionPayloadOpenAction(t *testing.T) {
	payload := &SessionPayload{
		Action:        SessionActionOpen,
		Preimage:      "a3f1e2d4b5c6a7e8",
		ReturnInvoice: "lnbcrt1p5abc...",
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded SessionPayload
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, SessionActionOpen, decoded.Action)
	require.Equal(t, payload.Preimage, decoded.Preimage)
	require.Equal(t, payload.ReturnInvoice, decoded.ReturnInvoice)
	require.Empty(t, decoded.SessionID)
	require.Empty(t, decoded.TopUpPreimage)
}

// TestSessionPayloadBearerAction verifies encoding/decoding of the bearer
// action payload.
func TestSessionPayloadBearerAction(t *testing.T) {
	payload := &SessionPayload{
		Action:    SessionActionBearer,
		SessionID: "7f3a1b2c4d5e6f",
		Preimage:  "a3f1e2d4b5c6a7e8",
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded SessionPayload
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, SessionActionBearer, decoded.Action)
	require.Equal(t, payload.SessionID, decoded.SessionID)
	require.Equal(t, payload.Preimage, decoded.Preimage)
}

// TestSessionPayloadTopUpAction verifies encoding/decoding of the topUp
// action payload.
func TestSessionPayloadTopUpAction(t *testing.T) {
	payload := &SessionPayload{
		Action:        SessionActionTopUp,
		SessionID:     "7f3a1b2c4d5e6f",
		TopUpPreimage: "b9c3a4e1d2f5",
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded SessionPayload
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, SessionActionTopUp, decoded.Action)
	require.Equal(t, payload.SessionID, decoded.SessionID)
	require.Equal(t, payload.TopUpPreimage, decoded.TopUpPreimage)
	require.Empty(t, decoded.Preimage)
}

// TestSessionPayloadCloseAction verifies encoding/decoding of the close
// action payload.
func TestSessionPayloadCloseAction(t *testing.T) {
	payload := &SessionPayload{
		Action:    SessionActionClose,
		SessionID: "7f3a1b2c4d5e6f",
		Preimage:  "a3f1e2d4b5c6a7e8",
	}

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded SessionPayload
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, SessionActionClose, decoded.Action)
	require.Equal(t, payload.SessionID, decoded.SessionID)
	require.Equal(t, payload.Preimage, decoded.Preimage)
}

// TestSessionReceiptClose verifies encoding/decoding of a close receipt with
// refund information.
func TestSessionReceiptClose(t *testing.T) {
	receipt := &SessionReceipt{
		Method:       MethodLightning,
		Reference:    "7f3a1b2c4d5e6f",
		Status:       ReceiptStatusSuccess,
		Timestamp:    "2026-03-10T21:00:00Z",
		RefundSats:   140,
		RefundStatus: "succeeded",
	}

	data, err := json.Marshal(receipt)
	require.NoError(t, err)

	var decoded SessionReceipt
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, receipt.Method, decoded.Method)
	require.Equal(t, receipt.Reference, decoded.Reference)
	require.Equal(t, receipt.Status, decoded.Status)
	require.Equal(t, receipt.Timestamp, decoded.Timestamp)
	require.Equal(t, int64(140), decoded.RefundSats)
	require.Equal(t, "succeeded", decoded.RefundStatus)
}

// TestSessionReceiptCloseSkipped verifies encoding when no refund is owed.
func TestSessionReceiptCloseSkipped(t *testing.T) {
	receipt := &SessionReceipt{
		Method:       MethodLightning,
		Reference:    "7f3a1b2c4d5e6f",
		Status:       ReceiptStatusSuccess,
		Timestamp:    "2026-03-10T21:00:00Z",
		RefundSats:   0,
		RefundStatus: "skipped",
	}

	data, err := json.Marshal(receipt)
	require.NoError(t, err)

	// RefundSats should be omitted when 0.
	require.NotContains(t, string(data), "refundSats")

	var decoded SessionReceipt
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, int64(0), decoded.RefundSats)
	require.Equal(t, "skipped", decoded.RefundStatus)
}

// TestNeedTopUpEvent verifies the SSE event data encoding.
func TestNeedTopUpEvent(t *testing.T) {
	event := &NeedTopUpEvent{
		SessionID:       "7f3a1b2c4d5e6f",
		BalanceSpent:    300,
		BalanceRequired: 2,
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	var decoded NeedTopUpEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, event.SessionID, decoded.SessionID)
	require.Equal(t, int64(300), decoded.BalanceSpent)
	require.Equal(t, int64(2), decoded.BalanceRequired)
}

// TestSessionCredentialFullRoundTrip verifies a full credential round-trip
// for a session bearer action, matching the spec example from
// draft-lightning-session-00 Appendix A.3.
func TestSessionCredentialFullRoundTrip(t *testing.T) {
	payload := &SessionPayload{
		Action:    SessionActionBearer,
		SessionID: "7f3a1b2c4d5e6f",
		Preimage:  "a3f1e2d4b5c6a7e8",
	}

	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	cred := &Credential{
		Challenge: ChallengeEcho{
			ID:      "pR4mNvKqU8wLsYtZ1bCdFg",
			Realm:   "api.example.com",
			Method:  MethodLightning,
			Intent:  IntentSession,
			Request: "eyJ...",
		},
		Payload: json.RawMessage(payloadJSON),
	}

	// Encode as Authorization header.
	credJSON, err := json.Marshal(cred)
	require.NoError(t, err)
	token := Base64URLEncode(credJSON)

	// Parse back.
	h := make(http.Header)
	h.Set("Authorization", AuthScheme+" "+token)

	parsed, err := ParseCredential(&h)
	require.NoError(t, err)
	require.Equal(t, IntentSession, parsed.Challenge.Intent)

	// Decode payload.
	var parsedPayload SessionPayload
	require.NoError(t, json.Unmarshal(parsed.Payload, &parsedPayload))
	require.Equal(t, SessionActionBearer, parsedPayload.Action)
	require.Equal(t, "7f3a1b2c4d5e6f", parsedPayload.SessionID)
	require.Equal(t, "a3f1e2d4b5c6a7e8", parsedPayload.Preimage)
}
