package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeSessionPricer is a SessionPricer capturing the calls the proxy makes.
type fakeSessionPricer struct {
	quote    *pricer.SessionQuote
	quoteErr error

	cost      int64
	settleErr error

	settlements chan *pricer.SessionUsage
}

func newFakeSessionPricer() *fakeSessionPricer {
	return &fakeSessionPricer{
		quote: &pricer.SessionQuote{
			UnitPriceSats: 7,
		},
		settlements: make(chan *pricer.SessionUsage, 1),
	}
}

func (f *fakeSessionPricer) GetPrice(_ context.Context,
	_ *http.Request) (int64, error) {

	return 11_500, nil
}

func (f *fakeSessionPricer) Close() error {
	return nil
}

func (f *fakeSessionPricer) QuoteSession(_ context.Context, _ *http.Request,
	_ string, _ string) (*pricer.SessionQuote, error) {

	if f.quoteErr != nil {
		return nil, f.quoteErr
	}

	return f.quote, nil
}

func (f *fakeSessionPricer) SettleSession(_ context.Context,
	usage *pricer.SessionUsage) (int64, error) {

	select {
	case f.settlements <- usage:
	default:
	}

	if f.settleErr != nil {
		return 0, f.settleErr
	}

	return f.cost, nil
}

// settlement is one reconciliation the fake settler recorded.
type settlement struct {
	sessionID string
	charged   int64
	actual    int64
}

// fakeSessionSettler stands in for the authenticator that owns the session
// balances.
type fakeSessionSettler struct {
	sessionID string
	charged   int64
	present   bool

	err error

	mu          sync.Mutex
	settlements chan settlement
}

func newFakeSessionSettler(sessionID string,
	charged int64) *fakeSessionSettler {

	return &fakeSessionSettler{
		sessionID:   sessionID,
		charged:     charged,
		present:     true,
		settlements: make(chan settlement, 1),
	}
}

func (f *fakeSessionSettler) Accept(_ *http.Header, _ string) bool {
	return true
}

func (f *fakeSessionSettler) FreshChallengeHeader(_ string,
	_ int64) (http.Header, error) {

	return make(http.Header), nil
}

func (f *fakeSessionSettler) BearerSessionID(_ *http.Header) (string, int64,
	bool) {

	if !f.present {
		return "", 0, false
	}

	return f.sessionID, f.charged, true
}

func (f *fakeSessionSettler) SettleSessionRequest(_ context.Context,
	sessionID string, chargedSats, actualSats int64) error {

	f.mu.Lock()
	defer f.mu.Unlock()

	select {
	case f.settlements <- settlement{
		sessionID: sessionID,
		charged:   chargedSats,
		actual:    actualSats,
	}:
	default:
	}

	return f.err
}

// newSessionService builds a service whose pricer is the given fake.
func newSessionService(fake *fakeSessionPricer) *Service {
	svc := &Service{Name: "inference"}
	svc.DynamicPrice.Enabled = true
	svc.pricer = fake

	return svc
}

// TestQuoteSessionPricesFoldsInTheQuote asserts the session-aware quote reaches
// the challenge alongside, not instead of, the one-shot charge price. Both
// intents can appear in one 402, and each has to be quoted its own question.
func TestQuoteSessionPricesFoldsInTheQuote(t *testing.T) {
	t.Parallel()

	fake := newFakeSessionPricer()
	fake.quote = &pricer.SessionQuote{UnitPriceSats: 7, DepositSats: 400}

	target := newSessionService(fake)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	prices := quoteSessionPrices(r, target, 11_500)
	require.Equal(t, auth.ChallengePrices{
		Charge:         11_500,
		SessionUnit:    7,
		SessionDeposit: 400,
	}, prices)
}

// TestQuoteSessionPricesFallsBack asserts a pricer that cannot answer the
// session question leaves the challenge quoting the charge price, which is what
// it quoted before this seam existed. A price server that predates the session
// RPCs must keep working.
func TestQuoteSessionPricesFallsBack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*Service, *fakeSessionPricer)
	}{{
		name: "pricer does not implement the session RPCs",
		setup: func(_ *Service, f *fakeSessionPricer) {
			f.quoteErr = status.Error(
				codes.Unimplemented, "not implemented",
			)
		},
	}, {
		name: "pricer failed",
		setup: func(_ *Service, f *fakeSessionPricer) {
			f.quoteErr = status.Error(
				codes.Unavailable, "pricer is down",
			)
		},
	}, {
		name: "service has no dynamic pricer",
		setup: func(s *Service, _ *fakeSessionPricer) {
			s.DynamicPrice.Enabled = false
		},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeSessionPricer()
			target := newSessionService(fake)
			tc.setup(target, fake)

			r := httptest.NewRequest(
				http.MethodPost, "/v1/chat/completions", nil,
			)

			prices := quoteSessionPrices(r, target, 11_500)
			require.Equal(t, auth.ChallengePrices{
				Charge: 11_500,
			}, prices)
		})
	}
}

