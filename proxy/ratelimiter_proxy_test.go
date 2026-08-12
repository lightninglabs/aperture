package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/stretchr/testify/require"
)

// TestRateLimiterZeroPrice makes sure an unauthenticated request that is free
// according to the pricer is still rate limited by IP.
func TestRateLimiterZeroPrice(t *testing.T) {
	p, service, backendCalls := newRateLimitTestProxy(
		t, auth.Level("on"), 1, time.Hour,
	)
	service.pricer = pricer.NewDefaultPricer(0)

	serveRateLimitRequest(t, p, http.StatusOK)
	serveRateLimitRequest(t, p, http.StatusTooManyRequests)
	require.Equal(t, int32(1), backendCalls.Load())
}

// newRateLimitTestProxy creates a proxy with a one-token burst and a backend
// that records how many requests were forwarded.
func newRateLimitTestProxy(t *testing.T, authLevel auth.Level, requests int,
	per time.Duration) (*Proxy, *Service, *atomic.Int32) {

	t.Helper()

	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter, _ *http.Request) {

		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	service := &Service{
		Name:       t.Name(),
		Address:    backendURL.Host,
		Protocol:   backendURL.Scheme,
		HostRegexp: ".*",
		PathRegexp: "^/limited$",
		Auth:       authLevel,
		RateLimits: []*RateLimitConfig{{
			Requests: requests,
			Per:      per,
			Burst:    1,
		}},
	}
	p, err := New(
		auth.NewMockAuthenticator(), []*Service{service}, nil, nil,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, p.Close())
	})

	return p, service, &backendCalls
}

// serveRateLimitRequest sends one request directly through the proxy.
func serveRateLimitRequest(t *testing.T, p *Proxy, expectedStatus int) {
	t.Helper()

	req := httptest.NewRequest("GET", "http://example.com/limited", nil)
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, req)
	require.Equal(t, expectedStatus, recorder.Code)
}
