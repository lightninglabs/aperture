package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/pricer"
	"gopkg.in/macaroon.v2"
)

// meteringContextKey is the request context key under which the metering
// information for an authorized request is stored, so the response modifier
// can attach the usage observer to the response body.
type meteringContextKey struct{}

// meteringInfo carries the state the response modifier needs to report the
// usage of a completed response back to the metered pricer.
type meteringInfo struct {
	// tokenID is the hex-encoded L402 token ID the request authenticated
	// with.
	tokenID string

	// serviceName is the name of the aperture service.
	serviceName string

	// path is the URL path of the request.
	path string

	// pricer is the metered pricer to report usage to.
	pricer pricer.MeteredPricer

	// tailBytes is the maximum number of trailing response body bytes to
	// capture for the usage report.
	tailBytes int
}

const (
	// reportTimeout is the maximum time a single usage report RPC may take.
	// Reports run detached from the request context, which is already done
	// by the time the response body has been fully copied.
	reportTimeout = 30 * time.Second

	// reportMaxAttempts is the number of times a usage report is attempted
	// before giving up. A failed report is silent revenue loss, so the
	// report is retried with backoff to narrow the window in which a
	// transient pricer blip drops a debit.
	reportMaxAttempts = 4
)

// reportInitialBackoff is the delay before the first retry of a failed usage
// report. It doubles on each subsequent attempt. It is a variable so tests can
// shrink it.
var reportInitialBackoff = 500 * time.Millisecond

// challengeMacaroonRegex extracts the base64-encoded macaroon from an L402
// WWW-Authenticate challenge header value.
var challengeMacaroonRegex = regexp.MustCompile(`macaroon="([^"]+)"`)

// meteredPricer returns the service's pricer as a MeteredPricer if metering
// is enabled and supported for the service.
func (s *Service) meteredPricer() (pricer.MeteredPricer, bool) {
	if !s.DynamicPrice.Enabled || !s.DynamicPrice.Metered {
		return nil, false
	}

	mp, ok := s.pricer.(pricer.MeteredPricer)

	return mp, ok
}

// usageTailBytes returns the configured cap for the captured response body
// tail of the service.
func (s *Service) usageTailBytes() int {
	if s.DynamicPrice.UsageTailBytes > 0 {
		return s.DynamicPrice.UsageTailBytes
	}

	return pricer.DefaultUsageTailBytes
}

// hasL402SchemeHeader reports whether the request carries an Authorization
// header value using the L402 (or legacy LSAT) scheme. This distinguishes a
// request that is simply not L402-authenticated (for example an MPP session)
// from one that presents a malformed L402 token.
func hasL402SchemeHeader(header *http.Header) bool {
	for _, value := range header.Values(l402.HeaderAuthorization) {
		if strings.HasPrefix(value, "L402 ") ||
			strings.HasPrefix(value, "LSAT ") {

			return true
		}
	}

	return false
}

// l402TokenIDFromAuthHeader extracts the L402 token ID from the Authorization
// header of a request that already passed authentication.
func l402TokenIDFromAuthHeader(header *http.Header) (string, error) {
	mac, _, err := l402.FromHeader(header)
	if err != nil {
		return "", err
	}

	identifier, err := l402.DecodeIdentifier(bytes.NewBuffer(mac.Id()))
	if err != nil {
		return "", err
	}

	return identifier.TokenID.String(), nil
}

// l402TokenIDFromChallengeHeader extracts the L402 token ID from the macaroon
// embedded in a freshly minted WWW-Authenticate challenge header.
func l402TokenIDFromChallengeHeader(header http.Header) (string, error) {
	for _, value := range header.Values("WWW-Authenticate") {
		matches := challengeMacaroonRegex.FindStringSubmatch(value)
		if len(matches) != 2 {
			continue
		}

		macBytes, err := base64.StdEncoding.DecodeString(matches[1])
		if err != nil {
			return "", fmt.Errorf("error decoding challenge "+
				"macaroon: %w", err)
		}

		mac := &macaroon.Macaroon{}
		if err := mac.UnmarshalBinary(macBytes); err != nil {
			return "", fmt.Errorf("error unmarshaling challenge "+
				"macaroon: %w", err)
		}

		identifier, err := l402.DecodeIdentifier(
			bytes.NewBuffer(mac.Id()),
		)
		if err != nil {
			return "", fmt.Errorf("error decoding challenge "+
				"macaroon identifier: %w", err)
		}

		return identifier.TokenID.String(), nil
	}

	return "", fmt.Errorf("no macaroon found in challenge header")
}