// TestCheckSessionMeteringAnnotates asserts a bearer request is annotated with
// what the settlement will need, and that the request body survives the
// serialization the annotation performs.
func TestCheckSessionMeteringAnnotates(t *testing.T) {
	t.Parallel()

	fake := newFakeSessionPricer()
	settler := newFakeSessionSettler("session-abc", 7)

	p := &Proxy{authenticator: settler}
	target := newSessionService(fake)

	r := httptest.NewRequest(
		http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test"}`),
	)
	r.Header.Set("Accept-Encoding", "gzip")

	annotated := p.checkSessionMetering(r, target)

	info, ok := annotated.Context().Value(
		sessionMeteringContextKey{},
	).(*sessionMeteringInfo)
	require.True(t, ok)
	require.Equal(t, "session-abc", info.sessionID)
	require.EqualValues(t, 7, info.chargedSats)
	require.Equal(t, "inference", info.serviceName)
	require.Contains(t, info.requestText, `{"model":"gpt-test"}`)

	// A gzip response would leave the captured tail unparseable, so the
	// client's encoding preference is dropped exactly as it is on the L402
	// metered path.
	require.Empty(t, annotated.Header.Get("Accept-Encoding"))

	// The backend still gets the whole body, which the serializer buffered
	// and put back.
	body, err := io.ReadAll(annotated.Body)
	require.NoError(t, err)
	require.Equal(t, `{"model":"gpt-test"}`, string(body))
}

// TestCheckSessionMeteringSkips asserts nothing is annotated when there is no
// session to settle against, so a plain L402 or unauthenticated request is left
// exactly as it was.
func TestCheckSessionMeteringSkips(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*Proxy, *Service, *fakeSessionSettler)
	}{{
		name: "not a bearer credential",
		setup: func(_ *Proxy, _ *Service, s *fakeSessionSettler) {
			s.present = false
		},
	}, {
		name: "service has no dynamic pricer",
		setup: func(_ *Proxy, svc *Service, _ *fakeSessionSettler) {
			svc.DynamicPrice.Enabled = false
		},
	}, {
		name: "authenticator holds no sessions",
		setup: func(p *Proxy, _ *Service, _ *fakeSessionSettler) {
			p.authenticator = &noSessionAuthenticator{}
		},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			settler := newFakeSessionSettler("session-abc", 7)
			p := &Proxy{authenticator: settler}
			target := newSessionService(newFakeSessionPricer())
			tc.setup(p, target, settler)

			r := httptest.NewRequest(
				http.MethodPost, "/v1/chat/completions", nil,
			)
			r.Header.Set("Accept-Encoding", "gzip")

			annotated := p.checkSessionMetering(r, target)

			_, ok := annotated.Context().Value(
				sessionMeteringContextKey{},
			).(*sessionMeteringInfo)
			require.False(t, ok)

			// An untouched request keeps its encoding preference.
			require.Equal(
				t, "gzip",
				annotated.Header.Get("Accept-Encoding"),
			)
		})
	}
}

// noSessionAuthenticator is an Authenticator that holds no session balances.
type noSessionAuthenticator struct{}

func (n *noSessionAuthenticator) Accept(_ *http.Header, _ string) bool {
	return true
}

func (n *noSessionAuthenticator) FreshChallengeHeader(_ string,
	_ int64) (http.Header, error) {

	return make(http.Header), nil
}

// sessionResponse builds a proxied response carrying the session metering
// annotation, ready to be handed to attachSessionObserver.
func sessionResponse(t *testing.T, info *sessionMeteringInfo, status int,
	body string) *http.Response {

	t.Helper()

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx := context.WithValue(r.Context(), sessionMeteringContextKey{}, info)

	res := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r.WithContext(ctx),
	}
	res.Header.Set("Content-Type", "text/event-stream")

	return res
}

// sessionInfo builds the annotation the response observer settles against.
func sessionInfo(fake *fakeSessionPricer,
	settler *fakeSessionSettler) *sessionMeteringInfo {

	return &sessionMeteringInfo{
		sessionID:   settler.sessionID,
		chargedSats: settler.charged,
		serviceName: "inference",
		path:        "/v1/chat/completions",
		pricer:      fake,
		settler:     settler,
		tailBytes:   pricer.DefaultUsageTailBytes,
		requestText: "POST /v1/chat/completions HTTP/1.1\r\n\r\n{}",
	}
}

