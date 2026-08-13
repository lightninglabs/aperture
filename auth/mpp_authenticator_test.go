package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// testRealm is the protection space every charge test issues challenges in.
const testRealm = "api.example.com"

// testHMACSecret is the server secret the charge tests bind challenges with.
var testHMACSecret = []byte("test-hmac-secret-key-32-bytes!!")

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

// mockChargeStore is an in-memory ChargeStore. The claim is taken under a
// mutex, which stands in for the unique index the real store claims against:
// what matters to the authenticator is that exactly one of any number of
// simultaneous claims on a hash comes back true.
type mockChargeStore struct {
	mu sync.Mutex

	// consumed maps a spent payment hash to the expiry of the challenge
	// that named it.
	consumed map[lntypes.Hash]time.Time

	// failWith, when set, is returned by every call, standing in for a
	// database the proxy cannot reach.
	failWith error
}

var _ ChargeStore = (*mockChargeStore)(nil)

func newMockChargeStore() *mockChargeStore {
	return &mockChargeStore{
		consumed: make(map[lntypes.Hash]time.Time),
	}
}

// ConsumeCharge claims the hash for the caller, reporting false to everyone
// after the first.
func (m *mockChargeStore) ConsumeCharge(_ context.Context,
	paymentHash lntypes.Hash, _ string, expiresAt time.Time) (bool, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failWith != nil {
		return false, m.failWith
	}

	if _, ok := m.consumed[paymentHash]; ok {
		return false, nil
	}

	m.consumed[paymentHash] = expiresAt

	return true, nil
}

// PruneConsumedCharges drops the records of challenges that expired before the
// given instant.
func (m *mockChargeStore) PruneConsumedCharges(_ context.Context,
	expiredBefore time.Time) (int64, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failWith != nil {
		return 0, m.failWith
	}

	var pruned int64
	for hash, expiresAt := range m.consumed {
		if expiresAt.Before(expiredBefore) {
			delete(m.consumed, hash)
			pruned++
		}
	}

	return pruned, nil
}

// isConsumed reports whether the store still remembers the hash.
func (m *mockChargeStore) isConsumed(paymentHash lntypes.Hash) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.consumed[paymentHash]

	return ok
}

// newTestChargeAuth builds a charge authenticator over a fresh in-memory
// consumption store, and returns both.
func newTestChargeAuth(t *testing.T, challenger mint.Challenger,
	checker InvoiceChecker) (*MPPAuthenticator, *mockChargeStore) {

	t.Helper()

	store := newMockChargeStore()
	auth, err := NewMPPAuthenticator(
		challenger, checker, testRealm, testHMACSecret, "regtest", nil,
		store,
	)
	require.NoError(t, err)

	return auth, store
}

// testPreimageAndHash generates a random preimage and its hash for testing.
func testPreimageAndHash(t require.TestingT) (lntypes.Preimage, lntypes.Hash) {
	if h, ok := t.(interface{ Helper() }); ok {
		h.Helper()
	}

	var preimage lntypes.Preimage
	_, err := rand.Read(preimage[:])
	require.NoError(t, err)
	return preimage, sha256.Sum256(preimage[:])
}

// testExpiry returns an expiry far enough ahead that a challenge carrying it is
// still live.
func testExpiry() string {
	return time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)
}

// testChargeChallenge builds the challenge the server would have issued for the
// given payment hash, bound with the test HMAC secret and carrying the given
// expiry.
func testChargeChallenge(t require.TestingT, paymentHash lntypes.Hash,
	expires string) mpp.ChallengeEcho {

	if h, ok := t.(interface{ Helper() }); ok {
		h.Helper()
	}

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
		Realm:   testRealm,
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
		Expires: expires,
	}
	params.ID = mpp.ComputeChallengeID(testHMACSecret, params)

	return mpp.ChallengeEcho{
		ID:      params.ID,
		Realm:   params.Realm,
		Method:  params.Method,
		Intent:  params.Intent,
		Request: params.Request,
		Expires: params.Expires,
	}
}

