package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"
	"regexp"

	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/freebie"
)

const (
	formatPattern = "%s - - \"%s %s %s\" \"%s\" \"%s\""
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
}

// New returns a new Proxy instance that proxies between the services specified,
// using the auth to validate each request's headers and get new challenge
// headers if necessary.
func New(auth auth.Authenticator, services []*Service) (*Proxy, error) {
	err := prepareServices(services)
	if err != nil {
		return nil, err
	}

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
		server:        grpcProxy,
		staticServer:  staticServer,
		authenticator: auth,
		services:      services,
	}, nil
}

// ServeHTTP checks a client's headers for appropriate authorization and either
// returns a challenge or forwards their request to the target backend service.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse and log the remote IP address. We also need the parsed IP
	// address for the freebie count.
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = "0.0.0.0"
	}
	remoteIp := net.ParseIP(remoteHost)
	if remoteIp == nil {
		remoteIp = net.IPv4zero
	}
	logRequest := func() {
		log.Infof(formatPattern, remoteIp.String(), r.Method,
			r.RequestURI, r.Proto, r.Referer(), r.UserAgent())
	}
	defer logRequest()

	// Serve static index HTML page.
	if r.Method == "GET" &&
		(r.URL.Path == "/" || r.URL.Path == "/index.html") {

		log.Debugf("Dispatching request %s to static file server.",
			r.URL.Path)
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
			ok, err := target.freebieDb.CanPass(r, remoteIp)
			if err != nil {
				log.Errorf("Error querying freebie db: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if !ok {
				p.handlePaymentRequired(w, r)
				return
			}
			_, err = target.freebieDb.TallyFreebie(r, remoteIp)
			if err != nil {
				log.Errorf("Error updating freebie db: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
	case target.Auth.IsOff():
	}

	// If we got here, it means everything is OK to pass the request to the
	// service backend via the reverse proxy.
	p.server.ServeHTTP(w, r)
}

// prepareServices prepares the backend service configurations to be used by the
// proxy.
func prepareServices(services []*Service) error {
	for _, service := range services {
		// Each freebie enabled service gets its own store.
		if service.Auth.IsFreebie() {
			service.freebieDb = freebie.NewMemIpMaskStore(
				service.Auth.FreebieCount(),
			)
		}
	}
	return nil
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
			log.Tracef("Req host [%s] doesn't match [%s].",
				req.Host, hostRegexp)
			continue
		}

		if service.PathRegexp == "" {
			log.Debugf("Host [%s] matched pattern [%s] and path "+
				"expression is empty. Using service [%s].",
				req.Host, hostRegexp, service.Address)
			return service, true
		}

		pathRegexp := regexp.MustCompile(service.PathRegexp)
		if !pathRegexp.MatchString(req.URL.Path) {
			log.Tracef("Req path [%s] doesn't match [%s].",
				req.URL.Path, pathRegexp)
			continue
		}

		log.Debugf("Host [%s] matched pattern [%s] and path [%s] "+
			"matched [%s]. Using service [%s].",
			req.Host, hostRegexp, req.URL.Path, pathRegexp,
			service.Address)
		return service, true
	}
	log.Errorf("No backend service matched request [%s%s].", req.Host,
		req.URL.Path)
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
	log.Debugf("Adding CORS headers to response.")

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
		log.Errorf("Error creating new challenge header, response 500.")
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
		log.Errorf("Error writing response: %v", err)
	}
}
