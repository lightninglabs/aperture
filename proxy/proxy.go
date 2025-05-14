package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"strings"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"google.golang.org/grpc/codes"
)

const (
	// formatPattern is the pattern in which the request log will be
	// printed. This is loosely oriented on the apache log format.
	// An example entry would look like this:
	// 2019-11-09 04:07:55.072 [INF] PRXY: 66.249.69.89 - -
	// "GET /availability/v1/btc.json HTTP/1.1" "" "Mozilla/5.0 ..."
	formatPattern  = "- - \"%s %s %s\" \"%s\" \"%s\""
	hdrContentType = "Content-Type"
	hdrGrpcStatus  = "Grpc-Status"
	hdrGrpcMessage = "Grpc-Message"
	hdrTypeGrpc    = "application/grpc"
)

// LocalService is an interface that describes a service that is handled
// internally by aperture and is not proxied to another backend.
type LocalService interface {
	http.Handler

	// IsHandling returns true if the local service is handling the given
	// request. If one of the local services returns true on this method
	// then a request is not forwarded/proxied to any of the remote
	// backends.
	IsHandling(r *http.Request) bool
}

// localService is a struct that represents a service that is local to aperture
// and is not proxied to a remote backend.
type localService struct {
	handler    http.Handler
	isHandling func(r *http.Request) bool
}

// NewLocalService creates a new local service.
func NewLocalService(h http.Handler, f func(r *http.Request) bool) LocalService {
	return &localService{handler: h, isHandling: f}
}

// ServeHTTP is the http.Handler implementation.
func (l *localService) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	l.handler.ServeHTTP(rw, r)
}

// IsHandling returns true if the local service is handling the given
// request.
func (l *localService) IsHandling(r *http.Request) bool {
	return l.isHandling(r)
}

// Proxy is a HTTP, HTTP/2 and gRPC handler that takes an incoming request,
// uses its authenticator to validate the request's headers, and either returns
// a challenge to the client or forwards the request to another server and
// proxies the response back to the client.
type Proxy struct {
	proxyBackend  *httputil.ReverseProxy
	localServices []LocalService
	authenticator auth.Authenticator
	services      []*Service
	blocklist     map[string]struct{}
}

// New returns a new Proxy instance that proxies between the services specified,
// using the auth to validate each request's headers and get new challenge
// headers if necessary.
func New(auth auth.Authenticator, services []*Service,
	blocklist []string, localServices ...LocalService) (*Proxy, error) {

	blMap := make(map[string]struct{})
	for _, ip := range blocklist {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			log.Warnf("Could not parse IP %q in blocklist; skipping", ip)
			continue
		}
		blMap[parsed.String()] = struct{}{}
	}

	proxy := &Proxy{
		localServices: localServices,
		authenticator: auth,
		services:      services,
		blocklist:     blMap,
	}
	err := proxy.UpdateServices(services)
	if err != nil {
		return nil, err
	}

	return proxy, nil
}

