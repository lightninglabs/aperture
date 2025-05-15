package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/proxy"
	proxytest "github.com/lightninglabs/aperture/proxy/testdata"
	"github.com/lightningnetwork/lnd/cert"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
)

const (
	testProxyAddr            = "localhost:10019"
	testHostRegexp           = "^localhost:.*$"
	testPathRegexpHTTP       = "^/http/.*$"
	testPathRegexpGRPC       = "^/proxy_test\\.Greeter/.*$"
	testTargetServiceAddress = "localhost:8082"
	testHTTPResponseBody     = "HTTP Hello"

	// DefaultAutogenValidity is the default validity of a self-signed
	// certificate. The value corresponds to 14 months
	// (14 months * 30 days * 24 hours).
	DefaultAutogenValidity = 14 * 30 * 24 * time.Hour
)

var (
	errBackend = fmt.Errorf("this is the error you wanted")
)

type testCase struct {
	name                         string
	auth                         auth.Level
	authWhitelist                []string
	authSkipInvoiceCreationPaths []string
	wantBackendErr               bool
}

// helloServer is a simple server that implements the GreeterServer interface.
type helloServer struct {
	proxytest.UnimplementedGreeterServer
}

// SayHello returns a simple string that also contains a string from the
// request.
func (s *helloServer) SayHello(_ context.Context,
	req *proxytest.HelloRequest) (*proxytest.HelloReply, error) {

	if req.ReturnError {
		return nil, errBackend
	}

	return &proxytest.HelloReply{
		Message: fmt.Sprintf("Hello %s", req.Name),
	}, nil
}

// SayHello returns a simple string that also contains a string from the
// request. This RPC method should be whitelisted to be called without any
// authentication required.
func (s *helloServer) SayHelloNoAuth(_ context.Context,
	req *proxytest.HelloRequest) (*proxytest.HelloReply, error) {

	if req.ReturnError {
		return nil, errBackend
	}

	return &proxytest.HelloReply{
		Message: fmt.Sprintf("Hello %s", req.Name),
	}, nil
}

// TestProxyHTTP tests that the proxy can forward HTTP requests to a backend
// service and handle L402 authentication correctly.
func TestProxyHTTP(t *testing.T) {
	testCases := []*testCase{{
		name: "no whitelist",
		auth: "on",
	}, {
		name:          "with whitelist",
		auth:          "on",
		authWhitelist: []string{"^/http/white.*$"},
	}, {
		name:                         "no whitelist with skip",
		auth:                         "on",
		authSkipInvoiceCreationPaths: []string{"^/http/skip.*$"},
	}, {
		name:                         "with whitelist with skip",
		auth:                         "on",
		authWhitelist:                []string{"^/http/white.*$"},
		authSkipInvoiceCreationPaths: []string{"^/http/skip.*$"},
	}}

	for _, tc := range testCases {
		t.Run(tc.name+" GET", func(t *testing.T) {
			runHTTPTest(t, tc, "GET")
		})

		t.Run(tc.name+" POST", func(t *testing.T) {
			runHTTPTest(t, tc, "POST")
		})
	}
}

// TestProxyHTTPBlocklist tests that the proxy can block HTTP requests from
// a blocked IP.
func TestProxyHTTPBlocklist(t *testing.T) {
	services := []*proxy.Service{{
		Address:    testTargetServiceAddress,
		HostRegexp: testHostRegexp,
		PathRegexp: testPathRegexpHTTP,
		Protocol:   "http",
		Auth:       "off",
	}}

	mockAuth := auth.NewMockAuthenticator()

	// Block the IP that will be used in the request.
	blockedIP := "127.0.0.1"
	p, err := proxy.New(mockAuth, services, []string{blockedIP})
	require.NoError(t, err)

	// Start the proxy server.
	server := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			t.Errorf("proxy serve error: %v", err)
		}
	}()
	defer closeOrFail(t, server)

	// Start the backend server.
	backendService := &http.Server{Addr: testTargetServiceAddress}
	go func() { _ = startBackendHTTP(backendService) }()
	defer closeOrFail(t, backendService)

	time.Sleep(100 * time.Millisecond)

	// Make a request with a spoofed RemoteAddr that matches the blocklist.
	req, err := http.NewRequest(
		"GET",
		fmt.Sprintf("http://%s/http/test", testProxyAddr),
		nil,
	)
	require.NoError(t, err)

	// Create a custom transport to override the local IP — simulate blocked IP.
	customTransport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			lAddr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
			d := net.Dialer{LocalAddr: lAddr}
			return d.DialContext(context.Background(), "tcp", testProxyAddr)
		},
	}
	client := &http.Client{Transport: customTransport}

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "access denied\n", string(body))
}

