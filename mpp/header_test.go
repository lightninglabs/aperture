package mpp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSetChallengeHeaderRoundTrip verifies that a challenge header can be set
// and parsed back to the original parameters.
func TestSetChallengeHeaderRoundTrip(t *testing.T) {
	params := &ChallengeParams{
		ID:      "x7Tg2pLqR9mKvNwY3hBcZa",
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJhbW91bnQiOiIxMDAiLCJjdXJyZW5jeSI6InNhdCJ9",
		Expires: "2026-03-15T12:05:00Z",
	}

	h := make(http.Header)
	SetChallengeHeader(h, params)

	// Verify the header was set.
	values := h.Values(headerWWWAuthenticate)
	require.Len(t, values, 1)
	require.Contains(t, values[0], "Payment ")

	// Parse it back.
	parsed, err := ParseChallengeHeader(values[0])
	require.NoError(t, err)
	require.Equal(t, params.ID, parsed.ID)
	require.Equal(t, params.Realm, parsed.Realm)
	require.Equal(t, params.Method, parsed.Method)
	require.Equal(t, params.Intent, parsed.Intent)
	require.Equal(t, params.Request, parsed.Request)
	require.Equal(t, params.Expires, parsed.Expires)
}

// TestSetChallengeHeaderAllOptional verifies that all optional parameters are
// included in the challenge header when set.
func TestSetChallengeHeaderAllOptional(t *testing.T) {
	params := &ChallengeParams{
		ID:          "testid123",
		Realm:       "api.example.com",
		Method:      MethodLightning,
		Intent:      IntentSession,
		Request:     "eyJ0ZXN0IjoidmFsdWUifQ",
		Expires:     "2026-03-15T12:05:00Z",
		Digest:      "sha-256=:X48E9q...:=",
		Description: "Test payment",
		Opaque:      "eyJvcmRlcklkIjoiMTIzIn0",
	}

	h := make(http.Header)
	SetChallengeHeader(h, params)

	parsed, err := ParseChallengeHeader(h.Get(headerWWWAuthenticate))
	require.NoError(t, err)
	require.Equal(t, params.Description, parsed.Description)
	require.Equal(t, params.Digest, parsed.Digest)
	require.Equal(t, params.Opaque, parsed.Opaque)
}

// TestSetChallengeHeaderMinimal verifies that only required parameters are
// emitted when optional ones are empty.
func TestSetChallengeHeaderMinimal(t *testing.T) {
	params := &ChallengeParams{
		ID:      "minimalid",
		Realm:   "example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJ0ZXN0IjoiMSJ9",
	}

	h := make(http.Header)
	SetChallengeHeader(h, params)

	value := h.Get(headerWWWAuthenticate)
	require.NotContains(t, value, "expires=")
	require.NotContains(t, value, "digest=")
	require.NotContains(t, value, "description=")
	require.NotContains(t, value, "opaque=")
}