// checkMeteredAccess consults the metered pricer for an authenticated request
// to a metered service. It returns the (possibly annotated) request and true
// if the request may proceed to the backend. If it returns false, a response
// has already been written: either a fresh 402 challenge because the token's
// balance is exhausted, or an error response.
func (p *Proxy) checkMeteredAccess(w http.ResponseWriter, r *http.Request,
	target *Service, resourceName string) (*http.Request, bool) {

	mp, ok := target.meteredPricer()
	if !ok {
		return r, true
	}

	// Only requests carrying an L402 token are metered through the
	// pricer. Requests authenticated through other schemes (for example
	// MPP sessions) have their own draw-down accounting.
	tokenID, err := l402TokenIDFromAuthHeader(&r.Header)
	if err != nil {
		// A request with no L402-scheme header at all simply is not
		// metered here and passes through. But a header that does carry
		// the L402 scheme yet fails to parse must not silently become
		// free unmetered access on a metered service.
		if hasL402SchemeHeader(&r.Header) {
			log.Errorf("Metered request carries an unparseable "+
				"L402 token: %v", err)
			sendDirectResponse(
				w, r, http.StatusInternalServerError,
				"malformed L402 token",
			)

			return r, false
		}

		log.Tracef("Metering skipped, no L402 token on request: %v",
			err)

		return r, true
	}

	result, err := mp.AuthorizeRequest(
		r.Context(), r, tokenID, target.Name,
	)
	if err != nil {
		log.Errorf("Error authorizing metered request for token "+
			"%s: %v", tokenID, err)
		sendDirectResponse(
			w, r, http.StatusInternalServerError,
			"failure authorizing metered request",
		)

		return r, false
	}

	if !result.Allowed {
		log.Debugf("Metered pricer denied request for token %s: %s",
			tokenID, result.Reason)

		// The token's balance is exhausted, so a fresh challenge is
		// minted for the client to purchase a new bundle. If the
		// pricer did not include a price, fall back to a regular
		// price query.
		price := result.PriceSats
		if price == 0 {
			price, err = target.pricer.GetPrice(r.Context(), r)
			if err != nil {
				log.Errorf("Error getting resource price: %v",
					err)
				sendDirectResponse(
					w, r,
					http.StatusInternalServerError,
					"failure fetching resource price",
				)

				return r, false
			}
		}

		p.handlePaymentRequired(w, r, target, resourceName, price)

		return r, false
	}

	// Strip the client's Accept-Encoding so the upstream response is
	// observed as plaintext. If the client's Accept-Encoding were
	// forwarded, the backend could return a gzip body, the usage tail
	// would be compressed bytes with no parseable usage object, and the
	// bundle would never be debited: unlimited free inference. With the
	// header removed, Go's http.Transport adds its own Accept-Encoding:
	// gzip and transparently decompresses, so res.Body yields plaintext.
	r.Header.Del("Accept-Encoding")

	// The request may proceed. Annotate it so the response modifier
	// reports the resulting usage back to the pricer.
	info := &meteringInfo{
		tokenID:     tokenID,
		serviceName: target.Name,
		path:        r.URL.Path,
		pricer:      mp,
		tailBytes:   target.usageTailBytes(),
	}
	ctx := context.WithValue(r.Context(), meteringContextKey{}, info)

	return r.WithContext(ctx), true
}

// notifyChallengeMinted informs the metered pricer about a freshly minted
// challenge so the pricer can associate the token, once paid, with the
// purchased balance. It returns an error if the pricer could not be notified,
// in which case the challenge must not be sent to the client: the client
// would pay for a bundle the pricer will not honor.
func notifyChallengeMinted(r *http.Request, target *Service,
	header http.Header, price int64) error {

	mp, ok := target.meteredPricer()
	if !ok {
		return nil
	}

	tokenID, err := l402TokenIDFromChallengeHeader(header)
	if err != nil {
		return err
	}

	err = mp.ChallengeMinted(r.Context(), r, tokenID, target.Name, price)
	if err != nil {
		return fmt.Errorf("error notifying pricer of minted "+
			"challenge for token %s: %w", tokenID, err)
	}

	return nil
}

