package auth

import (
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
