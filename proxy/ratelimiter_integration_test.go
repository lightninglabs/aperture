package proxy_test

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/proxy"
	proxytest "github.com/lightninglabs/aperture/proxy/testdata"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
)

// buildAuthHeader constructs an Authorization: L402 header with a valid
// macaroon and a given preimage hex string. The macaroon content isn't
// validated by the proxy for key derivation, only parsed.
func buildAuthHeader(t *testing.T, preimageHex string) string {
	t.Helper()

	dummyMac, err := macaroon.New(
		[]byte("key"), []byte("id"), "loc", macaroon.LatestVersion,
	)
	require.NoError(t, err)

	macBytes, err := dummyMac.MarshalBinary()
	require.NoError(t, err)

	macStr := base64.StdEncoding.EncodeToString(macBytes)

	return fmt.Sprintf("L402 %s:%s", macStr, preimageHex)
}

func TestHTTPRateLimit_RetryAfterAndCORS(t *testing.T) {
	// Configure a service with a rate limit for path /http/limited.
	services := []*proxy.Service{{
		Address:    testTargetServiceAddress,
		HostRegexp: testHostRegexp,
		PathRegexp: testPathRegexpHTTP,
		Protocol:   "http",
		Auth:       "off",
		RateLimits: []proxy.RateLimit{{
			PathRegexp: "^/http/limited.*$",
			Requests:   1,
			Per:        500 * time.Millisecond,
			Burst:      1,
		}},
	}}

	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, []string{})
	require.NoError(t, err)

	// Start proxy and backend servers.
	srv := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() { _ = srv.Close() })

	backend := &http.Server{Addr: testTargetServiceAddress}
	go func() { _ = startBackendHTTP(backend) }()
	t.Cleanup(func() { _ = backend.Close() })

	time.Sleep(100 * time.Millisecond)

	client := &http.Client{}
	url := fmt.Sprintf("http://%s/http/limited", testProxyAddr)

	// First request allowed.
	resp, err := client.Get(url)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// The second immediate request should be rate limited.
	resp, err = client.Get(url)
	require.NoError(t, err)

	defer resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// Retry-After should be set and sub-second rounded up to at least 1.
	ra := resp.Header.Get("Retry-After")
	require.Equal(t, "1", ra)

	// Ensure the CORS headers are present.
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	require.NotEmpty(t, resp.Header.Get("Access-Control-Allow-Methods"))

	// Check the html body message.
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, "rate limit exceeded\n", string(b))

	// After waiting 500ms, the request should succeed again.
	time.Sleep(500 * time.Millisecond)
	resp, err = client.Get(url)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Test whole-second accuracy: set per=2s and check Retry-After=2.
	services[0].RateLimits = []proxy.RateLimit{{
		PathRegexp: "^/http/limited.*$",
		Requests:   1,
		Per:        2 * time.Second,
		Burst:      1,
	}}
	require.NoError(t, p.UpdateServices(services))

	resp, err = client.Get(url)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	resp, err = client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// Due to integer truncation on fractional seconds, Retry-After may be
	// "1" if the computed delay is slightly under 2s.
	val := resp.Header.Get("Retry-After")
	require.Contains(t, []string{"1", "2"}, val)
}

func TestHTTPRateLimit_MultipleRules_Strictest(t *testing.T) {
	services := []*proxy.Service{{
		Address:    testTargetServiceAddress,
		HostRegexp: testHostRegexp,
		PathRegexp: testPathRegexpHTTP,
		Protocol:   "http",
		Auth:       "off",
		RateLimits: []proxy.RateLimit{{
			PathRegexp: "^/http/limited.*$",
			Requests:   2,
			Per:        time.Second,
			Burst:      2,
		}, {
			PathRegexp: "^/http/limited.*$",
			Requests:   1,
			Per:        time.Second,
			Burst:      1,
		}},
	}}
	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, []string{})
	require.NoError(t, err)

	srv := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() { _ = srv.Close() })

	backend := &http.Server{
		Addr: testTargetServiceAddress,
	}
	go func() { _ = startBackendHTTP(backend) }()
	t.Cleanup(func() { _ = backend.Close() })

	time.Sleep(100 * time.Millisecond)

	client := &http.Client{}
	url := fmt.Sprintf("http://%s/http/limited", testProxyAddr)

	// The first request should be allowed by both rules.
	resp, _ := client.Get(url)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// The second request should be rate limited by the strictest rule.
	resp, err = client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

