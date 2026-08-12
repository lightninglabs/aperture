package proxy

import (
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/stretchr/testify/require"
)

// TestUpdateServicesCopiesRateLimitConfig makes sure a published service owns
// its runtime state instead of sharing nested configuration pointers with the
// caller.
func TestUpdateServicesCopiesRateLimitConfig(t *testing.T) {
	t.Setenv("APERTURE_UPDATE_HEADER", "first-value")

	rule := &RateLimitConfig{
		PathRegexp: "^/limited$",
		Requests:   1,
		Per:        time.Hour,
	}
	service := newUpdateTestService(t.Name(), rule)
	service.Auth = auth.Level("on")
	service.AuthWhitelistPaths = []string{"^/public$"}
	service.AuthSkipInvoiceCreationPaths = []string{"^/no-invoice$"}
	service.Headers = map[string]string{
		"X-Upstream": "${APERTURE_UPDATE_HEADER}",
	}
	originalHeaders := service.Headers

	p, err := New(
		auth.NewMockAuthenticator(), []*Service{service}, nil, nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, p.Close())
	})

	active := p.services[0]
	require.NotSame(t, service, active)
	require.NotSame(t, rule, active.RateLimits[0])
	require.NotNil(t, rule.compiledPathRegexp)
	require.Nil(t, service.rateLimiter)
	require.False(t, active.RateLimits[0].Matches("/other"))
	require.False(t, rule.Matches("/other"))
	require.Equal(t, int64(defaultServicePrice), service.Price)
	require.Equal(t, "first-value", service.Headers["X-Upstream"])
	require.Equal(t, "first-value", originalHeaders["X-Upstream"])

	publicReq := httptest.NewRequest("GET", "/public", nil)
	require.Equal(t, auth.LevelOff, service.AuthRequired(publicReq))
	noInvoiceReq := httptest.NewRequest("GET", "/no-invoice", nil)
	require.True(t, service.SkipInvoiceCreation(noInvoiceReq))

	// Mutating caller-owned configuration after publication must not alter
	// the active snapshot.
	rule.PathRegexp = ""
	require.False(t, active.RateLimits[0].Matches("/other"))

	// Successful preparation materializes header directives in the caller's
	// map. An unrelated later update must not re-expand the original value.
	t.Setenv("APERTURE_UPDATE_HEADER", "second-value")
	require.NoError(t, p.UpdateServices([]*Service{service}))
	require.Equal(t, "first-value", service.Headers["X-Upstream"])
}

// TestUpdateServicesFailureDoesNotCopyPreparedConfig makes sure caller-visible
// normalization is committed only after every staged service validates.
func TestUpdateServicesFailureDoesNotCopyPreparedConfig(t *testing.T) {
	t.Setenv("APERTURE_FAILED_UPDATE_HEADER", "expanded")

	rule := &RateLimitConfig{
		PathRegexp: "^/candidate$",
		Requests:   1,
		Per:        time.Hour,
	}
	service := newUpdateTestService(t.Name(), rule)
	service.Headers = map[string]string{
		"X-Upstream": "${APERTURE_FAILED_UPDATE_HEADER}",
	}
	service.DynamicPrice.Metered = true

	p, err := New(
		auth.NewMockAuthenticator(),
		[]*Service{newUpdateTestService(t.Name()+"-active", &RateLimitConfig{
			Requests: 1,
			Per:      time.Hour,
		})}, nil, nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, p.Close())
	})
	active := p.services[0]

	err = p.UpdateServices([]*Service{service})
	require.Error(t, err)
	require.Equal(
		t, "${APERTURE_FAILED_UPDATE_HEADER}",
		service.Headers["X-Upstream"],
	)
	require.Zero(t, service.Price)
	require.Nil(t, rule.compiledPathRegexp)
	require.Same(t, active, p.services[0])
}

