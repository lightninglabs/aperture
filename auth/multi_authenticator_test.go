package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// mockAuthenticator is a simple mock for testing MultiAuthenticator.
type mockAuthenticator struct {
	acceptResult    bool
	challengeHeader http.Header
	challengeErr    error
}

var _ Authenticator = (*mockAuthenticator)(nil)

func (m *mockAuthenticator) Accept(_ *http.Header, _ string) bool {
	return m.acceptResult
}

func (m *mockAuthenticator) FreshChallengeHeader(_ string,
	_ int64) (http.Header, error) {

	return m.challengeHeader, m.challengeErr
}

// mockAuthWithReceipt is a mock that also implements ReceiptProvider.
type mockAuthWithReceipt struct {
	mockAuthenticator
	receipt http.Header
}

var _ ReceiptProvider = (*mockAuthWithReceipt)(nil)

func (m *mockAuthWithReceipt) ReceiptHeader(_ *http.Header,
	_ string) http.Header {

	return m.receipt
}

// TestMultiAuthenticatorAcceptFirstMatch verifies that Accept returns true on
// the first matching authenticator.
func TestMultiAuthenticatorAcceptFirstMatch(t *testing.T) {
	auth1 := &mockAuthenticator{acceptResult: false}
	auth2 := &mockAuthenticator{acceptResult: true}
	auth3 := &mockAuthenticator{acceptResult: true}

	multi := NewMultiAuthenticator(auth1, auth2, auth3)
	h := make(http.Header)

	result := multi.Accept(&h, "test-service")
	require.True(t, result)
}

// TestMultiAuthenticatorAcceptNoneMatch verifies that Accept returns false
// when no authenticator matches.
func TestMultiAuthenticatorAcceptNoneMatch(t *testing.T) {
	auth1 := &mockAuthenticator{acceptResult: false}
	auth2 := &mockAuthenticator{acceptResult: false}

	multi := NewMultiAuthenticator(auth1, auth2)
	h := make(http.Header)

	result := multi.Accept(&h, "test-service")
	require.False(t, result)
}

// TestMultiAuthenticatorChallengeHeaderMerge verifies that challenge headers
// from all authenticators are merged into the response.
func TestMultiAuthenticatorChallengeHeaderMerge(t *testing.T) {
	auth1 := &mockAuthenticator{
		challengeHeader: http.Header{
			"WWW-Authenticate": []string{
				`LSAT macaroon="abc", invoice="lnbc..."`,
				`L402 macaroon="abc", invoice="lnbc..."`,
			},
		},
	}
	auth2 := &mockAuthenticator{
		challengeHeader: http.Header{
			"WWW-Authenticate": []string{
				`Payment id="xyz", realm="example.com", method="lightning", intent="charge", request="abc"`,
			},
		},
	}

	multi := NewMultiAuthenticator(auth1, auth2)

	header, err := multi.FreshChallengeHeader("test-service", 100)
	require.NoError(t, err)

	// Should have all 3 WWW-Authenticate values.
	values := header.Values("WWW-Authenticate")
	require.Len(t, values, 3)
	require.Contains(t, values[0], "LSAT")
	require.Contains(t, values[1], "L402")
	require.Contains(t, values[2], "Payment")
}

// TestMultiAuthenticatorReceiptDelegation verifies that ReceiptHeader
// delegates to the authenticator that provides a receipt for the credential.
func TestMultiAuthenticatorReceiptDelegation(t *testing.T) {
	receiptHdr := http.Header{
		"Payment-Receipt": []string{"encoded-receipt-data"},
	}

	auth1 := &mockAuthenticator{acceptResult: false}
	auth2 := &mockAuthWithReceipt{
		mockAuthenticator: mockAuthenticator{acceptResult: true},
		receipt:           receiptHdr,
	}

	multi := NewMultiAuthenticator(auth1, auth2)
	h := make(http.Header)

	// Accept should select auth2.
	require.True(t, multi.Accept(&h, "test-service"))

	// ReceiptHeader should find auth2's receipt by trying each provider.
	receipt := multi.ReceiptHeader(&h, "test-service")
	require.NotNil(t, receipt)
	require.Equal(t, "encoded-receipt-data",
		receipt.Get("Payment-Receipt"))
}

// TestMultiAuthenticatorReceiptNoProvider verifies that ReceiptHeader returns
// nil when no authenticator implements ReceiptProvider.
func TestMultiAuthenticatorReceiptNoProvider(t *testing.T) {
	auth1 := &mockAuthenticator{acceptResult: true}
	multi := NewMultiAuthenticator(auth1)
	h := make(http.Header)

	require.True(t, multi.Accept(&h, "test-service"))

	// mockAuthenticator doesn't implement ReceiptProvider, so nil.
	receipt := multi.ReceiptHeader(&h, "test-service")
	require.Nil(t, receipt)
}

