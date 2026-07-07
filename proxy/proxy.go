package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mpp"
	"google.golang.org/grpc/codes"
)

// receiptContextKey is the context key used to pass Payment-Receipt headers
// from the authentication check to the response modifier.
type receiptContextKey struct{}

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
	// servicesMtx protects services and proxyBackend from concurrent
	// access during dynamic updates via UpdateServices.
	servicesMtx  sync.RWMutex
	proxyBackend *httputil.ReverseProxy

	// priorityLocalServices are checked before proxy service matching.
	// Use this for local endpoints that must not be intercepted by
	// broad proxy path patterns.
	priorityLocalServices []LocalService

	// localServices are checked after proxy service matching fails.
	// The static file server is typically the catch-all here.
	localServices []LocalService

	authenticator auth.Authenticator
	services      []*Service
	blocklist     map[string]struct{}
}

// rewriteRequestPath rewrites the request path according to service config.
// It prepends the configured prefix to the request path, preserving any
// percent-encoded characters in the original request via url.URL.JoinPath.
//
// NOTE: This does not rewrite Location headers in backend responses. If the
// backend returns redirects containing the prefixed path, clients will see the
// internal prefixed path. This is a known limitation.
func (s *Service) rewriteRequestPath(req *http.Request) {
	prefix := s.Rewrite.Prefix
	if prefix == "" {
		return
	}

	// Build a URL from the prefix so we can use URL.JoinPath, which
	// correctly handles RawPath and percent-encoded characters (e.g.,
	// %2F) that the backend may rely on for routing.
	prefixURL := &url.URL{Path: prefix}

	// Use EscapedPath() to preserve percent-encoded characters from the
	// original request. URL.JoinPath will propagate these into the
	// result's RawPath automatically.
	result := prefixURL.JoinPath(req.URL.EscapedPath())

	req.URL.Path = result.Path
	req.URL.RawPath = result.RawPath
}

// New returns a new Proxy instance that proxies between the services specified,
// using the auth to validate each request's headers and get new challenge
// headers if necessary.
func New(auth auth.Authenticator, services []*Service,
	blocklist []string, priorityLocalServices []LocalService,
	localServices ...LocalService) (*Proxy, error) {

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
		priorityLocalServices: priorityLocalServices,
		localServices:         localServices,
		authenticator:         auth,
		services:              services,
		blocklist:             blMap,
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

	// Priority local services are checked before proxy service matching
	// so that endpoints like the admin API are not intercepted by broad
	// proxy path patterns (e.g. "^/api/.*$").
	for _, ls := range p.priorityLocalServices {
		if ls.IsHandling(r) {
			prefixLog.Debugf("Dispatching request %s to "+
				"priority local service.", r.URL.Path)
			ls.ServeHTTP(w, r)
			return
		}
	}

	// Take a read lock to get a consistent snapshot of services and the
	// proxy backend. This is held for the duration of request handling
	// so that UpdateServices does not swap them mid-flight.
	p.servicesMtx.RLock()
	defer p.servicesMtx.RUnlock()

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

	// checkRateLimit is a helper that checks rate limits after determining
	// the authentication status. This ensures we only use L402 token IDs
	// for authenticated requests, preventing DoS via garbage tokens.
	checkRateLimit := func(authenticated bool) bool {
		if target.rateLimiter == nil {
			return true
		}
		key := ExtractRateLimitKey(r, remoteIP, authenticated)
		allowed, retryAfter := target.rateLimiter.Allow(r, key)
		if !allowed {
			prefixLog.Infof("Rate limit exceeded for key %s, "+
				"retry after %v", key, retryAfter)
			addCorsHeaders(w.Header())
			sendRateLimitResponse(w, r, retryAfter)
		}

		return allowed
	}

	skipInvoiceCreation := target.SkipInvoiceCreation(r)
	switch {
	case authLevel.IsOn():
		// Determine if the header contains the authentication
		// required for the given resource. The call to Accept is
		// called in each case body rather than outside the switch so
		// as to avoid calling this possibly expensive call for static
		// resources. If the service specifies an auth scheme, only
		// authenticators matching that scheme are tried.
		acceptAuth := p.acceptForService(
			&r.Header, resourceName, target,
		)
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

		// Inject receipt headers into the request context for the
		// response modifier to pick up.
		r = p.injectReceiptContext(r, resourceName)

		// User is authenticated, apply rate limit with L402 token ID.
		if !checkRateLimit(true) {
			return
		}

	case authLevel.IsFreebie():
		// We only need to respect the freebie counter if the user
		// is not authenticated at all.
		acceptAuth := p.acceptForService(
			&r.Header, resourceName, target,
		)
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

			// Unauthenticated freebie user, rate limit by IP.
			if !checkRateLimit(false) {
				return
			}
		} else {
			// Authenticated user on freebie path. Inject receipt
			// headers and apply rate limit by L402 token.
			r = p.injectReceiptContext(r, resourceName)

			if !checkRateLimit(true) {
				return
			}
		}

	default:
		// Auth is off, rate limit by IP for unauthenticated access.
		if !checkRateLimit(false) {
			return
		}
	}

	// If we got here, it means everything is OK to pass the request to the
	// service backend via the reverse proxy.
	p.proxyBackend.ServeHTTP(w, r)
}

