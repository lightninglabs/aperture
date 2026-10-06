package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/stretchr/testify/require"
)

// TestProxyMatchesCanonicalPath verifies that an ambiguous path cannot select
// a public service and then reach a protected backend path.
func TestProxyMatchesCanonicalPath(t *testing.T) {
	received := make(chan *http.Request, 1)
	backend := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			received <- r.Clone(r.Context())
			w.WriteHeader(http.StatusNoContent)
		},
	))
	t.Cleanup(backend.Close)

	address := strings.TrimPrefix(backend.URL, "http://")
	services := []*Service{
		{
			Name:       "public",
			Address:    address,
			Protocol:   "http",
			Auth:       "off",
			HostRegexp: ".*",
			PathRegexp: "^/public(?:/.*)?$",
		},
		{
			Name:       "private",
			Address:    address,
			Protocol:   "http",
			Auth:       "on",
			HostRegexp: ".*",
			PathRegexp: "^/private(?:/.*)?$",
		},
	}
	p, err := New(auth.NewMockAuthenticator(), services, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, p.Close())
	})

	for _, requestPath := range []string{
		"/public/../private",
		"/public/%2e%2e/private",
		"/public/..;/private",
		"/public/..%3B/private",
		"/public/..%5Cprivate",
	} {
		response := servePublicAuthRequest(
			p, requestPath, "192.0.2.1", nil,
		)
		require.Equal(t, http.StatusBadRequest, response.Code)
	}

	response := servePublicAuthRequest(
		p, "/private", "192.0.2.1", nil,
	)
	require.Equal(t, http.StatusPaymentRequired, response.Code)

	response = servePublicAuthRequest(
		p, "/private/%2e/resource", "192.0.2.1", http.Header{
			"Authorization": {"test credential"},
		},
	)
	require.Equal(t, http.StatusNoContent, response.Code)
	backendRequest := <-received
	require.Equal(t, "/private/resource", backendRequest.URL.Path)
	require.Empty(t, backendRequest.URL.RawPath)

	// Cleaning keeps a trailing slash.
	response = servePublicAuthRequest(
		p, "/private//resource/", "192.0.2.1", http.Header{
			"Authorization": {"test credential"},
		},
	)
	require.Equal(t, http.StatusNoContent, response.Code)
	backendRequest = <-received
	require.Equal(t, "/private/resource/", backendRequest.URL.Path)
}

