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

	// grpcMetadataAuthorization is a client-supplied header that grpc-gateway
	// surfaces to backends as authorization metadata, which Aperture does not
	// validate.
	grpcMetadataAuthorization = "Grpc-Metadata-Authorization"
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

	// writeDeadlineWindow, when non-zero, converts the server's absolute
	// write timeout into a rolling idle deadline for proxied responses:
	// each write to the client pushes the connection's write deadline this
	// far into the future. Without it, http.Server.WriteTimeout is a
	// single deadline armed when the response begins, and any streamed
	// response outliving it, an SSE inference stream being the canonical
	// case, is cut off mid-body no matter how healthily it is flowing.
	// With it, the timeout means what an operator almost certainly
	// intended: a stream dies when it stalls, not when it lasts.
	writeDeadlineWindow time.Duration
}

// SetWriteDeadlineWindow installs the rolling write deadline window applied
// to proxied responses. Pass the server's configured write timeout; zero
// disables the rolling extension and leaves whatever absolute deadline the
// server armed.
func (p *Proxy) SetWriteDeadlineWindow(window time.Duration) {
	p.writeDeadlineWindow = window
}

// deadlineBumpingWriter wraps a ResponseWriter so every write pushes the
// connection's write deadline forward by a fixed window. Unwrap keeps
// http.ResponseController able to reach the underlying writer's Flusher and
// deadline hooks, which the reverse proxy relies on for streaming.
type deadlineBumpingWriter struct {
	http.ResponseWriter

	// controller reaches the connection's deadline hooks through however
	// many wrappers sit between here and the real writer.
	controller *http.ResponseController

	// window is how far each write pushes the deadline into the future.
	window time.Duration

	// lastBump is when the deadline was last pushed, so the syscall runs
	// at most every quarter window rather than on every chunk.
	lastBump time.Time

	// unsupported latches once the underlying connection reports it has
	// no deadline support, so the error is not re-made on every write.
	unsupported bool
}

// bump pushes the write deadline forward, but only while the stream is
// healthy: a gap since the last write that is shorter than the window earns
// an extension, and one that exceeds the window does not.
//
// The second half is what makes the timeout still mean something. A write's
// bytes land in the server's buffer without touching the socket, so the
// deadline is only ever enforced by the flush that follows, and the flush
// runs after the write. If a write after a long stall could re-arm the
// deadline, its own flush would find a fresh deadline and succeed, and no
// stall would ever be caught. Refusing the extension leaves the expired
// deadline in force, and the flush tears the connection down exactly as the
// timeout promises.
func (d *deadlineBumpingWriter) bump() {
	if d.unsupported {
		return
	}

	now := time.Now()
	if !d.lastBump.IsZero() {
		sinceLast := now.Sub(d.lastBump)

		// Recently armed: the deadline still holds most of the
		// window, so spare the syscall.
		if sinceLast < d.window/4 {
			return
		}

		// Stalled past the window: the armed deadline has already
		// expired, and it stays expired so the flush that follows
		// this write refuses it.
		if sinceLast >= d.window {
			return
		}
	}

	if err := d.controller.SetWriteDeadline(now.Add(d.window)); err != nil {
		// A connection without deadline support gets the server's
		// absolute timeout behavior, the same as before this wrapper
		// existed.
		d.unsupported = true
		return
	}
	d.lastBump = now
}

func (d *deadlineBumpingWriter) WriteHeader(statusCode int) {
	d.bump()
	d.ResponseWriter.WriteHeader(statusCode)
}

