package auth

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"testing"

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

	auth, _, _, hmacSecret := newTestSessionAuth(t)
	preimage, paymentHash := testPreimageAndHash(t)

	challenge, sessionID := buildSessionChallenge(
		t, hmacSecret, paymentHash, 300,
	)

	bearer := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:    mpp.SessionActionBearer,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	})

	gotID, charged, ok := auth.BearerSessionID(&bearer)
	require.True(t, ok)
	require.Equal(t, sessionID, gotID)

	// The amount is the one the server minted into the challenge, which is
	// what handleBearer deducted.
	require.Equal(t, int64(2), charged)

	// An open credential is not a draw-down and must not be settled.
	open := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:        mpp.SessionActionOpen,
		Preimage:      hex.EncodeToString(preimage[:]),
		ReturnInvoice: testReturnInvoice(t, paymentHash),
	})
	_, _, ok = auth.BearerSessionID(&open)
	require.False(t, ok)

	// Neither is a close.
	closeCred := buildSessionCredential(t, challenge, &mpp.SessionPayload{
		Action:    mpp.SessionActionClose,
		SessionID: sessionID,
		Preimage:  hex.EncodeToString(preimage[:]),
	})
	_, _, ok = auth.BearerSessionID(&closeCred)
	require.False(t, ok)

	// And neither is a header carrying no MPP credential at all.
	empty := make(http.Header)
	_, _, ok = auth.BearerSessionID(&empty)
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
