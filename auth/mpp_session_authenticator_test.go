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
	"github.com/btcsuite/btcd/chaincfg/v2"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/zpay32"
	"github.com/stretchr/testify/require"
)

// mockSessionStore implements SessionStore for testing.
type mockSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session

	// credits records which session each already-credited payment hash
	// funded, mirroring the unique index the durable store relies on.
	credits map[lntypes.Hash]string
}

func newMockSessionStore() *mockSessionStore {
	return &mockSessionStore{
		sessions: make(map[string]*Session),
		credits:  make(map[lntypes.Hash]string),
	}
}

func (m *mockSessionStore) CreateSession(_ context.Context,
	session *Session) error {

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[session.SessionID]; exists {
		return fmt.Errorf("session already exists")
	}
	if owner, spent := m.credits[session.PaymentHash]; spent {
		return fmt.Errorf("deposit payment hash already credited to "+
			"session %s", owner)
	}
	s := *session
	s.CreatedAt = time.Now()
	s.UpdatedAt = time.Now()
	m.sessions[session.SessionID] = &s
	m.credits[session.PaymentHash] = session.SessionID
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

// CreditSession mirrors the durable store: the payment hash is claimed and the
// deposit grown under one lock, so a replay can never observe the hash as
// unspent and credit it a second time.
func (m *mockSessionStore) CreditSession(_ context.Context, sessionID string,
	paymentHash lntypes.Hash, addSats int64) (CreditOutcome, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	if addSats <= 0 {
		return CreditApplied, fmt.Errorf("credit must be positive, "+
			"got %d", addSats)
	}

	if owner, spent := m.credits[paymentHash]; spent {
		if owner == sessionID {
			return CreditReplayed, nil
		}

		return CreditForeign, nil
	}

	s, ok := m.sessions[sessionID]
	if !ok {
		return CreditApplied, fmt.Errorf("session not found")
	}
	if s.Status != sessionStatusOpen {
		return CreditApplied, fmt.Errorf("session already closed")
	}

	m.credits[paymentHash] = sessionID
	s.DepositSats += addSats
	s.UpdatedAt = time.Now()

	return CreditApplied, nil
}

// creditOwner returns the session a payment hash was credited to.
func (m *mockSessionStore) creditOwner(t *testing.T,
	paymentHash lntypes.Hash) string {

	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	owner, ok := m.credits[paymentHash]
	require.True(t, ok)

	return owner
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

// SettleSessionBalance mirrors the durable store: the spend moves by a signed
// amount and is clamped to the range the deposit allows, so an under-estimate
// settles to at most the whole balance rather than driving it negative.
func (m *mockSessionStore) SettleSessionBalance(_ context.Context,
	sessionID string, deltaSats int64) (int64, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return 0, fmt.Errorf("session not found")
	}
	if s.Status != "open" {
		return 0, fmt.Errorf("session already closed")
	}

	spent := s.SpentSats + deltaSats
	switch {
	case spent < 0:
		spent = 0
	case spent > s.DepositSats:
		spent = s.DepositSats
	}

	s.SpentSats = spent
	s.UpdatedAt = time.Now()

	return spent, nil
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

	// block, when set, holds the send open until it is closed, so a test
	// can read a receipt while the refund is still routing.
	block chan struct{}
}

type sentPayment struct {
	invoice string
	amtSats int64
}

func (m *mockPaymentSender) SendPayment(_ context.Context, invoice string,
	amtSats int64) (string, error) {

	if m.block != nil {
		<-m.block
	}

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

// openTestSession opens a session through the authenticator and returns its ID
// along with the challenge and preimage that opened it.
func openTestSession(t *testing.T, auth *MPPSessionAuthenticator,
	hmacSecret []byte, depositSats int64) (string, mpp.ChallengeEcho,
	lntypes.Preimage) {

	t.Helper()

	preimage, paymentHash := testPreimageAndHash(t)
	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, depositSats,
	)
	openPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	}
	openH := buildSessionCredential(t, challenge, openPayload)
	require.True(t, auth.Accept(&openH, "test-service"))

	return sessionID, challenge, preimage
}

// buildTopUp mints a settled top-up challenge and returns the credential that
// spends it, along with its payment hash.
func buildTopUp(t *testing.T, auth *MPPSessionAuthenticator,
	hmacSecret []byte, sessionID string, topUpSats int64) (http.Header,
	lntypes.Hash) {

	t.Helper()

	preimage, paymentHash := testPreimageAndHash(t)
	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	challenge, _ := buildSessionChallenge(
		t, hmacSecret, paymentHash, topUpSats,
	)
	payload := &mpp.SessionPayload{
		Action:        mpp.SessionActionTopUp,
		SessionID:     sessionID,
		TopUpPreimage: hex.EncodeToString(preimage[:]),
	}

	return buildSessionCredential(t, challenge, payload), paymentHash
}

