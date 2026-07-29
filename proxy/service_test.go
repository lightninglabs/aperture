package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExpandHeaderEnv makes sure ${NAME} environment references in service
// header values are expanded at startup, and that a reference to an unset
// variable fails rather than substituting an empty string.
func TestExpandHeaderEnv(t *testing.T) {
	t.Setenv("APERTURE_TEST_KEY", "s3cret")

	tests := []struct {
		name     string
		value    string
		expected string
		errRegex string
	}{{
		name:     "no reference passes through",
		value:    "application/json",
		expected: "application/json",
	}, {
		name:     "bare reference",
		value:    "${APERTURE_TEST_KEY}",
		expected: "s3cret",
	}, {
		name:     "reference composes with a literal prefix",
		value:    "Bearer ${APERTURE_TEST_KEY}",
		expected: "Bearer s3cret",
	}, {
		name:     "unset variable fails",
		value:    "Bearer ${APERTURE_TEST_UNSET}",
		errRegex: "APERTURE_TEST_UNSET.*not set",
	}, {
		name:     "unbraced dollar is left alone",
		value:    "cost is $5",
		expected: "cost is $5",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expanded, err := expandHeaderEnv(tc.value)
			if tc.errRegex != "" {
				require.ErrorContains(
					t, err, "APERTURE_TEST_UNSET",
				)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.expected, expanded)
		})
	}
}

// TestPrepareServicesHeaderEnv makes sure prepareServices applies the
// environment expansion to every configured header value.
func TestPrepareServicesHeaderEnv(t *testing.T) {
	t.Setenv("APERTURE_TEST_KEY", "s3cret")

	service := &Service{
		Name:       "test",
		HostRegexp: ".*",
		Headers: map[string]string{
			"Authorization": "Bearer ${APERTURE_TEST_KEY}",
			"X-Static":      "unchanged",
		},
	}
	require.NoError(t, prepareServices([]*Service{service}))
	require.Equal(t, "Bearer s3cret", service.Headers["Authorization"])
	require.Equal(t, "unchanged", service.Headers["X-Static"])

	missing := &Service{
		Name:       "test",
		HostRegexp: ".*",
		Headers: map[string]string{
			"Authorization": "Bearer ${APERTURE_TEST_UNSET}",
		},
	}
	require.ErrorContains(
		t, prepareServices([]*Service{missing}),
		"APERTURE_TEST_UNSET",
	)
}

// TestDirectorOverwritesClientHeaders makes sure a configured service header
// replaces the client's value outright rather than being appended after it. A
// client's own Authorization (the L402 header itself) must not shadow the
// configured upstream credential.
func TestDirectorOverwritesClientHeaders(t *testing.T) {
	service := &Service{
		Name:       "test",
		Address:    "upstream.example.com",
		Protocol:   "https",
		HostRegexp: ".*",
		Headers: map[string]string{
			"Authorization": "Bearer upstream-key",
		},
	}
	require.NoError(t, prepareServices([]*Service{service}))

	p := &Proxy{services: []*Service{service}}

	req := httptest.NewRequest("POST", "http://gateway/v1/foo", nil)
	req.Header.Set("Authorization", "L402 macaroon:preimage")

	p.director(req)

	require.Equal(
		t, []string{"Bearer upstream-key"},
		req.Header.Values("Authorization"),
	)
	require.Equal(t, "upstream.example.com", req.Host)
	require.Equal(t, "https", req.URL.Scheme)
}
