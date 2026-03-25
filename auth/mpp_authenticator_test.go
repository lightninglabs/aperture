package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

// mockChallenger implements mint.Challenger for testing.
type mockChallenger struct {
	paymentRequest string
	paymentHash    lntypes.Hash
	err            error
}

func (m *mockChallenger) NewChallenge(price int64) (string, lntypes.Hash,
	error) {

	return m.paymentRequest, m.paymentHash, m.err
}

func (m *mockChallenger) Stop() {}

// mockInvoiceChecker implements InvoiceChecker for testing.
type mockInvoiceChecker struct {
	settledHashes map[lntypes.Hash]bool
}

func newMockInvoiceChecker() *mockInvoiceChecker {
	return &mockInvoiceChecker{
		settledHashes: make(map[lntypes.Hash]bool),
	}
}

func (m *mockInvoiceChecker) VerifyInvoiceStatus(hash lntypes.Hash,
	state lnrpc.Invoice_InvoiceState, _ time.Duration) error {

	if state == lnrpc.Invoice_SETTLED && m.settledHashes[hash] {
		return nil
	}
	return fmt.Errorf("invoice not settled")
}

// testPreimageAndHash generates a random preimage and its hash for testing.
func testPreimageAndHash(t *testing.T) (lntypes.Preimage, lntypes.Hash) {
	t.Helper()
	var preimage lntypes.Preimage
	_, err := rand.Read(preimage[:])
	require.NoError(t, err)
	return preimage, sha256.Sum256(preimage[:])
}

// buildTestCredential creates a properly encoded MPP credential for testing.
func buildTestCredential(t *testing.T, challenge mpp.ChallengeEcho,
	preimage lntypes.Preimage) http.Header {

	t.Helper()

	payload := mpp.ChargePayload{
		Preimage: hex.EncodeToString(preimage[:]),
	}
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	cred := &mpp.Credential{
		Challenge: challenge,
		Payload:   json.RawMessage(payloadJSON),
	}
	credJSON, err := json.Marshal(cred)
	require.NoError(t, err)

	h := make(http.Header)
	h.Set("Authorization", mpp.AuthScheme+" "+mpp.Base64URLEncode(credJSON))
	return h
}

