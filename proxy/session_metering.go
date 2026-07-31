package proxy

import (
	"context"
	"io"
	"net/http"
	"sync"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/pricer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sessionMeteringContextKey is the request context key under which the session
// metering information for a bearer request is stored, so the response
// modifier can reconcile the request once its response has completed.
type sessionMeteringContextKey struct{}

// sessionMeteringInfo carries what the response modifier needs to settle a
// bearer request against its session balance.
type sessionMeteringInfo struct {
	// sessionID is the session the request drew against.
	sessionID string

	// chargedSats is the amount the authenticator already deducted from the
	// session balance when it accepted the bearer credential. It is the
	// estimate the settlement reconciles against.
	chargedSats int64

	// serviceName is the name of the aperture service.
	serviceName string

	// path is the URL path of the request.
	path string

	// pricer costs the completed response.
	pricer pricer.SessionPricer

	// settler owns the session balance the reconciliation lands on.
	settler auth.SessionSettler

	// tailBytes is the maximum number of trailing response body bytes to
	// capture for the settlement.
	tailBytes int

	// requestText is the serialized request, held until the response
	// completes so the settlement can tell the pricer which model to cost
	// the response at. A session books no model the way an L402 bundle
	// does, so this is the only place that information survives.
	requestText string
}

// sessionPricer returns the service's pricer as a SessionPricer if the service
// has a dynamic pricer that speaks the session RPCs.
//
// Metering is not required here the way it is for L402 draw-down. An L402
// bundle only exists because a metered pricer holds it, but a session balance
// exists in aperture regardless, so any pricer able to quote a per-request cost
// is worth asking.
func (s *Service) sessionPricer() (pricer.SessionPricer, bool) {
	if !s.DynamicPrice.Enabled {
		return nil, false
	}

	sp, ok := s.pricer.(pricer.SessionPricer)

	return sp, ok
}

// sessionSettler returns the proxy's authenticator as a SessionSettler if it
// holds prepaid session balances.
func (p *Proxy) sessionSettler() (auth.SessionSettler, bool) {
	settler, ok := p.authenticator.(auth.SessionSettler)

	return settler, ok
}

// quoteSessionPrices asks the service's pricer what one request against a
// session should cost, and folds the answer into the set of prices a fresh
// challenge quotes.
//
// A pricer that does not implement the session RPCs is not an error. Aperture
// ships the session intent against price servers that predate it, and the
// documented fallback is the one-shot charge price, which is what a session
// challenge quoted before this seam existed. Only a real failure is worth
// logging loudly; an Unimplemented reply is the expected shape of an older
// pricer and is left at debug.
func quoteSessionPrices(r *http.Request, target *Service,
	charge int64) auth.ChallengePrices {

	prices := auth.ChallengePrices{Charge: charge}

	sp, ok := target.sessionPricer()
	if !ok {
		return prices
	}

	quote, err := sp.QuoteSession(r.Context(), r, "", target.Name)
	switch {
	case status.Code(err) == codes.Unimplemented:
		log.Debugf("Pricer for service %s does not quote sessions, "+
			"falling back to the charge price of %d sats",
			target.Name, charge)

		return prices

	case err != nil:
		log.Errorf("Error quoting session price for service %s, "+
			"falling back to the charge price of %d sats: %v",
			target.Name, charge, err)

		return prices
	}

	prices.SessionUnit = quote.UnitPriceSats
	prices.SessionDeposit = quote.DepositSats

	return prices
}

// checkSessionMetering annotates a request authenticated by an MPP session
// bearer credential so the response modifier can reconcile what it was charged
// against what it turns out to cost.
//
// It deliberately does not admit or refuse the request. The authenticator
// already did both when it accepted the credential: it verified the preimage
// and deducted the challenge's per-unit amount from the balance, refusing the
// request outright if the balance could not cover it. What is left is only the
// second half of post-hoc reconciliation.
func (p *Proxy) checkSessionMetering(r *http.Request,
	target *Service) *http.Request {

	sp, ok := target.sessionPricer()
	if !ok {
		return r
	}

	settler, ok := p.sessionSettler()
	if !ok {
		return r
	}

	sessionID, chargedSats, ok := settler.BearerSessionID(&r.Header)
	if !ok {
		return r
	}

	// Serializing the request also buffers and restores its body, so this
	// has to happen before the request is handed to the backend. A failure
	// here is not fatal: the settlement simply falls back to the estimate,
	// which is what the buyer was quoted anyway.
	requestText, err := pricer.SerializeRequest(r)
	if err != nil {
		log.Errorf("Unable to serialize session %s request for "+
			"settlement, it will settle at its %d sat estimate: %v",
			sessionID, chargedSats, err)
	}

	// Strip the client's Accept-Encoding for the same reason the L402
	// metered path does: a gzip response body would leave the captured tail
	// unparseable, the request would never settle, and the session would be
	// billed the estimate forever regardless of what it actually consumed.
	r.Header.Del("Accept-Encoding")

	info := &sessionMeteringInfo{
		sessionID:   sessionID,
		chargedSats: chargedSats,
		serviceName: target.Name,
		path:        r.URL.Path,
		pricer:      sp,
		settler:     settler,
		tailBytes:   target.usageTailBytes(),
		requestText: requestText,
	}

	ctx := context.WithValue(r.Context(), sessionMeteringContextKey{}, info)

	return r.WithContext(ctx)
}

// attachSessionObserver wraps the response body of a bearer request so its true
// cost is settled against the session balance once the body has been copied to
// the client.
func attachSessionObserver(res *http.Response) {
	if res.Request == nil {
		return
	}

	info, ok := res.Request.Context().Value(
		sessionMeteringContextKey{},
	).(*sessionMeteringInfo)
	if !ok {
		return
	}

	// A hijacked protocol upgrade never flows through the body copy, so
	// there is nothing to cost. The estimate already deducted stands, which
	// is the right outcome for a request whose usage aperture cannot see.
	if res.StatusCode == http.StatusSwitchingProtocols {
		return
	}

	res.Body = &sessionObservingBody{
		inner: res.Body,
		info:  info,
		tail:  newTailBuffer(info.tailBytes),
		usage: pricer.SessionUsage{
			SessionID:       info.sessionID,
			Path:            info.path,
			ServiceName:     info.serviceName,
			HTTPStatus:      res.StatusCode,
			ContentType:     res.Header.Get("Content-Type"),
			ContentEncoding: res.Header.Get("Content-Encoding"),
			EstimateSats:    info.chargedSats,
			RequestText:     info.requestText,
		},
	}
}

// sessionObservingBody wraps a response body, captures a bounded tail of the
// bytes flowing through it, and settles the request against its session balance
// exactly once, when the body is exhausted or closed.
type sessionObservingBody struct {
	inner io.ReadCloser
	info  *sessionMeteringInfo
	tail  *tailBuffer
	usage pricer.SessionUsage

	settleOnce sync.Once
}

// Read passes through to the wrapped body while capturing the tail. On EOF the
// request is settled as complete.
func (b *sessionObservingBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if n > 0 {
		b.tail.Write(p[:n])
	}

	if err == io.EOF {
		b.settle(true)
	}

	return n, err
}

// Close closes the wrapped body. If the body was not read to EOF first, the
// request is settled as incomplete, for example when the client disconnected
// mid-stream.
func (b *sessionObservingBody) Close() error {
	err := b.inner.Close()

	b.settle(false)

	return err
}

// settle costs the captured response and reconciles it against the session
// balance exactly once, detached from the request context.
func (b *sessionObservingBody) settle(complete bool) {
	b.settleOnce.Do(func() {
		usage := b.usage
		usage.Complete = complete
		usage.ResponseTail = b.tail.Bytes()

		go settleSession(b.info, &usage)
	})
}

// settleSession costs a completed response and applies the difference to the
// session balance.
//
// A settlement that cannot be costed leaves the estimate standing rather than
// guessing. That is the conservative outcome in both directions: the buyer is
// charged what the challenge told them a request would cost, and the seller
// collects it, which is exactly the behaviour of a session with no pricer at
// all.
func settleSession(info *sessionMeteringInfo, usage *pricer.SessionUsage) {
	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	actual, err := info.pricer.SettleSession(ctx, usage)
	switch {
	case status.Code(err) == codes.Unimplemented:
		log.Debugf("Pricer for service %s does not settle sessions, "+
			"leaving session %s charged the %d sat estimate",
			info.serviceName, info.sessionID, info.chargedSats)

		return

	case err != nil:
		log.Errorf("Error costing session %s request against service "+
			"%s, leaving it charged the %d sat estimate: %v",
			info.sessionID, info.serviceName, info.chargedSats,
			err)

		return
	}

	err = info.settler.SettleSessionRequest(
		ctx, info.sessionID, info.chargedSats, actual,
	)
	if err != nil {
		log.Errorf("Unable to settle session %s against a cost of %d "+
			"sats: %v", info.sessionID, actual, err)
	}
}
