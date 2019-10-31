package proxy_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/proxy"
	proxytest "github.com/lightninglabs/kirin/proxy/testdata"
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
	testPathRegexpGRPC       = "^/proxy_test.*$"
	testTargetServiceAddress = "localhost:8082"
	testHTTPResponseBody     = "HTTP Hello"
)

var (
	serialNumberLimit = new(big.Int).Lsh(big.NewInt(1), 128)
	tlsCipherSuites   = []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
	}
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
	certPool, creds, cert, err := genCertPair(certFile, keyFile)
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
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		CipherSuites: tlsCipherSuites,
		MinVersion:   tls.VersionTLS12,
	}
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

	org := "kirin autogenerated cert"
	cert := tls.Certificate{}
	now := time.Now()
	validUntil := now.Add(1 * time.Hour)

	// Generate a serial number that's below the serialNumberLimit.
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, cert, fmt.Errorf("failed to generate serial "+
			"number: %s", err)
	}

	// Collect the host's IP addresses, including loopback, in a slice.
	ipAddresses := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	// addIP appends an IP address only if it isn't already in the slice.
	addIP := func(ipAddr net.IP) {
		for _, ip := range ipAddresses {
			if ip.Equal(ipAddr) {
				return
			}
		}
		ipAddresses = append(ipAddresses, ipAddr)
	}

	// Add all the interface IPs that aren't already in the slice.
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, nil, cert, err
	}
	for _, a := range addrs {
		ipAddr, _, err := net.ParseCIDR(a.String())
		if err == nil {
			addIP(ipAddr)
		}
	}

	// Collect the host's names into a slice.
	host, err := os.Hostname()
	if err != nil {
		return nil, nil, cert, err
	}
	dnsNames := []string{host}
	if host != "localhost" {
		dnsNames = append(dnsNames, "localhost")
	}

	// Generate a private key for the certificate.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, cert, err
	}

	// Construct the certificate template.
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{org},
			CommonName:   host,
		},
		NotBefore: now.Add(-time.Hour * 24),
		NotAfter:  validUntil,

		KeyUsage: x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true, // so can sign self.
		BasicConstraintsValid: true,

		DNSNames:    dnsNames,
		IPAddresses: ipAddresses,
	}

	derBytes, err := x509.CreateCertificate(
		rand.Reader, &template, &template, &priv.PublicKey, priv,
	)
	if err != nil {
		return nil, nil, cert, fmt.Errorf("failed to create "+
			"certificate: %v", err)
	}

	certBuf := &bytes.Buffer{}
	err = pem.Encode(
		certBuf,
		&pem.Block{Type: "CERTIFICATE",
			Bytes: derBytes,
		},
	)
	if err != nil {
		return nil, nil, cert, fmt.Errorf("failed to encode "+
			"certificate: %v", err)
	}

	keybytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, cert, fmt.Errorf("unable to encode privkey: "+
			"%v", err)
	}
	keyBuf := &bytes.Buffer{}
	err = pem.Encode(
		keyBuf,
		&pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: keybytes,
		},
	)
	if err != nil {
		return nil, nil, cert, fmt.Errorf("failed to encode private "+
			"key: %v", err)
	}
	cert, err = tls.X509KeyPair(certBuf.Bytes(), keyBuf.Bytes())
	if err != nil {
		return nil, nil, cert, fmt.Errorf("failed to create key pair: "+
			"%v", err)
	}

	// Write cert and key files.
	if err = ioutil.WriteFile(certFile, certBuf.Bytes(), 0644); err != nil {
		return nil, nil, cert, fmt.Errorf("unable to write cert file "+
			"at %v: %v", certFile, err)
	}
	if err = ioutil.WriteFile(keyFile, keyBuf.Bytes(), 0600); err != nil {
		os.Remove(certFile)
		return nil, nil, cert, fmt.Errorf("unable to write key file "+
			"at %v: %v", keyFile, err)
	}

	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM(certBuf.Bytes()) {
		return nil, nil, cert, fmt.Errorf("credentials: failed to " +
			"append certificate")
	}

	creds, err := credentials.NewClientTLSFromFile(certFile, "")
	if err != nil {
		return nil, nil, cert, fmt.Errorf("unable to load cert file: "+
			"%v", err)
	}
	return cp, creds, cert, nil
}
