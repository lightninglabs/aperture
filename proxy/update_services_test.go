package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
// proxy's own *Service values back in, so preparing them must wait for
// in-flight requests. Run with -race, which reports the conflicting access.
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
	// the request. Only the proxy's own locking orders the update after
	// the request's remaining reads.
	updateErrs := make(chan error, 1)
	go func() {
		updateErrs <- p.UpdateServices(services)
	}()
	close(authenticator.release)

	require.Equal(t, http.StatusPaymentRequired, <-statuses)
	require.NoError(t, <-updateErrs)
}
