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

	limiter := newStaticServiceLimiter(services)

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

	limiter := newStaticServiceLimiter([]*proxy.Service{
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
