package aperture

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btclog/v2"
	"github.com/goccy/go-yaml"
	flags "github.com/jessevdk/go-flags"
	"github.com/lightninglabs/aperture/aperturedb"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightningnetwork/lnd/build"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // Test the existing h2c mode.
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func proxyTestHeader(source string, port uint16) []byte {
	ip := net.ParseIP(source)
	family := byte(0x21)
	if ip4 := ip.To4(); ip4 != nil {
		ip, family = ip4, 0x11
	}
	payload := append([]byte{}, ip...)
	payload = append(payload, make([]byte, len(ip))...)
	payload = binary.BigEndian.AppendUint16(payload, port)
	payload = binary.BigEndian.AppendUint16(payload, 443)
	header := append([]byte{}, proxyV2Signature...)
	header = append(header, 0x21, family)
	header = binary.BigEndian.AppendUint16(header, uint16(len(payload)))
	return append(header, payload...)
}

func TestProxyProtocolParser(t *testing.T) {
	t.Parallel()
	peer := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
	ipv4 := proxyTestHeader("192.0.2.7", 54321)
	ipv6 := proxyTestHeader("2001:db8::7", 54321)
	withTLV := append(append([]byte{}, ipv4...), 0xea, 0, 3, 1, 2, 3)
	binary.BigEndian.PutUint16(withTLV[14:16], uint16(len(withTLV)-16))
	local := append([]byte{}, withTLV...)
	local[12], local[13] = 0x20, 0xff
	unspec := append([]byte{}, ipv4...)
	unspec[13] = 0
	udp := append([]byte{}, ipv4...)
	udp[13] = 0x12
	unix := append([]byte{}, ipv4[:16]...)
	unix[13] = 0x31
	binary.BigEndian.PutUint16(unix[14:16], 216)
	unix = append(unix, make([]byte, 216)...)

	for _, test := range []struct {
		name string
		data []byte
		want string
	}{
		{"ipv4", ipv4, "192.0.2.7:54321"},
		{"ipv6", ipv6, "[2001:db8::7]:54321"},
		{"unknown TLV", withTLV, "192.0.2.7:54321"},
		{"LOCAL ignores family and payload", local, peer.String()},
		{"UNSPEC", unspec, peer.String()},
		{"datagram fallback", udp, peer.String()},
		{"UNIX fallback", unix, peer.String()},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Limit each read to one byte and keep application bytes after
			// the preamble to detect overreads across the boundary.
			r := bytes.NewBuffer(append(append([]byte{}, test.data...), 'G'))
			addr, err := readProxyV2(oneByteReader{r}, peer)
			require.NoError(t, err)
			require.Equal(t, test.want, addr.String())
			require.Equal(t, "G", r.String())
		})
	}

	for _, test := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"signature", func(b []byte) []byte { b[0] = 0; return b }},
		{"version", func(b []byte) []byte { b[12] = 0x11; return b }},
		{"command", func(b []byte) []byte { b[12] = 0x22; return b }},
		{"family", func(b []byte) []byte { b[13] = 0x41; return b }},
		{"transport", func(b []byte) []byte { b[13] = 0x13; return b }},
		{"short address", func(b []byte) []byte { b[15] = 11; return b[:27] }},
		{"short TLV header", func(b []byte) []byte { b[15] = 13; return append(b, 1) }},
		{"short TLV value", func(b []byte) []byte { b[15] = 15; return append(b, 1, 0, 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := readProxyV2(bytes.NewReader(test.edit(
				append([]byte{}, ipv4...),
			)), peer)
			require.Error(t, err)
		})
	}
	for length := range ipv4 {
		_, err := readProxyV2(bytes.NewReader(ipv4[:length]), peer)
		require.Error(t, err, "truncation at byte %d", length)
	}
}

type oneByteReader struct{ io.Reader }

func (r oneByteReader) Read(b []byte) (int, error) {
	if len(b) > 1 {
		b = b[:1]
	}
	return r.Reader.Read(b)
}

