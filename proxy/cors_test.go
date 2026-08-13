package proxy_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

// corsBackendAddr is a separate listen address so these tests can run
// alongside the others without fighting over a port.
const corsBackendAddr = "localhost:8087"

// startBackendCORS starts a backend that sets its own CORS headers, the way any
// service that can also be reached directly will. The proxy runs its own CORS
// logic over whatever comes back from here.
func startBackendCORS(server *http.Server) error {
	handler := func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type")

		// A header of the backend's own, which only it knows to
		// expose. Nothing in aperture can guess this belongs on the
		// list, so it is the case that proves the list is merged
		// rather than replaced.
		h.Set("Access-Control-Expose-Headers", "X-Request-Id")
		h.Set("X-Request-Id", "abc123")

		_, err := w.Write([]byte(testHTTPResponseBody))
		if err != nil {
			panic(err)
		}
	}
	server.Handler = http.HandlerFunc(handler)

	return server.ListenAndServe()
}

// startCORSProxy brings up a proxy with auth off in front of a backend that
// sets its own CORS headers, and returns once both are accepting connections.
func startCORSProxy(t *testing.T) {
	t.Helper()

	services := []*proxy.Service{{
		Address:    corsBackendAddr,
		HostRegexp: testHostRegexp,
		PathRegexp: "^/api/.*$",
		Protocol:   "http",
		Auth:       "off",
	}}

	p, err := proxy.New(auth.NewMockAuthenticator(), services, nil, nil)
	require.NoError(t, err)

	server := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			t.Errorf("proxy serve error: %v", err)
		}
	}()
	t.Cleanup(func() { closeOrFail(t, server) })

	backend := &http.Server{Addr: corsBackendAddr}
	go func() { _ = startBackendCORS(backend) }()
	t.Cleanup(func() { closeOrFail(t, backend) })

	time.Sleep(100 * time.Millisecond)
}

// TestProxyCORSNotDuplicated tests that a backend setting its own CORS headers
// does not end up with two of each on the way out.
//
// This is not cosmetic for Access-Control-Allow-Origin, which is single
// valued: two of them is invalid per the fetch standard rather than a repeated
// "*", so the browser rejects the response and the fetch throws before the
// caller sees a status code. Command line clients never notice, which is what
// makes it worth a test.
//
// The list-based fields are held to one line for a weaker reason. Repeated
// lines there are legal, since every reader joins them with ", " before
// splitting, but a single line is the form with nothing to get wrong.
func TestProxyCORSNotDuplicated(t *testing.T) {
	startCORSProxy(t)

	req, err := http.NewRequest(
		"GET", fmt.Sprintf("http://%s/api/test", testProxyAddr), nil,
	)
	require.NoError(t, err)

	req.Header.Set("Origin", "https://example.com")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Exactly one of each, no matter that the backend set them too.
	corsFields := []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
		"Access-Control-Expose-Headers",
	}
	for _, field := range corsFields {
		require.Len(
			t, resp.Header.Values(field), 1,
			"expected exactly one %s", field,
		)
	}

	require.Equal(
		t, "*", resp.Header.Get("Access-Control-Allow-Origin"),
	)
}

// TestProxyCORSPreflightAllowsContentType tests that the preflight the proxy
// answers itself permits a JSON request body.
//
// A POST carrying application/json is not a simple request, so the browser
// preflights it. The proxy short circuits OPTIONS without ever asking the
// backend, so if Content-Type is missing from the allowed set here, every JSON
// API behind the proxy is unreachable from a browser regardless of what the
// backend would have said.
func TestProxyCORSPreflightAllowsContentType(t *testing.T) {
	startCORSProxy(t)

	req, err := http.NewRequest(
		"OPTIONS", fmt.Sprintf("http://%s/api/test", testProxyAddr),
		nil,
	)
	require.NoError(t, err)

	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	allowed := resp.Header.Get("Access-Control-Allow-Headers")
	require.Contains(t, allowed, "Content-Type")

	require.Len(t, resp.Header.Values("Access-Control-Allow-Origin"), 1)
}

// TestProxyCORSKeepsBackendExposedHeaders tests that a backend's own entries in
// the list based CORS fields survive the trip through the proxy.
//
// Access-Control-Expose-Headers is what a service uses to say which of its
// response headers JS is allowed to read. Only the backend knows that list.
// Overwriting it with aperture's own entries would leave every such header
// unreadable from the browser the moment the service moved behind the proxy,
// and the failure is silent: headers.get() returns null rather than raising,
// so it reads as the backend having stopped sending the header at all.
func TestProxyCORSKeepsBackendExposedHeaders(t *testing.T) {
	startCORSProxy(t)

	req, err := http.NewRequest(
		"GET", fmt.Sprintf("http://%s/api/test", testProxyAddr), nil,
	)
	require.NoError(t, err)

	req.Header.Set("Origin", "https://example.com")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	// One field line, carrying the backend's entry alongside ours.
	exposed := resp.Header.Values("Access-Control-Expose-Headers")
	require.Len(t, exposed, 1)

	entries := splitCORSList(exposed[0])
	require.Contains(t, entries, "X-Request-Id")
	require.Contains(t, entries, "WWW-Authenticate")
	require.Contains(t, entries, "Payment-Receipt")

	// The backend's entry keeps its place at the front, so the list reads
	// in the order that service published it.
	require.Equal(t, "X-Request-Id", entries[0])
}

// TestProxyCORSDoesNotRepeatSharedEntries tests that an entry both the backend
// and aperture name appears once rather than twice.
//
// The backend stub sets Access-Control-Allow-Headers: Content-Type, which is
// also on aperture's list. A naive append would emit it twice. That is
// harmless to a browser, which dedupes on read, but it grows without bound
// across any future layer that does the same thing.
func TestProxyCORSDoesNotRepeatSharedEntries(t *testing.T) {
	startCORSProxy(t)

	req, err := http.NewRequest(
		"GET", fmt.Sprintf("http://%s/api/test", testProxyAddr), nil,
	)
	require.NoError(t, err)

	req.Header.Set("Origin", "https://example.com")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	allowed := splitCORSList(
		resp.Header.Get("Access-Control-Allow-Headers"),
	)

	var contentTypes int
	for _, entry := range allowed {
		if strings.EqualFold(entry, "Content-Type") {
			contentTypes++
		}
	}
	require.Equal(t, 1, contentTypes, "got %v", allowed)

	// Same for the methods, every one of which the backend also names.
	methods := splitCORSList(
		resp.Header.Get("Access-Control-Allow-Methods"),
	)
	require.ElementsMatch(
		t, []string{"GET", "POST", "OPTIONS"}, methods,
	)
}

// splitCORSList splits a list based header field value into its entries.
func splitCORSList(value string) []string {
	var entries []string
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			entries = append(entries, entry)
		}
	}

	return entries
}
