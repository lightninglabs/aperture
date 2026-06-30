package aperture

import (
	"context"
	"sync"
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

// TestRefreshRebuildsTimeouts verifies that calling refresh replaces the
// timeout map so subsequent ServiceTimeouts calls reflect the new values.
func TestRefreshRebuildsTimeouts(t *testing.T) {
	t.Parallel()

	limiter := newStaticServiceLimiter([]*proxy.Service{
		{Name: "svc", Price: 10, Timeout: 60},
	})

	svc := l402.Service{Name: "svc", Tier: l402.BaseTier, Price: 10}

	caveats, err := limiter.ServiceTimeouts(context.Background(), svc)
	require.NoError(t, err)
	require.Len(t, caveats, 1)

	// Refresh with an updated timeout.
	limiter.refresh([]*proxy.Service{
		{Name: "svc", Price: 10, Timeout: 120},
	})

	caveats2, err := limiter.ServiceTimeouts(context.Background(), svc)
	require.NoError(t, err)
	require.Len(t, caveats2, 1)
	require.NotEqual(t, caveats[0].Value, caveats2[0].Value)

	// Refresh removing the timeout entirely (Timeout == 0).
	limiter.refresh([]*proxy.Service{
		{Name: "svc", Price: 10, Timeout: 0},
	})

	caveats3, err := limiter.ServiceTimeouts(context.Background(), svc)
	require.NoError(t, err)
	require.Empty(t, caveats3)
}

// TestRefreshConcurrentReads verifies that concurrent reads during a refresh
// do not race. Run with -race to catch data races.
func TestRefreshConcurrentReads(t *testing.T) {
	t.Parallel()

	limiter := newStaticServiceLimiter([]*proxy.Service{
		{Name: "svc", Price: 5, Timeout: 30},
	})

	svc := l402.Service{Name: "svc", Tier: l402.BaseTier, Price: 5}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(timeout int64) {
			defer wg.Done()
			limiter.refresh([]*proxy.Service{
				{Name: "svc", Price: 5, Timeout: timeout},
			})
		}(int64(i + 1))

		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = limiter.ServiceTimeouts(context.Background(), svc)
			_, _ = limiter.ServiceCapabilities(
				context.Background(), svc,
			)
			_, _ = limiter.ServiceConstraints(
				context.Background(), svc,
			)
		}()
	}
	wg.Wait()
}

// TestRefreshCreateDelete verifies that refresh propagates creates and deletes:
// a service added via refresh becomes visible, and one removed disappears.
func TestRefreshCreateDelete(t *testing.T) {
	t.Parallel()

	limiter := newStaticServiceLimiter([]*proxy.Service{
		{Name: "existing", Price: 1, Timeout: 10},
	})

	existing := l402.Service{Name: "existing", Tier: l402.BaseTier, Price: 1}
	added := l402.Service{Name: "new-svc", Tier: l402.BaseTier, Price: 2}

	// Create: refresh with an additional service.
	limiter.refresh([]*proxy.Service{
		{Name: "existing", Price: 1, Timeout: 10},
		{Name: "new-svc", Price: 2, Timeout: 20},
	})

	caveats, err := limiter.ServiceTimeouts(context.Background(), added)
	require.NoError(t, err)
	require.Len(t, caveats, 1)

	// Delete: refresh without the original service.
	limiter.refresh([]*proxy.Service{
		{Name: "new-svc", Price: 2, Timeout: 20},
	})

	caveats, err = limiter.ServiceTimeouts(context.Background(), existing)
	require.NoError(t, err)
	require.Empty(t, caveats)
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