// TestSessionTopUpReplayCreditsOnce asserts that resending an identical top-up
// credential adds nothing after the first time. Every check the credential has
// to pass is a fact about the payment rather than about the request: the
// preimage hashes to the payment hash forever, the challenge HMAC is stateless,
// and a settled invoice never stops being settled. So one paid invoice, resent,
// used to mint balance without bound.
//
// The retry is still accepted rather than refused. An honest client whose
// top-up response was lost resends exactly this, and answering with a fresh
// challenge would have it pay a second invoice for a top-up it already has.
func TestSessionTopUpReplayCreditsOnce(t *testing.T) {
	auth, store, _, hmacSecret := newTestSessionAuth(t)

	sessionID, _, _ := openTestSession(t, auth, hmacSecret, 300)
	topUpH, _ := buildTopUp(t, auth, hmacSecret, sessionID, 200)

	require.True(t, auth.Accept(&topUpH, "test-service"))

	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, int64(500), session.DepositSats)

	// Replay the identical credential. It is accepted as a retry, and the
	// balance does not move.
	for i := 0; i < 5; i++ {
		require.True(t, auth.Accept(&topUpH, "test-service"))

		session, err = store.GetSession(context.Background(), sessionID)
		require.NoError(t, err)
		require.Equal(t, int64(500), session.DepositSats)
	}

	// A genuinely new top-up still credits, so the guard has not simply
	// turned top-ups off.
	secondH, _ := buildTopUp(t, auth, hmacSecret, sessionID, 700)
	require.True(t, auth.Accept(&secondH, "test-service"))

	session, err = store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1200), session.DepositSats)
}

// TestSessionTopUpForeignHashRefused asserts that a top-up already credited to
// one session cannot be re-presented against another. Nothing in a credential
// binds it to a session in a way the challenge HMAC covers: the session ID
// rides in the payload, so pointing a spent top-up at a second session costs
// the buyer only the edit.
func TestSessionTopUpForeignHashRefused(t *testing.T) {
	auth, store, _, hmacSecret := newTestSessionAuth(t)

	firstID, _, _ := openTestSession(t, auth, hmacSecret, 300)
	secondID, _, _ := openTestSession(t, auth, hmacSecret, 400)

	topUpH, topUpHash := buildTopUp(t, auth, hmacSecret, firstID, 200)
	require.True(t, auth.Accept(&topUpH, "test-service"))

	// Rebuild the same credential naming the other session. The challenge
	// and preimage are untouched, only the payload's session ID differs.
	cred, err := mpp.ParseCredential(&topUpH)
	require.NoError(t, err)

	var payload mpp.SessionPayload
	require.NoError(t, json.Unmarshal(cred.Payload, &payload))
	payload.SessionID = secondID

	foreignH := buildSessionCredential(t, cred.Challenge, &payload)

	// Unlike a replay against its own session, this is refused outright.
	require.False(t, auth.Accept(&foreignH, "test-service"))

	first, err := store.GetSession(context.Background(), firstID)
	require.NoError(t, err)
	require.Equal(t, int64(500), first.DepositSats)

	second, err := store.GetSession(context.Background(), secondID)
	require.NoError(t, err)
	require.Equal(t, int64(400), second.DepositSats)

	require.Equal(t, firstID, store.creditOwner(t, topUpHash))
}

// TestSessionDepositCannotBeReplayedAsTopUp asserts that the deposit which
// opened a session cannot be handed back as a top-up on it. A session challenge
// says nothing about which action it is for, since the action lives in the
// payload the HMAC does not cover, so the credential that opens a session is
// also a well-formed top-up credential for it. Re-presenting it doubled the
// deposit for the price of one payment.
func TestSessionDepositCannotBeReplayedAsTopUp(t *testing.T) {
	auth, store, _, hmacSecret := newTestSessionAuth(t)

	sessionID, challenge, preimage := openTestSession(
		t, auth, hmacSecret, 300,
	)

	replayPayload := &mpp.SessionPayload{
		Action:        mpp.SessionActionTopUp,
		SessionID:     sessionID,
		TopUpPreimage: hex.EncodeToString(preimage[:]),
	}
	replayH := buildSessionCredential(t, challenge, replayPayload)

	require.True(t, auth.Accept(&replayH, "test-service"))

	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, int64(300), session.DepositSats)
}

// TestSessionTopUpCannotOpenSecondSession asserts the mirror image: a payment
// already credited as a top-up cannot then be used to open a session of its
// own. The same challenge serves both actions, so without the deposit and the
// top-up drawing on one record of spent payments, a buyer could pay one invoice
// and end up with a topped-up session and a fresh one besides.
func TestSessionTopUpCannotOpenSecondSession(t *testing.T) {
	auth, store, _, hmacSecret := newTestSessionAuth(t)

	sessionID, _, _ := openTestSession(t, auth, hmacSecret, 300)

	// Mint a top-up, but keep the preimage so it can be replayed as an
	// open.
	preimage, paymentHash := testPreimageAndHash(t)
	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	challenge, topUpID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 200,
	)
	topUpH := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:        mpp.SessionActionTopUp,
		SessionID:     sessionID,
		TopUpPreimage: hex.EncodeToString(preimage[:]),
	})
	require.True(t, auth.Accept(&topUpH, "test-service"))

	openH := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	})
	require.False(t, auth.Accept(&openH, "test-service"))

	_, err := store.GetSession(context.Background(), topUpID)
	require.Error(t, err)

	session, err := store.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, int64(500), session.DepositSats)
}