// buildTestCredential creates a properly encoded MPP credential for testing.
func buildTestCredential(t require.TestingT, challenge mpp.ChallengeEcho,
	preimage lntypes.Preimage) http.Header {

	if h, ok := t.(interface{ Helper() }); ok {
		h.Helper()
	}

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

// paidCredential mints a settled payment and returns a credential for it, which
// is what an honest buyer holds after paying a 402.
func paidCredential(t require.TestingT,
	checker *mockInvoiceChecker) (http.Header, lntypes.Hash) {

	preimage, paymentHash := testPreimageAndHash(t)
	checker.settledHashes[paymentHash] = true

	challenge := testChargeChallenge(t, paymentHash, testExpiry())

	return buildTestCredential(t, challenge, preimage), paymentHash
}

// TestMPPAuthenticatorAcceptValid verifies that a valid charge credential is
// accepted.
func TestMPPAuthenticatorAcceptValid(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth, _ := newTestChargeAuth(t, &mockChallenger{}, checker)

	challenge := testChargeChallenge(t, paymentHash, testExpiry())
	h := buildTestCredential(t, challenge, preimage)

	result := auth.Accept(&h, "test-service")
	require.True(t, result)
}

// TestMPPAuthenticatorAcceptInvalidPreimage verifies that a credential with a
// wrong preimage is rejected.
func TestMPPAuthenticatorAcceptInvalidPreimage(t *testing.T) {
	_, paymentHash := testPreimageAndHash(t)

	// Use a different preimage that doesn't match.
	var wrongPreimage lntypes.Preimage
	_, err := rand.Read(wrongPreimage[:])
	require.NoError(t, err)

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	challenge := testChargeChallenge(t, paymentHash, testExpiry())
	h := buildTestCredential(t, challenge, wrongPreimage)

	result := auth.Accept(&h, "test-service")
	require.False(t, result)

	// A credential that never proved the payment must not spend it either,
	// or anyone could burn a challenge they had not paid for.
	require.False(t, store.isConsumed(paymentHash))
}

// TestMPPAuthenticatorAcceptInvalidHMAC verifies that a credential with a
// tampered challenge ID is rejected.
func TestMPPAuthenticatorAcceptInvalidHMAC(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	// Use a fake challenge ID.
	challenge := testChargeChallenge(t, paymentHash, testExpiry())
	challenge.ID = "tampered-challenge-id"
	h := buildTestCredential(t, challenge, preimage)

	result := auth.Accept(&h, "test-service")
	require.False(t, result)
	require.False(t, store.isConsumed(paymentHash))
}

// TestMPPAuthenticatorAcceptExpired verifies that an expired challenge is
// rejected.
func TestMPPAuthenticatorAcceptExpired(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	// Set expires to the past.
	pastExpiry := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)

	challenge := testChargeChallenge(t, paymentHash, pastExpiry)
	h := buildTestCredential(t, challenge, preimage)

	result := auth.Accept(&h, "test-service")
	require.False(t, result)

	// An expired challenge is turned away before the store is reached,
	// which is what lets a consumption record be dropped once its expiry is
	// far enough in the past.
	require.False(t, store.isConsumed(paymentHash))
}

// TestMPPAuthenticatorAcceptMissingExpiry asserts that a challenge carrying no
// expiry at all is refused rather than being honoured forever.
//
// The server this code runs always stamps an expiry, and the expiry is covered
// by the challenge HMAC, so such a credential describes a challenge it never
// issued. Accepting it would also leave a consumption record that could never
// safely be pruned, since there would be no moment after which the credential
// was certain to be refused on its own.
func TestMPPAuthenticatorAcceptMissingExpiry(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	challenge := testChargeChallenge(t, paymentHash, "")
	h := buildTestCredential(t, challenge, preimage)

	require.False(t, auth.Accept(&h, "test-service"))
	require.False(t, store.isConsumed(paymentHash))
}

