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

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/zpay32"
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
	if s.Status != "open" {
		return fmt.Errorf("session already closed")
	}
	s.Status = "closed"
	s.UpdatedAt = time.Now()
	return nil
}

func (m *mockSessionStore) CloseSessionAndGetBalance(_ context.Context,
	sessionID string) (int64, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return 0, fmt.Errorf("session not found")
	}
	if s.Status != "open" {
		return 0, fmt.Errorf("session already closed")
	}
	s.Status = "closed"
	s.UpdatedAt = time.Now()

	return s.DepositSats - s.SpentSats, nil
}

// mockPaymentSender implements PaymentSender for testing.
type mockPaymentSender struct {
	mu       sync.Mutex
	payments []sentPayment
	err      error
}

type sentPayment struct {
	invoice string
	amtSats int64
}

func (m *mockPaymentSender) SendPayment(_ context.Context, invoice string,
	amtSats int64) (string, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return "", m.err
	}
	m.payments = append(m.payments, sentPayment{
		invoice: invoice,
		amtSats: amtSats,
	})
	return "refund-preimage-hex", nil
}

// testReturnInvoice generates a valid BOLT11 invoice with no encoded amount
// on the regtest network, suitable for use as a session return invoice.
func testReturnInvoice(t *testing.T, paymentHash lntypes.Hash) string {
	t.Helper()

	privKey, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	inv, err := zpay32.NewInvoice(
		&chaincfg.RegressionNetParams,
		paymentHash,
		time.Now(),
		zpay32.Description("return invoice"),
	)
	require.NoError(t, err)

	encoded, err := inv.Encode(
		zpay32.MessageSigner{
			SignCompact: func(msg []byte) ([]byte, error) {
				sig := ecdsa.SignCompact(privKey, msg, true)
				return sig, nil
			},
		},
	)
	require.NoError(t, err)

	return encoded
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

	returnInvoice := testReturnInvoice(t, paymentHash)
	payload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: returnInvoice,
	}
	h := buildSessionCredential(t, challenge, payload)

	result := auth.Accept(&h, "test-service")
	require.True(t, result)

	// Verify session was created.
	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, "open", session.Status)
	require.Equal(t, int64(300), session.DepositSats)
	require.Equal(t, returnInvoice, session.ReturnInvoice)
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
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Now send a bearer credential with the same valid challenge.
	bearerPayload := &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	}
	bearerH := buildSessionCredential(t, challenge, bearerPayload)
	require.True(t, auth.Accept(&bearerH, "test-service"))

	// Verify session balance was deducted. The challenge has amount="2".
	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, "open", session.Status)
	require.Equal(t, int64(2), session.SpentSats)
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
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Bearer with wrong preimage but valid challenge HMAC.
	bearerPayload := &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(wrongPreimage[:]),
	}
	bearerH := buildSessionCredential(t, challenge, bearerPayload)
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
		ReturnInvoice: testReturnInvoice(t, paymentHash),
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
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Simulate some spending.
	err := store.DeductSessionBalance(
		context.Background(), sessionID, 100,
	)
	require.NoError(t, err)

	// Close session with valid HMAC challenge.
	closePayload := &mpp.SessionPayload{
		Action:    mpp.SessionActionClose,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	}
	closeH := buildSessionCredential(t, challenge, closePayload)
	require.True(t, auth.Accept(&closeH, "test-service"))

	// Verify session is closed.
	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, "closed", session.Status)

	// Wait for async refund goroutine to complete.
	require.Eventually(t, func() bool {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		return len(sender.payments) == 1
	}, 5*time.Second, 10*time.Millisecond)

	// Verify refund was sent to the return invoice.
	sender.mu.Lock()
	require.NotEmpty(t, sender.payments[0].invoice)
	require.Equal(t, int64(200), sender.payments[0].amtSats) // 300-100
	sender.mu.Unlock()
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
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Close session with valid HMAC challenge.
	closePayload := &mpp.SessionPayload{
		Action:    mpp.SessionActionClose,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	}
	closeH := buildSessionCredential(t, challenge, closePayload)
	require.True(t, auth.Accept(&closeH, "test-service"))

	// Try to close again — should be rejected (already closed).
	closeH2 := buildSessionCredential(t, challenge, closePayload)
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