// TestParseChallengeHeaderMissingRequired verifies error handling for missing
// required parameters.
func TestParseChallengeHeaderMissingRequired(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "missing id",
			input: `Payment realm="r", method="lightning", intent="charge", request="abc"`,
		},
		{
			name:  "missing realm",
			input: `Payment id="1", method="lightning", intent="charge", request="abc"`,
		},
		{
			name:  "missing method",
			input: `Payment id="1", realm="r", intent="charge", request="abc"`,
		},
		{
			name:  "missing intent",
			input: `Payment id="1", realm="r", method="lightning", request="abc"`,
		},
		{
			name:  "missing request",
			input: `Payment id="1", realm="r", method="lightning", intent="charge"`,
		},
		{
			name:  "wrong scheme",
			input: `Bearer token="abc"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseChallengeHeader(tc.input)
			require.Error(t, err)
		})
	}
}

// TestParseCredentialRoundTrip verifies that a credential can be encoded and
// parsed back correctly.
func TestParseCredentialRoundTrip(t *testing.T) {
	// Build a credential matching the spec example from
	// draft-lightning-charge-00 Appendix A.2.
	cred := &Credential{
		Challenge: ChallengeEcho{
			ID:      "kM9xPqWvT2nJrHsY4aDfEb",
			Realm:   "api.example.com",
			Method:  MethodLightning,
			Intent:  IntentCharge,
			Request: "eyJ...",
			Expires: "2026-03-15T12:05:00Z",
		},
		Payload: json.RawMessage(`{"preimage":"a3f1e2d4b5c6a7e8"}`),
	}

	// Encode it.
	credJSON, err := json.Marshal(cred)
	require.NoError(t, err)
	token := Base64URLEncode(credJSON)

	// Set in header.
	h := make(http.Header)
	h.Set(headerAuthorization, AuthScheme+" "+token)

	// Parse it back.
	parsed, err := ParseCredential(&h)
	require.NoError(t, err)
	require.Equal(t, cred.Challenge.ID, parsed.Challenge.ID)
	require.Equal(t, cred.Challenge.Realm, parsed.Challenge.Realm)
	require.Equal(t, cred.Challenge.Method, parsed.Challenge.Method)
	require.Equal(t, cred.Challenge.Intent, parsed.Challenge.Intent)
	require.Equal(t, cred.Challenge.Request, parsed.Challenge.Request)
	require.Equal(t, cred.Challenge.Expires, parsed.Challenge.Expires)

	// Verify payload.
	var payload ChargePayload
	require.NoError(t, json.Unmarshal(parsed.Payload, &payload))
	require.Equal(t, "a3f1e2d4b5c6a7e8", payload.Preimage)
}

// TestParseCredentialWithSource verifies that the optional source field is
// correctly parsed.
func TestParseCredentialWithSource(t *testing.T) {
	cred := &Credential{
		Challenge: ChallengeEcho{
			ID:      "testid",
			Realm:   "example.com",
			Method:  MethodLightning,
			Intent:  IntentCharge,
			Request: "eyJ0ZXN0IjoiMSJ9",
		},
		Source:  "did:key:z6MkhaXgBZDvotDkL5257faiztiGiC2QtKLGpbnnEGta2doK",
		Payload: json.RawMessage(`{"preimage":"deadbeef"}`),
	}

	credJSON, err := json.Marshal(cred)
	require.NoError(t, err)

	h := make(http.Header)
	h.Set(headerAuthorization, AuthScheme+" "+Base64URLEncode(credJSON))

	parsed, err := ParseCredential(&h)
	require.NoError(t, err)
	require.Equal(t, cred.Source, parsed.Source)
}

// TestParseCredentialErrors verifies error handling for malformed credentials.
func TestParseCredentialErrors(t *testing.T) {
	tests := []struct {
		name   string
		header string
		errMsg string
	}{
		{
			name:   "no authorization header",
			header: "",
			errMsg: "no Authorization header",
		},
		{
			name:   "wrong scheme",
			header: "Bearer abc123",
			errMsg: "does not use Payment scheme",
		},
		{
			name:   "empty token",
			header: "Payment ",
			errMsg: "empty credential token",
		},
		{
			name:   "invalid base64",
			header: "Payment !!!invalid!!!",
			errMsg: "failed to decode credential",
		},
		{
			name: "invalid json",
			header: "Payment " + Base64URLEncode(
				[]byte("{invalid}"),
			),
			errMsg: "failed to unmarshal",
		},
		{
			name: "missing challenge.id",
			header: "Payment " + Base64URLEncode(
				[]byte(`{"challenge":{"method":"lightning","intent":"charge","request":"abc"},"payload":{}}`),
			),
			errMsg: "missing challenge.id",
		},
		{
			name: "missing payload",
			header: "Payment " + Base64URLEncode(
				[]byte(`{"challenge":{"id":"1","method":"lightning","intent":"charge","request":"abc"}}`),
			),
			errMsg: "missing payload",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := make(http.Header)
			if tc.header != "" {
				h.Set(headerAuthorization, tc.header)
			}
			_, err := ParseCredential(&h)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

// TestReceiptHeaderRoundTrip verifies that a receipt can be set and parsed
// back correctly.
func TestReceiptHeaderRoundTrip(t *testing.T) {
	receipt := &Receipt{
		Status:      ReceiptStatusSuccess,
		Method:      MethodLightning,
		Timestamp:   "2026-03-10T21:00:00Z",
		Reference:   "bc230847abcdef1234567890",
		ChallengeID: "kM9xPqWvT2nJrHsY4aDfEb",
	}

	h := make(http.Header)
	err := SetReceiptHeader(h, receipt)
	require.NoError(t, err)

	// Verify it was set.
	require.NotEmpty(t, h.Get(HeaderPaymentReceipt))

	// Parse it back.
	parsed, err := ParseReceiptHeader(h)
	require.NoError(t, err)
	require.Equal(t, receipt.Status, parsed.Status)
	require.Equal(t, receipt.Method, parsed.Method)
	require.Equal(t, receipt.Timestamp, parsed.Timestamp)
	require.Equal(t, receipt.Reference, parsed.Reference)
	require.Equal(t, receipt.ChallengeID, parsed.ChallengeID)
}

// TestBase64URLEncoding verifies base64url encoding without padding.
func TestBase64URLEncoding(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		encoded string
	}{
		{
			name:    "empty",
			input:   []byte{},
			encoded: "",
		},
		{
			name:    "simple",
			input:   []byte("hello"),
			encoded: "aGVsbG8",
		},
		{
			name:    "with url-unsafe chars",
			input:   []byte{0xff, 0xfe, 0xfd},
			encoded: "__79",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			encoded := Base64URLEncode(tc.input)
			// Verify no padding.
			require.NotContains(t, encoded, "=")

			if tc.encoded != "" {
				require.Equal(t, tc.encoded, encoded)
			}

			// Round-trip.
			decoded, err := Base64URLDecode(encoded)
			require.NoError(t, err)
			require.Equal(t, tc.input, decoded)
		})
	}
}

// TestBase64URLDecodeWithPadding verifies that padded input is accepted.
func TestBase64URLDecodeWithPadding(t *testing.T) {
	// "hello" base64url with padding.
	decoded, err := Base64URLDecode("aGVsbG8=")
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), decoded)
}

// TestEncodeDecodeRequest verifies request encoding round-trip.
func TestEncodeDecodeRequest(t *testing.T) {
	req := &ChargeRequest{
		Amount:   "100",
		Currency: CurrencySat,
		MethodDetails: ChargeMethodDetails{
			Invoice:     "lnbc1u1p...",
			PaymentHash: "bc230847...",
			Network:     "mainnet",
		},
	}

	encoded, err := EncodeRequest(req)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)
	// Verify no padding.
	require.NotContains(t, encoded, "=")

	var decoded ChargeRequest
	err = DecodeRequest(encoded, &decoded)
	require.NoError(t, err)
	require.Equal(t, req.Amount, decoded.Amount)
	require.Equal(t, req.Currency, decoded.Currency)
	require.Equal(t, req.MethodDetails.Invoice,
		decoded.MethodDetails.Invoice)
	require.Equal(t, req.MethodDetails.PaymentHash,
		decoded.MethodDetails.PaymentHash)
	require.Equal(t, req.MethodDetails.Network,
		decoded.MethodDetails.Network)
}

// TestEncodeRequestCanonical verifies that EncodeRequest produces deterministic
// output via JCS canonicalization.
func TestEncodeRequestCanonical(t *testing.T) {
	req := &ChargeRequest{
		Amount:   "100",
		Currency: CurrencySat,
		MethodDetails: ChargeMethodDetails{
			Invoice:     "lnbc1u1p...",
			PaymentHash: "bc230847...",
			Network:     "mainnet",
		},
	}

	// Encode twice and verify identical output.
	enc1, err := EncodeRequest(req)
	require.NoError(t, err)
	enc2, err := EncodeRequest(req)
	require.NoError(t, err)
	require.Equal(t, enc1, enc2)
}

// TestParseAuthParamsEdgeCases verifies the auth-param parser handles edge
// cases correctly.
func TestParseAuthParamsEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:  "standard quoted values",
			input: `id="abc", realm="example.com"`,
			expected: map[string]string{
				"id":    "abc",
				"realm": "example.com",
			},
		},
		{
			name:  "unquoted token values",
			input: `method=lightning, intent=charge`,
			expected: map[string]string{
				"method": "lightning",
				"intent": "charge",
			},
		},
		{
			name:  "mixed quoted and unquoted",
			input: `id="abc", method=lightning`,
			expected: map[string]string{
				"id":     "abc",
				"method": "lightning",
			},
		},
		{
			name:  "extra whitespace",
			input: `  id = "abc" ,  realm = "example.com"  `,
			expected: map[string]string{
				"id":    "abc",
				"realm": "example.com",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := parseAuthParams(tc.input)
			require.Equal(t, tc.expected, result)
		})
	}
}

// TestMultipleChallengeHeaders verifies that multiple Payment challenges can
// be set on the same response header (for intent negotiation per spec §7.3).
func TestMultipleChallengeHeaders(t *testing.T) {
	h := make(http.Header)

	chargeParams := &ChallengeParams{
		ID:      "charge-id",
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJjaGFyZ2UiOiJ0cnVlIn0",
	}
	sessionParams := &ChallengeParams{
		ID:      "session-id",
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentSession,
		Request: "eyJzZXNzaW9uIjoidHJ1ZSJ9",
	}

	SetChallengeHeader(h, chargeParams)
	SetChallengeHeader(h, sessionParams)

	values := h.Values(headerWWWAuthenticate)
	require.Len(t, values, 2)

	parsed1, err := ParseChallengeHeader(values[0])
	require.NoError(t, err)
	require.Equal(t, IntentCharge, parsed1.Intent)

	parsed2, err := ParseChallengeHeader(values[1])
	require.NoError(t, err)
	require.Equal(t, IntentSession, parsed2.Intent)
}
