package pricer

import (
	"fmt"
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

// limitedReader serves a body of a fixed size and records how many bytes have
// been read from it and whether it was closed.
type limitedReader struct {
	remaining int
	read      int
	closed    bool
	byteAt    func(i int) byte
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > l.remaining {
		n = l.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = l.byteAt(l.read + i)
	}
	l.read += n
	l.remaining -= n

	return n, nil
}

func (l *limitedReader) Close() error {
	l.closed = true
	return nil
}

// TestDumpRequestBoundsBodyRead verifies that dumping a request reads no more
// than the pricer cap (plus a small slack) from the body, that the pricer text
// is capped, and that the backend still receives the full body byte-identical
// as the remainder streams through.
func TestDumpRequestBoundsBodyRead(t *testing.T) {
	t.Parallel()

	const (
		size  = maxPricerBodyBytes*8 + 123
		slack = 4096
	)

	byteAt := func(i int) byte { return byte('a' + i%26) }
	src := &limitedReader{remaining: size, byteAt: byteAt}

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Body = src
	r.ContentLength = size

	reqText, err := dumpRequest(r)
	require.NoError(t, err)

	// Memory is bounded: nothing past the cap was pulled from the client's
	// body before the backend started consuming it.
	require.LessOrEqual(t, src.read, maxPricerBodyBytes+slack)
	require.Less(t, len(reqText), maxPricerBodyBytes+1024)
	require.Equal(t, int64(size), r.ContentLength)

	// The backend gets every byte, identical, and closing the restored body
	// closes the original.
	got, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.Len(t, got, size)
	for i := range got {
		if got[i] != byteAt(i) {
			t.Fatalf("body differs at offset %d", i)
		}
	}

	require.False(t, src.closed)
	require.NoError(t, r.Body.Close())
	require.True(t, src.closed)
}

// TestDumpRequestSmallBodyRepeated verifies that a body smaller than the cap
// is still fully restored, including across repeated dumps as the proxy
// serializes the same request more than once.
func TestDumpRequestSmallBodyRepeated(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-test","messages":[]}`
	r := httptest.NewRequest(
		"POST", "/v1/chat/completions", strings.NewReader(body),
	)

	for i := 0; i < 3; i++ {
		reqText, err := dumpRequest(r)
		require.NoError(t, err)
		require.Contains(t, reqText, body)
	}

	got, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(got))
}

// TestDumpRequestDeclaresBodySize checks that the pricer text keeps the full
// Content-Length when the body is capped, since the reservation uses it.
func TestDumpRequestDeclaresBodySize(t *testing.T) {
	t.Parallel()

	body := `{"model":"gpt-test","messages":[{"role":"user",` +
		`"content":"` + strings.Repeat("a", maxPricerBodyBytes*2) +
		`"}]}`

	var reqText string
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var err error
			reqText, err = dumpRequest(r)
			require.NoError(t, err)
		},
	))
	t.Cleanup(server.Close)

	resp, err := http.Post(
		server.URL, "application/json", strings.NewReader(body),
	)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Contains(
		t, reqText, fmt.Sprintf("Content-Length: %d\r\n", len(body)),
	)
	require.Less(t, len(reqText), maxPricerBodyBytes+1024)
}
