package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/mpp"
	"github.com/stretchr/testify/require"
)

// decodeSessionChallengeAmounts pulls the per-unit amount and the deposit out
// of a freshly minted session challenge header.
func decodeSessionChallengeAmounts(t *testing.T,
	header http.Header) (int64, int64) {

	t.Helper()

	var sessionValue string
	for _, value := range header.Values("WWW-Authenticate") {
		if len(value) > len(mpp.AuthScheme) &&
			value[:len(mpp.AuthScheme)] == mpp.AuthScheme {

			sessionValue = value
		}
	}
	require.NotEmpty(t, sessionValue)

	params, err := mpp.ParseChallengeHeader(sessionValue)
	require.NoError(t, err)

	var sessReq mpp.SessionRequest
	require.NoError(t, mpp.DecodeRequest(params.Request, &sessReq))

	amount, err := strconv.ParseInt(sessReq.Amount, 10, 64)
	require.NoError(t, err)

	deposit, err := strconv.ParseInt(sessReq.DepositAmount, 10, 64)
	require.NoError(t, err)

	return amount, deposit
}

// TestSessionChallengeUsesSessionPrice asserts the per-unit amount a session
// challenge advertises comes from the session-aware quote, not from the
// one-shot charge price.
//
// On a metered service those two numbers differ by orders of magnitude: the
// charge price buys a whole token bundle, while a session's unit price is the
// cost of one request. Quoting the bundle price here would drain a deposit on
// its first bearer request.
func TestSessionChallengeUsesSessionPrice(t *testing.T) {
	t.Parallel()

	auth, _, _, _ := newTestSessionAuth(t)

	header, err := auth.FreshChallengeHeaderWithPrices(
		"test-service", ChallengePrices{
			Charge:      11_500,
			SessionUnit: 7,
		},
	)
	require.NoError(t, err)

	amount, deposit := decodeSessionChallengeAmounts(t, header)
	require.Equal(t, int64(7), amount)

	// The deposit still follows the configured multiplier, applied to the
	// session price rather than the charge price.
	require.Equal(t, int64(7*20), deposit)
}

// TestSessionChallengeFallsBackToChargePrice asserts a pricer with no opinion
// about sessions leaves the challenge exactly where it was before this seam
// existed, quoting the one-shot charge price.
func TestSessionChallengeFallsBackToChargePrice(t *testing.T) {
	t.Parallel()

	auth, _, _, _ := newTestSessionAuth(t)

	withPrices, err := auth.FreshChallengeHeaderWithPrices(
		"test-service", ChallengePrices{Charge: 300},
	)
	require.NoError(t, err)

	amount, deposit := decodeSessionChallengeAmounts(t, withPrices)
	require.Equal(t, int64(300), amount)
	require.Equal(t, int64(300*20), deposit)

	// The single-price entry point has to agree with it, since that is what
	// an authenticator stack with no session pricer keeps calling.
	plain, err := auth.FreshChallengeHeader("test-service", 300)
	require.NoError(t, err)

	plainAmount, plainDeposit := decodeSessionChallengeAmounts(t, plain)
	require.Equal(t, amount, plainAmount)
	require.Equal(t, deposit, plainDeposit)
}

// TestSessionChallengeDepositOverride asserts a pricer that names a deposit
// wins over the configured multiplier. How much a session must hold to be worth
// opening is something the pricer can know and the multiplier cannot.
func TestSessionChallengeDepositOverride(t *testing.T) {
	t.Parallel()

	auth, _, _, _ := newTestSessionAuth(t)

	header, err := auth.FreshChallengeHeaderWithPrices(
		"test-service", ChallengePrices{
			Charge:         500,
			SessionUnit:    9,
			SessionDeposit: 4321,
		},
	)
	require.NoError(t, err)

	amount, deposit := decodeSessionChallengeAmounts(t, header)
	require.Equal(t, int64(9), amount)
	require.Equal(t, int64(4321), deposit)
}

// TestBearerSessionID asserts the proxy can recover the session and the amount
// already charged from a bearer credential, and that it recovers nothing from
// anything else.
func TestBearerSessionID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	auth, _, _, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)

	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 300,
	)

	open := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	})
	require.True(t, auth.Accept(&open, "test-service"))

	bearer := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	})

	gotID, charged, ok := auth.BearerSessionID(ctx, &bearer)
	require.True(t, ok)
	require.Equal(t, sessionID, gotID)

	// The amount is the one the server minted into the challenge, which is
	// what handleBearer deducted.
	require.Equal(t, int64(2), charged)

	// An open credential is not a draw-down and must not be settled.
	_, _, ok = auth.BearerSessionID(ctx, &open)
	require.False(t, ok)

	// Neither is a close.
	closeCred := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:    mpp.SessionActionClose,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	})
	_, _, ok = auth.BearerSessionID(ctx, &closeCred)
	require.False(t, ok)

	// And neither is a header carrying no MPP credential at all.
	empty := make(http.Header)
	_, _, ok = auth.BearerSessionID(ctx, &empty)
	require.False(t, ok)
}