func (d *deadlineBumpingWriter) Write(b []byte) (int, error) {
	d.bump()
	return d.ResponseWriter.Write(b)
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (d *deadlineBumpingWriter) Unwrap() http.ResponseWriter {
	return d.ResponseWriter
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
//
//nolint:gocyclo
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse and log the remote IP address. We also need the parsed IP
	// address for the freebie count.
	remoteIP, prefixLog := NewRemoteIPPrefixLog(log, r.RemoteAddr)

	// Log the request target as the client sent it, before canonicalization
	// rewrites it.
	requestURI := r.RequestURI
	logRequest := func() {
		prefixLog.Infof(formatPattern, r.Method, requestURI, r.Proto,
			r.Referer(), r.UserAgent())
	}
	defer logRequest()

	// Canonicalize the path before any component uses it for routing,
	// authentication, pricing, or rate limiting. This also ensures the
	// backend receives the same path that Aperture authorized.
	if !canonicalizeRequestPath(r) {
		addCorsHeaders(w.Header())
		sendDirectResponse(w, r, http.StatusBadRequest, "invalid path")
		return
	}

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

	// Roll the write deadline forward as the response flows, so a healthy
	// long-lived stream is not cut off by the server's absolute write
	// timeout while a stalled one still dies.
	if p.writeDeadlineWindow > 0 {
		w = &deadlineBumpingWriter{
			ResponseWriter: w,
			controller:     http.NewResponseController(w),
			window:         p.writeDeadlineWindow,
		}
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
	// so that UpdateServices does not swap or re-prepare them mid-flight.
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

	// Explicit auth off bypasses validation. Whitelisted requests only
	// validate L402 identity, without executing MPP payment actions.
	authEnabled := target.Auth.IsOn() || target.Auth.IsFreebie()
	var acceptAuth, l402Verified bool
	if authEnabled {
		acceptAuth, l402Verified = p.acceptForService(
			&r.Header, resourceName, target, authLevel.IsOff(),
		)
	}

	// A request that one credential authenticated can still carry L402s
	// that nothing verified: beside a Payment credential, or in another
	// header than the L402 that was verified. Remove them before the rate
	// limiter, the metering check and the director read them as the
	// caller's identity.
	if acceptAuth {
		removeUnverifiedL402(r.Header, l402Verified)
	}

	skipInvoiceCreation := target.SkipInvoiceCreation(r)
	switch {
	case authLevel.IsOn():
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
				if !checkRateLimit(false) {
					return
				}

				break
			}

			prefixLog.Infof("Authentication failed. Sending 402.")
			p.handlePaymentRequired(w, r, target, resourceName, price)
			return
		}

		// User is authenticated, apply rate limit with the L402 token
		// ID if an L402 is what verified, and by IP otherwise.
		// This runs before the metered check on purpose: that check
		// reserves an estimate against the token's balance, and the
		// reservation is only released by the usage report the response
		// observer sends. A request turned away here never reaches the
		// backend, so it would produce no report and leak its
		// reservation, shrinking the buyer's usable balance for nothing.
		if !checkRateLimit(l402Verified) {
			return
		}

		// For metered services, consult the pricer on every
		// authenticated request so prepaid balances are drawn down and
		// exhausted tokens are challenged afresh.
		var proceed bool
		r, proceed = p.checkMeteredAccess(w, r, target, resourceName)
		if !proceed {
			return
		}

		// An MPP session bearer request was already charged the
		// challenge's per-unit estimate by the authenticator. Annotate
		// it so the response modifier can reconcile that estimate
		// against what the request turns out to cost.
		r = p.checkSessionMetering(r, target)

		// Inject receipt headers into the request context for the
		// response modifier to pick up.
		r = p.injectReceiptContext(r, resourceName)

	case authLevel.IsFreebie():
		// We only need to respect the freebie counter if the user
		// is not authenticated at all.
		if !acceptAuth {
			// Check and consume together so concurrent requests
			// cannot claim the same remaining freebie.
			ok, err := target.freebieDB.TakeFreebie(r, remoteIP)
			if err != nil {
				prefixLog.Errorf("Error taking freebie: "+
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
					if !checkRateLimit(false) {
						return
					}

					break
				}

				p.handlePaymentRequired(
					w, r, target, resourceName, price,
				)
				return
			}

			// Unauthenticated freebie user, rate limit by IP.
			if !checkRateLimit(false) {
				return
			}
		} else {
			// Authenticated user on freebie path. Inject receipt
			// headers and apply rate limit by L402 token, if an
			// L402 is what verified.
			r = p.injectReceiptContext(r, resourceName)

			if !checkRateLimit(l402Verified) {
				return
			}
		}

	default:
		// Verified public requests use token limits. Anonymous requests
		// and explicit service-level auth off continue to use IP limits.
		if !checkRateLimit(l402Verified) {
			return
		}
	}

	if authEnabled {
		// grpc-gateway maps this alias to authorization metadata. Always
		// remove the client value because Aperture does not validate it.
		r.Header.Del(grpcMetadataAuthorization)

		// Requests admitted without authentication must not carry an
		// unverified identity to the backend.
		if !acceptAuth {
			r.Header.Del(l402.HeaderAuthorization)
			r.Header.Del(l402.HeaderMacaroon)
			r.Header.Del(l402.HeaderMacaroonMD)
		}
	}

	// If we got here, it means everything is OK to pass the request to the
	// service backend via the reverse proxy.
	p.proxyBackend.ServeHTTP(w, r)
}

