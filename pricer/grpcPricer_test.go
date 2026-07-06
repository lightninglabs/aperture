package pricer

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConfigValidate exercises the dynamic pricer configuration checks,
// including the metered-but-disabled misconfiguration and the UsageTailBytes
// bound.
func TestConfigValidate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{{
		name: "disabled is valid",
		cfg:  Config{},
	}, {
		name: "enabled and metered is valid",
		cfg:  Config{Enabled: true, Metered: true},
	}, {
		name:    "metered without enabled",
		cfg:     Config{Metered: true},
		wantErr: "requires dynamicprice.enabled",
	}, {
		name:    "negative usage tail bytes",
		cfg:     Config{Enabled: true, UsageTailBytes: -1},
		wantErr: "must not be negative",
	}, {
		name: "usage tail bytes at the max is allowed",
		cfg:  Config{Enabled: true, UsageTailBytes: MaxUsageTailBytes},
	}, {
		name: "usage tail bytes over the max",
		cfg: Config{
			Enabled:        true,
			UsageTailBytes: MaxUsageTailBytes + 1,
		},
		wantErr: "exceeds the maximum",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestDumpRequestCapsBody verifies that a large request body is capped in the
// serialized pricer text, while the original request body is restored intact so
// the backend still receives every byte.
func TestDumpRequestCapsBody(t *testing.T) {
	t.Parallel()

	// Build a body far larger than the pricer cap, with the model field at
	// the head where the pricer needs it.
	head := `{"model":"gpt-test","padding":"`
	body := head + strings.Repeat("a", maxPricerBodyBytes*2) + `"}`

	r := httptest.NewRequest(
		"POST", "/v1/chat/completions", strings.NewReader(body),
	)

	reqText, err := dumpRequest(r)
	require.NoError(t, err)

	// The serialized text must be bounded near the cap, not the full body.
	require.Less(t, len(reqText), maxPricerBodyBytes+len(head)+1024)

	// The model at the head must still be present for the pricer.
	require.Contains(t, reqText, `"model":"gpt-test"`)

	// The request body must be restored in full for the backend.
	gotBody, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(gotBody))
}

// TestDumpRequestNoBody verifies that a request without a body serializes its
// headers without error.
func TestDumpRequestNoBody(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Body = http.NoBody

	reqText, err := dumpRequest(r)
	require.NoError(t, err)
	require.Contains(t, reqText, "GET /v1/models")
}