// ServeHTTP checks a client's headers for appropriate authorization and either
// returns a challenge or forwards their request to the target backend service.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse and log the remote IP address. We also need the parsed IP
	// address for the freebie count.
	remoteIP, prefixLog := NewRemoteIPPrefixLog(log, r.RemoteAddr)
	logRequest := func() {
		prefixLog.Infof(formatPattern, r.Method, r.RequestURI, r.Proto,
			r.Referer(), r.UserAgent())
	}
	defer logRequest()

	// Blocklist check
	if _, blocked := p.blocklist[remoteIP.String()]; blocked {
		log.Debugf("Blocked request from IP: %s", remoteIP)
		addCorsHeaders(w.Header())
		sendDirectResponse(w, r, http.StatusForbidden, "access denied")
		return
	}

	// For OPTIONS requests we only need to set the CORS headers, not serve
	// any content;
	if r.Method == "OPTIONS" {
		addCorsHeaders(w.Header())
		sendDirectResponse(w, r, http.StatusOK, "")
		return
	}

	// If the request is a gRPC request, we need to set the Content-Type
	// header to application/grpc.
	if strings.HasPrefix(r.Header.Get(hdrContentType), hdrTypeGrpc) {
		w.Header().Set(hdrContentType, hdrTypeGrpc)
	}

	// Requests that can't be matched to a service backend will be
	// dispatched to the static file server. If the file exists in the
	// static file folder it will be served, otherwise the static server
	// will return a 404 for us.
	target, ok := matchService(r, p.services)
	if !ok {
		// This isn't a request for any configured remote backend that
		// we are proxying for. So we give it to the local service that
		// claims is responsible for it.
		for _, ls := range p.localServices {
			if ls.IsHandling(r) {
				prefixLog.Debugf("Dispatching request %s to "+
					"local service.", r.URL.Path)
				ls.ServeHTTP(w, r)
				return
			}
		}

		// If we get here, something is quite wrong. At least the static
		// file server should have picked up the request and serve a
		// 404 response. So nothing we can do here except returning an
		// error.
		addCorsHeaders(w.Header())
		sendDirectResponse(w, r, http.StatusInternalServerError, "")
		return
	}

	resourceName := target.ResourceName(r.URL.Path)

	// Determine auth level required to access service and dispatch request
	// accordingly.
	authLevel := target.AuthRequired(r)
	skipInvoiceCreation := target.SkipInvoiceCreation(r)
	switch {
	case authLevel.IsOn():
		// Determine if the header contains the authentication
		// required for the given resource. The call to Accept is
		// called in each case body rather than outside the switch so
		// as to avoid calling this possibly expensive call for static
		// resources.
		acceptAuth := p.authenticator.Accept(&r.Header, resourceName)
		if !acceptAuth {
			if skipInvoiceCreation {
				addCorsHeaders(w.Header())
				sendDirectResponse(
					w, r, http.StatusUnauthorized,
					"unauthorized",
				)

				return
			}

			price, err := target.pricer.GetPrice(r.Context(), r)
			if err != nil {
				prefixLog.Errorf("error getting "+
					"resource price: %v", err)
				sendDirectResponse(
					w, r, http.StatusInternalServerError,
					"failure fetching "+
						"resource price",
				)
				return
			}

			// If the price returned is zero, then break out of the
			// switch statement and allow access to the service.
			if price == 0 {
				break
			}

			prefixLog.Infof("Authentication failed. Sending 402.")
			p.handlePaymentRequired(w, r, resourceName, price)
			return
		}

	case authLevel.IsFreebie():
		// We only need to respect the freebie counter if the user
		// is not authenticated at all.
		acceptAuth := p.authenticator.Accept(&r.Header, resourceName)
		if !acceptAuth {
			ok, err := target.freebieDB.CanPass(r, remoteIP)
			if err != nil {
				prefixLog.Errorf("Error querying freebie db: "+
					"%v", err)
				sendDirectResponse(
					w, r, http.StatusInternalServerError,
					"freebie DB failure",
				)
				return
			}
			if !ok {
				price, err := target.pricer.GetPrice(
					r.Context(), r,
				)
				if err != nil {
					prefixLog.Errorf("error getting "+
						"resource price: %v", err)
					sendDirectResponse(
						w, r, http.StatusInternalServerError,
						"failure fetching "+
							"resource price",
					)
					return
				}

				// If the price returned is zero, then break
				// out of the switch statement and allow access
				// to the service.
				if price == 0 {
					break
				}

				p.handlePaymentRequired(
					w, r, resourceName, target.Price,
				)
				return
			}
			_, err = target.freebieDB.TallyFreebie(r, remoteIP)
			if err != nil {
				prefixLog.Errorf("Error updating freebie db: "+
					"%v", err)
				sendDirectResponse(
					w, r, http.StatusInternalServerError,
					"freebie DB failure",
				)
				return
			}
		}
	}

	// If we got here, it means everything is OK to pass the request to the
	// service backend via the reverse proxy.
	p.proxyBackend.ServeHTTP(w, r)
}

// UpdateServices re-configures the proxy to use a new set of backend services.
func (p *Proxy) UpdateServices(services []*Service) error {
	err := prepareServices(services)
	if err != nil {
		return err
	}

	certPool, err := certPool(services)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			RootCAs:            certPool,
			InsecureSkipVerify: true,
		},
	}

	p.proxyBackend = &httputil.ReverseProxy{
		Director:  p.director,
		Transport: &trailerFixingTransport{next: transport},
		ModifyResponse: func(res *http.Response) error {
			addCorsHeaders(res.Header)
			return nil
		},

		// A negative value means to flush immediately after each write
		// to the client.
		FlushInterval: -1,
	}

	return nil
}

// Close cleans up the Proxy by closing any remaining open connections.
func (p *Proxy) Close() error {
	var returnErr error
	for _, s := range p.services {
		if err := s.pricer.Close(); err != nil {
			log.Errorf("error while closing the pricer of "+
				"service %s: %v", s.Name, err)
			returnErr = err
		}
	}

	return returnErr
}