// canonicalizeRequestPath rejects paths that a backend could resolve to another
// resource than the one Aperture matches, and targets that are not an absolute
// path, and removes empty and dot segments from the rest. It cleans the escaped
// path one segment at a time, so the backend receives every remaining segment
// exactly as the client escaped it, and updates the request target to match the
// cleaned path.
func canonicalizeRequestPath(req *http.Request) bool {
	// Some backends normalize a path segment before routing, dropping
	// parameters or treating an alternate character as a separator, so a
	// path containing such a character can name a different resource there
	// than the one Aperture matched. URL.Path is decoded, so this refuses
	// both the literal and the percent-encoded spellings.
	if strings.ContainsAny(req.URL.Path, ";\\") {
		return false
	}

	// Backends resolve parent segments themselves, so a path with one
	// could select a public service or whitelist entry here and reach a
	// protected resource there.
	for segment := range strings.SplitSeq(req.URL.Path, "/") {
		if segment == ".." {
			return false
		}
	}

	// A target that is not an absolute path is matched without one but
	// reaches the backend as sent, and a dynamic-price resource named after
	// it would not be the service name followed by "/". Only "OPTIONS *"
	// may name no path, and an opaque URI such as "http:x" never names one.
	// An empty path, as in "GET http://host", is forwarded as "/", so that
	// is also the path to match.
	if req.URL.Opaque != "" {
		return false
	}
	escaped := req.URL.EscapedPath()
	if escaped != "" && !strings.HasPrefix(escaped, "/") {
		return escaped == "*" && req.Method == http.MethodOptions
	}

	// Drop empty and "." segments, whether plain or escaped, and keep every
	// other segment exactly as the client escaped it: decoding an escaped
	// reserved character, such as an encoded slash, before cleaning would
	// change which resource the backend sees. A trailing slash is kept,
	// since backends and file servers treat it as naming a different
	// resource.
	var clean strings.Builder
	for segment := range strings.SplitSeq(escaped, "/") {
		name, err := url.PathUnescape(segment)
		if err != nil {
			return false
		}
		if name == "" || name == "." {
			continue
		}
		clean.WriteByte('/')
		clean.WriteString(segment)
	}
	cleanEscaped := clean.String()
	switch {
	case cleanEscaped == "":
		cleanEscaped = "/"

	case strings.HasSuffix(escaped, "/"):
		cleanEscaped += "/"
	}
	if cleanEscaped == escaped {
		return true
	}

	cleanPath, err := url.PathUnescape(cleanEscaped)
	if err != nil {
		return false
	}

	// Like net/url, set RawPath only when it differs from the default
	// encoding of Path.
	req.URL.Path = cleanPath
	req.URL.RawPath = ""
	if req.URL.EscapedPath() != cleanEscaped {
		req.URL.RawPath = cleanEscaped
	}

	// The pricer serializes the request target rather than URL.Path, so
	// it has to name the cleaned path too.
	req.RequestURI = req.URL.RequestURI()

	return true
}

