package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

// mockSessionStore implements SessionStore for testing.
type mockSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{
		sessions: make(map[string]*Session),
	}
}

func (m *mockSessionStore) CreateSession(_ context.Context,
	session *Session) error {

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[session.SessionID]; exists {
		return fmt.Errorf("session already exists")
	}
	s := *session
	s.CreatedAt = time.Now()
	s.UpdatedAt = time.Now()
	m.sessions[session.SessionID] = &s
	return nil
}

func (m *mockSessionStore) GetSession(_ context.Context,
	sessionID string) (*Session, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	cp := *s
	return &cp, nil
}

func (m *mockSessionStore) UpdateSessionBalance(_ context.Context,
	sessionID string, addSats int64) error {

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.DepositSats += addSats
	s.UpdatedAt = time.Now()
	return nil
}

func (m *mockSessionStore) DeductSessionBalance(_ context.Context,
	sessionID string, amount int64) error {

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	if amount > s.DepositSats-s.SpentSats {
		return fmt.Errorf("insufficient balance")
	}
	s.SpentSats += amount
	s.UpdatedAt = time.Now()
	return nil
}

func (m *mockSessionStore) CloseSession(_ context.Context,
	sessionID string) error {

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.Status = "closed"
	s.UpdatedAt = time.Now()
	return nil
}

// mockPaymentSender implements PaymentSender for testing.
type mockPaymentSender struct {
	payments []sentPayment
	err      error
}

type sentPayment struct {
	invoice string
	amtSats int64
}

func (m *mockPaymentSender) SendPayment(_ context.Context, invoice string,
	amtSats int64) (string, error) {

	if m.err != nil {
		return "", m.err
	}
	m.payments = append(m.payments, sentPayment{
		invoice: invoice,
		amtSats: amtSats,
	})
	return "refund-preimage-hex", nil
}

// buildSessionCredential creates a properly encoded session credential.
func buildSessionCredential(t *testing.T, challenge mpp.ChallengeEcho,
	payload *mpp.SessionPayload) http.Header {

	t.Helper()

	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	cred := &mpp.Credential{
		Challenge: challenge,
		Payload:   json.RawMessage(payloadJSON),
	}
	credJSON, err := json.Marshal(cred)
	require.NoError(t, err)

	h := make(http.Header)
	h.Set("Authorization",
		mpp.AuthScheme+" "+mpp.Base64URLEncode(credJSON))
	return h
}

// buildSessionChallenge creates a challenge with valid HMAC for testing.
func buildSessionChallenge(t *testing.T, hmacSecret []byte,
	paymentHash lntypes.Hash,
	depositSats int64) (mpp.ChallengeEcho, string) {

	t.Helper()

	sessReq := &mpp.SessionRequest{
		Amount:         "2",
		Currency:       mpp.CurrencySat,
		DepositInvoice: "lnbcrt1deposit...",
		PaymentHash:    hex.EncodeToString(paymentHash[:]),
		DepositAmount:  fmt.Sprintf("%d", depositSats),
	}
	encodedReq, err := mpp.EncodeRequest(sessReq)
	require.NoError(t, err)

	params := &mpp.ChallengeParams{
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentSession,
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
	return challenge, hex.EncodeToString(paymentHash[:])
}

func newTestSessionAuth(t *testing.T) (*MPPSessionAuthenticator,
	*mockSessionStore, *mockPaymentSender, []byte) {

	t.Helper()

	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")
	store := newMockSessionStore()
	sender := &mockPaymentSender{}
	checker := newMockInvoiceChecker()

	auth := NewMPPSessionAuthenticator(&MPPSessionConfig{
		Challenger:        &mockChallenger{},
		Checker:           checker,
		SessionStore:      store,
		PaymentSender:     sender,
		Realm:             "api.example.com",
		HMACSecret:        hmacSecret,
		Network:           "regtest",
		DepositMultiplier: 20,
		IdleTimeout:       5 * time.Minute,
	})

	return auth, store, sender, hmacSecret
}

// TestSessionOpenAccept verifies that a valid open credential creates a
// session.
func TestSessionOpenAccept(t *testing.T) {
	auth, store, _, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)

	// Mark invoice as settled.
	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 300,
	)

	payload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: "lnbcrt1return...",
	}
	h := buildSessionCredential(t, challenge, payload)

	result := auth.Accept(&h, "test-service")
	require.True(t, result)

	// Verify session was created.
	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, "open", session.Status)
	require.Equal(t, int64(300), session.DepositSats)
	require.Equal(t, "lnbcrt1return...", session.ReturnInvoice)
}

// TestSessionBearerAccept verifies that a valid bearer credential is accepted
// for an existing session.
func TestSessionBearerAccept(t *testing.T) {
	auth, store, _, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)

	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	// First open the session.
	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 300,
	)
	openPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: "lnbcrt1return...",
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Now send a bearer credential.
	bearerPayload := &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	}

	// Bearer doesn't need valid HMAC, just any valid challenge echo.
	bearerChallenge := mpp.ChallengeEcho{
		ID:      "any-id",
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentSession,
		Request: "eyJ0ZXN0IjoiMSJ9",
	}
	bearerH := buildSessionCredential(t, bearerChallenge, bearerPayload)
	require.True(t, auth.Accept(&bearerH, "test-service"))

	// Verify session state unchanged.
	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, "open", session.Status)
	require.Equal(t, int64(0), session.SpentSats)
}

