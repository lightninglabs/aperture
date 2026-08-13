package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

// errNoMeteredCredential marks a request that carries no credential the
// metering pipeline can attribute usage to. Such a request is simply not
// metered here: it is either unauthenticated, or authenticated under a scheme
// with its own draw-down accounting, like an MPP session.
var errNoMeteredCredential = errors.New("no meterable credential on request")

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

	// reservedEstimate is the token estimate the pricer reserved when it
	// authorized this request, echoed back in the usage report so the
	// pricer can release the exact reservation.
	reservedEstimate int64
}

const (
	// reportTimeout is the maximum time a single usage report RPC may take.
	// Reports run detached from the request context, which is already done
	// by the time the response body has been fully copied.
	reportTimeout = 30 * time.Second
)

// reportSchedule bounds the retry behavior of a usage report: how many times
// it is attempted and how long it initially backs off between attempts. It is
// plumbed explicitly from usageObservingBody rather than read from mutable
// package state, so tests can inject a fast schedule without racing the
// detached report goroutine of a previous test.
type reportSchedule struct {
	// maxAttempts is the number of times a usage report is attempted
	// before giving up. A failed report is silent revenue loss, so the
	// report is retried with backoff to narrow the window in which a
	// transient pricer blip drops a debit.
	maxAttempts int

	// initialBackoff is the delay before the first retry of a failed
	// usage report. It doubles on each subsequent attempt.
	initialBackoff time.Duration
}

// defaultReportSchedule is the retry schedule used in production.
var defaultReportSchedule = reportSchedule{
	maxAttempts:    4,
	initialBackoff: 500 * time.Millisecond,
}

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

// hasPaymentSchemeHeader reports whether the request carries an Authorization
// header value using the Payment (MPP) scheme. RFC 9110 makes the auth-scheme
// token case-insensitive, so match it that way.
func hasPaymentSchemeHeader(header *http.Header) bool {
	for _, value := range header.Values(l402.HeaderAuthorization) {
		scheme, _, _ := strings.Cut(value, " ")
		if strings.EqualFold(scheme, mpp.AuthScheme) {
			return true
		}
	}

	return false
}

// mppChargeTokenIDFromAuthHeader extracts the metering token ID from a
// Payment charge credential on a request that already passed authentication.
//
// The ID is the hex payment hash of the invoice the credential paid, derived
// from the preimage in the payload. That is the same value the challenge's
// request carried in methodDetails.paymentHash and the same key the bundle
// was booked under at mint time, so a charge presented through the Payment
// door draws down the very bundle its payment purchased. Deriving it from the
// preimage rather than trusting the echoed request means the ID is backed by
// the proof of payment itself; the authenticator has already checked the two
// agree.
//
// A session credential yields errNoMeteredCredential: sessions have their own
// draw-down accounting through the session pricer and must not be double
// metered here.
func mppChargeTokenIDFromAuthHeader(header *http.Header) (string, error) {
	cred, err := mpp.ParseCredential(header)
	if err != nil {
		return "", fmt.Errorf("parsing payment credential: %w", err)
	}

	if cred.Challenge.Intent != mpp.IntentCharge {
		return "", fmt.Errorf("%w: payment intent %q has its own "+
			"accounting", errNoMeteredCredential,
			cred.Challenge.Intent)
	}

	var payload mpp.ChargePayload
	if err := json.Unmarshal(cred.Payload, &payload); err != nil {
		return "", fmt.Errorf("decoding charge payload: %w", err)
	}

	preimage, err := lntypes.MakePreimageFromStr(payload.Preimage)
	if err != nil {
		return "", fmt.Errorf("invalid charge preimage: %w", err)
	}

	return preimage.Hash().String(), nil
}

// hasL402CredentialForm reports whether the request presents an L402
// credential in any of the forms l402.FromHeader accepts: the Authorization
// header's L402 or LSAT scheme, or a macaroon carried directly in the
// Macaroon or Grpc-Metadata-Macaroon header. The presence test has to cover
// all three, because the authenticator does: a token that authenticates
// through one of the alternate headers but is invisible to metering would be
// unlimited unmetered access on a metered service.
func hasL402CredentialForm(header *http.Header) bool {
	if hasL402SchemeHeader(header) {
		return true
	}

	return header.Get(l402.HeaderMacaroon) != "" ||
		header.Get(l402.HeaderMacaroonMD) != ""
}