// TestUpdateServicesCertFailureDoesNotCopyPreparedConfig exercises the latest
// fallible staging boundary: certificate loading happens after rate-limit
// compilation, header materialization, price normalization and pricer setup.
func TestUpdateServicesCertFailureDoesNotCopyPreparedConfig(t *testing.T) {
	t.Setenv("APERTURE_CERT_FAILURE_HEADER", "expanded")

	activeService := newUpdateTestService(
		t.Name()+"-active", &RateLimitConfig{
			Requests: 2,
			Per:      time.Hour,
			Burst:    2,
		},
	)
	p, err := New(
		auth.NewMockAuthenticator(), []*Service{activeService}, nil, nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, p.Close())
	})

	active := p.services[0]
	request := httptest.NewRequest("GET", "/limited", nil)
	allowed, _ := active.rateLimiter.Allow(request, "active-key")
	require.True(t, allowed)
	labels := map[string]string{"service": activeService.Name}
	require.Equal(t, 1.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	rule := &RateLimitConfig{
		PathRegexp: "^/candidate$",
		Requests:   1,
		Per:        time.Hour,
	}
	candidate := newUpdateTestService(t.Name()+"-candidate", rule)
	candidate.Headers = map[string]string{
		"X-Upstream": "${APERTURE_CERT_FAILURE_HEADER}",
	}
	candidate.TLSCertPath = filepath.Join(t.TempDir(), "missing.pem")

	err = p.UpdateServices([]*Service{candidate})
	require.Error(t, err)
	require.Equal(
		t, "${APERTURE_CERT_FAILURE_HEADER}",
		candidate.Headers["X-Upstream"],
	)
	require.Zero(t, candidate.Price)
	require.Nil(t, rule.compiledPathRegexp)
	require.Nil(t, candidate.rateLimiter)
	require.Same(t, active, p.services[0])
	require.Equal(t, 1.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))
}

// TestUpdateServicesRateLimitSnapshotConcurrent exercises the pointer-reuse
// pattern used by runtime service updates while an old limiter is active. Run
// under the race detector, this catches writes to shared compiled regexps.
func TestUpdateServicesRateLimitSnapshotConcurrent(t *testing.T) {
	rule := &RateLimitConfig{
		PathRegexp: "^/limited$",
		Requests:   100,
		Per:        time.Second,
		Burst:      100,
	}
	service := newUpdateTestService(t.Name(), rule)

	p, err := New(
		auth.NewMockAuthenticator(), []*Service{service}, nil, nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, p.Close())
	})

	oldLimiter := p.services[0].rateLimiter
	request := httptest.NewRequest("GET", "/not-limited", nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		for range 10_000 {
			allowed, _ := oldLimiter.Allow(request, "test-key")
			if !allowed {
				t.Error("non-matching request was rate limited")
				return
			}
		}
	}()

	for range 200 {
		require.NoError(t, p.UpdateServices([]*Service{service}))
	}
	wg.Wait()
	require.Zero(t, oldLimiter.Size())
}

