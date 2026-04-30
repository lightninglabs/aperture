package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareRewriteValidation(t *testing.T) {
	testCases := []struct {
		name    string
		rewrite RewriteConfig
		wantErr string
	}{
		{
			name:    "valid prefix absolute path",
			rewrite: RewriteConfig{Prefix: "/v1/api"},
		},
		{
			name:    "empty prefix is noop",
			rewrite: RewriteConfig{Prefix: ""},
		},
		{
			name:    "reject relative prefix",
			rewrite: RewriteConfig{Prefix: "v1/api"},
			wantErr: "invalid prefix format",
		},
		{
			name:    "reject prefix with scheme and host",
			rewrite: RewriteConfig{Prefix: "https://example.com/v1/api"},
			wantErr: "invalid prefix format",
		},
		{
			name:    "reject prefix with query string",
			rewrite: RewriteConfig{Prefix: "/v1/api?foo=bar"},
			wantErr: "invalid prefix format",
		},
		{
			name:    "reject prefix with fragment",
			rewrite: RewriteConfig{Prefix: "/v1/api#section"},
			wantErr: "invalid prefix format",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{Rewrite: tc.rewrite}
			err := service.prepareRewrite()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestRewriteRequestPath(t *testing.T) {
	testCases := []struct {
		name            string
		prefix          string
		requestPath     string
		expectedPath    string
		expectedRawPath string
	}{
		{
			name:         "prefix is prepended",
			prefix:       "/api",
			requestPath:  "/users",
			expectedPath: "/api/users",
		},
		{
			name:         "trailing slashes preserved",
			prefix:       "/api/",
			requestPath:  "/users/",
			expectedPath: "/api/users/",
		},
		{
			name:   "encoded slash preserved in RawPath",
			prefix: "/api",
			// %2F decodes to / in Path but is preserved
			// in RawPath so backends can distinguish it.
			requestPath:     "/accounts/%2Fspecial",
			expectedPath:    "/api/accounts//special",
			expectedRawPath: "/api/accounts/%2Fspecial",
		},
		{
			name:         "empty request path (root)",
			prefix:       "/api",
			requestPath:  "/",
			expectedPath: "/api/",
		},
		{
			name:         "identity rewrite with root prefix",
			prefix:       "/",
			requestPath:  "/users",
			expectedPath: "/users",
		},
		{
			name:         "double slashes in request normalized",
			prefix:       "/api",
			requestPath:  "//users",
			expectedPath: "/api/users",
		},
		{
			name:         "no prefix is noop",
			prefix:       "",
			requestPath:  "/users",
			expectedPath: "/users",
		},
		{
			name:         "deeply nested prefix",
			prefix:       "/v1/internal/api",
			requestPath:  "/users/123",
			expectedPath: "/v1/internal/api/users/123",
		},
		{
			name:   "encoded space decoded in Path",
			prefix: "/api",
			// Spaces are auto-escaped by EscapedPath() so
			// RawPath is not needed.
			requestPath:  "/users/John%20Doe",
			expectedPath: "/api/users/John Doe",
		},
		{
			name:         "dot segments cleaned",
			prefix:       "/api",
			requestPath:  "/users/../admin",
			expectedPath: "/api/admin",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{
				Rewrite: RewriteConfig{Prefix: tc.prefix},
			}

			// Only run prepareRewrite for non-empty prefixes
			// since empty prefix is a noop.
			if tc.prefix != "" {
				err := service.prepareRewrite()
				require.NoError(t, err)
			}

			req := httptest.NewRequest(
				"GET", "http://example.com"+tc.requestPath,
				nil,
			)
			service.rewriteRequestPath(req)
			require.Equal(t, tc.expectedPath, req.URL.Path)

			if tc.expectedRawPath != "" {
				require.Equal(
					t, tc.expectedRawPath,
					req.URL.RawPath,
				)
			}
		})
	}
}

func TestRewriteRequestPathPreservesRawPath(t *testing.T) {
	service := &Service{
		Rewrite: RewriteConfig{Prefix: "/api"},
	}
	err := service.prepareRewrite()
	require.NoError(t, err)

	req := httptest.NewRequest(
		"GET", "http://example.com/users%2Fprofile", nil,
	)

	// Confirm RawPath is set by the parser before the rewrite.
	require.NotEmpty(t, req.URL.RawPath)

	service.rewriteRequestPath(req)

	// Path contains the clean decoded form.
	require.Equal(t, "/api/users/profile", req.URL.Path)

	// RawPath should preserve the encoded slash rather than being
	// blanket-cleared.
	require.Equal(t, "/api/users%2Fprofile", req.URL.RawPath)
}

func TestDirectorRewritePrefix(t *testing.T) {
	services := []*Service{{
		Name:       "test",
		Address:    "backend:8080",
		Protocol:   "https",
		Auth:       "off",
		HostRegexp: "^example\\.com$",
		PathRegexp: "^/.*$",
		Rewrite:    RewriteConfig{Prefix: "/api"},
	}}

	err := prepareServices(services, "")
	require.NoError(t, err)

	p := &Proxy{
		services: services,
	}

	req := httptest.NewRequest("GET", "http://example.com/users", nil)
	p.director(req)

	require.Equal(t, "backend:8080", req.Host)
	require.Equal(t, "backend:8080", req.URL.Host)
	require.Equal(t, "https", req.URL.Scheme)
	require.Equal(t, "/api/users", req.URL.Path)
}
