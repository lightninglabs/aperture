package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lightninglabs/aperture/pricer"
	"github.com/stretchr/testify/require"
)

// TestWithStreamUsage checks which bodies get include_usage and that the edit
// leaves every other byte where the client put it.
func TestWithStreamUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{{
		name: "stream without options",
		body: `{"model":"m","stream":true}`,
		want: `{"stream_options":{"include_usage":true},` +
			`"model":"m","stream":true}`,
	}, {
		name: "leading whitespace and spacing kept",
		body: " {\"stream\": true, \"model\": \"m\"}",
		want: " {\"stream_options\":{\"include_usage\":true}," +
			"\"stream\": true, \"model\": \"m\"}",
	}, {
		name: "empty options object",
		body: `{"stream":true,"stream_options":{}}`,
		want: `{"stream":true,"stream_options":{"include_usage":true}}`,
	}, {
		name: "options without include_usage",
		body: `{"stream":true,"stream_options":{"x":1}}`,
		want: `{"stream":true,"stream_options":` +
			`{"include_usage":true,"x":1}}`,
	}, {
		name: "null options",
		body: `{"stream":true,"stream_options":null}`,
		want: `{"stream":true,"stream_options":{"include_usage":true}}`,
	}, {
		name: "explicit include_usage false is kept",
		body: `{"stream":true,"stream_options":{"include_usage":false}}`,
		want: `{"stream":true,"stream_options":{"include_usage":false}}`,
	}, {
		name: "explicit include_usage true is kept",
		body: `{"stream":true,"stream_options":{"include_usage":true}}`,
		want: `{"stream":true,"stream_options":{"include_usage":true}}`,
	}, {
		name: "not a stream",
		body: `{"model":"m","stream":false}`,
		want: `{"model":"m","stream":false}`,
	}, {
		name: "stream unset",
		body: `{"model":"m"}`,
		want: `{"model":"m"}`,
	}, {
		name: "stream as a string",
		body: `{"stream":"true"}`,
		want: `{"stream":"true"}`,
	}, {
		name: "last repeated stream key wins",
		body: `{"stream":true,"stream":false}`,
		want: `{"stream":true,"stream":false}`,
	}, {
		name: "malformed options",
		body: `{"stream":true,"stream_options":[1]}`,
		want: `{"stream":true,"stream_options":[1]}`,
	}, {
		name: "invalid JSON",
		body: `{"stream":true,`,
		want: `{"stream":true,`,
	}, {
		name: "array body",
		body: `[{"stream":true}]`,
		want: `[{"stream":true}]`,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := string(withStreamUsage([]byte(tc.body)))
			require.Equal(t, tc.want, got)
			if got != tc.body {
				require.True(t, json.Valid([]byte(got)))
			}
		})
	}
}

// TestRequestStreamUsage checks the request-level guards and that a rewritten
// body is forwarded with a matching Content-Length.
func TestRequestStreamUsage(t *testing.T) {
	t.Parallel()

	const (
		stream    = `{"model":"m","stream":true}`
		rewritten = `{"stream_options":{"include_usage":true},` +
			`"model":"m","stream":true}`
	)

	newRequest := func(body, contentType string) *http.Request {
		r := httptest.NewRequest(
			http.MethodPost, "/v1/chat/completions",
			strings.NewReader(body),
		)
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("Content-Length", strconv.Itoa(len(body)))

		return r
	}

	t.Run("json stream is rewritten", func(t *testing.T) {
		t.Parallel()

		r := newRequest(stream, "application/json; charset=utf-8")
		require.NoError(t, requestStreamUsage(r))

		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, rewritten, string(got))
		require.EqualValues(t, len(rewritten), r.ContentLength)
		require.Equal(
			t, strconv.Itoa(len(rewritten)),
			r.Header.Get("Content-Length"),
		)
	})

	t.Run("chunked body gets a length", func(t *testing.T) {
		t.Parallel()

		r := newRequest(stream, "application/json")
		r.Header.Del("Content-Length")
		r.ContentLength = -1
		r.TransferEncoding = []string{"chunked"}
		require.NoError(t, requestStreamUsage(r))

		require.EqualValues(t, len(rewritten), r.ContentLength)
		require.Nil(t, r.TransferEncoding)
	})

	t.Run("chunked non-stream body gets a length", func(t *testing.T) {
		t.Parallel()

		// The pricer only sees a prefix of the body, so without a
		// length it would have to treat the size as unknown.
		const plain = `{"model":"m"}`
		r := newRequest(plain, "application/json")
		r.Header.Del("Content-Length")
		r.ContentLength = -1
		r.TransferEncoding = []string{"chunked"}
		require.NoError(t, requestStreamUsage(r))

		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, plain, string(got))
		require.EqualValues(t, len(plain), r.ContentLength)
		require.Equal(
			t, strconv.Itoa(len(plain)),
			r.Header.Get("Content-Length"),
		)
		require.Nil(t, r.TransferEncoding)
	})

	untouched := []struct {
		name string
		req  func() *http.Request
		body string
	}{{
		name: "not json",
		req: func() *http.Request {
			return newRequest(stream, "text/plain")
		},
		body: stream,
	}, {
		name: "no content type",
		req: func() *http.Request {
			r := newRequest(stream, "")
			r.Header.Del("Content-Type")

			return r
		},
		body: stream,
	}, {
		name: "content encoded",
		req: func() *http.Request {
			r := newRequest(stream, "application/json")
			r.Header.Set("Content-Encoding", "gzip")

			return r
		},
		body: stream,
	}, {
		name: "not a stream",
		req: func() *http.Request {
			return newRequest(`{"model":"m"}`, "application/json")
		},
		body: `{"model":"m"}`,
	}, {
		name: "body past the cap",
		req: func() *http.Request {
			r := newRequest(largeStreamBody(), "application/json")
			r.ContentLength = -1
			r.Header.Del("Content-Length")

			return r
		},
		body: largeStreamBody(),
	}}

	for _, tc := range untouched {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := tc.req()
			length := r.ContentLength
			header := r.Header.Get("Content-Length")
			require.NoError(t, requestStreamUsage(r))

			got, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, tc.body, string(got))
			require.Equal(t, length, r.ContentLength)
			require.Equal(t, header, r.Header.Get("Content-Length"))
		})
	}
}

