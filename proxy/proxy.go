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
		Director:  director(auth, services),
		Transport: transport,
		ModifyResponse: func(res *http.Response) error {
			addCorsHeaders(res.Header)
			return nil
		},
		FlushInterval: -1,
	}

	staticServer := http.FileServer(http.Dir("static"))

	return &Proxy{
		grpcProxy,
		staticServer,
		auth,
	}, nil
}

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
			return nil, fmt.Errorf("credentials: failed to append " +
				"certificate")
		}
	}

	return cp, nil
}

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

func director(auth auth.Authenticator, services []*Service) func(req *http.Request) {
	return func(req *http.Request) {
		target, ok := matchService(req, services)
		if ok {
			req.URL.Scheme = "http"
			if target.TLSCertPath != "" {
				req.URL.Scheme = "https"
			}

			req.URL.Host = target.Address
		}
	}
}

func addCorsHeaders(header http.Header) {
	header.Add("Access-Control-Allow-Origin", "*")
	header.Add("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	header.Add("Access-Control-Allow-Headers", "Authorization, Grpc-Metadata-macaroon")
}

// ServeHTTP checks a client's headers for appropriate authorization and either
// returns a challenge or forwards their request to the target backend service.
func (g *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" &&
		(r.URL.Path == "/" || r.URL.Path == "/index.html") {
		g.staticServer.ServeHTTP(w, r)
		return
	}

	if r.Method == "OPTIONS" {
		addCorsHeaders(w.Header())
		w.WriteHeader(200)
		return
	}

	if !g.authenticator.Accept(&r.Header) {
		challengeHeader, err := g.authenticator.FreshChallengeHeader(r)
		if err != nil {
			w.WriteHeader(500)
			return
		}

		for name, value := range challengeHeader {
			w.Header().Set(name, value[0])
			for i := 1; i < len(value); i++ {
				w.Header().Add(name, value[i])
			}
		}

		w.WriteHeader(402)
		return
	}

	g.server.ServeHTTP(w, r)
}
