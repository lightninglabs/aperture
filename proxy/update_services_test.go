package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

// blockingAuthenticator reports an Accept call on entered and holds it until
// release is closed, parking the request inside the proxy.
type blockingAuthenticator struct {
	*auth.MockAuthenticator

	entered chan struct{}
	release chan struct{}
}

// Accept signals that a request is in flight, then waits to be released.
func (a *blockingAuthenticator) Accept(header *http.Header,
	serviceName string) bool {

	a.entered <- struct{}{}
	<-a.release

	return a.MockAuthenticator.Accept(header, serviceName)
}

// TestUpdateServicesDuringRequest checks that UpdateServices does not rewrite
// a service while a request is still reading it. The admin API passes the
// same *Service values back in that it handed over before, so the proxy must
// prepare copies of them. Run with -race, which reports the conflicting
// access.
func TestUpdateServicesDuringRequest(t *testing.T) {
	t.Parallel()

	// A paid service answers an anonymous request with a payment
	// challenge, so the backend address is never dialed.
	services := []*proxy.Service{{
		Name:       "paid",
		Address:    "localhost:8080",
		HostRegexp: ".*",
		Auth:       "on",
	}}
	authenticator := &blockingAuthenticator{
		MockAuthenticator: auth.NewMockAuthenticator(),
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	p, err := proxy.New(authenticator, services, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Close()) })

	// Park an anonymous request in Accept. It has already matched its
	// service and reads the service's pricer once released, to price
	// the challenge.
	statuses := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		statuses <- w.Code
	}()
	<-authenticator.entered

	// Pass the same services back, as the admin API does, and release
	// the request. Nothing orders the update after the request's
	// remaining reads, so the two must not touch the same values.
	updateErrs := make(chan error, 1)
	go func() {
		updateErrs <- p.UpdateServices(services)
	}()
	close(authenticator.release)

	require.Equal(t, http.StatusPaymentRequired, <-statuses)
	require.NoError(t, <-updateErrs)
}

// TestUpdateServicesWithOpenRequest checks that UpdateServices does not wait
// for in-flight requests to finish. A long-lived request such as a gRPC stream
// may stay open indefinitely, and an update that waits for it also stalls
// every request arriving behind the update.
func TestUpdateServicesWithOpenRequest(t *testing.T) {
	t.Parallel()

	services := []*proxy.Service{{
		Name:       "paid",
		Address:    "localhost:8080",
		HostRegexp: ".*",
		Auth:       "on",
		Price:      200,
	}}
	authenticator := &blockingAuthenticator{
		MockAuthenticator: auth.NewMockAuthenticator(),
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	p, err := proxy.New(authenticator, services, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Close()) })

	// Let the parked requests go on every way out, so that a failure is
	// reported rather than left to hang.
	release := sync.OnceFunc(func() { close(authenticator.release) })
	t.Cleanup(release)

	// Park a request in Accept and keep it there, standing in for a
	// stream that never ends.
	statuses := make(chan int, 2)
	go func() {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		statuses <- w.Code
	}()
	<-authenticator.entered

	// Reprice the service the way the admin API does, by swapping in an
	// updated copy.
	updated := *services[0]
	updated.Price = 1
	updateErrs := make(chan error, 1)
	go func() {
		updateErrs <- p.UpdateServices([]*proxy.Service{&updated})
	}()

	select {
	case err := <-updateErrs:
		require.NoError(t, err)

	case <-time.After(5 * time.Second):
		require.Fail(t, "UpdateServices blocked on an open request")
	}

	// A request arriving after the update is not held up either.
	go func() {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		statuses <- w.Code
	}()

	select {
	case <-authenticator.entered:

	case <-time.After(5 * time.Second):
		require.Fail(t, "request blocked behind UpdateServices")
	}

	release()
	require.Equal(t, http.StatusPaymentRequired, <-statuses)
	require.Equal(t, http.StatusPaymentRequired, <-statuses)
}
