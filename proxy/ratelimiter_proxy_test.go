package proxy

import (
	"errors"
	"net"
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

// TestRateLimiterDoesNotConsumeFreebie makes sure a rate limited request does
// not consume one of the client's free requests.
func TestRateLimiterDoesNotConsumeFreebie(t *testing.T) {
	p, service, backendCalls := newRateLimitTestProxy(
		t, auth.Level("freebie 2"), 1, time.Hour,
	)

	serveRateLimitRequest(t, p, http.StatusOK)
	serveRateLimitRequest(t, p, http.StatusTooManyRequests)

	// Replace the exhausted test bucket rather than depending on wall-clock
	// refill timing. The next request should still have the second freebie
	// available because the denied request was not tallied.
	service.rateLimiter.removeCacheMetric()
	service.rateLimiter = NewRateLimiter(
		service.Name, service.RateLimits, withManagedCacheMetric(),
	)
	service.rateLimiter.activateCacheMetric(0)
	serveRateLimitRequest(t, p, http.StatusOK)
	serveRateLimitRequest(t, p, http.StatusPaymentRequired)
	require.Equal(t, int32(2), backendCalls.Load())
}

// TestRateLimiterRefundsFreebieFailure makes sure an internal freebie store
// failure does not consume rate-limit capacity for a request that is never
// forwarded.
func TestRateLimiterRefundsFreebieFailure(t *testing.T) {
	p, service, backendCalls := newRateLimitTestProxy(
		t, auth.Level("freebie 2"), 1, time.Hour,
	)
	service.freebieDB = &failOnceFreebieDB{}

	serveRateLimitRequest(t, p, http.StatusInternalServerError)
	serveRateLimitRequest(t, p, http.StatusOK)
	require.Equal(t, int32(1), backendCalls.Load())
}

// failOnceFreebieDB fails its first tally and accepts subsequent tallies.
type failOnceFreebieDB struct {
	failed atomic.Bool
}

func (f *failOnceFreebieDB) CanPass(*http.Request, net.IP) (bool, error) {
	return true, nil
}

func (f *failOnceFreebieDB) TallyFreebie(*http.Request,
	net.IP) (bool, error) {

	if f.failed.CompareAndSwap(false, true) {
		return false, errors.New("injected freebie tally failure")
	}

	return true, nil
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

	// UpdateServices publishes an immutable prepared snapshot, so return the
	// service owned by the proxy when a test needs to replace an internal
	// dependency such as the pricer or freebie store.
	return p, p.services[0], &backendCalls
}

// serveRateLimitRequest sends one request directly through the proxy.
func serveRateLimitRequest(t *testing.T, p *Proxy, expectedStatus int) {
	t.Helper()

	req := httptest.NewRequest("GET", "http://example.com/limited", nil)
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, req)
	require.Equal(t, expectedStatus, recorder.Code)
}
