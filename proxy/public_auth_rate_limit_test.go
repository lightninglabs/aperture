package proxy

import (
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/stretchr/testify/require"
)

// TestPublicPathRateLimits checks that only verified L402 tokens get their own
// rate limit bucket; rotating forged tokens must not evade the IP limit.
func TestPublicPathRateLimits(t *testing.T) {
	valid := newPublicAuthToken(t, 1)
	forged := newPublicAuthToken(t, 2)
	anotherForged := newPublicAuthToken(t, 3)
	a := &publicAuthRecorder{
		scheme:   auth.AuthSchemeL402,
		accepted: map[l402.TokenID]bool{valid.id: true},
	}
	service := &Service{
		Auth:               "on",
		AuthWhitelistPaths: []string{"^/public$"},
		RateLimits: []*RateLimitConfig{{
			Requests: 1,
			Per:      24 * time.Hour,
			Burst:    1,
		}},
	}
	p, _ := newPublicAuthProxy(t, service, a)
	for _, tc := range []struct {
		header http.Header
		ip     string
		status int
	}{
		{forged.header, "192.0.2.1", http.StatusNoContent},
		{anotherForged.header, "192.0.2.1", http.StatusTooManyRequests},
		{nil, "198.51.100.1", http.StatusNoContent},
		{valid.header, "192.0.2.1", http.StatusNoContent},
		{valid.header, "198.51.100.1", http.StatusTooManyRequests},
	} {
		response := servePublicAuthRequest(
			p, "/public", tc.ip, tc.header,
		)
		require.Equal(t, tc.status, response.Code)
	}
	require.Zero(t, a.challenges)
}