func TestProxyProtocolConfig(t *testing.T) {
	t.Parallel()
	cfg := NewConfig()
	require.False(t, cfg.ProxyProtocol.Enabled)
	require.Positive(t, cfg.ProxyProtocol.HeaderTimeout)
	err := yaml.Unmarshal([]byte(`proxyprotocol:
  enabled: true
  trustedproxies: ["127.0.0.1", "2001:db8::/32"]
  headertimeout: 3s
`), cfg)
	require.NoError(t, err)
	networks, err := proxyProtocolNetworks(cfg.ProxyProtocol)
	require.NoError(t, err)
	require.Len(t, networks, 2)
	require.Equal(t, 3*time.Second, cfg.ProxyProtocol.HeaderTimeout)
	require.True(t, trustedProxy(&net.TCPAddr{IP: net.ParseIP("127.0.0.1")}, networks))
	require.True(t, trustedProxy(&net.TCPAddr{IP: net.ParseIP("2001:db8::1")}, networks))
	require.False(t, trustedProxy(&net.TCPAddr{IP: net.ParseIP("192.0.2.1")}, networks))

	cliCfg := NewConfig()
	_, err = flags.NewParser(cliCfg, flags.None).ParseArgs([]string{
		"--proxyprotocol.enabled", "--proxyprotocol.trustedproxy=127.0.0.1",
		"--proxyprotocol.headertimeout=4s",
	})
	require.NoError(t, err)
	require.True(t, cliCfg.ProxyProtocol.Enabled)
	require.Equal(t, []string{"127.0.0.1"}, cliCfg.ProxyProtocol.TrustedProxies)
	require.Equal(t, 4*time.Second, cliCfg.ProxyProtocol.HeaderTimeout)

	for _, invalid := range []*ProxyProtocolConfig{
		{Enabled: true, HeaderTimeout: time.Second},
		{Enabled: true, TrustedProxies: []string{"127.0.0.1"}},
		{Enabled: true, HeaderTimeout: -time.Second, TrustedProxies: []string{"127.0.0.1"}},
		{Enabled: true, HeaderTimeout: time.Second, TrustedProxies: []string{"hostname"}},
		{Enabled: true, HeaderTimeout: time.Second, TrustedProxies: []string{"192.0.2.0/99"}},
	} {
		_, err := proxyProtocolNetworks(invalid)
		require.Error(t, err)
	}
}

func proxyTestServer(t *testing.T, handler http.Handler, useTLS bool,
	timeout time.Duration, trusted []string) *httptest.Server {

	t.Helper()
	cfg := &ProxyProtocolConfig{
		Enabled: true, HeaderTimeout: timeout, TrustedProxies: trusted,
	}
	networks, err := proxyProtocolNetworks(cfg)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnContext = proxyProtocolContext
	server.Listener = &proxyProtocolListener{
		Listener: server.Listener, timeout: timeout, networks: networks,
	}
	if useTLS {
		server.EnableHTTP2 = true
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server
}

func proxyTestDial(ctx context.Context, addr string, header []byte) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(header); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func TestProxyProtocolHTTP(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"http1", "https1", "h2", "h2c"} {
		t.Run(mode, func(t *testing.T) {
			useTLS := mode == "https1" || mode == "h2"
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.True(t, isProxyProtocolRequest(r))
				peer := r.Context().Value(proxyProtocolPeerKey{}).(net.Addr)
				require.True(t, peer.(*net.TCPAddr).IP.IsLoopback())
				_, _ = fmt.Fprintf(w, "%s %s", r.RemoteAddr, r.Proto)
			})
			server := proxyTestServer(t, h2c.NewHandler(handler, &http2.Server{}), //nolint:staticcheck
				useTLS, time.Second, []string{"127.0.0.1"})
			dial := func(ctx context.Context, _, addr string) (net.Conn, error) {
				return proxyTestDial(ctx, addr, proxyTestHeader("2001:db8::7", 54321))
			}
			var transport http.RoundTripper
			if mode == "h2c" {
				h2 := &http2.Transport{
					AllowHTTP: true,
					DialTLSContext: func(ctx context.Context, network, addr string,
						_ *tls.Config) (net.Conn, error) {
						return dial(ctx, network, addr)
					},
				}
				t.Cleanup(h2.CloseIdleConnections)
				transport = h2
			} else {
				h1 := &http.Transport{
					DialContext: dial, ForceAttemptHTTP2: mode == "h2",
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				}
				t.Cleanup(h1.CloseIdleConnections)
				transport = h1
			}
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			proto := "HTTP/1.1"
			if mode == "h2" || mode == "h2c" {
				proto = "HTTP/2.0"
			}
			for range 2 {
				resp, err := client.Get(server.URL)
				require.NoError(t, err)
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, resp.Body.Close())
				require.NoError(t, err)
				require.Equal(t, "[2001:db8::7]:54321 "+proto, string(body))
			}
		})
	}
}