// acceptForService checks authentication, respecting the service's per-service
// AuthScheme setting. If the authenticator is a MultiAuthenticator and the
// service has an AuthScheme set, only matching sub-authenticators are tried.
// Public requests only check L402 credentials.
//
// The second result reports whether an L402 is what authenticated the request.
// A request can carry credentials for several schemes, so one that another
// scheme authenticated can still carry an L402 that nothing verified.
func (p *Proxy) acceptForService(header *http.Header, resourceName string,
	target *Service, public bool) (bool, bool) {

	scheme := target.AuthScheme
	if public {
		// Machine Payments Protocol (MPP) authentication can consume a
		// payment or debit a session, so it cannot validate public requests.
		if scheme == auth.AuthSchemeMPP {
			return false, false
		}

		scheme = auth.AuthSchemeL402
		if target.DynamicPrice.Enabled {
			resourceName = publicResourceName(
				header, target.Name, resourceName,
			)
		}

		// A direct authenticator cannot select a different scheme, so
		// public requests require an L402 authenticator.
		tagged, ok := p.authenticator.(auth.SchemeTagged)
		if ok && tagged.Scheme() != auth.AuthSchemeL402 {
			return false, false
		}
	}

	multi, ok := p.authenticator.(*auth.MultiAuthenticator)
	if !ok {
		// A direct authenticator that declares another scheme cannot
		// have verified an L402.
		accepted := p.authenticator.Accept(header, resourceName)
		tagged, ok := p.authenticator.(auth.SchemeTagged)
		isL402 := !ok || tagged.Scheme() == auth.AuthSchemeL402

		return accepted, accepted && isL402
	}

	// Try L402 on its own first, which is the order the authenticators are
	// composed in anyway, so that a success here is known to be an L402.
	if scheme == "" || strings.Contains(scheme, auth.AuthSchemeL402) {
		accepted := multi.AcceptForScheme(
			header, resourceName, auth.AuthSchemeL402,
		)
		if accepted {
			return true, true
		}
	}

	// Public requests stop at L402, as does a service that only takes it.
	if public || (scheme != "" &&
		!strings.Contains(scheme, auth.AuthSchemeMPP)) {

		return false, false
	}

	accepted := multi.AcceptForScheme(
		header, resourceName, auth.AuthSchemeMPP,
	)

	return accepted, false
}

// removeUnverifiedL402 deletes the L402 credentials that did not authenticate
// the request, so a backend cannot take one for the caller's identity. When an
// L402 authenticated it, only the header field it was read from keeps L402
// values: an L402 macaroon in another macaroon header is removed even if it
// names the same token, since Aperture verified neither its signature nor its
// caveats. Otherwise every Authorization value containing an L402 and every
// L402 macaroon is removed. Values of other kinds stay, such as the Payment
// credential that authenticated the request or an lnd macaroon a backend
// checks itself.
func removeUnverifiedL402(header http.Header, l402Verified bool) {
	verified := ""
	if l402Verified {
		verified = l402.CredentialHeader(&header)
	}

	// An accepted Payment value is a single base64url token after its
	// scheme, so it never contains an L402 itself.
	if verified != l402.HeaderAuthorization {
		removeHeaderValues(
			header, l402.HeaderAuthorization, l402.ContainsCredential,
		)
	}

	for _, name := range []string{
		l402.HeaderMacaroonMD, l402.HeaderMacaroon,
	} {
		if name != verified {
			removeHeaderValues(header, name, l402.IsMacaroonCredential)
		}
	}
}

// removeHeaderValues deletes the values of a header field that match and keeps
// the others in their order.
func removeHeaderValues(header http.Header, name string,
	matches func(string) bool) {

	var kept []string
	for _, value := range header.Values(name) {
		if !matches(value) {
			kept = append(kept, value)
		}
	}

	header.Del(name)
	for _, value := range kept {
		header.Add(name, value)
	}
}

