package proxy_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/proxy"
	proxytest "github.com/lightninglabs/kirin/proxy/testdata"
	"github.com/lightningnetwork/lnd/cert"
	"github.com/lightningnetwork/lnd/macaroons"
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

// helloServer is a simple server that implements the GreeterServer interface.
type helloServer struct{}

// SayHello returns a simple string that also contains a string from the
// request.
func (s *helloServer) SayHello(ctx context.Context,
	req *proxytest.HelloRequest) (*proxytest.HelloReply, error) {

	return &proxytest.HelloReply{
		Message: fmt.Sprintf("Hello %s", req.Name),
	}, nil
}

// SayHello returns a simple string that also contains a string from the
// request. This RPC method should be whitelisted to be called without any
// authentication required.
func (s *helloServer) SayHelloNoAuth(ctx context.Context,
	req *proxytest.HelloRequest) (*proxytest.HelloReply, error) {

	return &proxytest.HelloReply{
		Message: fmt.Sprintf("Hello %s", req.Name),
	}, nil
}

// TestProxyHTTP tests that the proxy can forward HTTP requests to a backend
// service and handle LSAT authentication correctly.
func TestProxyHTTP(t *testing.T) {
	// Create a list of services to proxy between.
	services := []*proxy.Service{{
		Address:    testTargetServiceAddress,
		HostRegexp: testHostRegexp,
		PathRegexp: testPathRegexpHTTP,
		Protocol:   "http",
	}}

	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, "static")
	if err != nil {
		t.Fatalf("failed to create new proxy: %v", err)
	}

	// Start server that gives requests to the proxy.
	server := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go server.ListenAndServe()
	defer server.Close()

	// Start the target backend service.
	backendService := &http.Server{Addr: testTargetServiceAddress}
	go startBackendHTTP(backendService)
	defer backendService.Close()

	// Wait for servers to start.
	time.Sleep(100 * time.Millisecond)

	// Test making a request to the backend service without the
	// Authorization header set.
	client := &http.Client{}
	url := fmt.Sprintf("http://%s/http/test", testProxyAddr)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("errored making http request: %v", err)
	}

	if resp.Status != "402 Payment Required" {
		t.Fatalf("expected 402 status code, got: %v", resp.Status)
	}

	authHeader := resp.Header.Get("Www-Authenticate")
	if !strings.Contains(authHeader, "LSAT") {
		t.Fatalf("expected partial LSAT in response header, got: %v",
			authHeader)
	}

	// Make sure that if the Auth header is set, the client's request is
	// proxied to the backend service.
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("error creating request: %v", err)
	}
	req.Header.Add("Authorization", "foobar")

	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("errored making http request: %v", err)
	}

	if resp.Status != "200 OK" {
		t.Fatalf("expected 200 OK status code, got: %v", resp.Status)
	}

	// Ensure that we got the response body we expect.
	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if string(bodyBytes) != testHTTPResponseBody {
		t.Fatalf("expected response body %v, got %v",
			testHTTPResponseBody, string(bodyBytes))
	}
}

