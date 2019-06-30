package proxy_test

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/proxy"
)

const (
	testFQDN                 = "localhost:10019"
	testTargetServiceAddress = "localhost:8082"
	testHTTPResponseBody     = "HTTP Hello"
)

func TestProxy(t *testing.T) {
	// Create a list of services to proxy between.
	services := []*proxy.Service{
		&proxy.Service{
			Address: testTargetServiceAddress,
			FQDN:    testFQDN,
		},
	}

	auth := auth.NewMockAuthenticator()
	proxy, err := proxy.New(auth, services)
	if err != nil {
		t.Fatalf("failed to create new proxy: %v", err)
	}

	// Start server that gives requests to the proxy.
	server := &http.Server{
		Addr:    testFQDN,
		Handler: http.HandlerFunc(proxy.ServeHTTP),
	}

	go func() {
		if err := server.ListenAndServe(); err != nil {
			t.Fatalf("failed to serve to proxy: %v", err)
		}
	}()

	// Start the target backend service.
	go func() {
		if err := startHTTPHello(); err != nil {
			t.Fatalf("failed to start backend service: %v", err)
		}
	}()

	// Test making a request to the backend service without the
	// Authorization header set.
	client := &http.Client{}
	url := fmt.Sprintf("http://%s", testFQDN)
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

func startHTTPHello() error {
	sayHello := func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testHTTPResponseBody))
	}
	http.HandleFunc("/", sayHello)
	return http.ListenAndServe(testTargetServiceAddress, nil)
}