// director is a method that rewrites an incoming request to be forwarded to a
// backend service.
func (p *Proxy) director(req *http.Request) {
	target, ok := matchService(req, p.services)
	if ok {
		// Rewrite address and protocol in the request so the
		// real service is called instead.
		req.Host = target.Address
		req.URL.Host = target.Address
		req.URL.Scheme = target.Protocol

		// Make sure we always forward the authorization in the correct/
		// default format so the backend knows what to do with it.
		mac, preimage, err := l402.FromHeader(&req.Header)
		if err == nil {
			// It could be that there is no auth information because
			// none is needed for this particular request. So we
			// only continue if no error is set.
			err := l402.SetHeader(&req.Header, mac, preimage)
			if err != nil {
				log.Errorf("could not set header: %v", err)
			}
		}

		// Now overwrite header fields of the client request
		// with the fields from the configuration file.
		for name, value := range target.Headers {
			req.Header.Add(name, value)
		}
	}
}

// certPool builds a pool of x509 certificates from the backend services.
func certPool(services []*Service) (*x509.CertPool, error) {
	cp := x509.NewCertPool()
	for _, service := range services {
		if service.TLSCertPath == "" {
			continue
		}

		b, err := os.ReadFile(service.TLSCertPath)
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
		hostRegexp := service.compiledHostRegexp
		if !hostRegexp.MatchString(req.Host) {
			log.Tracef("Req host [%s] doesn't match [%s].",
				req.Host, hostRegexp)
			continue
		}

		if service.compiledPathRegexp == nil {
			log.Debugf("Host [%s] matched pattern [%s] and path "+
				"expression is empty. Using service [%s].",
				req.Host, hostRegexp, service.Address)
			return service, true
		}

		pathRegexp := service.compiledPathRegexp
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
	log.Debugf("No backend service matched request [%s%s].", req.Host,
		req.URL.Path)
	return nil, false
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
func (p *Proxy) handlePaymentRequired(w http.ResponseWriter, r *http.Request,
	serviceName string, servicePrice int64) {

	header, err := p.authenticator.FreshChallengeHeader(
		serviceName, servicePrice,
	)
	if err != nil {
		log.Errorf("Error creating new challenge header: %v", err)
		sendDirectResponse(
			w, r, http.StatusInternalServerError,
			"challenge failure",
		)
		return
	}

	addCorsHeaders(header)

	for name, value := range header {
		w.Header().Set(name, value[0])
		for i := 1; i < len(value); i++ {
			w.Header().Add(name, value[i])
		}
	}

	sendDirectResponse(w, r, http.StatusPaymentRequired, "payment required")
}

// sendDirectResponse sends a response directly to the client without proxying
// anything to a backend. The given error is transported in a way the client can
// understand. This means, for a gRPC client it is sent as specific header
// fields.
func sendDirectResponse(w http.ResponseWriter, r *http.Request,
	statusCode int, errInfo string) {

	// Find out if the client is a normal HTTP or a gRPC client. Every gRPC
	// request should have the Content-Type header field set accordingly
	// so we can use that.
	switch {
	case strings.HasPrefix(r.Header.Get(hdrContentType), hdrTypeGrpc):
		w.Header().Set(hdrGrpcStatus, strconv.Itoa(int(codes.Internal)))
		w.Header().Set(hdrGrpcMessage, errInfo)

		// As per the gRPC spec, we need to send a 200 OK status code
		// even if the request failed. The Grpc-Status and Grpc-Message
		// header fields are enough to inform any gRPC compliant client
		// about the error. See:
		// https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md#responses
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, errInfo, statusCode)
	}
}

type trailerFixingTransport struct {
	next http.RoundTripper
}

// RoundTrip is a transport round tripper implementation that fixes an issue
// in the official httputil.ReverseProxy implementation. Apparently the HTTP/2
// trailers aren't properly forwarded in some cases. We fix this by always
// copying the Grpc-Status and Grpc-Message fields to the trailers, as those are
// usually expected to be in the trailer fields.
// Inspired by https://github.com/elazarl/goproxy/issues/408.
func (l *trailerFixingTransport) RoundTrip(req *http.Request) (*http.Response,
	error) {

	resp, err := l.next.RoundTrip(req)
	if resp != nil && len(resp.Trailer) == 0 {
		if len(resp.Header.Values(hdrGrpcStatus)) > 0 {
			resp.Trailer = make(http.Header)
			grpcStatus := resp.Header.Get(hdrGrpcStatus)
			grpcMessage := resp.Header.Get(hdrGrpcMessage)
			resp.Trailer.Add(hdrGrpcStatus, grpcStatus)
			resp.Trailer.Add(hdrGrpcMessage, grpcMessage)
		}
	}
	return resp, err
}