func TestProxyProtocolGRPCGateways(t *testing.T) {
	t.Parallel()
	for _, useTLS := range []bool{false, true} {
		t.Run(fmt.Sprintf("tls=%v", useTLS), func(t *testing.T) {
			grpcServer := grpc.NewServer()
			healthpb.RegisterHealthServer(grpcServer, health.NewServer())
			t.Cleanup(grpcServer.Stop)
			server := proxyTestServer(t, h2c.NewHandler(grpcServer, &http2.Server{}), //nolint:staticcheck
				useTLS, time.Second, []string{"127.0.0.1"})
			cfg := NewConfig()
			cfg.ProxyProtocol.Enabled = true
			creds := insecure.NewCredentials()
			if useTLS {
				creds = credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})
			}
			conn, err := grpc.NewClient(server.Listener.Addr().String(),
				grpc.WithTransportCredentials(creds), proxyProtocolDialOption(cfg))
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
			require.NoError(t, err)
			require.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.Status)
		})
	}
}

func TestProxyProtocolAcceptIsolation(t *testing.T) {
	t.Parallel()
	server := proxyTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.RemoteAddr)
	}), false, 10*time.Second, []string{"127.0.0.1"})

	// Keep a partial header open while a second connection completes.
	stalled, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = stalled.Close() })
	_, err = stalled.Write(proxyV2Signature[:1])
	require.NoError(t, err)
	conn, err := proxyTestDial(t.Context(), server.Listener.Addr().String(), proxyTestHeader("192.0.2.1", 1234))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: aperture\r\nConnection: close\r\n\r\n")
	require.NoError(t, err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	require.Equal(t, "192.0.2.1:1234", string(body))
	// The preamble timeout is longer than the successful request's
	// deadline, so synchronous parsing cannot pass by waiting it out.
	require.NoError(t, stalled.Close())
}

func TestProxyProtocolRejections(t *testing.T) {
	t.Parallel()
	server := proxyTestServer(t, http.NotFoundHandler(), false,
		100*time.Millisecond, []string{"127.0.0.1"})
	stalled, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer stalled.Close()
	_, err = stalled.Write(proxyV2Signature[:1])
	require.NoError(t, err)
	require.NoError(t, stalled.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, err = stalled.Read(make([]byte, 1))
	require.Error(t, err)
	if netErr, ok := err.(net.Error); ok {
		require.False(t, netErr.Timeout(), "server must close the incomplete preamble")
	}

	for _, data := range [][]byte{
		[]byte("GET / HTTP/1.1\r\nHost: aperture\r\n\r\n"),
		[]byte("PROXY TCP4 192.0.2.1 192.0.2.2 1234 443\r\n"),
		append(proxyTestHeader("192.0.2.1", 1234)[:20], 'x'),
	} {
		c, err := proxyTestDial(t.Context(), server.Listener.Addr().String(), data)
		require.NoError(t, err)
		require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
		_, err = c.Read(make([]byte, 1))
		_ = c.Close()
		require.Error(t, err)
		if netErr, ok := err.(net.Error); ok {
			require.False(t, netErr.Timeout())
		}
	}

	untrusted := proxyTestServer(t, http.NotFoundHandler(), false,
		time.Second, []string{"192.0.2.0/24"})
	c, err := net.Dial("tcp", untrusted.Listener.Addr().String())
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, err = c.Read(make([]byte, 1))
	require.Error(t, err)
	if netErr, ok := err.(net.Error); ok {
		require.False(t, netErr.Timeout())
	}
}

func TestProxyProtocolIPPolicies(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	p, err := proxy.New(nil, []*proxy.Service{{
		Name: "test", Address: strings.TrimPrefix(backend.URL, "http://"),
		Protocol: "http", HostRegexp: ".*", PathRegexp: ".*", Auth: auth.Level("off"),
		RateLimits: []*proxy.RateLimitConfig{{Requests: 1, Per: time.Hour, Burst: 1}},
	}}, []string{"192.0.2.1"}, nil)
	require.NoError(t, err)
	defer p.Close()
	server := proxyTestServer(t, p, false, time.Second, []string{"127.0.0.1"})
	for _, test := range []struct {
		ip     string
		status int
	}{
		{"192.0.2.1", http.StatusForbidden},
		{"198.51.100.1", http.StatusOK},
		{"198.51.100.2", http.StatusTooManyRequests},
		{"203.0.113.1", http.StatusOK},
	} {
		transport := &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return proxyTestDial(ctx, addr, proxyTestHeader(test.ip, 4321))
		}}
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		transport.CloseIdleConnections()
		require.Equal(t, test.status, resp.StatusCode, test.ip)
	}
}