// largeStreamBody returns a stream request body one byte past the rewrite cap.
func largeStreamBody() string {
	head := `{"stream":true,"model":"m","pad":"`
	tail := `"}`
	pad := maxStreamUsageBodyBytes + 1 - len(head) - len(tail)

	return head + strings.Repeat("a", pad) + tail
}

// serializingPricer is a metered pricer that records the request text the
// pricer RPCs would carry on authorization.
type serializingPricer struct {
	*fakeMeteredPricer

	reqText string
}

func (s *serializingPricer) AuthorizeRequest(ctx context.Context,
	r *http.Request, tokenID, serviceName string) (*pricer.AuthorizeResult,
	error) {

	text, err := pricer.SerializeRequest(r)
	if err != nil {
		return nil, err
	}
	s.reqText = text

	return s.fakeMeteredPricer.AuthorizeRequest(
		ctx, r, tokenID, serviceName,
	)
}

// TestCheckMeteredAccessRequestsStreamUsage checks that a metered streaming
// request reaches both the pricer and the backend asking for usage, with a
// Content-Length that matches, and that an unmetered service never has its
// body touched.
func TestCheckMeteredAccessRequestsStreamUsage(t *testing.T) {
	t.Parallel()

	const (
		body      = `{"model":"m","stream":true}`
		rewritten = `{"stream_options":{"include_usage":true},` +
			`"model":"m","stream":true}`
	)

	mac, _ := newTestMacaroon(t)
	newRequest := func() *http.Request {
		r := httptest.NewRequest(
			http.MethodPost, "/v1/chat/completions",
			strings.NewReader(body),
		)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", authHeaderForMacaroon(t, mac))

		return r
	}

	t.Run("metered", func(t *testing.T) {
		t.Parallel()

		fake := newFakeMeteredPricer()
		fake.authorizeResult = &pricer.AuthorizeResult{Allowed: true}
		price := &serializingPricer{fakeMeteredPricer: fake}
		svc := newMeteredService(fake)
		svc.pricer = price

		w := httptest.NewRecorder()
		r, proceed := (&Proxy{}).checkMeteredAccess(
			w, newRequest(), svc, "inference",
		)
		require.True(t, proceed)

		require.Contains(
			t, price.reqText, "Content-Length: "+
				strconv.Itoa(len(rewritten)),
		)
		require.True(t, strings.HasSuffix(price.reqText, rewritten))

		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, rewritten, string(got))
		require.EqualValues(t, len(rewritten), r.ContentLength)
	})

	t.Run("not metered", func(t *testing.T) {
		t.Parallel()

		fake := newFakeMeteredPricer()
		svc := newMeteredService(fake)
		svc.DynamicPrice.Metered = false

		w := httptest.NewRecorder()
		r, proceed := (&Proxy{}).checkMeteredAccess(
			w, newRequest(), svc, "inference",
		)
		require.True(t, proceed)

		got, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(got))
	})
}