// TestSessionBearerWrongPreimage verifies that a bearer with wrong preimage is
// rejected.
func TestSessionBearerWrongPreimage(t *testing.T) {
	auth, _, _, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)
	_, wrongPreimage := testPreimageAndHash(t)

	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	// Open session.
	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 300,
	)
	openPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: "lnbcrt1return...",
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Bearer with wrong preimage.
	bearerPayload := &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(wrongPreimage[:]),
	}
	bearerChallenge := mpp.ChallengeEcho{
		ID:      "any-id",
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentSession,
		Request: "eyJ0ZXN0IjoiMSJ9",
	}
	bearerH := buildSessionCredential(t, bearerChallenge, bearerPayload)
	require.False(t, auth.Accept(&bearerH, "test-service"))
}

// TestSessionTopUpAccept verifies that a top-up adds to the session balance.
func TestSessionTopUpAccept(t *testing.T) {
	auth, store, _, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)

	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	// Open session.
	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 300,
	)
	openPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: "lnbcrt1return...",
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Create a top-up invoice.
	topUpPreimage, topUpHash := testPreimageAndHash(t)
	auth.checker.(*mockInvoiceChecker).settledHashes[topUpHash] = true

	topUpChallenge, _ := buildSessionChallenge(
		t, hmacSecret, topUpHash, 200,
	)
	topUpPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionTopUp,
		SessionID:     sessionID,
		TopUpPreimage: hex.EncodeToString(topUpPreimage[:]),
	}
	topUpH := buildSessionCredential(t, topUpChallenge, topUpPayload)
	require.True(t, auth.Accept(&topUpH, "test-service"))

	// Verify balance increased.
	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, int64(500), session.DepositSats) // 300 + 200
}

// TestSessionCloseWithRefund verifies that closing a session triggers a
// refund.
func TestSessionCloseWithRefund(t *testing.T) {
	auth, store, sender, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)

	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	// Open session.
	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 300,
	)
	openPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: "lnbcrt1return...",
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Simulate some spending.
	err := store.DeductSessionBalance(
		context.Background(), sessionID, 100,
	)
	require.NoError(t, err)

	// Close session.
	closePayload := &mpp.SessionPayload{
		Action:    mpp.SessionActionClose,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	}
	closeChallenge := mpp.ChallengeEcho{
		ID:      "any-id",
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentSession,
		Request: "eyJ0ZXN0IjoiMSJ9",
	}
	closeH := buildSessionCredential(t, closeChallenge, closePayload)
	require.True(t, auth.Accept(&closeH, "test-service"))

	// Verify session is closed.
	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, "closed", session.Status)

	// Verify refund was sent.
	require.Len(t, sender.payments, 1)
	require.Equal(t, "lnbcrt1return...", sender.payments[0].invoice)
	require.Equal(t, int64(200), sender.payments[0].amtSats) // 300-100
}

// TestSessionCloseAlreadyClosed verifies that closing an already-closed
// session is rejected.
func TestSessionCloseAlreadyClosed(t *testing.T) {
	auth, _, _, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)

	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	// Open session.
	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 300,
	)
	openPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: "lnbcrt1return...",
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Close session.
	closePayload := &mpp.SessionPayload{
		Action:    mpp.SessionActionClose,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	}
	closeChallenge := mpp.ChallengeEcho{
		ID:      "any-id",
		Realm:   "api.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentSession,
		Request: "eyJ0ZXN0IjoiMSJ9",
	}
	closeH := buildSessionCredential(t, closeChallenge, closePayload)
	require.True(t, auth.Accept(&closeH, "test-service"))

	// Try to close again.
	closeH2 := buildSessionCredential(t, closeChallenge, closePayload)
	require.False(t, auth.Accept(&closeH2, "test-service"))
}

// TestSessionFreshChallengeHeader verifies the session challenge header
// generation.
func TestSessionFreshChallengeHeader(t *testing.T) {
	_, paymentHash := testPreimageAndHash(t)
	hmacSecret := []byte("test-hmac-secret-key-32-bytes!!")

	auth := NewMPPSessionAuthenticator(&MPPSessionConfig{
		Challenger: &mockChallenger{
			paymentRequest: "lnbcrt2000n1ptest...",
			paymentHash:    paymentHash,
		},
		Checker:           newMockInvoiceChecker(),
		SessionStore:      newMockSessionStore(),
		PaymentSender:     &mockPaymentSender{},
		Realm:             "api.example.com",
		HMACSecret:        hmacSecret,
		Network:           "regtest",
		DepositMultiplier: 20,
		IdleTimeout:       5 * time.Minute,
	})

	header, err := auth.FreshChallengeHeader("test-service", 10)
	require.NoError(t, err)

	values := header.Values("WWW-Authenticate")
	require.Len(t, values, 1)

	params, err := mpp.ParseChallengeHeader(values[0])
	require.NoError(t, err)
	require.Equal(t, mpp.MethodLightning, params.Method)
	require.Equal(t, mpp.IntentSession, params.Intent)
	require.True(t, mpp.VerifyChallengeID(hmacSecret, params, params.ID))

	// Decode request.
	var sessReq mpp.SessionRequest
	err = mpp.DecodeRequest(params.Request, &sessReq)
	require.NoError(t, err)
	require.Equal(t, "10", sessReq.Amount)
	require.Equal(t, mpp.CurrencySat, sessReq.Currency)
	require.Equal(t, "200", sessReq.DepositAmount) // 10 * 20
	require.Equal(t, "lnbcrt2000n1ptest...", sessReq.DepositInvoice)
	require.Equal(t, hex.EncodeToString(paymentHash[:]),
		sessReq.PaymentHash)
	require.Equal(t, "300", sessReq.IdleTimeout) // 5 min
}
