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

func newUpdateTestService(name string, rule *RateLimitConfig) *Service {
	return &Service{
		Name:       name,
		HostRegexp: ".*",
		PathRegexp: ".*",
		Auth:       auth.Level("off"),
		RateLimits: []*RateLimitConfig{rule},
	}
}