// attachUsageObserver wraps the response body of a metered request so the
// usage is reported to the pricer once the body has been fully copied to the
// client (or the copy was aborted).
func attachUsageObserver(res *http.Response) {
	if res.Request == nil {
		return
	}

	info, ok := res.Request.Context().Value(
		meteringContextKey{},
	).(*meteringInfo)
	if !ok {
		return
	}

	// Hijacked protocol upgrades bypass the regular body copy, so there
	// is no meaningful usage to observe.
	if res.StatusCode == http.StatusSwitchingProtocols {
		return
	}

	res.Body = &usageObservingBody{
		inner: res.Body,
		info:  info,
		tail:  newTailBuffer(info.tailBytes),
		usage: pricer.Usage{
			TokenID:         info.tokenID,
			Path:            info.path,
			ServiceName:     info.serviceName,
			HTTPStatus:      res.StatusCode,
			ContentType:     res.Header.Get("Content-Type"),
			ContentEncoding: res.Header.Get("Content-Encoding"),
		},
	}
}

// usageObservingBody wraps a response body, captures a bounded tail of the
// bytes flowing through it and reports the usage to the metered pricer
// exactly once, when the body is exhausted or closed.
type usageObservingBody struct {
	inner io.ReadCloser
	info  *meteringInfo
	tail  *tailBuffer
	usage pricer.Usage

	reportOnce sync.Once
}

// Read passes through to the wrapped body while capturing the tail. On EOF
// the usage is reported as complete.
func (b *usageObservingBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if n > 0 {
		b.tail.Write(p[:n])
	}

	if err == io.EOF {
		b.report(true)
	}

	return n, err
}

// Close closes the wrapped body. If the body was not read to EOF first, the
// usage is reported as incomplete, for example when the client disconnected
// mid-stream.
func (b *usageObservingBody) Close() error {
	err := b.inner.Close()

	b.report(false)

	return err
}

// report sends the usage report to the pricer exactly once, detached from the
// request context.
func (b *usageObservingBody) report(complete bool) {
	b.reportOnce.Do(func() {
		usage := b.usage
		usage.Complete = complete
		usage.ResponseTail = b.tail.Bytes()

		go reportUsageWithRetry(b.info.pricer, &usage)
	})
}

// reportUsageWithRetry reports usage to the pricer, retrying with exponential
// backoff on failure. A report that never succeeds is silent revenue loss, so
// the final failure is logged loudly. A durable, un-acked-report queue is the
// real fix and is left as a follow-up; the bounded retry here only narrows the
// window in which a transient pricer failure drops a debit.
func reportUsageWithRetry(mp pricer.MeteredPricer, usage *pricer.Usage) {
	backoff := reportInitialBackoff

	var err error
	for attempt := 1; attempt <= reportMaxAttempts; attempt++ {
		func() {
			ctx, cancel := context.WithTimeout(
				context.Background(), reportTimeout,
			)
			defer cancel()

			err = mp.ReportUsage(ctx, usage)
		}()
		if err == nil {
			return
		}

		log.Warnf("Usage report for token %s failed on attempt "+
			"%d/%d: %v", usage.TokenID, attempt,
			reportMaxAttempts, err)

		if attempt < reportMaxAttempts {
			time.Sleep(backoff)
			backoff *= 2
		}
	}

	log.Errorf("Giving up reporting usage for token %s after %d "+
		"attempts, this debit is lost: %v", usage.TokenID,
		reportMaxAttempts, err)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	buf []byte
	max int
}

// newTailBuffer creates a tail buffer capped at max bytes.
func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

// Write appends p to the buffer, discarding the oldest bytes once the cap is
// exceeded.
func (t *tailBuffer) Write(p []byte) {
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return
	}

	if overflow := len(t.buf) + len(p) - t.max; overflow > 0 {
		t.buf = append(t.buf[:0], t.buf[overflow:]...)
	}

	t.buf = append(t.buf, p...)
}

// Bytes returns the captured tail.
func (t *tailBuffer) Bytes() []byte {
	return t.buf
}
