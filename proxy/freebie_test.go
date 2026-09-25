package proxy_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

// TestProxyConcurrentFreebies checks that concurrent anonymous requests consume
// exactly the configured allowance.
func TestProxyConcurrentFreebies(t *testing.T) {
	t.Parallel()

	// A successful backend response identifies an admitted freebie. A
	// positive price makes exhausted requests return Payment Required.
	backend := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	))
	t.Cleanup(backend.Close)
	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	// Cover a burst of writes, contention for the last freebie, and an
	// allowance of zero. Each subtest gets a fresh store.
	for _, limit := range []int{32, 1, 0} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			level := auth.Level(fmt.Sprintf("freebie %d", limit))
			services := []*proxy.Service{{
				Name:       "freebie",
				Address:    backendURL.Host,
				HostRegexp: ".*",
				Protocol:   backendURL.Scheme,
				Auth:       level,
				Price:      1,
			}}
			p, err := proxy.New(
				auth.NewMockAuthenticator(), services, nil, nil,
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, p.Close()) })

			// Release all requests together to exercise overlapping
			// checks and increments for the same client's
			// allowance.
			const numRequests = 128
			start := make(chan struct{})
			statuses := make(chan int, numRequests)
			var ready sync.WaitGroup
			ready.Add(numRequests)
			for range numRequests {
				go func() {
					r := httptest.NewRequest(
						http.MethodGet, "/", nil,
					)
					r.RemoteAddr = "192.0.2.1:12345"
					w := httptest.NewRecorder()

					ready.Done()
					<-start
					p.ServeHTTP(w, r)
					statuses <- w.Code
				}()
			}
			ready.Wait()
			close(start)

			// Collect every result before asserting, so all
			// handlers finish before cleanup and only this
			// goroutine counts.
			counts := make(map[int]int)
			for range numRequests {
				counts[<-statuses]++
			}
			require.Equal(t, limit, counts[http.StatusOK])
			require.Equal(
				t, numRequests-limit,
				counts[http.StatusPaymentRequired],
			)
		})
	}
}
