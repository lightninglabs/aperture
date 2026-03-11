package aperture

import (
	"context"
	"testing"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

func TestStaticServiceLimiterIgnoresRuntimePriceChanges(t *testing.T) {
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
		Price: 250,
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