// TestProxyHTTP tests that the proxy can forward gRPC requests to a backend
// service and handle LSAT authentication correctly.
func TestProxyGRPC(t *testing.T) {
	// Since gRPC only really works over TLS, we need to generate a
	// certificate and key pair first.
	tempDirName, err := ioutil.TempDir("", "proxytest")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}
	certFile := path.Join(tempDirName, "proxy.cert")
	keyFile := path.Join(tempDirName, "proxy.key")
	certPool, creds, certData, err := genCertPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("unable to create cert pair: %v", err)
	}

	// Create a list of services to proxy between.
	services := []*proxy.Service{{
		Address:     testTargetServiceAddress,
		HostRegexp:  testHostRegexp,
		PathRegexp:  testPathRegexpGRPC,
		Protocol:    "https",
		TLSCertPath: certFile,
	}}

	// Create the proxy server and start serving on TLS.
	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, "static")
	if err != nil {
		t.Fatalf("failed to create new proxy: %v", err)
	}
	server := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
		TLSConfig: &tls.Config{
			RootCAs:            certPool,
			InsecureSkipVerify: true,
		},
	}
	go server.ListenAndServeTLS(certFile, keyFile)
	defer server.Close()

	// Start the target backend service also on TLS.
	tlsConf := cert.TLSConfFromCert(certData)
	serverOpts := []grpc.ServerOption{
		grpc.Creds(credentials.NewTLS(tlsConf)),
	}
	backendService := grpc.NewServer(serverOpts...)
	go startBackendGRPC(backendService)
	defer backendService.Stop()

	// Dial to the proxy now, without any authentication.
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	conn, err := grpc.Dial(testProxyAddr, opts...)
	if err != nil {
		t.Fatalf("unable to connect to RPC server: %v", err)
	}
	client := proxytest.NewGreeterClient(conn)

	// Make request without authentication. We expect an error that can
	// be parsed by gRPC.
	req := &proxytest.HelloRequest{Name: "foo"}
	res, err := client.SayHello(
		context.Background(), req, grpc.WaitForReady(true),
	)
	if err == nil {
		t.Fatalf("expected error to be returned without auth")
	}
	statusErr, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected error to be status.Status")
	}
	if statusErr.Code() != codes.Internal {
		t.Fatalf("unexpected code. wanted %d, got %d",
			codes.Internal, statusErr.Code())
	}
	if statusErr.Message() != "payment required" {
		t.Fatalf("invalid error. expected [%s] got [%s]",
			"payment required", err.Error())
	}

	// Dial to the proxy again, this time with a dummy macaroon.
	dummyMac, err := macaroon.New(
		[]byte("key"), []byte("id"), "loc", macaroon.LatestVersion,
	)
	opts = []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(macaroons.NewMacaroonCredential(
			dummyMac,
		)),
	}
	conn, err = grpc.Dial(testProxyAddr, opts...)
	if err != nil {
		t.Fatalf("unable to connect to RPC server: %v", err)
	}
	client = proxytest.NewGreeterClient(conn)

	// Make the request. This time no error should be returned.
	req = &proxytest.HelloRequest{Name: "foo"}
	res, err = client.SayHello(context.Background(), req)
	if err != nil {
		t.Fatalf("unable to call service: %v", err)
	}
	if res.Message != "Hello foo" {
		t.Fatalf("unexpected reply, wanted %s, got %s",
			"Hello foo", res.Message)
	}
}

// TestWhitelistHTTP verifies that a white list entry for a service allows an
// authentication exception to be configured.
func TestWhitelistHTTP(t *testing.T) {
	// Create a service with authentication on by default, with one
	// exception configured as whitelist.
	services := []*proxy.Service{{
		Address:            testTargetServiceAddress,
		HostRegexp:         testHostRegexp,
		PathRegexp:         testPathRegexpHTTP,
		Protocol:           "http",
		Auth:               "on",
		AuthWhitelistPaths: []string{"^/http/white.*$"},
	}}

	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, "static")
	if err != nil {
		t.Fatalf("failed to create new proxy: %v", err)
	}

	// Start server that gives requests to the proxy.
	server := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go server.ListenAndServe()
	defer server.Close()

	// Start the target backend service.
	backendService := &http.Server{Addr: testTargetServiceAddress}
	go startBackendHTTP(backendService)
	defer backendService.Close()

	// Wait for servers to start.
	time.Sleep(100 * time.Millisecond)

	// Test making a request to the backend service to an URL where
	// authentication is enabled.
	client := &http.Client{}
	url := fmt.Sprintf("http://%s/http/black", testProxyAddr)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("errored making http request: %v", err)
	}
	if resp.Status != "402 Payment Required" {
		t.Fatalf("expected 402 status code, got: %v", resp.Status)
	}
	authHeader := resp.Header.Get("Www-Authenticate")
	if !strings.Contains(authHeader, "LSAT") {
		t.Fatalf("expected partial LSAT in response header, got: %v",
			authHeader)
	}

	// Make sure that if we query an URL that is on the whitelist, we don't
	// get the 402 response.
	url = fmt.Sprintf("http://%s/http/white", testProxyAddr)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatalf("error creating request: %v", err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("errored making http request: %v", err)
	}
	if resp.Status != "200 OK" {
		t.Fatalf("expected 200 OK status code, got: %v", resp.Status)
	}

	// Ensure that we got the response body we expect.
	defer resp.Body.Close()
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	if string(bodyBytes) != testHTTPResponseBody {
		t.Fatalf("expected response body %v, got %v",
			testHTTPResponseBody, string(bodyBytes))
	}
}