// TestMPPAuthenticatorAcceptUnsettled verifies that a credential for an
// unsettled invoice is rejected.
func TestMPPAuthenticatorAcceptUnsettled(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)

	// Don't mark the invoice as settled.
	checker := newMockInvoiceChecker()

	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	challenge := testChargeChallenge(t, paymentHash, testExpiry())
	h := buildTestCredential(t, challenge, preimage)

	result := auth.Accept(&h, "test-service")
	require.False(t, result)

	// The payment is not spent, so a buyer answered while its HTLC was
	// still settling can present the very same credential again once the
	// invoice shows up as settled.
	require.False(t, store.isConsumed(paymentHash))

	checker.settledHashes[paymentHash] = true
	require.True(t, auth.Accept(&h, "test-service"))
}

// TestMPPAuthenticatorAcceptNonPayment verifies that non-Payment auth headers
// are silently ignored.
func TestMPPAuthenticatorAcceptNonPayment(t *testing.T) {
	auth, _ := newTestChargeAuth(
		t, &mockChallenger{}, newMockInvoiceChecker(),
	)

	// L402 header should be silently rejected.
	h := make(http.Header)
	h.Set("Authorization", "L402 abc123:deadbeef")
	require.False(t, auth.Accept(&h, "test-service"))

	// No header at all.
	h2 := make(http.Header)
	require.False(t, auth.Accept(&h2, "test-service"))
}

// TestMPPAuthenticatorRequiresChargeStore asserts that the authenticator
// refuses to exist without somewhere to record spent payments. Everything else
// it checks is a property of the payment rather than of the request carrying
// it, so an authenticator with nowhere to write could not tell a first
// presentation from a replay, and one payment would buy unlimited service.
func TestMPPAuthenticatorRequiresChargeStore(t *testing.T) {
	auth, err := NewMPPAuthenticator(
		&mockChallenger{}, newMockInvoiceChecker(), testRealm,
		testHMACSecret, "regtest", nil, nil,
	)
	require.Error(t, err)
	require.Nil(t, auth)
}