// runHTTPTest tests that the proxy can forward HTTP requests to a backend
// service and handle L402 authentication correctly.
func runHTTPTest(t *testing.T, tc *testCase, method string) {
	// Create a list of services to proxy between.
	services := []*proxy.Service{{
		Address:                      testTargetServiceAddress,
		HostRegexp:                   testHostRegexp,
		PathRegexp:                   testPathRegexpHTTP,
		Protocol:                     "http",
		Auth:                         tc.auth,
		AuthWhitelistPaths:           tc.authWhitelist,
		AuthSkipInvoiceCreationPaths: tc.authSkipInvoiceCreationPaths,
	}}

	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, []string{})
	require.NoError(t, err)

	// Start server that gives requests to the proxy.
	server := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			t.Errorf("Error serving on %s: %v", testProxyAddr, err)
		}
	}()
	defer closeOrFail(t, server)

	// Start the target backend service.
	backendService := &http.Server{Addr: testTargetServiceAddress}
	go func() { _ = startBackendHTTP(backendService) }()
	defer closeOrFail(t, backendService)

	// Wait for servers to start.
	time.Sleep(100 * time.Millisecond)

	// Test making a request to the backend service without the
	// Authorization header set.
	client := &http.Client{}
	url := fmt.Sprintf("http://%s/http/test", testProxyAddr)

	req, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)

	if method == "POST" {
		req.Header.Add("Content-Type", "application/json")
		req.Body = io.NopCloser(strings.NewReader(`{}`))
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	require.Equal(t, "402 Payment Required", resp.Status)

	bodyContent, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "payment required\n", string(bodyContent))
	require.EqualValues(t, len(bodyContent), resp.ContentLength)

	authHeader := resp.Header.Get("Www-Authenticate")
	require.Regexp(t, "(LSAT|L402)", authHeader)
	_ = resp.Body.Close()

	// Make sure that if we query an URL that is on the whitelist, we don't
	// get the 402 response.
	if len(tc.authWhitelist) > 0 {
		url = fmt.Sprintf("http://%s/http/white", testProxyAddr)
		req, err := http.NewRequest(method, url, nil)
		require.NoError(t, err)
		resp, err = client.Do(req)
		require.NoError(t, err)

		require.Equal(t, "200 OK", resp.Status)

		// Ensure that we got the response body we expect.
		defer closeOrFail(t, resp.Body)
		bodyBytes, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, testHTTPResponseBody, string(bodyBytes))
		require.EqualValues(t, len(bodyBytes), resp.ContentLength)
	}

	// Make sure that if we query a URL that is on the skip invoice
	// creation list, we get a 401 if auth fails.
	if len(tc.authSkipInvoiceCreationPaths) > 0 {
		urlToSkip := fmt.Sprintf("http://%s/http/skip", testProxyAddr)
		reqToSkip, err := http.NewRequest(method, urlToSkip, nil)
		require.NoError(t, err)

		if method == "POST" {
			reqToSkip.Header.Add("Content-Type", "application/json")
			reqToSkip.Body = io.NopCloser(strings.NewReader(`{}`))
		}

		respSkipped, err := client.Do(reqToSkip)
		require.NoError(t, err)

		require.Equal(t, http.StatusUnauthorized, respSkipped.StatusCode)
		require.Equal(t, "401 Unauthorized", respSkipped.Status)

		bodySkippedContent, err := io.ReadAll(respSkipped.Body)
		require.NoError(t, err)
		require.Equal(t, "unauthorized\n", string(bodySkippedContent))
		require.EqualValues(t, len(bodySkippedContent), respSkipped.ContentLength)
		_ = respSkipped.Body.Close()
	}

	// Make sure that if the Auth header is set, the client's request is
	// proxied to the backend service for a non-skipped, non-whitelisted path.
	req, err = http.NewRequest(method, url, nil)
	require.NoError(t, err)
	req.Header.Add("Authorization", "foobar")

	resp, err = client.Do(req)
	require.NoError(t, err)

	require.Equal(t, "200 OK", resp.Status)

	// Ensure that we got the response body we expect.
	defer closeOrFail(t, resp.Body)
	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, testHTTPResponseBody, string(bodyBytes))
	require.EqualValues(t, len(bodyBytes), resp.ContentLength)
}

