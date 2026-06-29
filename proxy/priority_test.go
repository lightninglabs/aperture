package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

// TestCatchAllPriorityHijacksBackend verifies that a catch-all local service
// placed in priorityLocalServices will intercept requests that should be
// proxied to a configured backend. This is the regression test for C-1.
func TestCatchAllPriorityHijacksBackend(t *testing.T) {
	// Start a real backend HTTP server so we can detect whether the proxy
	// actually forwarded the request.
	const backendBody = "BACKEND_OK"
	backend := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(backendBody))
		},
	))
	defer backend.Close()

	// Parse the backend address (host:port) from the test server URL.
	backendAddr := strings.TrimPrefix(backend.URL, "http://")

	services := []*proxy.Service{{
		Address:    backendAddr,
		HostRegexp: ".*",
		PathRegexp: "^/api/.*$",
		Protocol:   "http",
		Auth:       "off",
	}}

	// Create a catch-all handler that returns a distinctive body so we
	// can tell whether it intercepted the request.
	const catchAllBody = "CATCHALL_INDEX_HTML"
	catchAll := proxy.NewLocalService(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(catchAllBody))
		}),
		func(_ *http.Request) bool { return true },
	)

	// Create a prefix-matched handler (like the dashboard proxy).
	const prefixBody = "PREFIX_HANDLER"
	prefixHandler := proxy.NewLocalService(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(prefixBody))
		}),
		func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, "/dashboard/")
		},
	)

	mockAuth := auth.NewMockAuthenticator()

	// ---------- Bug scenario: catch-all in priorityLocalServices ----------
	// When the catch-all is a priority service, it intercepts everything
	// including requests that should go to the backend.
	bugProxy, err := proxy.New(
		mockAuth, services, "", nil,
		[]proxy.LocalService{prefixHandler, catchAll}, // priority
	)
	require.NoError(t, err)

	// A request to /api/test should reach the backend, but with the bug
	// it will be intercepted by the catch-all.
	req := httptest.NewRequest("GET", "http://localhost/api/test", nil)
	rec := httptest.NewRecorder()
	bugProxy.ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Result().Body)
	require.Equal(t, catchAllBody, string(body),
		"Bug confirmation: catch-all in priorityLocalServices "+
			"should hijack backend requests")

	// ---------- Fix scenario: catch-all in localServices ----------
	// When the catch-all is moved to localServices (checked after proxy
	// backend matching), backend requests are correctly forwarded.
	fixedProxy, err := proxy.New(
		mockAuth, services, "", nil,
		[]proxy.LocalService{prefixHandler}, // priority: only prefix
		catchAll,                            // local: catch-all
	)
	require.NoError(t, err)

	// A request to /api/test should now reach the backend.
	req = httptest.NewRequest("GET", "http://localhost/api/test", nil)
	rec = httptest.NewRecorder()
	fixedProxy.ServeHTTP(rec, req)

	body, _ = io.ReadAll(rec.Result().Body)
	require.Equal(t, backendBody, string(body),
		"Fix verification: backend should handle /api/test "+
			"when catch-all is in localServices")

	// A request to /dashboard/ should still be handled by the prefix
	// priority handler.
	req = httptest.NewRequest("GET", "http://localhost/dashboard/foo", nil)
	rec = httptest.NewRecorder()
	fixedProxy.ServeHTTP(rec, req)

	body, _ = io.ReadAll(rec.Result().Body)
	require.Equal(t, prefixBody, string(body),
		"Priority prefix handler should still work")

	// A request to an unknown path (no backend match) should fall through
	// to the catch-all in localServices.
	req = httptest.NewRequest("GET", "http://localhost/unknown/path", nil)
	rec = httptest.NewRecorder()
	fixedProxy.ServeHTTP(rec, req)

	body, _ = io.ReadAll(rec.Result().Body)
	require.Equal(t, catchAllBody, string(body),
		"Catch-all in localServices should handle unmatched paths")

	// Verify the prefix handler in priority does NOT handle /api/* paths.
	req = httptest.NewRequest(
		"GET", "http://localhost/api/other", nil,
	)
	rec = httptest.NewRecorder()
	fixedProxy.ServeHTTP(rec, req)

	body, _ = io.ReadAll(rec.Result().Body)
	require.Equal(t, backendBody, string(body),
		"Backend should handle all /api/* paths")

}