// publicResourceName returns a signed resource candidate from the same service.
// Authentication still verifies the macaroon against the returned resource
// before any identity is trusted.
//
// A dynamic-price resource is named after its service followed by the request
// path. ValidateServiceName keeps slashes out of service names, so a name that
// equals this service's name, or starts with it and a slash, cannot have been
// issued by any other service. Without that rule a token name could be
// ambiguous between this service and another, and neither this check nor the
// fallback, which is the requested resource itself, could tell them apart.
func publicResourceName(header *http.Header, serviceName,
	fallback string) string {

	mac, _, err := l402.FromHeader(header)
	if err != nil {
		return fallback
	}
	services, err := l402.ServicesFromMacaroon(mac)
	if err != nil {
		return fallback
	}

	resourcePrefix := serviceName + "/"
	for _, service := range services {
		if service.Name == serviceName ||
			strings.HasPrefix(service.Name, resourcePrefix) {

			return service.Name
		}
	}

	return fallback
}

// UpdateServices re-configures the proxy to use a new set of backend services.
func (p *Proxy) UpdateServices(services []*Service) error {
	// Hold the write lock while preparing, not just while swapping.
	// Callers such as the admin API pass back the *Service values that
	// in-flight requests are still reading, and prepareServices rewrites
	// them in place (compiled regexps, rate limiter, pricer, header map).
	p.servicesMtx.Lock()
	defer p.servicesMtx.Unlock()

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

			// For metered requests, observe the response body so
			// the resulting usage is reported to the pricer once
			// the response completes.
			attachUsageObserver(res)

			// For MPP session bearer requests, observe it so the
			// estimate deducted at request time is reconciled
			// against what the response actually cost.
			attachSessionObserver(res)

			return nil
		},

		// A backend that never produced a response skips
		// ModifyResponse, and with it the usage observer that returns
		// the reservation taken at authorization time. Release it here
		// so a backend outage or a client that hangs up early cannot
		// quietly eat into the buyer's balance.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request,
			err error) {

			log.Errorf("Error proxying request to backend: %v", err)

			if info, ok := r.Context().Value(
				meteringContextKey{},
			).(*meteringInfo); ok {
				releaseReservation(
					info, http.StatusBadGateway,
				)
			}

			w.WriteHeader(http.StatusBadGateway)
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

		// Now overwrite header fields of the client request with the
		// fields from the configuration file. This must replace rather
		// than append: the client's own Authorization (the L402 header
		// itself) would otherwise ride ahead of a configured upstream
		// credential, and backends that read only the first value
		// would reject the request.
		for name, value := range target.Headers {
			req.Header.Set(name, value)
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
//
// This runs over the backend's own response headers on the way out, and a
// backend that sets its own CORS headers, which any service that can also be
// reached directly will do, would otherwise end up with two of each. What that
// duplication means depends on the field, so the two kinds are handled
// differently.
//
// Access-Control-Allow-Origin is single-valued. Two of them is not a more
// emphatic "*", it is invalid per the fetch standard and the browser rejects
// the response outright, so aperture's view replaces whatever was there.
//
// The other three are list-based fields. Per RFC 9110 repeated field lines are
// combined with ", " and the fetch standard's "get, decode, and split" reads
// them as one list, so a duplicate there was never invalid: it merged. Which
// means replacing them would silently drop the backend's own entries, and a
// backend exposing Access-Control-Expose-Headers: X-Request-Id would find that
// header unreadable from JS the moment it moved behind aperture. So aperture's
// entries are merged into whatever the backend already asked for.
func addCorsHeaders(header http.Header) {
	log.Debugf("Adding CORS headers to response.")

	header.Set("Access-Control-Allow-Origin", "*")

	mergeCorsList(header, "Access-Control-Allow-Methods",
		"GET", "POST", "OPTIONS")
	mergeCorsList(header, "Access-Control-Expose-Headers",
		"WWW-Authenticate", "Payment-Receipt")

	// Content-Type has to be allowed for any JSON API behind the proxy. A
	// POST carrying application/json is not a simple request, so the
	// browser preflights it, and we answer that preflight ourselves
	// without ever consulting the backend.
	mergeCorsList(
		header, "Access-Control-Allow-Headers",
		"Content-Type", "Authorization", "Grpc-Metadata-macaroon",
		"WWW-Authenticate", "Payment-Receipt",
	)
}

// mergeCorsList adds the given entries to a list-based CORS header field,
// keeping whatever the backend already put there and skipping any entry it had
// already named. The result is written as a single field line, which is the
// unambiguous form: repeated lines are legal here but only because every
// reader has to join them first, and there is no reason to make them.
//
// Matching is case-insensitive because header names are, and a backend that
// wrote "content-type" means the same thing we do.
func mergeCorsList(header http.Header, field string, entries ...string) {
	var (
		merged []string
		seen   = make(map[string]struct{})
	)

	add := func(entry string) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return
		}

		key := strings.ToLower(entry)
		if _, ok := seen[key]; ok {
			return
		}

		seen[key] = struct{}{}
		merged = append(merged, entry)
	}

	// The backend's own entries come first, so a browser reading the list
	// sees them in the order that service published them.
	for _, line := range header.Values(field) {
		for _, entry := range strings.Split(line, ",") {
			add(entry)
		}
	}

	for _, entry := range entries {
		add(entry)
	}

	header.Set(field, strings.Join(merged, ", "))
}