// TestBearerSessionIDRefusesUnprovenCredentials asserts that the settlement
// seam verifies the credential itself rather than assuming authentication
// already vouched for it.
//
// A request can carry credentials for several schemes at once and only one of
// them has to be the one that passed. Without these checks, a caller holding
// any valid credential could attach a session credential naming a stranger's
// session and have this request's cost settled against that stranger's balance.
func TestBearerSessionIDRefusesUnprovenCredentials(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	auth, store, _, hmacSecret := newTestSessionAuth(t)

	victimPreimage, victimHash := testPreimageAndHash(t)
	auth.checker.(*mockInvoiceChecker).settledHashes[victimHash] = true

	challenge, victimSession := buildSessionChallenge(
		t, hmacSecret, victimHash, 300,
	)
	open := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(victimPreimage[:]),
		ReturnInvoice: testReturnInvoice(t, victimHash),
	})
	require.True(t, auth.Accept(&open, "test-service"))

	// An attacker holding a server-minted challenge but not the victim's
	// deposit preimage cannot name the victim's session.
	var wrongPreimage [32]byte
	wrongPreimage[0] = 0xff

	forged := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: victimSession,
		Preimage:  hex.EncodeToString(wrongPreimage[:]),
	})
	_, _, ok := auth.BearerSessionID(ctx, &forged)
	require.False(t, ok)

	// Neither can a credential whose challenge this server never minted,
	// which is what carries the amount already deducted.
	_, otherHmac := testPreimageAndHash(t)
	unsigned, _ := buildSessionChallenge(
		t, []byte("a-different-servers-secret-key!"), otherHmac, 300,
	)
	unsigned.ID = challenge.ID
	unsignedCred := buildSessionCredential(t, unsigned, &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: victimSession,
		Preimage:  hex.EncodeToString(victimPreimage[:]),
	})
	_, _, ok = auth.BearerSessionID(ctx, &unsignedCred)
	require.False(t, ok)

	// And a closed session settles nothing further.
	_, err := store.CloseSessionAndGetBalance(ctx, victimSession)
	require.NoError(t, err)

	legit := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: victimSession,
		Preimage:  hex.EncodeToString(victimPreimage[:]),
	})
	_, _, ok = auth.BearerSessionID(ctx, &legit)
	require.False(t, ok)
}

// TestSettleSessionRequest asserts post-hoc reconciliation moves the balance in
// both directions and leaves it alone when the estimate was exact.
func TestSettleSessionRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name      string
		charged   int64
		actual    int64
		wantSpent int64
	}{{
		name:      "under estimate charges the shortfall",
		charged:   50,
		actual:    130,
		wantSpent: 130,
	}, {
		name:      "over estimate gives back the excess",
		charged:   200,
		actual:    75,
		wantSpent: 75,
	}, {
		name:      "exact estimate moves nothing",
		charged:   100,
		actual:    100,
		wantSpent: 100,
	}, {
		name:      "a free response gives the whole charge back",
		charged:   100,
		actual:    0,
		wantSpent: 0,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			auth, store, _, _ := newTestSessionAuth(t)

			const sessionID = "session-under-test"
			require.NoError(t, store.CreateSession(ctx, &Session{
				SessionID:   sessionID,
				DepositSats: 10_000,
				SpentSats:   tc.charged,
				Status:      "open",
			}))

			require.NoError(t, auth.SettleSessionRequest(
				ctx, sessionID, tc.charged, tc.actual,
			))

			session, err := store.GetSession(ctx, sessionID)
			require.NoError(t, err)
			require.Equal(t, tc.wantSpent, session.SpentSats)
		})
	}
}

// TestSettleSessionRequestOverdraw asserts that a request costing more than the
// session had left settles to the whole balance rather than failing or going
// negative. This is the bounded loss post-hoc reconciliation accepts on
// purpose: the service has already been rendered, so the seller absorbs the
// difference on at most one in-flight request.
func TestSettleSessionRequestOverdraw(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	auth, store, _, _ := newTestSessionAuth(t)

	const sessionID = "nearly-empty"
	require.NoError(t, store.CreateSession(ctx, &Session{
		SessionID:   sessionID,
		DepositSats: 100,
		SpentSats:   90,
		Status:      "open",
	}))

	require.NoError(t, auth.SettleSessionRequest(ctx, sessionID, 90, 5_000))

	session, err := store.GetSession(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, int64(100), session.SpentSats)

	// A close now refunds nothing, and refunds nothing negative.
	refund, err := store.CloseSessionAndGetBalance(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, int64(0), refund)
}

// TestSettleSessionRequestUnknownSession asserts a settlement against a session
// the store does not hold surfaces as an error rather than passing silently.
func TestSettleSessionRequestUnknownSession(t *testing.T) {
	t.Parallel()

	auth, _, _, _ := newTestSessionAuth(t)

	err := auth.SettleSessionRequest(
		context.Background(), "no-such-session", 10, 20,
	)
	require.Error(t, err)

	// A no-op settlement never reaches the store, so it cannot fail on a
	// session that is not there. That is deliberate: the common case of an
	// exact estimate should not cost a database round trip.
	require.NoError(t, auth.SettleSessionRequest(
		context.Background(), "no-such-session", 10, 10,
	))
}

