package proxy_test

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

// newStreamingProxy stands up a proxy in front of the given backend handler
// and returns the proxy's base URL. The service has auth off, since what
// these tests exercise is the byte path of the response, not the payment for
// it.
func newStreamingProxy(t *testing.T, backend http.Handler,
	configure func(*proxy.Proxy),
	serverTweak func(*httptest.Server)) string {

	t.Helper()

	backendServer := httptest.NewServer(backend)
	t.Cleanup(backendServer.Close)

	backendURL, err := url.Parse(backendServer.URL)
	require.NoError(t, err)

	services := []*proxy.Service{{
		Address:    backendURL.Host,
		HostRegexp: ".*",
		PathRegexp: "^/stream.*$",
		Protocol:   "http",
		Auth:       "off",
	}}

	p, err := proxy.New(
		auth.NewMockAuthenticator(), services, []string{}, nil,
	)
	require.NoError(t, err)

	if configure != nil {
		configure(p)
	}

	proxyServer := httptest.NewUnstartedServer(
		http.HandlerFunc(p.ServeHTTP),
	)
	if serverTweak != nil {
		serverTweak(proxyServer)
	}
	proxyServer.Start()
	t.Cleanup(proxyServer.Close)

	return proxyServer.URL
}

// TestProxyStreamsResponseIncrementally proves the flush guarantee end to
// end: a chunk written by the backend reaches the client while the backend is
// still holding the response open. A proxy that buffered the body would
// deadlock this test, since the backend refuses to finish until the client
// has seen the first chunk.
func TestProxyStreamsResponseIncrementally(t *testing.T) {
	t.Parallel()

	firstChunkSeen := make(chan struct{})

	backend := http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()

		// Refuse to finish the response until the client has read the
		// first chunk through the proxy.
		select {
		case <-firstChunkSeen:
		case <-time.After(5 * time.Second):
			t.Error("client never saw the first chunk; the " +
				"proxy is buffering the response")
			return
		}

		fmt.Fprint(w, "data: second\n\n")
	})

	baseURL := newStreamingProxy(t, backend, nil, nil)

	resp, err := http.Get(baseURL + "/stream")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)

	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "data: first\n", line)
	close(firstChunkSeen)

	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(rest), "data: second")
}

// TestProxyRollsWriteDeadlineForStreams proves that a healthy stream outlives
// the server's absolute write timeout when the rolling window is installed,
// and is killed by it when it is not.
//
// The server's WriteTimeout is a single deadline armed as the response
// begins; a streamed generation routinely outlives any sane value of it while
// writing the whole way through. The proxy's rolling window pushes the
// deadline forward on every write, turning the timeout into what it should
// have meant all along: a stall killer.
func TestProxyRollsWriteDeadlineForStreams(t *testing.T) {
	t.Parallel()

	const (
		writeTimeout = 400 * time.Millisecond
		chunks       = 6
		chunkGap     = 150 * time.Millisecond
	)

	// The stream lasts chunks*chunkGap = 900ms, more than twice the write
	// timeout, while never pausing longer than chunkGap between writes.
	backend := http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		for i := 0; i < chunks; i++ {
			time.Sleep(chunkGap)
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			w.(http.Flusher).Flush()
		}
	})

	readAll := func(baseURL string) (string, error) {
		resp, err := http.Get(baseURL + "/stream")
		if err != nil {
			return "", err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		return string(body), err
	}

	t.Run("rolling window carries the stream", func(t *testing.T) {
		t.Parallel()

		baseURL := newStreamingProxy(
			t, backend,
			func(p *proxy.Proxy) {
				p.SetWriteDeadlineWindow(writeTimeout)
			},
			func(s *httptest.Server) {
				s.Config.WriteTimeout = writeTimeout
			},
		)

		body, err := readAll(baseURL)
		require.NoError(t, err)
		for i := 0; i < chunks; i++ {
			require.Contains(
				t, body, fmt.Sprintf("chunk-%d", i),
			)
		}
	})

	t.Run("absolute deadline kills the same stream", func(t *testing.T) {
		t.Parallel()

		// The control: the identical stream against the identical
		// server, without the rolling window. If this passed, the
		// test above would be proving nothing.
		baseURL := newStreamingProxy(
			t, backend, nil,
			func(s *httptest.Server) {
				s.Config.WriteTimeout = writeTimeout
			},
		)

		body, err := readAll(baseURL)
		if err == nil {
			require.NotContains(
				t, body,
				fmt.Sprintf("chunk-%d", chunks-1),
				"stream survived the absolute write "+
					"deadline; the control is not "+
					"controlling",
			)
		}
	})
}

// TestProxyStreamKilledWhenStalled proves the rolling window still kills a
// genuinely stalled stream: the deadline only moves when bytes move, so a
// backend that stops writing for longer than the window tears the connection
// down rather than pinning it open forever.
func TestProxyStreamKilledWhenStalled(t *testing.T) {
	t.Parallel()

	const writeTimeout = 300 * time.Millisecond

	backendDone := make(chan struct{})
	backend := http.HandlerFunc(func(w http.ResponseWriter,
		r *http.Request) {

		defer close(backendDone)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		fmt.Fprint(w, "data: alive\n\n")
		w.(http.Flusher).Flush()

		// Stall far past the window, then try to write again. The
		// connection's deadline has passed, so this write cannot
		// reach the client.
		time.Sleep(4 * writeTimeout)
		fmt.Fprint(w, "data: too-late\n\n")
	})

	baseURL := newStreamingProxy(
		t, backend,
		func(p *proxy.Proxy) {
			p.SetWriteDeadlineWindow(writeTimeout)
		},
		func(s *httptest.Server) {
			s.Config.WriteTimeout = writeTimeout
		},
	)

	resp, err := http.Get(baseURL + "/stream")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.True(
		t, err != nil || !strings.Contains(string(body), "too-late"),
		"a stalled stream outlived the write deadline window",
	)

	select {
	case <-backendDone:
	case <-time.After(5 * time.Second):
		t.Fatal("backend handler never finished")
	}
}