// meteringTokenIDFromAuthHeader resolves the token ID a request's usage is
// metered under, from whichever credential scheme the request authenticated
// with. A request that carries no meterable credential at all returns
// errNoMeteredCredential; a request whose credential fails to parse returns
// a hard error, since a malformed credential on a metered service must not
// silently become free unmetered access.
//
// The L402 parse is attempted first and unconditionally, mirroring the
// authenticator: l402.FromHeader reads the token from the Authorization
// header or from the Macaroon and Grpc-Metadata-Macaroon headers, so gating
// the parse on the Authorization scheme alone would let a paid token
// presented through an alternate header authenticate and then walk past
// metering. A Payment credential is consulted next, so a request that
// genuinely authenticated through the Payment door is metered under it even
// when a stray unparseable L402 header rides along.
func meteringTokenIDFromAuthHeader(header *http.Header) (string, error) {
	tokenID, l402Err := l402TokenIDFromAuthHeader(header)
	if l402Err == nil {
		return tokenID, nil
	}

	if hasPaymentSchemeHeader(header) {
		return mppChargeTokenIDFromAuthHeader(header)
	}

	if hasL402CredentialForm(header) {
		return "", fmt.Errorf("unparseable L402 credential: %w",
			l402Err)
	}

	return "", errNoMeteredCredential
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

	// Requests carrying an L402 token or a Payment (MPP) charge
	// credential are metered through the pricer. Requests authenticated
	// through other schemes (for example MPP sessions) have their own
	// draw-down accounting.
	tokenID, err := meteringTokenIDFromAuthHeader(&r.Header)
	switch {
	// A request with no meterable credential at all simply is not metered
	// here and passes through.
	case errors.Is(err, errNoMeteredCredential):
		log.Tracef("Metering skipped: %v", err)

		return r, true

	// A header that does carry a meterable scheme yet fails to parse must
	// not silently become free unmetered access on a metered service.
	case err != nil:
		log.Errorf("Metered request carries an unparseable "+
			"credential: %v", err)
		sendDirectResponse(
			w, r, http.StatusInternalServerError,
			"malformed credential",
		)

		return r, false
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
		tokenID:          tokenID,
		serviceName:      target.Name,
		path:             r.URL.Path,
		pricer:           mp,
		tailBytes:        target.usageTailBytes(),
		reservedEstimate: result.ReservedEstimate,
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

	// The same 402 may also carry Payment (MPP) charge offers, each minted
	// from its own invoice. Book those too, keyed by the invoice's payment
	// hash, which is the identity a charge credential proves with its
	// preimage at request time. Whichever offer the client pays is the
	// bundle that gets drawn down; the sibling bookings are never
	// activated and expire unused, which the pricer already handles.
	for _, hash := range mppChargeHashesFromChallengeHeader(header) {
		err = mp.ChallengeMinted(r.Context(), r, hash, target.Name,
			price)
		if err != nil {
			return fmt.Errorf("error notifying pricer of minted "+
				"payment challenge for token %s: %w", hash,
				err)
		}
	}

	return nil
}

// mppChargeHashesFromChallengeHeader extracts the payment hashes of every
// well-formed Payment charge offer in a freshly minted WWW-Authenticate
// header. A header with no Payment offers, which is every header when MPP is
// disabled, yields an empty slice.
func mppChargeHashesFromChallengeHeader(header http.Header) []string {
	challenges, err := mpp.ParseChallengeHeaders(header)
	if err != nil {
		// No Payment offer in the header at all.
		return nil
	}

	var hashes []string
	for _, challenge := range challenges {
		if challenge.Intent != mpp.IntentCharge {
			continue
		}

		var chargeReq mpp.ChargeRequest
		err := mpp.DecodeRequest(challenge.Request, &chargeReq)
		if err != nil {
			log.Warnf("Skipping metering booking for undecodable "+
				"payment charge offer: %v", err)
			continue
		}

		// Normalize through lntypes so the booked key is byte for
		// byte the one the credential's preimage will derive.
		hash, err := lntypes.MakeHashFromStr(
			chargeReq.MethodDetails.PaymentHash,
		)
		if err != nil {
			log.Warnf("Skipping metering booking for payment "+
				"charge offer with invalid payment hash: %v",
				err)
			continue
		}

		hashes = append(hashes, hash.String())
	}

	return hashes
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

	// Hijacked protocol upgrades bypass the regular body copy, so there is
	// no meaningful usage to observe. The reservation still has to come
	// back, though, so report an empty, incomplete usage rather than
	// dropping it.
	if res.StatusCode == http.StatusSwitchingProtocols {
		releaseReservation(info, res.StatusCode)
		return
	}

	res.Body = &usageObservingBody{
		inner:    res.Body,
		info:     info,
		tail:     newTailBuffer(info.tailBytes),
		schedule: defaultReportSchedule,
		usage: pricer.Usage{
			TokenID:          info.tokenID,
			Path:             info.path,
			ServiceName:      info.serviceName,
			HTTPStatus:       res.StatusCode,
			ContentType:      res.Header.Get("Content-Type"),
			ContentEncoding:  res.Header.Get("Content-Encoding"),
			ReservedEstimate: info.reservedEstimate,
		},
	}
}

// releaseReservation reports an empty, incomplete usage for a metered request
// that will never produce a response body to observe.
//
// AuthorizeRequest reserves a token estimate against the buyer's balance, and
// the only thing that gives it back is the usage report carrying the echoed
// reservation. Any path that authorizes a request and then abandons it, a
// transport failure, a client that disconnects before response headers, a
// protocol upgrade, would otherwise strand that reservation: the pricer holds
// no timer, so the tokens stay reserved until it restarts. Enough of those and
// a bundle with balance left reads as exhausted and the buyer re-buys one.
//
// The report carries no body, so the pricer debits nothing and releases the
// reservation in full, which is the right outcome for a request the backend
// never served.
func releaseReservation(info *meteringInfo, httpStatus int) {
	if info == nil || info.pricer == nil || info.reservedEstimate == 0 {
		return
	}

	usage := &pricer.Usage{
		TokenID:          info.tokenID,
		Path:             info.path,
		ServiceName:      info.serviceName,
		HTTPStatus:       httpStatus,
		Complete:         false,
		ReservedEstimate: info.reservedEstimate,
	}

	go reportUsageWithRetry(info.pricer, usage, defaultReportSchedule)
}

// usageObservingBody wraps a response body, captures a bounded tail of the
// bytes flowing through it and reports the usage to the metered pricer
// exactly once, when the body is exhausted or closed.
type usageObservingBody struct {
	inner    io.ReadCloser
	info     *meteringInfo
	tail     *tailBuffer
	usage    pricer.Usage
	schedule reportSchedule

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

		go reportUsageWithRetry(b.info.pricer, &usage, b.schedule)
	})
}

// reportUsageWithRetry reports usage to the pricer, retrying with exponential
// backoff on failure. A report that never succeeds is silent revenue loss, so
// the final failure is logged loudly. A durable, un-acked-report queue is the
// real fix and is left as a follow-up; the bounded retry here only narrows the
// window in which a transient pricer failure drops a debit.
func reportUsageWithRetry(mp pricer.MeteredPricer, usage *pricer.Usage,
	schedule reportSchedule) {

	backoff := schedule.initialBackoff

	var err error
	for attempt := 1; attempt <= schedule.maxAttempts; attempt++ {
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
			schedule.maxAttempts, err)

		if attempt < schedule.maxAttempts {
			time.Sleep(backoff)
			backoff *= 2
		}
	}

	log.Errorf("Giving up reporting usage for token %s after %d "+
		"attempts, this debit is lost: %v", usage.TokenID,
		schedule.maxAttempts, err)
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
