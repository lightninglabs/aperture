package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"regexp"

	"github.com/lightninglabs/kirin/auth"
)

// Proxy is a HTTP, HTTP/2 and gRPC handler that takes an incoming request,
// uses its authenticator to validate the request's headers, and either returns
// a challenge to the client or forwards the request to another server and
// proxies the response back to the client.
type Proxy struct {
	server *httputil.ReverseProxy

	staticServer http.Handler

	authenticator auth.Authenticator

	services []*Service

	freebieCounter map[string]uint8
}

// New returns a new Proxy instance that proxies between the services specified,
// using the auth to validate each request's headers and get new challenge
// headers if necessary.
func New(auth auth.Authenticator, services []*Service) (*Proxy, error) {
	cp, err := certPool(services)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		RootCAs:            cp,
		InsecureSkipVerify: true,
	}
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   tlsConfig,
	}

	grpcProxy := &httputil.ReverseProxy{
		Director:  director(services),
		Transport: transport,
		ModifyResponse: func(res *http.Response) error {
			addCorsHeaders(res.Header)
			return nil
		},

		// A negative value means to flush immediately after each write
		// to the client.
		FlushInterval: -1,
	}

	staticServer := http.FileServer(http.Dir("static"))

	return &Proxy{
		server:         grpcProxy,
		staticServer:   staticServer,
		authenticator:  auth,
		services:       services,
		freebieCounter: map[string]uint8{},
	}, nil
}

// certPool builds a pool of x509 certificates from the backend services.
func certPool(services []*Service) (*x509.CertPool, error) {
	cp := x509.NewCertPool()
	for _, service := range services {
		if service.TLSCertPath == "" {
			continue
		}

		b, err := ioutil.ReadFile(service.TLSCertPath)
		if err != nil {
			return nil, err
		}

		if !cp.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("credentials: failed to " +
				"append certificate")
		}
	}

	return cp, nil
}

// matchService tries to match a backend service to an HTTP request by regular
// expression matching the host and path.
func matchService(req *http.Request, services []*Service) (*Service, bool) {
	for _, service := range services {
		hostRegexp := regexp.MustCompile(service.HostRegexp)
		if !hostRegexp.MatchString(req.Host) {
			continue
		}

		if service.PathRegexp == "" {
			return service, true
		}

		urlRegexp := regexp.MustCompile(service.PathRegexp)
		if !urlRegexp.MatchString(req.URL.Path) {
			continue
		}

		return service, true
	}
	return nil, false
}

// director returns a closure that rewrites an incoming request to be forwarded
// to a backend service.
func director(services []*Service) func(req *http.Request) {
	return func(req *http.Request) {
		target, ok := matchService(req, services)
		if ok {
			// Rewrite address and protocol in the request so the
			// real service is called instead.
			req.Host = target.Address
			req.URL.Host = target.Address
			req.URL.Scheme = target.Protocol

			// Don't forward the authorization header since the
			// services won't know what it is.
			req.Header.Del("Authorization")
		}
	}
}

// addCorsHeaders adds HTTP header fields that are required for Cross Origin
// Resource Sharing. These header fields are needed to signal to the browser
// that it's ok to allow requests to sub domains, even if the JS was served from
// the top level domain.
func addCorsHeaders(header http.Header) {
	header.Add("Access-Control-Allow-Origin", "*")
	header.Add("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	header.Add("Access-Control-Expose-Headers", "WWW-Authenticate")
	header.Add(
		"Access-Control-Allow-Headers",
		"Authorization, Grpc-Metadata-macaroon, WWW-Authenticate",
	)
}

// handlePaymentRequired returns fresh challenge header fields and status code
// to the client signaling that a payment is required to fulfil the request.
func (p *Proxy) handlePaymentRequired(w http.ResponseWriter, r *http.Request) {
	addCorsHeaders(r.Header)

	header, err := p.authenticator.FreshChallengeHeader(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	for name, value := range header {
		w.Header().Set(name, value[0])
		for i := 1; i < len(value); i++ {
			w.Header().Add(name, value[i])
		}
	}

	w.WriteHeader(http.StatusPaymentRequired)
	if _, err := w.Write([]byte("payment required")); err != nil {
		fmt.Printf("error writing response: %v", err)
	}
}

// ServeHTTP checks a client's headers for appropriate authorization and either
// returns a challenge or forwards their request to the target backend service.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Serve static index HTML page.
	if r.Method == "GET" &&
		(r.URL.Path == "/" || r.URL.Path == "/index.html") {
		p.staticServer.ServeHTTP(w, r)
		return
	}

	// For OPTIONS requests we only need to set the CORS headers, not serve
	// any content;
	if r.Method == "OPTIONS" {
		addCorsHeaders(w.Header())
		w.WriteHeader(http.StatusOK)
		return
	}

	// Every request that makes it to here must be matched to a backend
	// service. Otherwise it a wrong request and receives a 404 not found.
	target, ok := matchService(r, p.services)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Determine auth level required to access service and dispatch request
	// accordingly.
	switch {
	case target.Auth.IsOn():
		if !p.authenticator.Accept(&r.Header) {
			p.handlePaymentRequired(w, r)
			return
		}
	case target.Auth.IsFreebie():
		// We only need to respect the freebie counter if the user
		// is not authenticated at all.
		if !p.authenticator.Accept(&r.Header) {
			counter, ok := p.freebieCounter[r.RemoteAddr]
			if !ok {
				counter = 0
			}
			if counter >= target.Auth.FreebieCount() {
				p.handlePaymentRequired(w, r)
				return
			}
			p.freebieCounter[r.RemoteAddr] = counter + 1
		}
	case target.Auth.IsOff():
	}

	// If we got here, it means everything is OK to pass the request to the
	// service backend via the reverse proxy.
	p.server.ServeHTTP(w, r)
}