// TestWhitelistGRPC verifies that a white list entry for a service allows an
// authentication exception to be configured.
func TestWhitelistGRPC(t *testing.T) {
	// Since gRPC only really works over TLS, we need to generate a
	// certificate and key pair first.
	tempDirName, err := ioutil.TempDir("", "proxytest")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}
	certFile := path.Join(tempDirName, "proxy.cert")
	keyFile := path.Join(tempDirName, "proxy.key")
	certPool, creds, certData, err := genCertPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("unable to create cert pair: %v", err)
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}

	// Create a list of services to proxy between.
	services := []*proxy.Service{{
		Address:     testTargetServiceAddress,
		HostRegexp:  testHostRegexp,
		PathRegexp:  testPathRegexpGRPC,
		Protocol:    "https",
		TLSCertPath: certFile,
		Auth:        "on",
		AuthWhitelistPaths: []string{
			"^/proxy_test\\.Greeter/SayHelloNoAuth.*$",
		},
	}}

	// Create the proxy server and start serving on TLS.
	mockAuth := auth.NewMockAuthenticator()
	p, err := proxy.New(mockAuth, services, "static")
	if err != nil {
		t.Fatalf("failed to create new proxy: %v", err)
	}
	server := &http.Server{
		Addr:    testProxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
		TLSConfig: &tls.Config{
			RootCAs:            certPool,
			InsecureSkipVerify: true,
		},
	}
	go server.ListenAndServeTLS(certFile, keyFile)
	defer server.Close()

	// Start the target backend service also on TLS.
	tlsConf := cert.TLSConfFromCert(certData)
	serverOpts := []grpc.ServerOption{
		grpc.Creds(credentials.NewTLS(tlsConf)),
	}
	backendService := grpc.NewServer(serverOpts...)
	go startBackendGRPC(backendService)
	defer backendService.Stop()

	// Dial to the proxy now, without any authentication.
	conn, err := grpc.Dial(testProxyAddr, opts...)
	if err != nil {
		t.Fatalf("unable to connect to RPC server: %v", err)
	}
	client := proxytest.NewGreeterClient(conn)

	// Test making a request to the backend service to an URL where
	// authentication is enabled.
	req := &proxytest.HelloRequest{Name: "foo"}
	res, err := client.SayHello(
		context.Background(), req, grpc.WaitForReady(true),
	)
	if err == nil {
		t.Fatalf("expected error to be returned without auth")
	}
	statusErr, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected error to be status.Status")
	}
	if statusErr.Code() != codes.Internal {
		t.Fatalf("unexpected code. wanted %d, got %d",
			codes.Internal, statusErr.Code())
	}
	if statusErr.Message() != "payment required" {
		t.Fatalf("invalid error. expected [%s] got [%s]",
			"payment required", err.Error())
	}

	// Make sure that if we query an URL that is on the whitelist, we don't
	// get the 402 response.
	conn, err = grpc.Dial(testProxyAddr, opts...)
	if err != nil {
		t.Fatalf("unable to connect to RPC server: %v", err)
	}
	client = proxytest.NewGreeterClient(conn)

	// Make the request. This time no error should be returned.
	req = &proxytest.HelloRequest{Name: "foo"}
	res, err = client.SayHelloNoAuth(context.Background(), req)
	if err != nil {
		t.Fatalf("unable to call service: %v", err)
	}
	if res.Message != "Hello foo" {
		t.Fatalf("unexpected reply, wanted %s, got %s",
			"Hello foo", res.Message)
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
	err := cert.GenCertPair(
		"kirin autogenerated cert", certFile, keyFile, nil, nil,
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