// TestMPPAuthenticatorFreshChallengeHeader verifies that a valid challenge
// header is generated.
func TestMPPAuthenticatorFreshChallengeHeader(t *testing.T) {
	preimage, paymentHash := testPreimageAndHash(t)
	_ = preimage

	auth, _ := newTestChargeAuth(
		t,
		&mockChallenger{
			paymentRequest: "lnbcrt1000n1ptest...",
			paymentHash:    paymentHash,
		},
		newMockInvoiceChecker(),
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
	require.Equal(t, testRealm, params.Realm)
	require.Equal(t, mpp.MethodLightning, params.Method)
	require.Equal(t, mpp.IntentCharge, params.Intent)
	require.NotEmpty(t, params.ID)
	require.NotEmpty(t, params.Request)

	// Every challenge carries an expiry, which is what bounds how long a
	// record of its consumption has to be kept.
	require.NotEmpty(t, params.Expires)
	expiresAt, err := time.Parse(time.RFC3339, params.Expires)
	require.NoError(t, err)
	require.WithinDuration(
		t, time.Now().Add(defaultChallengeExpiry), expiresAt,
		time.Minute,
	)

	// Verify the HMAC ID is valid.
	require.True(
		t, mpp.VerifyChallengeID(testHMACSecret, params, params.ID),
	)

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

	auth, _ := newTestChargeAuth(
		t, &mockChallenger{}, newMockInvoiceChecker(),
	)

	challenge := testChargeChallenge(t, paymentHash, testExpiry())
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
	require.Equal(t, challenge.ID, receipt.ChallengeID)
}

// TestMPPAuthenticatorAcceptWrongIntent verifies that a session intent
// credential is not accepted by the charge authenticator.
func TestMPPAuthenticatorAcceptWrongIntent(t *testing.T) {
	auth, _ := newTestChargeAuth(
		t, &mockChallenger{}, newMockInvoiceChecker(),
	)

	// Build a credential with session intent.
	cred := &mpp.Credential{
		Challenge: mpp.ChallengeEcho{
			ID:      "some-id",
			Realm:   testRealm,
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

	checker := newMockInvoiceChecker()
	checker.settledHashes[paymentHash] = true

	auth, _ := newTestChargeAuth(
		t,
		&mockChallenger{
			paymentRequest: "lnbcrt1000n1ptest...",
			paymentHash:    paymentHash,
		},
		checker,
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

// TestMPPAuthenticatorRefusesReplay asserts the property the whole store exists
// for: one paid invoice authorizes exactly one request. Nothing in the
// credential distinguishes its first presentation from its thousandth, so
// without a record of what has been spent the same bytes buy service forever.
func TestMPPAuthenticatorRefusesReplay(t *testing.T) {
	checker := newMockInvoiceChecker()
	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	credHeader, paymentHash := paidCredential(t, checker)

	require.True(t, auth.Accept(&credHeader, "test-service"))
	require.True(t, store.isConsumed(paymentHash))

	// Every later presentation of the identical credential is refused, on
	// this service and on any other.
	for i := 0; i < 10; i++ {
		require.False(t, auth.Accept(&credHeader, "test-service"))
		require.False(t, auth.Accept(&credHeader, "other-service"))
	}
}

// TestMPPAuthenticatorDistinctPaymentsStillWork asserts the guard refuses
// replays rather than refusing service. A second, separately paid credential
// must still be honoured, including one issued after the first was spent.
func TestMPPAuthenticatorDistinctPaymentsStillWork(t *testing.T) {
	checker := newMockInvoiceChecker()
	auth, _ := newTestChargeAuth(t, &mockChallenger{}, checker)

	first, _ := paidCredential(t, checker)
	require.True(t, auth.Accept(&first, "test-service"))
	require.False(t, auth.Accept(&first, "test-service"))

	second, _ := paidCredential(t, checker)
	require.True(t, auth.Accept(&second, "test-service"))
	require.False(t, auth.Accept(&second, "test-service"))

	// And the first is still refused, so spending the second did not clear
	// the record of the first.
	require.False(t, auth.Accept(&first, "test-service"))
}

// TestMPPAuthenticatorConcurrentReplays asserts that consumption and acceptance
// are one act. This is the case a check-then-act implementation loses: every
// goroutine reads the payment as unspent, every one of them decides it is the
// first, and one payment buys as many requests as there are threads.
func TestMPPAuthenticatorConcurrentReplays(t *testing.T) {
	checker := newMockInvoiceChecker()
	auth, _ := newTestChargeAuth(t, &mockChallenger{}, checker)

	credHeader, _ := paidCredential(t, checker)

	const presentations = 32

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted int

		// start holds every goroutine at the line until they can be
		// released together, so the presentations genuinely overlap
		// rather than trickling through one after another.
		start = make(chan struct{})
	)

	for i := 0; i < presentations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start

			// Each goroutine presents its own copy of the very same
			// bytes, as separate requests would.
			header := credHeader.Clone()
			ok := auth.Accept(&header, "test-service")

			mu.Lock()
			defer mu.Unlock()

			if ok {
				accepted++
			}
		}()
	}

	close(start)
	wg.Wait()

	require.Equal(t, 1, accepted)
}

// TestMPPAuthenticatorFailsClosedOnStoreError asserts that a proxy that cannot
// reach its store refuses the request rather than serving it. A store that
// cannot answer is a store that cannot tell a first presentation from a replay,
// and guessing in the buyer's favour is how one payment becomes unlimited
// service during an outage.
func TestMPPAuthenticatorFailsClosedOnStoreError(t *testing.T) {
	checker := newMockInvoiceChecker()
	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	credHeader, _ := paidCredential(t, checker)

	store.failWith = fmt.Errorf("database is on fire")
	require.False(t, auth.Accept(&credHeader, "test-service"))

	// The payment was never spent, so the buyer is not out of pocket once
	// the store comes back.
	store.failWith = nil
	require.True(t, auth.Accept(&credHeader, "test-service"))
}

// TestMPPAuthenticatorPruneKeepsLiveRecords asserts that the sweep only drops
// records that can no longer change an answer. A record whose challenge is
// still live must survive it, because dropping that one would let the
// credential naming it be presented a second time.
func TestMPPAuthenticatorPruneKeepsLiveRecords(t *testing.T) {
	checker := newMockInvoiceChecker()
	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	// One credential whose challenge is still live.
	live, liveHash := paidCredential(t, checker)
	require.True(t, auth.Accept(&live, "test-service"))

	// And one whose challenge expired long ago. It was spent while it was
	// still live, which is why its record is there at all.
	stalePreimage, staleHash := testPreimageAndHash(t)
	checker.settledHashes[staleHash] = true
	staleExpiry := time.Now().Add(-2 * time.Hour).UTC()
	stale := buildTestCredential(t, testChargeChallenge(
		t, staleHash, staleExpiry.Format(time.RFC3339),
	), stalePreimage)

	consumed, err := store.ConsumeCharge(
		context.Background(), staleHash, "stale-challenge", staleExpiry,
	)
	require.NoError(t, err)
	require.True(t, consumed)

	auth.prune()

	// The expired record is gone and the live one is not.
	require.False(t, store.isConsumed(staleHash))
	require.True(t, store.isConsumed(liveHash))

	// Losing the expired record cannot resurrect its credential, because
	// the challenge is refused on its own expiry.
	require.False(t, auth.Accept(&stale, "test-service"))

	// The live credential is still refused a second time, which is what the
	// surviving record buys.
	require.False(t, auth.Accept(&live, "test-service"))
}

// TestMPPAuthenticatorPruneRespectsRetentionMargin asserts that a record is not
// dropped the instant its challenge expires. The clock that decides a challenge
// has expired and the clock that decides a record may go need not be the same
// one, so the sweep keeps a record for a margin past the expiry.
func TestMPPAuthenticatorPruneRespectsRetentionMargin(t *testing.T) {
	checker := newMockInvoiceChecker()
	auth, store := newTestChargeAuth(t, &mockChallenger{}, checker)

	_, paymentHash := testPreimageAndHash(t)

	// A challenge that expired a moment ago, well inside the margin.
	justExpired := time.Now().Add(-time.Second).UTC()
	consumed, err := store.ConsumeCharge(
		context.Background(), paymentHash, "recent", justExpired,
	)
	require.NoError(t, err)
	require.True(t, consumed)

	auth.prune()
	require.True(t, store.isConsumed(paymentHash))

	// Only once the margin has gone by as well does the record become
	// eligible.
	auth.retentionMargin = 0
	auth.prune()
	require.False(t, store.isConsumed(paymentHash))
}

// TestMPPAuthenticatorPruneLoopStops asserts the sweep is a goroutine the proxy
// can shut down, and that stopping it twice is not an error.
func TestMPPAuthenticatorPruneLoopStops(t *testing.T) {
	auth, store := newTestChargeAuth(
		t, &mockChallenger{}, newMockInvoiceChecker(),
	)

	_, paymentHash := testPreimageAndHash(t)
	consumed, err := store.ConsumeCharge(
		context.Background(), paymentHash, "old",
		time.Now().Add(-24*time.Hour).UTC(),
	)
	require.NoError(t, err)
	require.True(t, consumed)

	// The loop sweeps once on entry, so the long expired record goes
	// without waiting for a tick.
	auth.Start()
	require.Eventually(t, func() bool {
		return !store.isConsumed(paymentHash)
	}, time.Second, 10*time.Millisecond)

	auth.Stop()
	auth.Stop()
}

// TestMPPAuthenticatorAcceptsOncePerPayment is the property behind every case
// above: over any interleaving of first presentations and replays, the number
// of requests served equals the number of distinct payments made. A guard that
// refuses too much would serve fewer, and the replay this fixes served more.
func TestMPPAuthenticatorAcceptsOncePerPayment(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		checker := newMockInvoiceChecker()

		store := newMockChargeStore()
		auth, err := NewMPPAuthenticator(
			&mockChallenger{}, checker, testRealm, testHMACSecret,
			"regtest", nil, store,
		)
		require.NoError(rt, err)

		// A handful of separately paid credentials, and a sequence of
		// presentations drawn freely from them, so a credential can
		// appear before, after and among its own replays.
		numPayments := rapid.IntRange(1, 6).Draw(rt, "numPayments")

		creds := make([]http.Header, numPayments)
		for i := range creds {
			creds[i], _ = paidCredential(rt, checker)
		}

		presentations := rapid.SliceOfN(
			rapid.IntRange(0, numPayments-1), 1, 40,
		).Draw(rt, "presentations")

		var (
			accepted int
			seen     = make(map[int]bool)
		)
		for _, idx := range presentations {
			header := creds[idx].Clone()
			if auth.Accept(&header, "test-service") {
				accepted++

				// A payment must never be served twice, so the
				// first acceptance of each is the only one.
				require.False(rt, seen[idx])
				seen[idx] = true
			}
		}

		// Exactly the payments that were presented at all were served,
		// each of them once.
		require.Equal(rt, len(seen), accepted)
		for _, idx := range presentations {
			require.True(rt, seen[idx])
		}
	})
}

