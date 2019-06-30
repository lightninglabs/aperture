package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"

	"github.com/lightninglabs/kirin/auth"
)

// Proxy is a HTTP, HTTP/2 and gRPC handler that takes an incoming request,
// uses its authenticator to validate the request's headers, and either returns
// a challenge to the client or forwards the request to another server and
// proxies the response back to the client.
type Proxy struct {
	server *httputil.ReverseProxy

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
	nameToService := formatServices(services)
	grpcProxy := &httputil.ReverseProxy{
		Director:      director(auth, nameToService),
		Transport:     transport,
		FlushInterval: -1,
	}

	return &Proxy{
		grpcProxy,
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

func formatServices(servicesList []*Service) map[string]*Service {
	services := make(map[string]*Service)
	for _, service := range servicesList {
		services[service.FQDN] = service
	}

	return services
}

func director(auth auth.Authenticator, services map[string]*Service) func(req *http.Request) {
	return func(req *http.Request) {
		target := services[req.Host]
		if target != nil {
			req.URL.Scheme = "http"
			if target.TLSCertPath != "" {
				req.URL.Scheme = "https"
			}

			req.URL.Host = target.Address
		}
	}
}

// ServeHTTP checks a client's headers for appropriate authorization and either
// returns a challenge or forwards their request to the target backend service.
func (g *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