func TestHTTPRateLimit_PerIdentityIsolationAndGlobal(t *testing.T) {
	services := []*proxy.Service{{
		Address:    testTargetServiceAddress,
		HostRegexp: testHostRegexp,
		PathRegexp: testPathRegexpHTTP,
		Protocol:   "http",
		Auth:       "off",
		RateLimits: []proxy.RateLimit{{
			PathRegexp: "^/http/limited.*$",
			Requests:   1,
			Per:        time.Second,
			Burst:      1,
		}},
	}}
	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, []string{})
	require.NoError(t, err)

	srv := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() { _ = srv.Close() })

	backend := &http.Server{
		Addr: testTargetServiceAddress,
	}
	go func() { _ = startBackendHTTP(backend) }()
	t.Cleanup(func() { _ = backend.Close() })

	time.Sleep(100 * time.Millisecond)

	client := &http.Client{}
	url := fmt.Sprintf("http://%s/http/limited", testProxyAddr)
	preA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	preB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	authA := buildAuthHeader(t, preA)
	authB := buildAuthHeader(t, preB)

	// A: first allowed, second denied.
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", authA)
	resp, _ := client.Do(req)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Immediate second request should be denied.
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", authA)
	resp, err = client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	// B: should be allowed independently.
	req, _ = http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", authB)
	resp, _ = client.Do(req)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// No identity (global bucket): first is allowed, then denied; and
	// subsequent anonymous request shares same bucket.
	resp, _ = client.Get(url)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	err = resp.Body.Close()
	require.NoError(t, err)

	resp, err = client.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}

func TestGRPCRateLimit_ResponsesAndCORS(t *testing.T) {
	// Start TLS infra like runGRPCTest.
	certFile := path.Join(t.TempDir(), "proxy.cert")
	keyFile := path.Join(t.TempDir(), "proxy.key")
	cp, creds, certData, err := genCertPair(certFile, keyFile)
	require.NoError(t, err)

	// gRPC server
	httpListener, err := net.Listen("tcp", testProxyAddr)
	require.NoError(t, err)
	tlsListener := tls.NewListener(
		httpListener, configFromCert(&certData, cp),
	)
	t.Cleanup(func() { _ = tlsListener.Close() })

	services := []*proxy.Service{{
		Address:     testTargetServiceAddress,
		HostRegexp:  testHostRegexp,
		PathRegexp:  testPathRegexpGRPC,
		Protocol:    "https",
		TLSCertPath: certFile,
		Auth:        "off",
		RateLimits: []proxy.RateLimit{{
			PathRegexp: "^/proxy_test\\.Greeter/SayHello.*$",
			Requests:   1,
			Per:        2 * time.Second,
			Burst:      1,
		}},
	}}

	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, []string{})
	require.NoError(t, err)

	srv := &http.Server{
		Addr:      testProxyAddr,
		Handler:   http.HandlerFunc(p.ServeHTTP),
		TLSConfig: configFromCert(&certData, cp),
	}
	go func() { _ = srv.Serve(tlsListener) }()
	t.Cleanup(func() { _ = srv.Close() })

	// Start backend gRPC server.
	serverOpts := []grpc.ServerOption{
		grpc.Creds(credentials.NewTLS(configFromCert(&certData, cp))),
	}
	backend := grpc.NewServer(serverOpts...)
	go func() { _ = startBackendGRPC(backend) }()
	t.Cleanup(func() { backend.Stop() })

	// Dial client.
	conn, err := grpc.Dial(
		testProxyAddr, grpc.WithTransportCredentials(creds),
	)
	require.NoError(t, err)
	client := proxytest.NewGreeterClient(conn)

	// First call allowed.
	_, err = client.SayHello(
		t.Context(), &proxytest.HelloRequest{Name: "x"},
	)
	require.NoError(t, err)

	// The second immediate call should be rate-limited.
	var hdrMD, trMD metadata.MD
	_, err = client.SayHello(
		t.Context(), &proxytest.HelloRequest{Name: "x"},
		grpc.Header(&hdrMD), grpc.Trailer(&trMD),
	)
	require.Error(t, err)

	st, _ := status.FromError(err)
	require.Equal(t, "rate limit exceeded", st.Message())

	// CORS headers should be present in either headers or trailers.
	vals := hdrMD.Get("Access-Control-Allow-Origin")
	if len(vals) == 0 {
		vals = trMD.Get("Access-Control-Allow-Origin")
	}
	require.NotEmpty(t, vals)
	require.Equal(t, "*", vals[0])
}