// acceptForService checks authentication, respecting the service's per-service
// AuthScheme setting. If the authenticator is a MultiAuthenticator and the
// service has an AuthScheme set, only matching sub-authenticators are tried.
func (p *Proxy) acceptForService(header *http.Header, resourceName string,
	target *Service) bool {

	multi, ok := p.authenticator.(*auth.MultiAuthenticator)
	if ok && target.AuthScheme != "" {
		return multi.AcceptForScheme(
			header, resourceName, target.AuthScheme,
		)
	}

	return p.authenticator.Accept(header, resourceName)
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

	p.servicesMtx.Lock()
	defer p.servicesMtx.Unlock()

	p.services = services

	p.proxyBackend = &httputil.ReverseProxy{
		Director:  p.director,
		Transport: &trailerFixingTransport{next: transport},
		ModifyResponse: func(res *http.Response) error {
			addCorsHeaders(res.Header)

			// Inject Payment-Receipt headers if present in the
			// request context. Per the MPP spec, responses with
			// Payment-Receipt must include Cache-Control: private
			// to prevent shared caches from storing receipts.
			if res.Request != nil {
				if receiptHdr, ok := res.Request.Context().Value(
					receiptContextKey{},
				).(http.Header); ok {
					for k, vals := range receiptHdr {
						for _, v := range vals {
							res.Header.Add(k, v)
						}
					}
					res.Header.Set(
						"Cache-Control", "private",
					)
				}
			}

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
	p.servicesMtx.RLock()
	defer p.servicesMtx.RUnlock()

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

// injectReceiptContext checks if the authenticator implements ReceiptProvider
// and stores receipt headers in the request context for the response modifier.
func (p *Proxy) injectReceiptContext(r *http.Request,
	resourceName string) *http.Request {

	rp, ok := p.authenticator.(auth.ReceiptProvider)
	if !ok {
		return r
	}

	receiptHdr := rp.ReceiptHeader(&r.Header, resourceName)
	if receiptHdr == nil {
		return r
	}

	ctx := context.WithValue(r.Context(), receiptContextKey{}, receiptHdr)
	return r.WithContext(ctx)
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

		target.rewriteRequestPath(req)

		// Make sure we always forward the authorization in the
		// correct/default format so the backend knows what to do
		// with it. For MPP Payment credentials, the header is
		// already in the correct format and doesn't need rewriting.
		mac, preimage, discharges, err := l402.FromHeader(&req.Header)
		if err == nil {
			// It could be that there is no auth information because
			// none is needed for this particular request. So we
			// only continue if no error is set.
			err := l402.SetHeader(
				&req.Header, mac, preimage, discharges,
			)
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
	header.Add("Access-Control-Expose-Headers",
		"WWW-Authenticate, Payment-Receipt")
	header.Add(
		"Access-Control-Allow-Headers",
		"Authorization, Grpc-Metadata-macaroon, "+
			"WWW-Authenticate, Payment-Receipt",
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

	// Set Cache-Control: no-store per the Payment HTTP Authentication
	// Scheme spec to prevent caching of challenge responses.
	header.Set("Cache-Control", "no-store")

	for name, value := range header {
		w.Header().Set(name, value[0])
		for i := 1; i < len(value); i++ {
			w.Header().Add(name, value[i])
		}
	}

	// Check if a Payment scheme challenge is present. If so, use RFC
	// 9457 Problem Details JSON in the response body per the MPP spec.
	hasMPP := false
	for _, v := range header.Values("WWW-Authenticate") {
		if strings.HasPrefix(v, mpp.AuthScheme+" ") {
			hasMPP = true
			break
		}
	}

	if hasMPP {
		w.Header().Set("Content-Type", mpp.ProblemContentType)
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write(mpp.PaymentRequiredProblem()) //nolint:errcheck
	} else {
		sendDirectResponse(
			w, r, http.StatusPaymentRequired,
			"payment required",
		)
	}
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

// sendRateLimitResponse sends a rate limit exceeded response to the client.
// For HTTP clients, it returns 429 Too Many Requests with Retry-After header.
// For gRPC clients, it returns a ResourceExhausted status.
func sendRateLimitResponse(w http.ResponseWriter, r *http.Request,
	retryAfter time.Duration) {

	// Round up to ensure clients don't retry before the limit resets.
	retrySeconds := int(math.Ceil(retryAfter.Seconds()))
	if retrySeconds < 1 {
		retrySeconds = 1
	}

	// Set Retry-After header for both HTTP and gRPC.
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))

	// Check if this is a gRPC request.
	if strings.HasPrefix(r.Header.Get(hdrContentType), hdrTypeGrpc) {
		w.Header().Set(
			hdrGrpcStatus,
			strconv.Itoa(int(codes.ResourceExhausted)),
		)
		w.Header().Set(hdrGrpcMessage, "rate limit exceeded")

		// gRPC requires 200 OK even for errors.
		w.WriteHeader(http.StatusOK)
	} else {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
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
