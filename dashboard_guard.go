package aperture

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lightninglabs/aperture/proxy"
)

// dashboardProxyPrefix is the path the dashboard JS uses to reach the admin
// REST API. The Go server strips this prefix and forwards internally to
// adminRESTPrefix with the admin macaroon attached.
const dashboardProxyPrefix = "/api/proxy/"

// forwardingHeaders are the request headers a reverse proxy adds when it
// relays a request on behalf of another client. A request that carries any of
// them did not come from a local user, even when the TCP peer is loopback.
var forwardingHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Real-IP",
	"Forwarded",
	"Via",
}

// directLoopback reports whether the request was sent straight to aperture
// by a process on this machine. A loopback peer address alone is not enough:
// a local reverse proxy connects from loopback for every request it relays,
// including requests from the public internet, so the request must also carry
// none of the headers such a proxy adds.
func directLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}

	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}

	for _, h := range forwardingHeaders {
		if _, ok := r.Header[http.CanonicalHeaderKey(h)]; ok {
			return false
		}
	}

	return true
}

// requireLoopback rejects requests that fail directLoopback. The dashboard
// proxy injects admin credentials server-side, so it must only be reachable
// by a local user.
func requireLoopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !directLoopback(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validateDashboardListen checks the dedicated dashboard listen address. The
// host must be a loopback IP literal unless allowNonLoopback is set, since the
// dashboard hands out admin access to anyone who can reach it. Host names,
// including localhost, are refused because what they resolve to is outside
// aperture's control.
func validateDashboardListen(addr string, allowNonLoopback bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid admin.dashboardlisten %q: %w", addr,
			err)
	}

	if allowNonLoopback {
		return nil
	}

	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("admin.dashboardlisten %q is not a loopback "+
			"address: the dashboard grants admin access to "+
			"anyone who can reach it, so bind it to 127.0.0.1 "+
			"or [::1] and use an SSH tunnel, or set "+
			"admin.dashboardallownonloopback to override", addr)
	}

	return nil
}

// dashboardMounts decides where the dashboard is served. proxyHandler serves
// the credential-injecting /api/proxy/ path and dashHandler the static
// assets; neither should carry a peer check yet.
//
// With dedicated set, nothing is mounted on the main listener and the
// returned handler is meant for its own loopback server. Otherwise the
// dashboard is mounted on the main listener, and the catch-all only claims
// requests that pass directLoopback, so every other request falls through to
// the static file server.
func dashboardMounts(proxyHandler, dashHandler http.Handler, dedicated,
	allowNonLoopback bool) ([]proxy.LocalService, []proxy.LocalService,
	http.Handler) {

	if dedicated {
		h := http.Handler(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(
					r.URL.Path, dashboardProxyPrefix,
				) {

					proxyHandler.ServeHTTP(w, r)
					return
				}
				dashHandler.ServeHTTP(w, r)
			},
		))

		// The escape hatch exists for listeners whose peers are not
		// on this machine, so the peer check would refuse them all.
		if !allowNonLoopback {
			h = requireLoopback(h)
		}

		return nil, nil, h
	}

	priority := []proxy.LocalService{proxy.NewLocalService(
		requireLoopback(proxyHandler), func(r *http.Request) bool {
			return directLoopback(r) && strings.HasPrefix(
				r.URL.Path, dashboardProxyPrefix,
			)
		},
	)}

	// The matcher and the wrapper apply the same check. The matcher lets
	// a public request fall through to the static file server rather
	// than receive a 403, and the wrapper stays as a second guard.
	fallback := []proxy.LocalService{proxy.NewLocalService(
		requireLoopback(dashHandler), directLoopback,
	)}

	return priority, fallback, nil
}

// startDashboardServer binds the dedicated dashboard listener and serves h on
// it in the background. The returned function stops the server and waits for
// it to exit.
func startDashboardServer(cfg *Config, h http.Handler) (func(), error) {
	lis, err := net.Listen("tcp", cfg.Admin.DashboardListen)
	if err != nil {
		return nil, fmt.Errorf("unable to listen on "+
			"admin.dashboardlisten %v: %w",
			cfg.Admin.DashboardListen, err)
	}

	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)

		err := srv.Serve(lis)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("Dashboard server stopped: %v", err)
		}
	}()

	log.Infof("Admin dashboard serving on dedicated listener %v",
		lis.Addr())

	return func() {
		if err := srv.Close(); err != nil {
			log.Errorf("Error closing dashboard server: %v", err)
		}
		<-done
	}, nil
}