// TestMPPAuthenticatorReusableChargeOnMeteredService verifies that the
// reusable-charge policy turns a repeat presentation from a refused replay
// into an accepted request, and only on the services the policy names.
//
// On a metered service the credential is the key to a prepaid usage bundle:
// the metering pipeline debits every request against the bundle and refuses
// when it runs dry, so the draw-down is what bounds the spend and the
// credential has to stay presentable. Everywhere else the spec's single-use
// rule stands.
func TestMPPAuthenticatorReusableChargeOnMeteredService(t *testing.T) {
	checker := newMockInvoiceChecker()
	auth, _ := newTestChargeAuth(t, &mockChallenger{}, checker)

	auth.SetReusableChargePolicy(func(serviceName string) bool {
		return serviceName == "metered-service"
	})

	// On the metered service, the same credential keeps working: the
	// second presentation is the buyer spending the rest of its bundle.
	h, _ := paidCredential(t, checker)
	require.True(t, auth.Accept(&h, "metered-service"))
	require.True(t, auth.Accept(&h, "metered-service"))

	// On a service the policy does not name, the strict rule stands: the
	// first presentation spends the payment and the second is a replay.
	h2, _ := paidCredential(t, checker)
	require.True(t, auth.Accept(&h2, "strict-service"))
	require.False(t, auth.Accept(&h2, "strict-service"))
}

// TestMPPAuthenticatorNilReusablePolicyStaysStrict verifies that clearing the
// policy restores the single-use rule everywhere, so a deployment that
// removes its metered services does not keep serving replays.
func TestMPPAuthenticatorNilReusablePolicyStaysStrict(t *testing.T) {
	checker := newMockInvoiceChecker()
	auth, _ := newTestChargeAuth(t, &mockChallenger{}, checker)

	auth.SetReusableChargePolicy(func(string) bool { return true })
	auth.SetReusableChargePolicy(nil)

	h, _ := paidCredential(t, checker)
	require.True(t, auth.Accept(&h, "metered-service"))
	require.False(t, auth.Accept(&h, "metered-service"))
}