func TestProxyProtocolLoopbackIsolation(t *testing.T) {
	t.Parallel()
	handler := requireLoopback(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server := proxyTestServer(t, handler, true, time.Second, []string{"127.0.0.1"})
	local := append(append([]byte{}, proxyV2Signature...), 0x20, 0, 0, 0)
	for _, header := range [][]byte{
		proxyTestHeader("127.0.0.1", 1234),
		proxyTestHeader("::1", 1234),
		proxyTestHeader("192.0.2.1", 1234),
		local,
	} {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return proxyTestDial(ctx, addr, header)
			},
		}
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		resp, err := client.Get(server.URL)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		transport.CloseIdleConnections()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	}

	// Disabling PROXY support preserves the direct local dashboard path.
	direct := httptest.NewServer(handler)
	defer direct.Close()
	resp, err := direct.Client().Get(direct.URL)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestProxyProtocolAdminREST(t *testing.T) {
	t.Parallel()
	cfg := NewConfig()
	cfg.BaseDir = t.TempDir()
	cfg.Admin.Enabled = true
	cfg.Admin.MacaroonPath = filepath.Join(cfg.BaseDir, "admin.macaroon")
	cfg.Authenticator.Network = "regtest"
	cfg.Insecure = true
	cfg.ProxyProtocol.Enabled = true
	cfg.ProxyProtocol.TrustedProxies = []string{"127.0.0.1"}
	server := httptest.NewUnstartedServer(nil)
	cfg.ListenAddr = server.Listener.Addr().String()
	db := aperturedb.NewTestDB(t)
	secrets, _, _, transactions, services := initSQLStores(db.BaseDB)
	priority, fallback, cleanup, err := createAdminServer(
		cfg, transactions, secrets, services,
		func() []*proxy.Service { return nil },
		func([]*proxy.Service) error { return nil },
	)
	require.NoError(t, err)
	defer cleanup()
	p, err := proxy.New(nil, nil, nil, priority, fallback...)
	require.NoError(t, err)
	defer p.Close()
	server.Config.Handler = h2c.NewHandler(p, &http2.Server{}) //nolint:staticcheck
	server.Config.ConnContext = proxyProtocolContext
	networks, err := proxyProtocolNetworks(cfg.ProxyProtocol)
	require.NoError(t, err)
	server.Listener = &proxyProtocolListener{
		Listener: server.Listener, networks: networks,
		timeout: cfg.ProxyProtocol.HeaderTimeout,
	}
	server.Start()
	defer server.Close()
	mac, err := os.ReadFile(cfg.Admin.MacaroonPath)
	require.NoError(t, err)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return proxyTestDial(ctx, addr, proxyTestHeader("192.0.2.10", 1234))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	for _, authorized := range []bool{false, true} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			server.URL+"/api/admin/info", nil)
		require.NoError(t, err)
		if authorized {
			req.Header.Set("Grpc-Metadata-Macaroon", hex.EncodeToString(mac))
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, resp.Body.Close())
		require.NoError(t, err)
		if authorized {
			require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
			require.Contains(t, string(body), `"network":"regtest"`)
		} else {
			require.Equal(t, http.StatusUnauthorized, resp.StatusCode, string(body))
		}
	}
}

func TestProxyProtocolLogging(t *testing.T) {
	// Keep the package logger local to this sequential test. Parallel tests
	// resume after the logger has been restored.
	var logs bytes.Buffer
	logger := btclog.NewSLogger(btclog.NewDefaultHandler(&logs))
	logger.SetLevel(btclog.LevelInfo)
	proxy.UseLogger(logger)
	defer proxy.UseLogger(build.NewSubLogger(proxy.Subsystem, nil))
	p, err := proxy.New(nil, nil, nil, []proxy.LocalService{
		proxy.NewLocalService(http.NotFoundHandler(), func(*http.Request) bool { return true }),
	})
	require.NoError(t, err)
	defer p.Close()
	server := proxyTestServer(t, p, false, time.Second, []string{"127.0.0.1"})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return proxyTestDial(ctx, addr, proxyTestHeader("192.0.2.77", 1234))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Get(server.URL + "/original-client")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	server.Close()
	require.Contains(t, logs.String(), "192.0.2.77")
	require.Contains(t, logs.String(), "/original-client")
}
