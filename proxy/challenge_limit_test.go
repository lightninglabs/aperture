package proxy

import (
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/stretchr/testify/require"
)

// TestChallengeRateLimit checks that unauthenticated requests from one client
// network cannot make the proxy mint more than the configured number of
// challenges, that other networks are unaffected, and that every refused
// request is told when to retry.
func TestChallengeRateLimit(t *testing.T) {
	const burst = 4

	a := &publicAuthRecorder{scheme: auth.AuthSchemeL402}
	service := &Service{Auth: "on"}
	p, _ := newPublicAuthProxy(t, service, a)
	service.pricer = pricer.NewDefaultPricer(100)

	require.NoError(t, p.SetChallengeRateLimit(&ChallengeRateLimitConfig{
		Requests: burst,
		Per:      time.Hour,
	}))

	// A scanner sweeping the endpoint from one network, with addresses
	// that differ only in the last octet.
	var limited int
	for i := 0; i < 50; i++ {
		ip := "192.0.2." + string(rune('1'+i%9))
		resp := servePublicAuthRequest(p, "/resource", ip, nil)
		switch resp.Code {
		case http.StatusPaymentRequired:
		case http.StatusTooManyRequests:
			limited++
			require.NotEmpty(t, resp.Header().Get("Retry-After"))
		default:
			t.Fatalf("unexpected status %d", resp.Code)
		}
	}
	require.Equal(t, burst, a.challenges)
	require.Equal(t, 50-burst, limited)

	// Another network still gets a challenge.
	resp := servePublicAuthRequest(p, "/resource", "198.51.100.1", nil)
	require.Equal(t, http.StatusPaymentRequired, resp.Code)
	require.Equal(t, burst+1, a.challenges)

	// Removing the limit restores unbounded issuance.
	require.NoError(t, p.SetChallengeRateLimit(nil))
	for i := 0; i < 10; i++ {
		resp = servePublicAuthRequest(p, "/resource", "192.0.2.1", nil)
		require.Equal(t, http.StatusPaymentRequired, resp.Code)
	}
	require.Equal(t, burst+11, a.challenges)
}

// TestChallengeRateLimitInvalid checks that a nonsensical limit is rejected
// rather than silently disabling protection.
func TestChallengeRateLimitInvalid(t *testing.T) {
	p := &Proxy{}
	require.Error(t, p.SetChallengeRateLimit(
		&ChallengeRateLimitConfig{Requests: 5},
	))
	require.Error(t, p.SetChallengeRateLimit(
		&ChallengeRateLimitConfig{Requests: -1, Per: time.Hour},
	))
}