// freshChallengeHeader mints a challenge quoting the given set of prices,
// falling back to the single-price interface for an authenticator that has no
// use for more than the one-shot charge price.
func (p *Proxy) freshChallengeHeader(serviceName string,
	prices auth.ChallengePrices) (http.Header, error) {

	if priced, ok := p.authenticator.(auth.PricedChallenger); ok {
		return priced.FreshChallengeHeaderWithPrices(
			serviceName, prices,
		)
	}

	return p.authenticator.FreshChallengeHeader(serviceName, prices.Charge)
}

// handlePaymentRequired returns fresh challenge header fields and status code
// to the client signaling that a payment is required to fulfil the request.
func (p *Proxy) handlePaymentRequired(w http.ResponseWriter, r *http.Request,
	target *Service, serviceName string, servicePrice int64) {

	// The intents a challenge can carry ask different questions of the
	// pricer. L402 and the MPP charge intent quote a one-shot purchase,
	// which on a metered service is a whole token bundle, while the MPP
	// session intent quotes what one request drawn against a prepaid
	// balance costs. Quote both, so a session's per-unit amount is not the
	// price of a bundle.
	prices := quoteSessionPrices(r, target, servicePrice)

	header, err := p.freshChallengeHeader(serviceName, prices)
	if err != nil {
		log.Errorf("Error creating new challenge header: %v", err)
		sendDirectResponse(
			w, r, http.StatusInternalServerError,
			"challenge failure",
		)
		return
	}

	// For metered services, the pricer must learn about the minted token
	// before the challenge goes out: once the client pays, the pricer is
	// the one honoring the purchased balance. If it cannot be notified,
	// the challenge must not be sent, as the client would pay for a
	// bundle the pricer will not honor.
	if err := notifyChallengeMinted(r, target, header, servicePrice); err != nil {
		log.Errorf("Error notifying pricer of minted challenge: %v",
			err)
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
	//
	// The header is parsed rather than prefix-matched, because a Payment
	// challenge need not open its field value: RFC 9110 Section 5.3 lets
	// anything on the path fold repeated header lines into one
	// comma-joined value, which leaves the challenge sitting behind
	// another scheme's. The auth-scheme token is also case-insensitive
	// per RFC 9110 Section 11.1, which a prefix match does not honor.
	challenges, parseErr := mpp.ParseChallengeHeaders(header)
	hasMPP := parseErr == nil && len(challenges) > 0

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
