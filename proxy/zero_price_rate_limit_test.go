package proxy

import (
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/stretchr/testify/require"
)

// TestZeroPriceRateLimits checks that a zero price does not bypass IP limits
// when authentication fails, including after the freebie allowance runs out.
func TestZeroPriceRateLimits(t *testing.T) {
	forged := newPublicAuthToken(t, 2)
	for _, level := range []auth.Level{"on", "freebie 1"} {
		t.Run(string(level), func(t *testing.T) {
			a := &publicAuthRecorder{scheme: auth.AuthSchemeL402}
			service := &Service{
				Auth: level,
				RateLimits: []*RateLimitConfig{{
					Requests: 1,
					Per:      24 * time.Hour,
					Burst:    1,
				}},
			}
			p, received := newPublicAuthProxy(t, service, a)
			service.pricer = pricer.NewDefaultPricer(0)
			for _, tc := range []struct {
				header http.Header
				ip     string
				status int
			}{
				{
					forged.header, "192.0.2.1",
					http.StatusNoContent,
				},
				{nil, "192.0.2.1", http.StatusTooManyRequests},
				{nil, "198.51.100.1", http.StatusNoContent},
			} {
				response := servePublicAuthRequest(
					p, "/resource", tc.ip, tc.header,
				)
				require.Equal(t, tc.status, response.Code)
			}
			require.Len(t, received, 2)
			require.Zero(t, a.challenges)
		})
	}
}