// TestProxyGRPC tests that the proxy can forward gRPC requests to a backend
// service and handle L402 authentication correctly.
func TestProxyGRPC(t *testing.T) {
	testCases := []*testCase{{
		name: "no whitelist",
		auth: "on",
	}, {
		name:           "no whitelist expect err",
		auth:           "on",
		wantBackendErr: true,
	}, {
		name: "with whitelist",
		auth: "on",
		authWhitelist: []string{
			"^/proxy_test\\.Greeter/SayHelloNoAuth.*$",
		},
	}, {
		name: "gRPC no whitelist with skip for SayHello",
		auth: "on",
		authSkipInvoiceCreationPaths: []string{
			`^/proxy_test[.]Greeter/SayHello.*$`,
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runGRPCTest(t, tc)
		})
	}

	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("stream closed w/o trailers repro %d", i)
		t.Run(name, func(t *testing.T) {
			runGRPCTest(t, &testCase{
				name:           name,
				auth:           "on",
				wantBackendErr: true,
			})
		})
	}
}

// runGRPCTest tests that the proxy can forward gRPC requests to a backend
// service and handle L402 authentication correctly.
func runGRPCTest(t *testing.T, tc *testCase) {
	// Since gRPC only really works over TLS, we need to generate a
	// certificate and key pair first.
	tempDirName, err := os.MkdirTemp("", "proxytest")
	require.NoError(t, err)
	certFile := path.Join(tempDirName, "proxy.cert")
	keyFile := path.Join(tempDirName, "proxy.key")
	certPool, creds, certData, err := genCertPair(certFile, keyFile)
	require.NoError(t, err)
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
	}

	httpListener, err := net.Listen("tcp", testProxyAddr)
	if err != nil {
		t.Errorf("Error listening on %s: %v", testProxyAddr, err)
	}
	tlsListener := tls.NewListener(
		httpListener, configFromCert(&certData, certPool),
	)
	defer closeOrFail(t, tlsListener)

	// Create a list of services to proxy between.
	services := []*proxy.Service{{
		Address:                      testTargetServiceAddress,
		HostRegexp:                   testHostRegexp,
		PathRegexp:                   testPathRegexpGRPC,
		Protocol:                     "https",
		TLSCertPath:                  certFile,
		Auth:                         tc.auth,
		AuthWhitelistPaths:           tc.authWhitelist,
		AuthSkipInvoiceCreationPaths: tc.authSkipInvoiceCreationPaths,
	}}

	// Create the proxy server and start serving on TLS.
	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, []string{})
	require.NoError(t, err)
	server := &http.Server{
		Addr:      testProxyAddr,
		Handler:   http.HandlerFunc(p.ServeHTTP),
		TLSConfig: configFromCert(&certData, certPool),
	}
	go func() {
		err := server.Serve(tlsListener)
		if !isClosedErr(err) {
			t.Errorf("Error serving on %s: %v", testProxyAddr, err)
		}
	}()

	// Start the target backend service also on TLS.
	serverOpts := []grpc.ServerOption{
		grpc.Creds(credentials.NewTLS(configFromCert(
			&certData, certPool,
		))),
	}
	backendService := grpc.NewServer(serverOpts...)
	go func() { _ = startBackendGRPC(backendService) }()
	defer backendService.Stop()

	// Dial to the proxy now, without any authentication.
	conn, err := grpc.Dial(testProxyAddr, opts...)
	require.NoError(t, err)
	client := proxytest.NewGreeterClient(conn)

	// Make request without authentication. We expect an error that can
	// be parsed by gRPC. We also need to extract any metadata that are
	// sent in the trailer to make sure the challenge is returned properly.
	req := &proxytest.HelloRequest{Name: "foo"}
	captureMetadata := metadata.MD{}
	_, err = client.SayHello(
		context.Background(), req, grpc.WaitForReady(true),
		grpc.Trailer(&captureMetadata),
	)
	require.Error(t, err)
	if len(tc.authSkipInvoiceCreationPaths) > 0 {
		statusErr, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.Internal, statusErr.Code())
		require.Equal(t, "unauthorized", statusErr.Message())
	} else {
		require.True(t, l402.IsPaymentRequired(err))

		// We expect the WWW-Authenticate header field to be set to an L402
		// auth response.
		expectedHeaderContent, _ := mockAuth.FreshChallengeHeader("", 0)
		capturedHeader := captureMetadata.Get("WWW-Authenticate")
		require.Len(t, capturedHeader, 2)
		require.Equal(
			t, expectedHeaderContent.Values("WWW-Authenticate"),
			capturedHeader,
		)
	}

	// Make sure that if we query an URL that is on the whitelist, we don't
	// get the 402 response.
	if len(tc.authWhitelist) > 0 {
		conn, err = grpc.Dial(testProxyAddr, opts...)
		require.NoError(t, err)
		client = proxytest.NewGreeterClient(conn)

		// Make the request. This time no error should be returned.
		req = &proxytest.HelloRequest{Name: "foo"}
		res, err := client.SayHelloNoAuth(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, "Hello foo", res.Message)
	}

	// Dial to the proxy again, this time with a dummy macaroon.
	dummyMac, err := macaroon.New(
		[]byte("key"), []byte("id"), "loc", macaroon.LatestVersion,
	)
	require.NoError(t, err)

	cred, err := macaroons.NewMacaroonCredential(dummyMac)
	require.NoError(t, err)
	opts = []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(cred),
	}
	conn, err = grpc.Dial(testProxyAddr, opts...)
	require.NoError(t, err)
	client = proxytest.NewGreeterClient(conn)

	// Make the request. This time no error should be returned.
	req = &proxytest.HelloRequest{
		Name: "foo", ReturnError: tc.wantBackendErr,
	}
	res, err := client.SayHello(context.Background(), req)

	if tc.wantBackendErr {
		require.Error(t, err)
		statusErr, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, errBackend.Error(), statusErr.Message())
		require.Equal(t, codes.Unknown, statusErr.Code())
	} else {
		require.NoError(t, err)
		require.Equal(t, "Hello foo", res.Message)
	}
}

