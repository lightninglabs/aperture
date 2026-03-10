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
			name:    "reject relative prefix",
			rewrite: RewriteConfig{Prefix: "v1/api"},
			wantErr: "invalid prefix format",
		},
		{
			name:    "reject prefix with scheme and host",
			rewrite: RewriteConfig{Prefix: "https://example.com/v1/api"},
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

func TestPrepareRewriteUnknownKeys(t *testing.T) {
	testCases := []struct {
		name    string
		rewrite RewriteConfig
		wantErr string
	}{
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
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestRewriteRequestPath(t *testing.T) {
	testCases := []struct {
		name         string
		prefix       string
		requestPath  string
		expectedPath string
	}{
		{
			name:         "prefix is prepended",
			prefix:       "/api",
			requestPath:  "/users",
			expectedPath: "/api/users",
		},
		{
			name:         "joinpath normalizes trailing slashes",
			prefix:       "/api/",
			requestPath:  "/users/",
			expectedPath: "/api/users/",
		},
		{
			name:         "joinpath normalizes encoded slash segment",
			prefix:       "/api",
			requestPath:  "/accounts/%2Fspecial",
			expectedPath: "/api/accounts/special",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{
				Rewrite: RewriteConfig{Prefix: tc.prefix},
			}
			err := service.prepareRewrite()
			require.NoError(t, err)

			req := httptest.NewRequest(
				"GET", "http://example.com"+tc.requestPath, nil,
			)
			service.rewriteRequestPath(req)
			require.Equal(t, tc.expectedPath, req.URL.Path)
		})
	}
}

func TestRewriteRequestPathClearsRawPath(t *testing.T) {
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

	require.Equal(t, "/api/users/profile", req.URL.Path)
	require.Empty(t, req.URL.RawPath,
		"RawPath must be cleared so net/http uses Path unambiguously",
	)
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

	err := prepareServices(services)
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