// TestMultiAuthenticatorReceiptNilFromProvider verifies that ReceiptHeader
// returns nil when the ReceiptProvider returns nil (e.g., the credential
// type doesn't match what that provider handles).
func TestMultiAuthenticatorReceiptNilFromProvider(t *testing.T) {
	auth1 := &mockAuthWithReceipt{
		mockAuthenticator: mockAuthenticator{acceptResult: true},
		receipt:           nil, // Provider returns nil.
	}

	multi := NewMultiAuthenticator(auth1)
	h := make(http.Header)

	receipt := multi.ReceiptHeader(&h, "test-service")
	require.Nil(t, receipt)
}

// mockPricedAuth is a mock authenticator that knows the difference between the
// one-shot charge price and a session's per-unit price.
type mockPricedAuth struct {
	mockAuthenticator

	gotPrices ChallengePrices
}

var _ PricedChallenger = (*mockPricedAuth)(nil)

func (m *mockPricedAuth) FreshChallengeHeaderWithPrices(_ string,
	prices ChallengePrices) (http.Header, error) {

	m.gotPrices = prices

	return m.challengeHeader, m.challengeErr
}

// mockSettlerAuth is a mock authenticator holding one session balance.
type mockSettlerAuth struct {
	mockAuthenticator

	sessionID string
	charged   int64
	knows     bool

	settledFor string
}

var _ SessionSettler = (*mockSettlerAuth)(nil)

func (m *mockSettlerAuth) BearerSessionID(_ context.Context,
	_ *http.Header) (string, int64, bool) {
	if !m.knows {
		return "", 0, false
	}

	return m.sessionID, m.charged, true
}

func (m *mockSettlerAuth) SettleSessionRequest(_ context.Context,
	sessionID string, _, _ int64) error {

	if sessionID != m.sessionID {
		return fmt.Errorf("unknown session %s", sessionID)
	}

	m.settledFor = sessionID

	return nil
}

// TestMultiAuthenticatorPriceFanOut verifies that each sub-authenticator is
// asked its own pricing question: one that only knows a single price keeps
// receiving the charge price, and one that distinguishes the intents receives
// the whole set.
func TestMultiAuthenticatorPriceFanOut(t *testing.T) {
	plainHeader := make(http.Header)
	plainHeader.Set("WWW-Authenticate", "L402 macaroon=\"abc\"")

	pricedHeader := make(http.Header)
	pricedHeader.Set("WWW-Authenticate", "Payment id=\"xyz\"")

	plain := &mockAuthenticator{challengeHeader: plainHeader}
	priced := &mockPricedAuth{
		mockAuthenticator: mockAuthenticator{
			challengeHeader: pricedHeader,
		},
	}

	multi := NewMultiAuthenticator(plain, priced)

	prices := ChallengePrices{
		Charge:         11_500,
		SessionUnit:    7,
		SessionDeposit: 400,
	}

	merged, err := multi.FreshChallengeHeaderWithPrices("svc", prices)
	require.NoError(t, err)
	require.Len(t, merged.Values("WWW-Authenticate"), 2)
	require.Equal(t, prices, priced.gotPrices)

	// The single-price entry point still reaches the priced authenticator,
	// carrying only the charge price.
	_, err = multi.FreshChallengeHeader("svc", 900)
	require.NoError(t, err)
	require.Equal(t, ChallengePrices{Charge: 900}, priced.gotPrices)
}

// TestMultiAuthenticatorSessionFanOut verifies that the session seam finds the
// one authenticator holding the balance and ignores the rest.
func TestMultiAuthenticatorSessionFanOut(t *testing.T) {
	plain := &mockAuthenticator{}
	stranger := &mockSettlerAuth{sessionID: "other", knows: false}
	owner := &mockSettlerAuth{
		sessionID: "session-abc",
		charged:   7,
		knows:     true,
	}

	multi := NewMultiAuthenticator(plain, stranger, owner)

	header := make(http.Header)
	sessionID, charged, ok := multi.BearerSessionID(
		context.Background(), &header,
	)
	require.True(t, ok)
	require.Equal(t, "session-abc", sessionID)
	require.EqualValues(t, 7, charged)

	// The settlement has to reach the holder even though an earlier settler
	// rejects it, so the first success wins rather than the first attempt.
	require.NoError(t, multi.SettleSessionRequest(
		context.Background(), "session-abc", 7, 19,
	))
	require.Equal(t, "session-abc", owner.settledFor)
	require.Empty(t, stranger.settledFor)

	// A session nobody holds is an error rather than a silent no-op.
	require.Error(t, multi.SettleSessionRequest(
		context.Background(), "nobody", 7, 19,
	))
}

// TestMultiAuthenticatorNoSettlers verifies that a stack with no session
// authenticator reports no session rather than pretending to hold one.
func TestMultiAuthenticatorNoSettlers(t *testing.T) {
	multi := NewMultiAuthenticator(&mockAuthenticator{})

	header := make(http.Header)
	_, _, ok := multi.BearerSessionID(context.Background(), &header)
	require.False(t, ok)

	require.Error(t, multi.SettleSessionRequest(
		context.Background(), "session-abc", 7, 19,
	))
}