// TestCanonicalizeRequestPath checks the canonical form of request paths and
// which spellings of a parent traversal are refused.
func TestCanonicalizeRequestPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/a/b", want: "/a/b"},
		{path: "/a//b", want: "/a/b"},
		{path: "/a/./b", want: "/a/b"},

		// Backends and file servers treat a trailing slash as naming
		// a different resource.
		{path: "/a//b/", want: "/a/b/"},
		{path: "/", want: "/"},

		// An empty path is forwarded as "/", so it is matched as one.
		{path: "", want: "/"},

		// Other runs of dots are ordinary names.
		{path: "/a/.../b", want: "/a/.../b"},
		{path: "/a/..b", want: "/a/..b"},
	}
	for _, test := range tests {
		req := &http.Request{URL: &url.URL{Path: test.path}}
		require.True(t, canonicalizeRequestPath(req), test.path)
		require.Equal(t, test.want, req.URL.Path, test.path)
	}

	// Parent segments are refused, and so are semicolons and backslashes,
	// which some servers drop or treat as separators before routing.
	for _, requestPath := range []string{
		"/..", "/a/../b", "/a/..;/b", "/a/..;x=1/b",
		`/a/..\b`, `/a\..\b`,
		"/a;x/b", "/a/b;", `/a\b`, `/a/b\`,
	} {
		req := &http.Request{URL: &url.URL{Path: requestPath}}
		require.False(t, canonicalizeRequestPath(req), requestPath)
	}

	// The escaped form of a canonical path is kept.
	req := &http.Request{URL: &url.URL{Path: "/a/b", RawPath: "/a%2Fb"}}
	require.True(t, canonicalizeRequestPath(req))
	require.Equal(t, "/a%2Fb", req.URL.RawPath)

	// Only "OPTIONS *" may name no path. "GET *" and an opaque target,
	// which name none either, are refused.
	options := &http.Request{
		Method: http.MethodOptions, URL: &url.URL{Path: "*"},
	}
	require.True(t, canonicalizeRequestPath(options))
	require.Equal(t, "*", options.URL.Path)
	for _, target := range []*url.URL{
		{Path: "*"},
		{Scheme: "http", Opaque: "premium/data"},
	} {
		req := &http.Request{Method: http.MethodGet, URL: target}
		require.False(t, canonicalizeRequestPath(req), target.String())
	}
}

// TestCanonicalizeEscapedPath checks that cleaning keeps every remaining
// segment as the client escaped it, so an encoded character never turns into
// a delimiter, and that the request target the pricer serializes follows the
// cleaned path.
func TestCanonicalizeEscapedPath(t *testing.T) {
	tests := []struct {
		target      string
		wantPath    string
		wantEscaped string
	}{
		// An encoded slash or equals sign stays encoded while the
		// segments around it are cleaned.
		{
			target:      "/x//a%2Fb",
			wantPath:    "/x/a/b",
			wantEscaped: "/x/a%2Fb",
		},
		{
			target:      "/x//a%3Db",
			wantPath:    "/x/a=b",
			wantEscaped: "/x/a%3Db",
		},

		// An escaped dot segment is still a dot segment.
		{target: "/a/%2e/b", wantPath: "/a/b", wantEscaped: "/a/b"},

		// A path that needs no cleaning is left as it was sent.
		{
			target:      "/accounts/%2Fspecial",
			wantPath:    "/accounts//special",
			wantEscaped: "/accounts/%2Fspecial",
		},
	}
	for _, test := range tests {
		req := httptest.NewRequest(
			http.MethodGet, test.target+"?x=1", nil,
		)
		require.True(t, canonicalizeRequestPath(req), test.target)
		require.Equal(t, test.wantPath, req.URL.Path, test.target)
		require.Equal(
			t, test.wantEscaped, req.URL.EscapedPath(), test.target,
		)

		// The query is kept with the cleaned path.
		require.Equal(
			t, test.wantEscaped+"?x=1", req.RequestURI, test.target,
		)
		text, err := pricer.SerializeRequest(req)
		require.NoError(t, err)
		line, _, _ := strings.Cut(text, "\r\n")
		require.Equal(
			t, "GET "+test.wantEscaped+"?x=1 HTTP/1.1", line,
			test.target,
		)
	}

	// Like net/url, RawPath is only set when it differs from the default
	// encoding of Path.
	req := httptest.NewRequest(http.MethodGet, "/a/%2e/b", nil)
	require.True(t, canonicalizeRequestPath(req))
	require.Empty(t, req.URL.RawPath)
}

// TestProxyKeepsEscapedPath checks that the backend receives encoded
// characters as the client sent them, with and without a rewrite prefix, even
// when other parts of the path are cleaned.
func TestProxyKeepsEscapedPath(t *testing.T) {
	tests := []struct {
		prefix string
		target string
		want   string
	}{
		{target: "/accounts/%2Fspecial", want: "/accounts/%2Fspecial"},
		{
			prefix: "/api",
			target: "/accounts/%2Fspecial",
			want:   "/api/accounts/%2Fspecial",
		},
		{target: "/x//a%2Fb", want: "/x/a%2Fb"},
		{target: "/x//a%3Db", want: "/x/a%3Db"},
	}
	for _, test := range tests {
		service := &Service{
			Auth:    "off",
			Rewrite: RewriteConfig{Prefix: test.prefix},
		}
		p, received := newPublicAuthProxy(
			t, service, auth.NewMockAuthenticator(),
		)
		response := servePublicAuthRequest(
			p, test.target, "192.0.2.1", nil,
		)
		require.Equal(t, http.StatusNoContent, response.Code, test.target)

		forwarded := <-received
		require.Equal(
			t, test.want, forwarded.URL.EscapedPath(), test.target,
		)
	}
}

// TestProxyCanonicalPathAuthWhitelist verifies that an ambiguous path cannot
// use a public whitelist entry to reach a protected path in the same service.
func TestProxyCanonicalPathAuthWhitelist(t *testing.T) {
	service := &Service{
		Auth:               "on",
		PathRegexp:         "^/.*$",
		AuthWhitelistPaths: []string{"^/public(?:/.*)?$"},
	}
	p, received := newPublicAuthProxy(
		t, service, auth.NewMockAuthenticator(),
	)

	response := servePublicAuthRequest(
		p, "/public/../private", "192.0.2.1", nil,
	)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Empty(t, received)
}

// TestProxyRefusesTargetsWithoutPath checks that a request target that is not
// an absolute path cannot reach a service. A dynamic-price service named "svc"
// would otherwise mint a token named "svc*" for "GET *", which a separate
// service of that name would accept, and an opaque target would be matched as
// "/" but reach the backend as the path it spells.
func TestProxyRefusesTargetsWithoutPath(t *testing.T) {
	a := &publicAuthRecorder{scheme: auth.AuthSchemeL402}
	service := &Service{Auth: "on"}
	p, received := newPublicAuthProxy(t, service, a)

	// With no path pattern, the service matches any target, and with
	// dynamic prices its resource names end in the target.
	service.DynamicPrice.Enabled = true

	for _, target := range []string{"*", "http:premium/data"} {
		response := httptest.NewRecorder()
		p.ServeHTTP(
			response, httptest.NewRequest(http.MethodGet, target, nil),
		)
		require.Equal(t, http.StatusBadRequest, response.Code, target)
	}
	require.Empty(t, a.resources)
	require.Zero(t, a.challenges)
	require.Empty(t, received)

	// "OPTIONS *" is still answered.
	response := httptest.NewRecorder()
	p.ServeHTTP(
		response, httptest.NewRequest(http.MethodOptions, "*", nil),
	)
	require.Equal(t, http.StatusOK, response.Code)
	require.Zero(t, a.challenges)
}

// TestProxyRefusesAmbiguousSeparators checks that an anonymous request cannot
// reach a protected resource through a semicolon or a backslash, literal or
// percent-encoded, which a backend may drop or treat as a separator after
// Aperture matched the path against a free service or a public whitelist.
func TestProxyRefusesAmbiguousSeparators(t *testing.T) {
	received := make(chan *http.Request, 16)
	backend := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			received <- r.Clone(r.Context())
			w.WriteHeader(http.StatusNoContent)
		},
	))
	t.Cleanup(backend.Close)
	address := strings.TrimPrefix(backend.URL, "http://")

	newProxy := func(services ...*Service) *Proxy {
		for _, service := range services {
			service.Address = address
			service.Protocol = "http"
			service.HostRegexp = ".*"
		}
		p, err := New(auth.NewMockAuthenticator(), services, nil, nil)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, p.Close())
		})

		return p
	}

	proxies := map[string]*Proxy{
		// A paid service in front of a free catch-all for the same
		// backend.
		"free catch-all": newProxy(
			&Service{
				Name:       "premium",
				Auth:       "on",
				PathRegexp: "^/premium/",
			},
			&Service{
				Name:       "free",
				Auth:       "off",
				PathRegexp: "^/.*$",
			},
		),

		// A public whitelist entry within a paid service.
		"public whitelist": newProxy(&Service{
			Name:               "service",
			Auth:               "on",
			PathRegexp:         "^/.*$",
			AuthWhitelistPaths: []string{`\.css$`},
		}),
	}

	for name, p := range proxies {
		for _, requestPath := range []string{
			"/premium;x/data", "/premium%3Bx/data",
			`/premium\data`, "/premium%5Cdata",
			"/private/data;.css", "/private/data%3B.css",
		} {
			response := servePublicAuthRequest(
				p, requestPath, "192.0.2.1", nil,
			)
			require.Equal(
				t, http.StatusBadRequest, response.Code,
				"%s: %s", name, requestPath,
			)
		}
	}
	require.Empty(t, received)
}