// TestSessionSettlementOnCompletedResponse asserts the whole reconciliation
// loop: the response tail is captured, handed to the pricer to cost, and the
// difference between the estimate and the true cost is handed back to whoever
// owns the balance.
func TestSessionSettlementOnCompletedResponse(t *testing.T) {
	t.Parallel()

	fake := newFakeSessionPricer()
	fake.cost = 19

	settler := newFakeSessionSettler("session-abc", 7)
	res := sessionResponse(
		t, sessionInfo(fake, settler), http.StatusOK,
		"data: {\"usage\":{\"total_tokens\":40}}\n\ndata: [DONE]\n\n",
	)

	attachSessionObserver(res)

	drained, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Contains(t, string(drained), "[DONE]")

	usage := requireSettlement(t, fake)
	require.Equal(t, "session-abc", usage.SessionID)
	require.True(t, usage.Complete)
	require.EqualValues(t, 7, usage.EstimateSats)
	require.Contains(t, string(usage.ResponseTail), "[DONE]")
	require.Contains(t, usage.RequestText, "/v1/chat/completions")

	got := requireReconciliation(t, settler)
	require.Equal(t, settlement{
		sessionID: "session-abc",
		charged:   7,
		actual:    19,
	}, got)
}

// TestSessionSettlementOnAbortedResponse asserts a client that hangs up
// mid-stream still settles, marked incomplete. Without it the request would be
// billed its estimate forever no matter how much inference the backend had
// already produced.
func TestSessionSettlementOnAbortedResponse(t *testing.T) {
	t.Parallel()

	fake := newFakeSessionPricer()
	fake.cost = 3

	settler := newFakeSessionSettler("session-abc", 7)
	res := sessionResponse(
		t, sessionInfo(fake, settler), http.StatusOK,
		"data: {\"choices\":[]}\n\n",
	)

	attachSessionObserver(res)

	// Close without draining, which is what a client disconnect looks like.
	require.NoError(t, res.Body.Close())

	usage := requireSettlement(t, fake)
	require.False(t, usage.Complete)

	got := requireReconciliation(t, settler)
	require.EqualValues(t, 3, got.actual)
}

// TestSessionSettlementIsExactlyOnce asserts a body that is both drained and
// closed settles once. Settling twice would apply the same reconciliation to
// the balance a second time.
func TestSessionSettlementIsExactlyOnce(t *testing.T) {
	t.Parallel()

	fake := newFakeSessionPricer()
	fake.cost = 5

	settler := newFakeSessionSettler("session-abc", 7)
	res := sessionResponse(
		t, sessionInfo(fake, settler), http.StatusOK, "data: [DONE]\n\n",
	)

	attachSessionObserver(res)

	_, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	requireSettlement(t, fake)
	requireReconciliation(t, settler)

	// Nothing further may arrive.
	select {
	case extra := <-settler.settlements:
		t.Fatalf("session settled twice: %+v", extra)

	case <-time.After(200 * time.Millisecond):
	}
}

// TestSessionSettlementLeavesEstimateOnPricerFailure asserts that a pricer that
// cannot cost the response leaves the estimate standing rather than guessing.
// Charging what the challenge quoted is the honest answer when nothing better
// is known, and it is what a session with no pricer at all does.
func TestSessionSettlementLeavesEstimateOnPricerFailure(t *testing.T) {
	t.Parallel()

	for _, code := range []codes.Code{
		codes.Unimplemented, codes.Unavailable,
	} {
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()

			fake := newFakeSessionPricer()
			fake.settleErr = status.Error(code, "no")

			settler := newFakeSessionSettler("session-abc", 7)
			res := sessionResponse(
				t, sessionInfo(fake, settler), http.StatusOK,
				"data: [DONE]\n\n",
			)

			attachSessionObserver(res)
			_, err := io.ReadAll(res.Body)
			require.NoError(t, err)

			requireSettlement(t, fake)

			select {
			case got := <-settler.settlements:
				t.Fatalf("balance moved on a failed cost: %+v",
					got)

			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

// TestSessionObserverSkipsProtocolUpgrade asserts a hijacked upgrade is left
// alone. Its body never flows through the copy, so there is nothing to cost,
// and the estimate already deducted stands.
func TestSessionObserverSkipsProtocolUpgrade(t *testing.T) {
	t.Parallel()

	fake := newFakeSessionPricer()
	settler := newFakeSessionSettler("session-abc", 7)

	res := sessionResponse(
		t, sessionInfo(fake, settler), http.StatusSwitchingProtocols, "",
	)

	attachSessionObserver(res)

	_, wrapped := res.Body.(*sessionObservingBody)
	require.False(t, wrapped)

	// A response with no annotation at all is likewise untouched.
	plain := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("body")),
		Request: httptest.NewRequest(
			http.MethodPost, "/v1/chat/completions", nil,
		),
	}
	attachSessionObserver(plain)

	_, wrapped = plain.Body.(*sessionObservingBody)
	require.False(t, wrapped)
}

// requireSettlement waits for the pricer to be asked to cost a response.
func requireSettlement(t *testing.T,
	fake *fakeSessionPricer) *pricer.SessionUsage {

	t.Helper()

	select {
	case usage := <-fake.settlements:
		return usage

	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the response to be costed")

		return nil
	}
}

// requireReconciliation waits for the balance owner to be handed the
// difference.
func requireReconciliation(t *testing.T,
	settler *fakeSessionSettler) settlement {

	t.Helper()

	select {
	case got := <-settler.settlements:
		return got

	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the session to be reconciled")

		return settlement{}
	}
}