// TestUpdateServicesRateLimitMetricsTransactional makes sure a failed update
// leaves both the active limiter and its cache gauge unchanged. Successful
// replacement resets the new cache, and removing the limiter removes its
// gauge.
func TestUpdateServicesRateLimitMetricsTransactional(t *testing.T) {
	rule := &RateLimitConfig{
		PathRegexp: "^/limited$",
		Requests:   2,
		Per:        time.Hour,
		Burst:      2,
	}
	service := newUpdateTestService(t.Name(), rule)

	p, err := New(
		auth.NewMockAuthenticator(), []*Service{service}, nil, nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, p.Close())
	})

	labels := map[string]string{"service": service.Name}
	active := p.services[0]
	require.Equal(t, 0.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	request := httptest.NewRequest("GET", "/limited", nil)
	allowed, _ := active.rateLimiter.Allow(request, "test-key")
	require.True(t, allowed)
	require.Equal(t, 1.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	// Exercise a validation failure that occurs after the replacement rate
	// limiter has been constructed. It must not reset the live cache gauge.
	lateFailure := *service
	lateFailure.DynamicPrice.Metered = true
	require.Error(t, p.UpdateServices([]*Service{&lateFailure}))
	require.Same(t, active, p.services[0])
	require.Equal(t, 1.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	badRule := *rule
	badRule.PathRegexp = "["
	badService := *service
	badService.RateLimits = []*RateLimitConfig{&badRule}
	require.Error(t, p.UpdateServices([]*Service{&badService}))
	require.Same(t, active, p.services[0])
	require.False(t, active.RateLimits[0].Matches("/other"))
	require.Equal(t, 1.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	require.NoError(t, p.UpdateServices([]*Service{service}))
	require.Equal(t, 0.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	withoutLimits := *service
	withoutLimits.RateLimits = nil
	require.NoError(t, p.UpdateServices([]*Service{&withoutLimits}))
	_, ok := prometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	)
	require.False(t, ok)
}

// TestRateLimitCacheMetricsMultipleProxies makes sure replacing one Proxy's
// limiter cannot delete or overwrite another active Proxy's contribution that
// uses the same service label.
func TestRateLimitCacheMetricsMultipleProxies(t *testing.T) {
	serviceName := t.Name()
	newService := func() *Service {
		return newUpdateTestService(serviceName, &RateLimitConfig{
			Requests: 2,
			Per:      time.Hour,
			Burst:    2,
		})
	}

	firstService := newService()
	first, err := New(
		auth.NewMockAuthenticator(), []*Service{firstService}, nil, nil,
	)
	require.NoError(t, err)
	firstClosed := false
	t.Cleanup(func() {
		if !firstClosed {
			require.NoError(t, first.Close())
		}
	})

	secondService := newService()
	second, err := New(
		auth.NewMockAuthenticator(), []*Service{secondService}, nil, nil,
	)
	require.NoError(t, err)
	secondClosed := false
	t.Cleanup(func() {
		if !secondClosed {
			require.NoError(t, second.Close())
		}
	})

	request := httptest.NewRequest("GET", "/limited", nil)
	allowed, _ := first.services[0].rateLimiter.Allow(request, "first-key")
	require.True(t, allowed)
	allowed, _ = second.services[0].rateLimiter.Allow(request, "second-key")
	require.True(t, allowed)

	labels := map[string]string{"service": serviceName}
	require.Equal(t, 2.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	require.NoError(t, first.Close())
	firstClosed = true
	require.Equal(t, 1.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	allowed, _ = second.services[0].rateLimiter.Allow(request, "third-key")
	require.True(t, allowed)
	require.Equal(t, 2.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	require.NoError(t, second.Close())
	secondClosed = true
	_, ok := prometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	)
	require.False(t, ok)
}

// TestRateLimitManagedMetricsDoNotDeleteLegacyGauge makes sure Proxy snapshot
// lifecycle operations only affect active_cache_size. The original cache_size
// gauge remains compatible with standalone RateLimiter users.
func TestRateLimitManagedMetricsDoNotDeleteLegacyGauge(t *testing.T) {
	serviceName := t.Name()
	standalone := NewRateLimiter(serviceName, []*RateLimitConfig{{
		Requests: 2,
		Per:      time.Hour,
		Burst:    2,
	}})
	request := httptest.NewRequest("GET", "/limited", nil)
	allowed, _ := standalone.Allow(request, "standalone-key")
	require.True(t, allowed)

	service := newUpdateTestService(serviceName, &RateLimitConfig{
		Requests: 2,
		Per:      time.Hour,
		Burst:    2,
	})
	p, err := New(
		auth.NewMockAuthenticator(), []*Service{service}, nil, nil,
	)
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if !closed {
			require.NoError(t, p.Close())
		}
	})

	labels := map[string]string{"service": serviceName}
	require.Equal(t, 1.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_cache_size", labels,
	))
	require.Equal(t, 0.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	))

	require.NoError(t, p.Close())
	closed = true
	_, ok := prometheusGaugeValue(
		t, "aperture_ratelimit_active_cache_size", labels,
	)
	require.False(t, ok)
	require.Equal(t, 1.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_cache_size", labels,
	))

	allowed, _ = standalone.Allow(request, "standalone-key-2")
	require.True(t, allowed)
	require.Equal(t, 2.0, mustPrometheusGaugeValue(
		t, "aperture_ratelimit_cache_size", labels,
	))
}

func newUpdateTestService(name string, rule *RateLimitConfig) *Service {
	return &Service{
		Name:       name,
		HostRegexp: ".*",
		PathRegexp: ".*",
		Auth:       auth.Level("off"),
		RateLimits: []*RateLimitConfig{rule},
	}
}

func mustPrometheusGaugeValue(t *testing.T, name string,
	labels map[string]string) float64 {

	t.Helper()

	value, ok := prometheusGaugeValue(t, name, labels)
	require.True(t, ok)

	return value
}