// startBackendHTTP starts the given HTTP server and blocks until the server
// is shut down.
func startBackendHTTP(server *http.Server) error {
	sayHello := func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(testHTTPResponseBody))
		if err != nil {
			panic(err)
		}
	}
	server.Handler = http.HandlerFunc(sayHello)
	return server.ListenAndServe()
}

// startBackendGRPC starts the given RPC server and blocks until the server is
// shut down.
func startBackendGRPC(grpcServer *grpc.Server) error {
	server := helloServer{}
	proxytest.RegisterGreeterServer(grpcServer, &server)
	grpcListener, err := net.Listen("tcp", testTargetServiceAddress)
	if err != nil {
		return fmt.Errorf("RPC server unable to listen on %s",
			testTargetServiceAddress)

	}
	return grpcServer.Serve(grpcListener)
}

// genCertPair generates a pair of private key and certificate and returns them
// in different formats needed to spin up test servers and clients.
func genCertPair(certFile, keyFile string) (*x509.CertPool,
	credentials.TransportCredentials, tls.Certificate, error) {

	crt := tls.Certificate{}
	certBytes, keyBytes, err := cert.GenCertPair(
		"aperture autogenerated cert", nil, nil, false,
		DefaultAutogenValidity,
	)
	if err != nil {
		return nil, nil, crt, fmt.Errorf("unable to generate cert "+
			"pair: %v", err)
	}
	err = cert.WriteCertPair(certFile, keyFile, certBytes, keyBytes)
	if err != nil {
		return nil, nil, crt, err
	}

	crt, x509Cert, err := cert.LoadCert(certFile, keyFile)
	if err != nil {
		return nil, nil, crt, fmt.Errorf("unable to load cert: %v", err)
	}

	cp := x509.NewCertPool()
	cp.AddCert(x509Cert)

	creds, err := credentials.NewClientTLSFromFile(certFile, "")
	if err != nil {
		return nil, nil, crt, fmt.Errorf("unable to load cert file: "+
			"%v", err)
	}
	return cp, creds, crt, nil
}

// configFromCert creates a new TLS configuration from a certificate and a cert
// pool. These configs shouldn't be shared among different server instances to
// avoid data races.
func configFromCert(crt *tls.Certificate, certPool *x509.CertPool) *tls.Config {
	tlsConf := cert.TLSConfFromCert(*crt)
	tlsConf.CipherSuites = []uint16{
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	}
	tlsConf.RootCAs = certPool
	tlsConf.InsecureSkipVerify = true

	haveNPN := false
	for _, p := range tlsConf.NextProtos {
		if p == "h2" {
			haveNPN = true
			break
		}
	}
	if !haveNPN {
		tlsConf.NextProtos = append(tlsConf.NextProtos, "h2")
	}
	tlsConf.NextProtos = append(tlsConf.NextProtos, "h2-14")
	// make sure http 1.1 is *after* all of the other ones.
	tlsConf.NextProtos = append(tlsConf.NextProtos, "http/1.1")

	return tlsConf
}

func closeOrFail(t *testing.T, c io.Closer) {
	err := c.Close()
	if !isClosedErr(err) {
		t.Fatal(err)
	}
}

func isClosedErr(err error) bool {
	if err == nil {
		return true
	}

	if err == http.ErrServerClosed {
		return true
	}

	if strings.Contains(err.Error(), "use of closed network connection") {
		return true
	}

	return false
}
