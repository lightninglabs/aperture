package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/netutil"
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

// serveForwarded sends an unauthenticated request that reaches the proxy from
// the given TCP peer carrying the given X-Forwarded-For lines.
func serveForwarded(p *Proxy, peer string, xff ...string) *httptest.ResponseRecorder {
	header := http.Header{}
	for _, line := range xff {
		header.Add("X-Forwarded-For", line)
	}

	return servePublicAuthRequest(p, "/resource", peer, header)
}

// TestChallengeRateLimitBehindProxy checks the challenge limit behind a
// reverse proxy on the loopback interface. Without trusted proxies every
// client shares the proxy's bucket. With the proxy trusted, clients get
// independent buckets, and a client cannot dodge the limit or spend another
// client's bucket by forging the left of X-Forwarded-For, because the proxy
// appends the address it saw last.
func TestChallengeRateLimitBehindProxy(t *testing.T) {
	const burst = 2

	newProxy := func(trust bool) (*Proxy, *publicAuthRecorder) {
		a := &publicAuthRecorder{scheme: auth.AuthSchemeL402}
		service := &Service{Auth: "on"}
		p, _ := newPublicAuthProxy(t, service, a)
		service.pricer = pricer.NewDefaultPricer(100)
		require.NoError(t, p.SetChallengeRateLimit(
			&ChallengeRateLimitConfig{
				Requests: burst, Per: time.Hour,
			},
		))
		if trust {
			nets, err := netutil.ParseTrustedProxies(
				[]string{"127.0.0.1"},
			)
			require.NoError(t, err)
			p.SetTrustedProxies(nets)
		}

		return p, a
	}

	const (
		alice = "198.51.100.10"
		bob   = "203.0.113.20"
		forge = "192.0.2.99"

		direct = "198.51.101.50"
	)

	// Not trusting the proxy, the first two clients use up the one shared
	// bucket and a third is locked out, which is the failure to avoid.
	p, a := newProxy(false)
	for _, client := range []string{alice, bob} {
		resp := serveForwarded(p, "127.0.0.1", client)
		require.Equal(t, http.StatusPaymentRequired, resp.Code)
	}
	resp := serveForwarded(p, "127.0.0.1", "198.18.0.77")
	require.Equal(t, http.StatusTooManyRequests, resp.Code)
	require.Equal(t, burst, a.challenges)

	// Trusting the proxy, alice exhausts her own bucket only.
	p, a = newProxy(true)
	for i := 0; i < burst; i++ {
		resp := serveForwarded(p, "127.0.0.1", alice)
		require.Equal(t, http.StatusPaymentRequired, resp.Code)
	}
	resp = serveForwarded(p, "127.0.0.1", alice)
	require.Equal(t, http.StatusTooManyRequests, resp.Code)

	// Bob, behind the same proxy, is unaffected.
	resp = serveForwarded(p, "127.0.0.1", bob)
	require.Equal(t, http.StatusPaymentRequired, resp.Code)
	require.Equal(t, burst+1, a.challenges)

	// Alice prepends fresh forged addresses to rotate out of her bucket,
	// in one header line and as extra lines. The proxy still appends her
	// real address last, so she stays limited.
	for i := 0; i < 5; i++ {
		fake := fmt.Sprintf("192.0.2.%d", i+1)
		resp = serveForwarded(
			p, "127.0.0.1", fake+", "+forge+", "+alice,
		)
		require.Equal(t, http.StatusTooManyRequests, resp.Code)

		resp = serveForwarded(p, "127.0.0.1", fake, forge+", "+alice)
		require.Equal(t, http.StatusTooManyRequests, resp.Code)
	}

	// Forging bob's address on the left does not spend bob's bucket, nor
	// does naming a victim let a client use the victim's headroom: carol
	// is counted as carol.
	const carol = "198.18.0.5"
	for i := 0; i < burst; i++ {
		resp = serveForwarded(p, "127.0.0.1", bob+", "+carol)
		require.Equal(t, http.StatusPaymentRequired, resp.Code)
	}
	resp = serveForwarded(p, "127.0.0.1", bob+", "+carol)
	require.Equal(t, http.StatusTooManyRequests, resp.Code)

	// Bob only used one challenge himself, so he still has one left.
	resp = serveForwarded(p, "127.0.0.1", bob)
	require.Equal(t, http.StatusPaymentRequired, resp.Code)

	// A client that reaches aperture directly, not through the proxy,
	// cannot use the header at all: it is counted by its own address.
	for i := 0; i < burst; i++ {
		resp = serveForwarded(p, direct, "198.51.100.200")
		require.Equal(t, http.StatusPaymentRequired, resp.Code)
	}
	resp = serveForwarded(p, direct, "198.51.100.201")
	require.Equal(t, http.StatusTooManyRequests, resp.Code)
}