// closeSessionForRefund opens a session, spends part of it, and closes it,
// returning the receipt header the close produced.
func closeSessionForRefund(t *testing.T, auth *MPPSessionAuthenticator,
	hmacSecret []byte, spend int64) http.Header {

	t.Helper()

	const deposit = 300

	preimage, paymentHash := testPreimageAndHash(t)
	auth.checker.(*mockInvoiceChecker).settledHashes[paymentHash] = true

	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, deposit,
	)

	open := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	})
	require.True(t, auth.Accept(&open, "test-service"))

	if spend > 0 {
		require.NoError(t, auth.sessionStore.DeductSessionBalance(
			context.Background(), sessionID, spend,
		))
	}

	closeCred := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:    mpp.SessionActionClose,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	})
	require.True(t, auth.Accept(&closeCred, "test-service"))

	return closeCred
}

// receiptRefund decodes the refund amount and status out of a session receipt.
func receiptRefund(t *testing.T, auth *MPPSessionAuthenticator,
	closeCred http.Header) (int64, string) {

	t.Helper()

	header := auth.ReceiptHeader(&closeCred, "test-service")
	require.NotNil(t, header)

	raw, err := mpp.Base64URLDecode(header.Get(mpp.HeaderPaymentReceipt))
	require.NoError(t, err)

	var receipt mpp.SessionReceipt
	require.NoError(t, json.Unmarshal(raw, &receipt))

	return receipt.RefundSats, receipt.RefundStatus
}

// TestSessionRefundStatusSkipped asserts that a session closed with nothing
// left reports "skipped": no refund was owed, so none was attempted. Before
// this it reported an out-of-spec "pending" that no client could interpret.
func TestSessionRefundStatusSkipped(t *testing.T) {
	auth, _, sender, hmacSecret := newTestSessionAuth(t)

	closeCred := closeSessionForRefund(t, auth, hmacSecret, 300)

	refund, status := receiptRefund(t, auth, closeCred)
	require.EqualValues(t, 0, refund)
	require.Equal(t, mpp.RefundStatusSkipped, status)

	// Nothing may be sent for a session with no remainder.
	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Empty(t, sender.payments)
}

// TestSessionRefundStatusSucceeded asserts a refund that goes through reports
// "succeeded" once the payment has settled.
func TestSessionRefundStatusSucceeded(t *testing.T) {
	auth, _, sender, hmacSecret := newTestSessionAuth(t)

	closeCred := closeSessionForRefund(t, auth, hmacSecret, 100)

	require.Eventually(t, func() bool {
		_, status := receiptRefund(t, auth, closeCred)

		return status == mpp.RefundStatusSucceeded
	}, 5*time.Second, 10*time.Millisecond)

	refund, _ := receiptRefund(t, auth, closeCred)
	require.EqualValues(t, 200, refund)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.payments, 1)
	require.EqualValues(t, 200, sender.payments[0].amtSats)
}

// TestSessionRefundStatusFailed asserts a refund the wallet refuses reports
// "failed", and that the amount still owed is carried on the receipt so the
// buyer knows what is being held.
//
// The amountless return invoice a Wavelength-backed seller cannot pay is
// exactly this path, which is why the sender surfaces it as a distinct error
// rather than as a silent send of the wrong amount.
func TestSessionRefundStatusFailed(t *testing.T) {
	auth, _, sender, hmacSecret := newTestSessionAuth(t)
	sender.err = errors.New("wavelength cannot pay an amountless invoice")

	closeCred := closeSessionForRefund(t, auth, hmacSecret, 100)

	require.Eventually(t, func() bool {
		refund, status := receiptRefund(t, auth, closeCred)

		return status == mpp.RefundStatusFailed && refund == 200
	}, 5*time.Second, 10*time.Millisecond)
}

// TestSessionRefundInFlightIsNotClaimedSuccessful asserts that a receipt
// written while the refund is still routing does not claim success.
//
// The spec admits only succeeded, failed and skipped, none of which means
// "still trying", and of those only "failed" is safe to be wrong about. A
// receipt claiming success for a payment that never lands tells the buyer their
// money is back when it is not, and the buyer holds the return invoice either
// way, so a refund settling after the receipt costs them nothing.
func TestSessionRefundInFlightIsNotClaimedSuccessful(t *testing.T) {
	auth, _, sender, hmacSecret := newTestSessionAuth(t)

	// Hold the payment open so the receipt is read mid-flight.
	release := make(chan struct{})
	sender.block = release
	t.Cleanup(func() {
		close(release)
	})

	closeCred := closeSessionForRefund(t, auth, hmacSecret, 100)

	refund, status := receiptRefund(t, auth, closeCred)
	require.EqualValues(t, 200, refund)
	require.Equal(t, mpp.RefundStatusFailed, status)
	require.NotEqual(t, mpp.RefundStatusSucceeded, status)
}
