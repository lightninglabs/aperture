package aperture

import (
	"context"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

// TestServiceTimeoutsComputedPerCall verifies that timeout caveats are
// computed at the time ServiceTimeouts is called, not at limiter creation
// time. This is a regression test for a bug where timeouts were precomputed
// once, causing all L402s to share the same expiration timestamp based on
// server startup time.
func TestServiceTimeoutsComputedPerCall(t *testing.T) {
	t.Parallel()

	services := []*proxy.Service{
		{
			Name:    "test-service",
			Price:   1,
			Timeout: 3600,
		},
	}

	limiter, err := newStaticServiceLimiter(services)
	require.NoError(t, err)

	svc := l402.Service{
		Name:  "test-service",
		Tier:  l402.BaseTier,
		Price: 1,
	}

	// Get timeouts at two different points in time. If the caveat is
	// computed per call, the expiration values should differ.
	caveats1, err := limiter.ServiceTimeouts(context.Background(), svc)
	require.NoError(t, err)
	require.Len(t, caveats1, 1)

	// Sleep briefly to ensure time advances.
	time.Sleep(1100 * time.Millisecond)

	caveats2, err := limiter.ServiceTimeouts(context.Background(), svc)
	require.NoError(t, err)
	require.Len(t, caveats2, 1)

	// The two timeout values should be different since they were computed
	// at different times.
	require.NotEqual(t, caveats1[0].Value, caveats2[0].Value,
		"timeout caveats should be computed per call, not cached "+
			"from init time")
}

func TestStaticServiceLimiterAllCaveatTypes(t *testing.T) {
	t.Parallel()

	limiter, err := newStaticServiceLimiter([]*proxy.Service{
		{
			Name:         "svc",
			Price:        100,
			Timeout:      60,
			Capabilities: "read,write",
			Constraints: map[string]string{
				"region": "us",
			},
		},
	})
	require.NoError(t, err)

	service := l402.Service{
		Name:  "svc",
		Tier:  l402.BaseTier,
		Price: 100,
	}

	capabilities, err := limiter.ServiceCapabilities(
		context.Background(), service,
	)
	require.NoError(t, err)
	require.Len(t, capabilities, 1)

	constraints, err := limiter.ServiceConstraints(
		context.Background(), service,
	)
	require.NoError(t, err)
	require.Len(t, constraints, 1)

	timeouts, err := limiter.ServiceTimeouts(
		context.Background(), service,
	)
	require.NoError(t, err)
	require.Len(t, timeouts, 1)
}

// TestStaticServiceLimiterDynamicResources checks that a token minted for a
// dynamic-price resource, at whatever price the pricer quoted, carries the
// restrictions configured for its service, and that the verifier enforces its
// timeout.
func TestStaticServiceLimiterDynamicResources(t *testing.T) {
	t.Parallel()

	// No price is configured: the proxy applies the default one only
	// after this limiter has been built.
	limiter, err := newStaticServiceLimiter([]*proxy.Service{{
		Name:         "svc",
		Timeout:      60,
		Capabilities: "read",
		Constraints:  map[string]string{"region": "us"},
	}})
	require.NoError(t, err)

	ctx := context.Background()
	resource := l402.Service{
		Name:  "svc/v1/items",
		Tier:  l402.BaseTier,
		Price: 42,
	}

	// Backends check capabilities under the configured service name.
	capabilities, err := limiter.ServiceCapabilities(ctx, resource)
	require.NoError(t, err)
	require.Equal(t, []l402.Caveat{
		l402.NewCapabilitiesCaveat("svc", "read"),
	}, capabilities)

	constraints, err := limiter.ServiceConstraints(ctx, resource)
	require.NoError(t, err)
	require.Equal(t, []l402.Caveat{
		{Condition: "region", Value: "us"},
	}, constraints)

	timeouts, err := limiter.ServiceTimeouts(ctx, resource)
	require.NoError(t, err)
	require.Len(t, timeouts, 1)

	// The verifier checks expiry under the resource name, so the timeout
	// must be written under it too.
	services, err := l402.NewServicesCaveat(resource)
	require.NoError(t, err)
	caveats := append([]l402.Caveat{services}, timeouts...)
	verify := func(now time.Time) error {
		return l402.VerifyCaveats(
			caveats,
			l402.NewServicesSatisfier(resource.Name),
			l402.NewTimeoutSatisfier(
				resource.Name, func() time.Time { return now },
			),
		)
	}
	require.NoError(t, verify(time.Now()))
	require.Error(t, verify(time.Now().Add(2*time.Minute)))

	// The same restrictions apply to a static token priced differently
	// from the configuration, and to no other service.
	static := l402.Service{Name: "svc", Tier: l402.BaseTier, Price: 1}
	capabilities, err = limiter.ServiceCapabilities(ctx, static)
	require.NoError(t, err)
	require.Len(t, capabilities, 1)

	other := l402.Service{Name: "svcx/v1/items", Tier: l402.BaseTier}
	timeouts, err = limiter.ServiceTimeouts(ctx, other)
	require.NoError(t, err)
	require.Empty(t, timeouts)
}

// TestStaticServiceLimiterRejectsUnreadableTimeout checks that no token is
// minted with a timeout caveat the verifier could not read back.
func TestStaticServiceLimiterRejectsUnreadableTimeout(t *testing.T) {
	t.Parallel()

	limiter, err := newStaticServiceLimiter([]*proxy.Service{{
		Name:    "svc",
		Timeout: 60,
	}})
	require.NoError(t, err)

	// A caveat splits its condition from its value at the first '='.
	_, err = limiter.ServiceTimeouts(context.Background(), l402.Service{
		Name: "svc/key=value",
		Tier: l402.BaseTier,
	})
	require.Error(t, err)
}

// TestStaticServiceLimiterSharedName checks that services sharing a name, and
// with it their tokens, must agree on the restrictions those tokens carry,
// compared the way they take effect rather than as configured text.
func TestStaticServiceLimiterSharedName(t *testing.T) {
	t.Parallel()

	route := func(price, timeout int64, capabilities string,
		constraints map[string]string) *proxy.Service {

		return &proxy.Service{
			Name:         "svc",
			Price:        price,
			Timeout:      timeout,
			Capabilities: capabilities,
			Constraints:  constraints,
		}
	}
	region := func(value string) map[string]string {
		return map[string]string{"region": value}
	}

	tests := []struct {
		name     string
		services []*proxy.Service
		conflict bool
	}{
		{
			name: "same restrictions",
			services: []*proxy.Service{
				route(1, 60, "read,write", region("us")),
				route(100, 60, "read,write", region("us")),
			},
		},
		{
			name: "capabilities in another order",
			services: []*proxy.Service{
				route(1, 60, "read,write", nil),
				route(100, 60, "write,read", nil),
			},
		},
		{
			name: "no timeout either way",
			services: []*proxy.Service{
				route(1, 0, "read", nil),
				route(100, -1, "read", map[string]string{}),
			},
		},
		{
			// A token of the 1-sat route would otherwise carry the
			// capabilities of the 100-sat one.
			name: "broader capabilities",
			services: []*proxy.Service{
				route(1, 60, "read", nil),
				route(100, 60, "read,write", nil),
			},
			conflict: true,
		},
		{
			name: "different timeout",
			services: []*proxy.Service{
				route(1, 60, "read", nil),
				route(100, 0, "read", nil),
			},
			conflict: true,
		},
		{
			name: "different constraints",
			services: []*proxy.Service{
				route(1, 60, "read", region("us")),
				route(100, 60, "read", region("eu")),
			},
			conflict: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			limiter, err := newStaticServiceLimiter(test.services)
			if test.conflict {
				require.ErrorContains(
					t, err, `services named "svc"`,
				)
				return
			}
			require.NoError(t, err)

			// The restrictions are recorded once, whichever route
			// mints the token.
			constraints, err := limiter.ServiceConstraints(
				context.Background(), l402.Service{
					Name:  "svc",
					Tier:  l402.BaseTier,
					Price: 1,
				},
			)
			require.NoError(t, err)
			require.Len(
				t, constraints,
				len(test.services[0].Constraints),
			)
		})
	}
}