// TestMPPAuthenticatorAcceptValid verifies that a valid charge credential is
// accepted.
func TestMPPAuthenticatorAcceptValid(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth := NewMPPAuthenticator(
		&mockChallenger{}, checker,
		"api.example.com", hmacSecret, "regtest", nil,
	)

	// Build a charge request.
	chargeReq := &mpp.ChargeRequest{
		Amount:   "100",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1...",
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	// Build challenge params and compute HMAC.
	params := &mpp.ChallengeParams{
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
	}
	params.ID = mpp.ComputeChallengeID(hmacSecret, params)

	// Build credential with echoed challenge.
	challenge := mpp.ChallengeEcho{
		ID:      params.ID,
		Realm:   params.Realm,
		Method:  params.Method,
		Intent:  params.Intent,
		Request: params.Request,
	}
	h := buildTestCredential(t, challenge, preimage)

	result := auth.Accept(&h, "test-service")
	require.True(t, result)
}

// TestMPPAuthenticatorAcceptInvalidPreimage verifies that a credential with a
// wrong preimage is rejected.
func TestMPPAuthenticatorAcceptInvalidPreimage(t *testing.T) {
	_, paymentHash := testPreimageAndHash(t)
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	// Use a different preimage that doesn't match.
	var wrongPreimage lntypes.Preimage
	_, err := rand.Read(wrongPreimage[:])
	require.NoError(t, err)

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth := NewMPPAuthenticator(
		&mockChallenger{}, checker,
		"api.example.com", hmacSecret, "regtest", nil,
	)

	chargeReq := &mpp.ChargeRequest{
		Amount:   "100",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1...",
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	params := &mpp.ChallengeParams{
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
	}
	params.ID = mpp.ComputeChallengeID(hmacSecret, params)

	challenge := mpp.ChallengeEcho{
		ID:      params.ID,
		Realm:   params.Realm,
		Method:  params.Method,
		Intent:  params.Intent,
		Request: params.Request,
	}
	h := buildTestCredential(t, challenge, wrongPreimage)

	result := auth.Accept(&h, "test-service")
	require.False(t, result)
}

// TestMPPAuthenticatorAcceptInvalidHMAC verifies that a credential with a
// tampered challenge ID is rejected.
func TestMPPAuthenticatorAcceptInvalidHMAC(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth := NewMPPAuthenticator(
		&mockChallenger{}, checker,
		"api.example.com", hmacSecret, "regtest", nil,
	)

	chargeReq := &mpp.ChargeRequest{
		Amount:   "100",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1...",
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	// Use a fake challenge ID.
	challenge := mpp.ChallengeEcho{
		ID:      "tampered-challenge-id",
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
	}
	h := buildTestCredential(t, challenge, preimage)

	result := auth.Accept(&h, "test-service")
	require.False(t, result)
}

// TestMPPAuthenticatorAcceptExpired verifies that an expired challenge is
// rejected.
func TestMPPAuthenticatorAcceptExpired(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth := NewMPPAuthenticator(
		&mockChallenger{}, checker,
		"api.example.com", hmacSecret, "regtest", nil,
	)

	chargeReq := &mpp.ChargeRequest{
		Amount:   "100",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1...",
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	// Set expires to the past.
	pastExpiry := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)

	params := &mpp.ChallengeParams{
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
		Expires: pastExpiry,
	}
	params.ID = mpp.ComputeChallengeID(hmacSecret, params)

	challenge := mpp.ChallengeEcho{
		ID:      params.ID,
		Realm:   params.Realm,
		Method:  params.Method,
		Intent:  params.Intent,
		Request: params.Request,
		Expires: pastExpiry,
	}
	h := buildTestCredential(t, challenge, preimage)

	result := auth.Accept(&h, "test-service")
	require.False(t, result)
}

// TestMPPAuthenticatorAcceptUnsettled verifies that a credential for an
// unsettled invoice is rejected.
func TestMPPAuthenticatorAcceptUnsettled(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	// Don't mark the invoice as settled.
	checker := newMockInvoiceChecker()

	auth := NewMPPAuthenticator(
		&mockChallenger{}, checker,
		"api.example.com", hmacSecret, "regtest", nil,
	)

	chargeReq := &mpp.ChargeRequest{
		Amount:   "100",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1...",
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	params := &mpp.ChallengeParams{
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
	}
	params.ID = mpp.ComputeChallengeID(hmacSecret, params)

	challenge := mpp.ChallengeEcho{
		ID:      params.ID,
		Realm:   params.Realm,
		Method:  params.Method,
		Intent:  params.Intent,
		Request: params.Request,
	}
	h := buildTestCredential(t, challenge, preimage)

	result := auth.Accept(&h, "test-service")
	require.False(t, result)
}

// TestMPPAuthenticatorAcceptNonPayment verifies that non-Payment auth headers
// are silently ignored.
func TestMPPAuthenticatorAcceptNonPayment(t *testing.T) {
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")
	auth := NewMPPAuthenticator(
		&mockChallenger{}, newMockInvoiceChecker(),
		"api.example.com", hmacSecret, "regtest", nil,
	)

	// L402 header should be silently rejected.
	h := make(http.Header)
	h.Set("Authorization", "L402 abc123:deadbeef")
	require.False(t, auth.Accept(&h, "test-service"))

	// No header at all.
	h2 := make(http.Header)
	require.False(t, auth.Accept(&h2, "test-service"))
}

// TestMPPAuthenticatorFreshChallengeHeader verifies that a valid challenge
// header is generated.
func TestMPPAuthenticatorFreshChallengeHeader(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)
	_ = preimage

	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	auth := NewMPPAuthenticator(
		&mockChallenger{
			paymentRequest: "lnbcrt1000n1ptest...",
			paymentHash:    paymentHash,
		},
		newMockInvoiceChecker(),
		"api.example.com", hmacSecret, "regtest", nil,
	)

	header, err := auth.FreshChallengeHeader("test-service", 100)
	require.NoError(t, err)

	// Should have a WWW-Authenticate: Payment header.
	values := header.Values("WWW-Authenticate")
	require.Len(t, values, 1)
	require.Contains(t, values[0], "Payment ")

	// Parse the challenge header.
	params, err := mpp.ParseChallengeHeader(values[0])
	require.NoError(t, err)
	require.Equal(t, "api.example.com", params.Realm)
	require.Equal(t, mpp.MethodLightning, params.Method)
	require.Equal(t, mpp.IntentCharge, params.Intent)
	require.NotEmpty(t, params.ID)
	require.NotEmpty(t, params.Request)

	// Verify the HMAC ID is valid.
	require.True(t, mpp.VerifyChallengeID(hmacSecret, params, params.ID))

	// Decode the request and verify contents.
	var chargeReq mpp.ChargeRequest
	err = mpp.DecodeRequest(params.Request, &chargeReq)
	require.NoError(t, err)
	require.Equal(t, "100", chargeReq.Amount)
	require.Equal(t, mpp.CurrencySat, chargeReq.Currency)
	require.Equal(t, "lnbcrt1000n1ptest...",
		chargeReq.MethodDetails.Invoice)
	require.Equal(t, hex.EncodeToString(paymentHash[:]),
		chargeReq.MethodDetails.PaymentHash)
	require.Equal(t, "regtest", chargeReq.MethodDetails.Network)
}

// TestMPPAuthenticatorReceiptHeader verifies that a receipt header is
// generated for a valid credential.
func TestMPPAuthenticatorReceiptHeader(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	auth := NewMPPAuthenticator(
		&mockChallenger{}, newMockInvoiceChecker(),
		"api.example.com", hmacSecret, "regtest", nil,
	)

	chargeReq := &mpp.ChargeRequest{
		Amount:   "100",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1...",
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	params := &mpp.ChallengeParams{
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
	}
	params.ID = mpp.ComputeChallengeID(hmacSecret, params)

	challenge := mpp.ChallengeEcho{
		ID:      params.ID,
		Realm:   params.Realm,
		Method:  params.Method,
		Intent:  params.Intent,
		Request: params.Request,
	}
	h := buildTestCredential(t, challenge, preimage)

	receiptHdr := auth.ReceiptHeader(&h, "test-service")
	require.NotNil(t, receiptHdr)

	// Parse the receipt.
	receipt, err := mpp.ParseReceiptHeader(receiptHdr)
	require.NoError(t, err)
	require.Equal(t, mpp.ReceiptStatusSuccess, receipt.Status)
	require.Equal(t, mpp.MethodLightning, receipt.Method)
	require.Equal(t, hex.EncodeToString(paymentHash[:]),
		receipt.Reference)
	require.NotEmpty(t, receipt.Timestamp)
	require.Equal(t, params.ID, receipt.ChallengeID)
}

// TestMPPAuthenticatorAcceptWrongIntent verifies that a session intent
// credential is not accepted by the charge authenticator.
func TestMPPAuthenticatorAcceptWrongIntent(t *testing.T) {
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	auth := NewMPPAuthenticator(
		&mockChallenger{}, newMockInvoiceChecker(),
		"api.example.com", hmacSecret, "regtest", nil,
	)

	// Build a credential with session intent.
	cred := &mpp.Credential{
		Challenge: mpp.ChallengeEcho{
			ID:      "some-id",
			Realm:   "api.example.com",
			Method:  mpp.MethodLightning,
			Intent:  mpp.IntentSession,
			Request: "eyJ0ZXN0IjoiMSJ9",
		},
		Payload: json.RawMessage(`{"action":"bearer","sessionId":"abc","preimage":"deadbeef"}`),
	}

	credJSON, err := json.Marshal(cred)
	require.NoError(t, err)

	h := make(http.Header)
	h.Set("Authorization", mpp.AuthScheme+" "+mpp.Base64URLEncode(credJSON))

	result := auth.Accept(&h, "test-service")
	require.False(t, result)
}

// TestMPPAuthenticatorEndToEnd tests the full flow: generate challenge, then
// verify a credential built from it.
func TestMPPAuthenticatorEndToEnd(t *testing.T) {
	// Generate a preimage and hash.
	preimage, paymentHash := testPreimageAndHash(t)

	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth := NewMPPAuthenticator(
		&mockChallenger{
			paymentRequest: "lnbcrt1000n1ptest...",
			paymentHash:    paymentHash,
		},
		checker,
		"api.example.com", hmacSecret, "regtest", nil,
	)

	// Step 1: Generate a challenge.
	challengeHeader, err := auth.FreshChallengeHeader("test-service", 100)
	require.NoError(t, err)

	// Step 2: Parse the challenge.
	wwwAuth := challengeHeader.Get("WWW-Authenticate")
	params, err := mpp.ParseChallengeHeader(wwwAuth)
	require.NoError(t, err)

	// Step 3: Build a credential (client side).
	challenge := mpp.ChallengeEcho{
		ID:      params.ID,
		Realm:   params.Realm,
		Method:  params.Method,
		Intent:  params.Intent,
		Request: params.Request,
		Expires: params.Expires,
	}
	credHeader := buildTestCredential(t, challenge, preimage)

	// Step 4: Verify the credential.
	result := auth.Accept(&credHeader, "test-service")
	require.True(t, result)

	// Step 5: Generate receipt.
	receiptHdr := auth.ReceiptHeader(&credHeader, "test-service")
	require.NotNil(t, receiptHdr)

	receipt, err := mpp.ParseReceiptHeader(receiptHdr)
	require.NoError(t, err)
	require.Equal(t, mpp.ReceiptStatusSuccess, receipt.Status)
}
