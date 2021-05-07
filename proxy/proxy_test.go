package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"path"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/proxy"
	proxytest "github.com/lightninglabs/aperture/proxy/testdata"
	"github.com/lightningnetwork/lnd/cert"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
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
)

type testCase struct {
	name          string
	auth          auth.Level
	authWhitelist []string
}

// helloServer is a simple server that implements the GreeterServer interface.
type helloServer struct{}

// SayHello returns a simple string that also contains a string from the
// request.
func (s *helloServer) SayHello(_ context.Context,
	req *proxytest.HelloRequest) (*proxytest.HelloReply, error) {

	return &proxytest.HelloReply{
		Message: fmt.Sprintf("Hello %s", req.Name),
	}, nil
}

// SayHello returns a simple string that also contains a string from the
// request. This RPC method should be whitelisted to be called without any
// authentication required.
func (s *helloServer) SayHelloNoAuth(_ context.Context,
	req *proxytest.HelloRequest) (*proxytest.HelloReply, error) {

	return &proxytest.HelloReply{
		Message: fmt.Sprintf("Hello %s", req.Name),
	}, nil
}

// TestProxyHTTP tests that the proxy can forward HTTP requests to a backend
// service and handle LSAT authentication correctly.
func TestProxyHTTP(t *testing.T) {
	testCases := []*testCase{{
		name: "no whitelist",
		auth: "on",
	}, {
		name:          "with whitelist",
		auth:          "on",
		authWhitelist: []string{"^/http/white.*$"},
	}}

	for _, tc := range testCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			runHTTPTest(t, tc)
		})
	}
}

// TestProxyHTTP tests that the proxy can forward HTTP requests to a backend
// service and handle LSAT authentication correctly.
func runHTTPTest(t *testing.T, tc *testCase) {
	// Create a list of services to proxy between.
	services := []*proxy.Service{{
		Address:            testTargetServiceAddress,
		HostRegexp:         testHostRegexp,
		PathRegexp:         testPathRegexpHTTP,
		Protocol:           "http",
		Auth:               tc.auth,
		AuthWhitelistPaths: tc.authWhitelist,
	}}

	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, true, "static")
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
	resp, err := client.Get(url)
	require.NoError(t, err)

	require.Equal(t, "402 Payment Required", resp.Status)

	authHeader := resp.Header.Get("Www-Authenticate")
	require.Contains(t, authHeader, "LSAT")
	_ = resp.Body.Close()

	// Make sure that if we query an URL that is on the whitelist, we don't
	// get the 402 response.
	if len(tc.authWhitelist) > 0 {
		url = fmt.Sprintf("http://%s/http/white", testProxyAddr)
		req, err := http.NewRequest("GET", url, nil)
		require.NoError(t, err)
		resp, err = client.Do(req)
		require.NoError(t, err)

		require.Equal(t, "200 OK", resp.Status)

		// Ensure that we got the response body we expect.
		defer closeOrFail(t, resp.Body)
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, testHTTPResponseBody, string(bodyBytes))
	}

	// Make sure that if the Auth header is set, the client's request is
	// proxied to the backend service.
	req, err := http.NewRequest("GET", url, nil)
	require.NoError(t, err)
	req.Header.Add("Authorization", "foobar")

	resp, err = client.Do(req)
	require.NoError(t, err)

	require.Equal(t, "200 OK", resp.Status)

	// Ensure that we got the response body we expect.
	defer closeOrFail(t, resp.Body)
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, testHTTPResponseBody, string(bodyBytes))
}

// TestProxyHTTP tests that the proxy can forward gRPC requests to a backend
// service and handle LSAT authentication correctly.
func TestProxyGRPC(t *testing.T) {
	testCases := []*testCase{{
		name: "no whitelist",
		auth: "on",
	}, {
		name: "with whitelist",
		auth: "on",
		authWhitelist: []string{
			"^/proxy_test\\.Greeter/SayHelloNoAuth.*$",
		},
	}}

	for _, tc := range testCases {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			runGRPCTest(t, tc)
		})
	}
}

// TestProxyHTTP tests that the proxy can forward gRPC requests to a backend
// service and handle LSAT authentication correctly.
func runGRPCTest(t *testing.T, tc *testCase) {
	// Since gRPC only really works over TLS, we need to generate a
	// certificate and key pair first.
	tempDirName, err := ioutil.TempDir("", "proxytest")
	require.NoError(t, err)
	certFile := path.Join(tempDirName, "proxy.cert")
	keyFile := path.Join(tempDirName, "proxy.key")
	certPool, creds, certData, err := genCertPair(certFile, keyFile)
	require.NoError(t, err)
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}

	// Create a list of services to proxy between.
	services := []*proxy.Service{{
		Address:            testTargetServiceAddress,
		HostRegexp:         testHostRegexp,
		PathRegexp:         testPathRegexpGRPC,
		Protocol:           "https",
		TLSCertPath:        certFile,
		Auth:               tc.auth,
		AuthWhitelistPaths: tc.authWhitelist,
	}}

	// Create the proxy server and start serving on TLS.
	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, true, "static")
	require.NoError(t, err)
	server := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
		TLSConfig: &tls.Config{
			RootCAs:            certPool,
			InsecureSkipVerify: true,
		},
	}
	go func() { _ = server.ListenAndServeTLS(certFile, keyFile) }()
	defer closeOrFail(t, server)

	// Start the target backend service also on TLS.
	tlsConf := cert.TLSConfFromCert(certData)
	serverOpts := []grpc.ServerOption{
		grpc.Creds(credentials.NewTLS(tlsConf)),
	}
	backendService := grpc.NewServer(serverOpts...)
	go func() { _ = startBackendGRPC(backendService) }()
	defer backendService.Stop()

	// Dial to the proxy now, without any authentication.
	conn, err := grpc.Dial(testProxyAddr, opts...)
	require.NoError(t, err)
	client := proxytest.NewGreeterClient(conn)

	// Make request without authentication. We expect an error that can
	// be parsed by gRPC.
	req := &proxytest.HelloRequest{Name: "foo"}
	_, err = client.SayHello(
		context.Background(), req, grpc.WaitForReady(true),
	)
	require.Error(t, err)
	statusErr, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Internal, statusErr.Code())
	require.Equal(t, "payment required", statusErr.Message())

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
	opts = []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(macaroons.NewMacaroonCredential(
			dummyMac,
		)),
	}
	conn, err = grpc.Dial(testProxyAddr, opts...)
	require.NoError(t, err)
	client = proxytest.NewGreeterClient(conn)

	// Make the request. This time no error should be returned.
	req = &proxytest.HelloRequest{Name: "foo"}
	res, err := client.SayHello(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "Hello foo", res.Message)
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
	err := cert.GenCertPair(
		"aperture autogenerated cert", certFile, keyFile, nil, nil,
		cert.DefaultAutogenValidity,
	)
	if err != nil {
		return nil, nil, crt, fmt.Errorf("unable to generate cert "+
			"pair: %v", err)
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

func closeOrFail(t *testing.T, c io.Closer) {
	err := c.Close()
	if err != nil {
		t.Fatal(err)
	}
}
