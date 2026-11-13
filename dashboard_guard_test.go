package aperture

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// newGuardRequest creates a request with the given peer address and headers.
func newGuardRequest(path, remoteAddr string, hdrs map[string]string,
) *http.Request {

	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = remoteAddr
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}

	return r
}

// TestDirectLoopback asserts that a loopback peer alone is not enough, and
// that every header a reverse proxy adds disqualifies the request.
func TestDirectLoopback(t *testing.T) {
	t.Parallel()

	require.True(t, directLoopback(
		newGuardRequest("/", "127.0.0.1:5000", nil),
	))
	require.True(t, directLoopback(
		newGuardRequest("/", "[::1]:5000", nil),
	))

	// A forwarding header disqualifies a loopback peer, whatever its
	// value is and whichever way it is cased on the wire.
	for _, h := range forwardingHeaders {
		for _, v := range []string{"203.0.113.9", ""} {
			r := newGuardRequest("/", "127.0.0.1:5000", nil)
			r.Header[http.CanonicalHeaderKey(h)] = []string{v}
			require.False(t, directLoopback(r), "header %s", h)

			r = newGuardRequest("/", "[::1]:5000", nil)
			r.Header[http.CanonicalHeaderKey(h)] = []string{v}
			require.False(t, directLoopback(r), "header %s", h)
		}
	}

	for _, addr := range []string{
		"203.0.113.9:5000", "[2001:db8::1]:5000", "10.0.0.1:1",
		"127.0.0.1", "garbage", "", "localhost:80", "[::1]",
	} {
		require.False(t, directLoopback(
			newGuardRequest("/", addr, nil),
		), "addr %q", addr)
	}
}

// TestValidateDashboardListen checks the loopback requirement on the
// dedicated dashboard address and its escape hatch.
func TestValidateDashboardListen(t *testing.T) {
	t.Parallel()

	for _, addr := range []string{
		"127.0.0.1:8090", "[::1]:8090", "127.0.0.2:8090",
	} {
		require.NoError(t, validateDashboardListen(addr, false), addr)
	}

	bad := []string{
		"0.0.0.0:8090", "8.8.8.8:8090", ":8090", "[::]:8090",
		"example.com:8090", "localhost:8090", "192.168.1.5:8090",
	}
	for _, addr := range bad {
		err := validateDashboardListen(addr, false)
		require.ErrorContains(t, err, "not a loopback", addr)

		require.NoError(t, validateDashboardListen(addr, true), addr)
	}

	// A malformed address is an error even with the escape hatch.
	require.Error(t, validateDashboardListen("8090", false))
	require.Error(t, validateDashboardListen("8090", true))

	// The check runs as part of config validation.
	cfg := NewConfig()
	cfg.Authenticator.Disable = true
	cfg.ListenAddr = "127.0.0.1:8081"
	cfg.Admin = &AdminConfig{DashboardListen: "0.0.0.0:1"}
	require.ErrorContains(t, cfg.validate(), "not a loopback")
	cfg.Admin.DashboardAllowNonLoopback = true
	require.NoError(t, cfg.validate())
}

// TestDashboardMountsDedicated asserts that a dedicated dashboard is not
// mounted on the main listener at all and is guarded on its own listener.
func TestDashboardMountsDedicated(t *testing.T) {
	t.Parallel()

	prio, fall, h := dashboardMounts(
		textHandler("api"), textHandler("dash"), true, false,
	)
	require.Empty(t, prio)
	require.Empty(t, fall)
	require.NotNil(t, h)

	serve := func(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	rec := serve(h, newGuardRequest("/", "127.0.0.1:1", nil))
	require.Equal(t, "dash", rec.Body.String())
	rec = serve(h, newGuardRequest(
		dashboardProxyPrefix+"stats", "127.0.0.1:1", nil,
	))
	require.Equal(t, "api", rec.Body.String())

	// Without the escape hatch a non-loopback peer is refused, and so is
	// a request relayed by a local reverse proxy.
	rec = serve(h, newGuardRequest("/", "203.0.113.9:1", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = serve(h, newGuardRequest("/", "127.0.0.1:1", map[string]string{
		"X-Forwarded-For": "203.0.113.9",
	}))
	require.Equal(t, http.StatusForbidden, rec.Code)

	// With it, the peer check is dropped.
	_, _, h = dashboardMounts(
		textHandler("api"), textHandler("dash"), true, true,
	)
	rec = serve(h, newGuardRequest("/", "203.0.113.9:1", nil))
	require.Equal(t, "dash", rec.Body.String())
}

func textHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
}

// TestDashboardFallsThroughToStatic builds the real main proxy handler with
// the dashboard mounted on it and asserts that only direct loopback requests
// reach the dashboard, while every other request gets the static page or a
// 404.
func TestDashboardFallsThroughToStatic(t *testing.T) {
	t.Parallel()

	for _, withStatic := range []bool{true, false} {
		root := t.TempDir()
		err := os.WriteFile(
			filepath.Join(root, "index.html"), []byte("landing"),
			0o600,
		)
		require.NoError(t, err)

		cfg := &Config{
			ServeStatic:   withStatic,
			StaticRoot:    root,
			Authenticator: &AuthConfig{},
			HashMail:      &HashMailConfig{},
		}

		prio, fall, h := dashboardMounts(
			textHandler("admin-api"), textHandler("dashboard"),
			false, false,
		)
		require.Nil(t, h)

		prxy, _, err := createProxy(
			cfg, nil, nil, nil, nil, nil, nil, nil, nil, prio, fall,
		)
		require.NoError(t, err)

		do := func(path, peer string,
			hdrs map[string]string) *httptest.ResponseRecorder {

			rec := httptest.NewRecorder()
			prxy.ServeHTTP(rec, newGuardRequest(path, peer, hdrs))
			return rec
		}

		// A direct local request reaches the dashboard and its API
		// proxy.
		rec := do("/", "127.0.0.1:1", nil)
		require.Equal(t, "dashboard", rec.Body.String())
		rec = do(dashboardProxyPrefix+"stats", "[::1]:1", nil)
		require.Equal(t, "admin-api", rec.Body.String())

		// The same request relayed by a local reverse proxy, for
		// each forwarding header, must not.
		for _, hdr := range forwardingHeaders {
			hdrs := map[string]string{hdr: "203.0.113.9"}

			for _, path := range []string{
				"/", "/services/",
				dashboardProxyPrefix + "stats",
			} {
				rec := do(path, "127.0.0.1:1", hdrs)
				require.NotContains(t, rec.Body.String(),
					"dashboard", "%s %s", hdr, path)
				require.NotContains(t, rec.Body.String(),
					"admin-api", "%s %s", hdr, path)
			}

			rec := do("/", "127.0.0.1:1", hdrs)
			if withStatic {
				require.Equal(t, http.StatusOK, rec.Code)
				require.Contains(t, rec.Body.String(),
					"landing")
			} else {
				require.Equal(t, http.StatusNotFound, rec.Code)
			}
		}

		// A remote peer never reaches it either.
		rec = do("/", "203.0.113.9:1", nil)
		require.NotContains(t, rec.Body.String(), "dashboard")
		rec = do(dashboardProxyPrefix+"stats", "203.0.113.9:1", nil)
		require.NotContains(t, rec.Body.String(), "admin-api")
	}
}