// TestSessionConcurrentCloseReceipts verifies that concurrent session closes
// produce correct receipts for each session (no cross-contamination).
func TestSessionConcurrentCloseReceipts(t *testing.T) {
	auth, store, sender, hmacSecret := newTestSessionAuth(t)

	const numSessions = 10
	type sessionInfo struct {
		preimage    lntypes.Preimage
		paymentHash lntypes.Hash
		sessionID   string
		challenge   mpp.ChallengeEcho
		depositSats int64
	}

	sessions := make([]sessionInfo, numSessions)

	// Open all sessions.
	for i := 0; i < numSessions; i++ {
		preimage, paymentHash := testPreimageAndHash(t)
		auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

		depositSats := int64((i + 1) * 100)
		challenge, sessionID := buildSessionChallenge(
			t, hmacSecret, paymentHash, depositSats,
		)

		returnInv := testReturnInvoice(t, paymentHash)
		openPayload := &mpp.SessionPayload{
			Action:        mpp.SessionActionOpen,
			Preimage:      hex.EncodeToString(preimage[:]),
			ReturnInvoice: returnInv,
		}
		openH := buildSessionCredential(t, challenge, openPayload)
		require.True(t, auth.Accept(&openH, "test-service"))

		sessions[i] = sessionInfo{
			preimage:    preimage,
			paymentHash: paymentHash,
			sessionID:   sessionID,
			challenge:   challenge,
			depositSats: depositSats,
		}
	}

	// Close all sessions concurrently.
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(s sessionInfo) {
			defer wg.Done()
			<-start

			closePayload := &mpp.SessionPayload{
				Action:    mpp.SessionActionClose,
				SessionID: s.sessionID,
				Preimage: hex.EncodeToString(
					s.preimage[:],
				),
			}
			closeH := buildSessionCredential(
				t, s.challenge, closePayload,
			)
			auth.Accept(&closeH, "test-service")
		}(sessions[i])
	}

	close(start)
	wg.Wait()

	// Verify all sessions are closed.
	for _, s := range sessions {
		session, err := store.GetSession(
			context.Background(), s.sessionID,
		)
		require.NoError(t, err)
		require.Equal(t, "closed", session.Status)
	}

	// Wait for async refund goroutines to complete. The mock sender
	// is instant, so a short poll is sufficient.
	require.Eventually(t, func() bool {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		return len(sender.payments) == numSessions
	}, 5*time.Second, 10*time.Millisecond)
}

// TestSessionConcurrentBearerAccept verifies that concurrent bearer requests
// on the same session don't corrupt state.
func TestSessionConcurrentBearerAccept(t *testing.T) {
	auth, store, _, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)

	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	// Open session with a large deposit.
	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 10000,
	)
	returnInv := testReturnInvoice(t, paymentHash)
	openPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: returnInv,
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	// Send many concurrent bearer requests.
	const numRequests = 50
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < numRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			bearerPayload := &mpp.SessionPayload{
				Action:    mpp.SessionActionBearer,
				SessionID: sessionID,
				Preimage: hex.EncodeToString(
					preimage[:],
				),
			}
			bearerH := buildSessionCredential(
				t, challenge, bearerPayload,
			)
			auth.Accept(&bearerH, "test-service")
		}()
	}

	close(start)
	wg.Wait()

	// Verify no balance overdraft. Each bearer deducts 2 sats (the
	// amount in the challenge). 50 requests * 2 sats = 100 sats.
	session, err := store.GetSession(
		context.Background(), sessionID,
	)
	require.NoError(t, err)
	require.Equal(t, int64(10000), session.DepositSats)
	require.Equal(t, int64(100), session.SpentSats)
	require.True(t, session.SpentSats <= session.DepositSats)
}
