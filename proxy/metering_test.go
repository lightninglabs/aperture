package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"encoding/json"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// newTestMacaroon mints a macaroon with a valid L402 identifier and returns
// it together with the hex-encoded token ID.
func newTestMacaroon(t *testing.T) (*macaroon.Macaroon, string) {
	t.Helper()

	var id l402.Identifier
	_, err := rand.Read(id.TokenID[:])
	require.NoError(t, err)
	_, err = rand.Read(id.PaymentHash[:])
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, l402.EncodeIdentifier(&buf, &id))

	mac, err := macaroon.New(
		[]byte("rootkey"), buf.Bytes(), "loc", macaroon.LatestVersion,
	)
	require.NoError(t, err)

	return mac, id.TokenID.String()
}

// authHeaderForMacaroon builds the L402 Authorization header value for the
// given macaroon.
func authHeaderForMacaroon(t *testing.T, mac *macaroon.Macaroon) string {
	t.Helper()

	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)

	preimage := strings.Repeat("00", 32)

	return fmt.Sprintf(
		"L402 %s:%s", base64.StdEncoding.EncodeToString(macBytes),
		preimage,
	)
}

// fakeMeteredPricer is a MeteredPricer capturing the calls made by the proxy.
type fakeMeteredPricer struct {
	price int64

	authorizeResult *pricer.AuthorizeResult
	authorizeErr    error

	mintedTokens chan string
	usageReports chan *pricer.Usage
}

func newFakeMeteredPricer() *fakeMeteredPricer {
	return &fakeMeteredPricer{
		price:        42,
		mintedTokens: make(chan string, 1),
		usageReports: make(chan *pricer.Usage, 1),
	}
}

func (f *fakeMeteredPricer) GetPrice(_ context.Context,
	_ *http.Request) (int64, error) {

	return f.price, nil
}

func (f *fakeMeteredPricer) ChallengeMinted(_ context.Context,
	_ *http.Request, tokenID string, _ string, _ int64) error {

	f.mintedTokens <- tokenID

	return nil
}

func (f *fakeMeteredPricer) AuthorizeRequest(_ context.Context,
	_ *http.Request, _ string, _ string) (*pricer.AuthorizeResult, error) {

	return f.authorizeResult, f.authorizeErr
}

func (f *fakeMeteredPricer) ReportUsage(_ context.Context,
	usage *pricer.Usage) error {

	f.usageReports <- usage

	return nil
}

func (f *fakeMeteredPricer) Close() error {
	return nil
}

// fakeChallengeAuth is an authenticator whose challenge headers embed a real
// L402 identifier macaroon, so the metering code can extract a token ID.
type fakeChallengeAuth struct {
	t   *testing.T
	mac *macaroon.Macaroon
}

func (a *fakeChallengeAuth) Accept(_ *http.Header, _ string) bool {
	return false
}

func (a *fakeChallengeAuth) FreshChallengeHeader(_ string,
	_ int64) (http.Header, error) {

	macBytes, err := a.mac.MarshalBinary()
	require.NoError(a.t, err)

	header := http.Header{}
	header.Set("WWW-Authenticate", fmt.Sprintf(
		"L402 macaroon=\"%s\", invoice=\"lnbcrt1invoice\"",
		base64.StdEncoding.EncodeToString(macBytes),
	))

	return header, nil
}

// newMeteredService builds a service whose pricer is the given fake and that
// has metering enabled.
func newMeteredService(fake *fakeMeteredPricer) *Service {
	svc := &Service{
		Name: "inference",
	}
	svc.DynamicPrice.Enabled = true
	svc.DynamicPrice.Metered = true
	svc.pricer = fake

	return svc
}

// TestTailBuffer tests that the tail buffer keeps exactly the trailing bytes.
func TestTailBuffer(t *testing.T) {
	t.Parallel()

	buf := newTailBuffer(8)

	// Small writes accumulate until the cap.
	buf.Write([]byte("abc"))
	buf.Write([]byte("def"))
	require.Equal(t, []byte("abcdef"), buf.Bytes())

	// Exceeding the cap drops the oldest bytes.
	buf.Write([]byte("ghi"))
	require.Equal(t, []byte("bcdefghi"), buf.Bytes())

	// A write bigger than the cap keeps only its tail.
	buf.Write([]byte("0123456789ABCDEF"))
	require.Equal(t, []byte("89ABCDEF"), buf.Bytes())
}

// TestUsageObservingBody tests that usage is reported exactly once, with the
// captured tail and the correct completion flag.
func TestUsageObservingBody(t *testing.T) {
	t.Parallel()

	newBody := func(fake *fakeMeteredPricer,
		content string) *usageObservingBody {

		return &usageObservingBody{
			inner: io.NopCloser(strings.NewReader(content)),
			info: &meteringInfo{
				pricer: fake,
			},
			tail: newTailBuffer(8),
			usage: pricer.Usage{
				TokenID:    "token",
				HTTPStatus: 200,
			},
			schedule: reportSchedule{
				maxAttempts:    1,
				initialBackoff: time.Millisecond,
			},
		}
	}

	waitReport := func(fake *fakeMeteredPricer) *pricer.Usage {
		select {
		case usage := <-fake.usageReports:
			return usage
		case <-time.After(5 * time.Second):
			t.Fatal("no usage report received")
			return nil
		}
	}

	// Reading the body to EOF reports complete usage with the tail.
	fake := newFakeMeteredPricer()
	body := newBody(fake, "hello streaming world")
	_, err := io.Copy(io.Discard, body)
	require.NoError(t, err)

	usage := waitReport(fake)
	require.True(t, usage.Complete)
	require.Equal(t, []byte("ng world"), usage.ResponseTail)

	// Closing after EOF must not produce a second report.
	require.NoError(t, body.Close())
	select {
	case <-fake.usageReports:
		t.Fatal("duplicate usage report")
	case <-time.After(50 * time.Millisecond):
	}

	// Closing before EOF reports truncated usage.
	fake = newFakeMeteredPricer()
	body = newBody(fake, "hello streaming world")

	firstBytes := make([]byte, 5)
	_, err = body.Read(firstBytes)
	require.NoError(t, err)
	require.NoError(t, body.Close())

	usage = waitReport(fake)
	require.False(t, usage.Complete)
	require.Equal(t, []byte("hello"), usage.ResponseTail)
}

// TestChallengeHeaderTokenID tests that the token ID embedded in a fresh
// challenge header round-trips through the parser.
func TestChallengeHeaderTokenID(t *testing.T) {
	t.Parallel()

	mac, wantTokenID := newTestMacaroon(t)

	auth := &fakeChallengeAuth{t: t, mac: mac}
	header, err := auth.FreshChallengeHeader("inference", 42)
	require.NoError(t, err)

	tokenID, err := l402TokenIDFromChallengeHeader(header)
	require.NoError(t, err)
	require.Equal(t, wantTokenID, tokenID)

	// A header without a macaroon must error.
	_, err = l402TokenIDFromChallengeHeader(http.Header{})
	require.Error(t, err)
}

// TestCheckMeteredAccess tests the per-request authorization decision paths.
func TestCheckMeteredAccess(t *testing.T) {
	t.Parallel()

	mac, tokenID := newTestMacaroon(t)
	challengeMac, challengeTokenID := newTestMacaroon(t)

	newRequest := func() *http.Request {
		r := httptest.NewRequest(
			"POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"glm"}`),
		)
		r.Header.Set("Authorization", authHeaderForMacaroon(t, mac))

		return r
	}

	t.Run("allowed request is annotated", func(t *testing.T) {
		fake := newFakeMeteredPricer()
		fake.authorizeResult = &pricer.AuthorizeResult{Allowed: true}
		svc := newMeteredService(fake)
		p := &Proxy{}

		w := httptest.NewRecorder()
		r, proceed := p.checkMeteredAccess(
			w, newRequest(), svc, "inference",
		)

		require.True(t, proceed)

		info, ok := r.Context().Value(
			meteringContextKey{},
		).(*meteringInfo)
		require.True(t, ok)
		require.Equal(t, tokenID, info.tokenID)
		require.Equal(t, "inference", info.serviceName)
		require.Equal(
			t, pricer.DefaultUsageTailBytes, info.tailBytes,
		)
	})

	t.Run("exhausted token gets fresh 402", func(t *testing.T) {
		fake := newFakeMeteredPricer()
		fake.authorizeResult = &pricer.AuthorizeResult{
			Allowed:   false,
			PriceSats: 1337,
			Reason:    "balance exhausted",
		}
		svc := newMeteredService(fake)
		p := &Proxy{
			authenticator: &fakeChallengeAuth{
				t: t, mac: challengeMac,
			},
		}

		w := httptest.NewRecorder()
		_, proceed := p.checkMeteredAccess(
			w, newRequest(), svc, "inference",
		)

		require.False(t, proceed)
		require.Equal(t, http.StatusPaymentRequired, w.Code)
		require.Contains(
			t, w.Header().Get("WWW-Authenticate"), "macaroon=",
		)

		// The pricer must have been told about the fresh challenge.
		select {
		case minted := <-fake.mintedTokens:
			require.Equal(t, challengeTokenID, minted)
		case <-time.After(time.Second):
			t.Fatal("pricer not notified of minted challenge")
		}
	})

	t.Run("pricer error fails closed", func(t *testing.T) {
		fake := newFakeMeteredPricer()
		fake.authorizeErr = fmt.Errorf("pricer down")
		svc := newMeteredService(fake)
		p := &Proxy{}

		w := httptest.NewRecorder()
		_, proceed := p.checkMeteredAccess(
			w, newRequest(), svc, "inference",
		)

		require.False(t, proceed)
		require.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("non-metered service passes through", func(t *testing.T) {
		fake := newFakeMeteredPricer()
		svc := newMeteredService(fake)
		svc.DynamicPrice.Metered = false
		p := &Proxy{}

		w := httptest.NewRecorder()
		r, proceed := p.checkMeteredAccess(
			w, newRequest(), svc, "inference",
		)

		require.True(t, proceed)
		require.Nil(t, r.Context().Value(meteringContextKey{}))
	})

	t.Run("non-L402 auth skips metering", func(t *testing.T) {
		fake := newFakeMeteredPricer()
		fake.authorizeResult = &pricer.AuthorizeResult{Allowed: false}
		svc := newMeteredService(fake)
		p := &Proxy{}

		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set("Payment", "some-mpp-credential")

		w := httptest.NewRecorder()
		_, proceed := p.checkMeteredAccess(w, r, svc, "inference")

		require.True(t, proceed)
	})
}

// TestCheckMeteredAccessStripsAcceptEncoding tests that a metered request has
// its Accept-Encoding header removed, so the upstream response is observed as
// plaintext rather than a gzip body the usage tail cannot be parsed from. A
// gzip body would carry no parseable usage object, so the bundle would never
// be debited: unlimited free inference.
func TestCheckMeteredAccessStripsAcceptEncoding(t *testing.T) {
	t.Parallel()

	mac, _ := newTestMacaroon(t)

	fake := newFakeMeteredPricer()
	fake.authorizeResult = &pricer.AuthorizeResult{Allowed: true}
	svc := newMeteredService(fake)
	p := &Proxy{}

	r := httptest.NewRequest(
		"POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm"}`),
	)
	r.Header.Set("Authorization", authHeaderForMacaroon(t, mac))
	r.Header.Set("Accept-Encoding", "gzip")

	w := httptest.NewRecorder()
	r, proceed := p.checkMeteredAccess(w, r, svc, "inference")
	require.True(t, proceed)

	// The client's Accept-Encoding must be gone, so Go's transport adds its
	// own and transparently decompresses, yielding a plaintext tail.
	require.Empty(t, r.Header.Get("Accept-Encoding"))

	// The request must still be annotated for metering.
	_, ok := r.Context().Value(meteringContextKey{}).(*meteringInfo)
	require.True(t, ok)
}

// TestCheckMeteredAccessMalformedL402 tests that a request carrying an
// L402-scheme Authorization header that fails to parse is rejected with an
// internal error on a metered service, rather than silently passing through as
// free unmetered access.
func TestCheckMeteredAccessMalformedL402(t *testing.T) {
	t.Parallel()

	fake := newFakeMeteredPricer()
	fake.authorizeResult = &pricer.AuthorizeResult{Allowed: true}
	svc := newMeteredService(fake)
	p := &Proxy{}

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "L402 not-a-valid-token")

	w := httptest.NewRecorder()
	_, proceed := p.checkMeteredAccess(w, r, svc, "inference")

	require.False(t, proceed)
	require.Equal(t, http.StatusInternalServerError, w.Code)

	// A request with no L402 scheme at all still passes through untouched.
	r = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("Payment", "some-mpp-credential")

	w = httptest.NewRecorder()
	_, proceed = p.checkMeteredAccess(w, r, svc, "inference")
	require.True(t, proceed)
}

// TestReportUsageWithRetry tests that a usage report is retried after a
// transient failure and eventually succeeds, so a momentary pricer blip does
// not silently drop a debit.
func TestReportUsageWithRetry(t *testing.T) {
	t.Parallel()

	// Use a fast schedule so the test does not sleep for seconds. Passing
	// it as a parameter, rather than mutating the package-level default,
	// keeps this test isolated from any report goroutine spawned by
	// another test that outlives its own test function.
	schedule := reportSchedule{
		maxAttempts:    4,
		initialBackoff: time.Millisecond,
	}

	// The pricer fails the first two attempts and succeeds on the third.
	fake := &flakyPricer{failFor: 2}

	usage := &pricer.Usage{TokenID: "token"}
	reportUsageWithRetry(fake, usage, schedule)

	require.Equal(t, 3, fake.calls)
}

// flakyPricer is a MeteredPricer whose ReportUsage fails a fixed number of
// times before succeeding, recording how many times it was called.
type flakyPricer struct {
	failFor int
	calls   int
}

func (f *flakyPricer) GetPrice(context.Context, *http.Request) (int64, error) {
	return 0, nil
}

func (f *flakyPricer) ChallengeMinted(context.Context, *http.Request, string,
	string, int64) error {

	return nil
}

func (f *flakyPricer) AuthorizeRequest(context.Context, *http.Request, string,
	string) (*pricer.AuthorizeResult, error) {

	return &pricer.AuthorizeResult{Allowed: true}, nil
}

func (f *flakyPricer) ReportUsage(_ context.Context, _ *pricer.Usage) error {
	f.calls++
	if f.calls <= f.failFor {
		return fmt.Errorf("transient failure %d", f.calls)
	}

	return nil
}

func (f *flakyPricer) Close() error {
	return nil
}

// TestCheckMeteredAccessBodyIntact tests that the request body survives the
// authorization round-trip and is still forwardable afterwards.
func TestCheckMeteredAccessBodyIntact(t *testing.T) {
	t.Parallel()

	mac, _ := newTestMacaroon(t)

	fake := newFakeMeteredPricer()
	fake.authorizeResult = &pricer.AuthorizeResult{Allowed: true}
	svc := newMeteredService(fake)
	p := &Proxy{}

	bodyJSON := `{"model":"glm-4.7","messages":[{"role":"user"}]}`
	r := httptest.NewRequest(
		"POST", "/v1/chat/completions", strings.NewReader(bodyJSON),
	)
	r.Header.Set("Authorization", authHeaderForMacaroon(t, mac))

	w := httptest.NewRecorder()
	r, proceed := p.checkMeteredAccess(w, r, svc, "inference")
	require.True(t, proceed)

	gotBody, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.Equal(t, bodyJSON, string(gotBody))
}

// TestAttachUsageObserver tests that only annotated requests get their
// response bodies wrapped.
func TestAttachUsageObserver(t *testing.T) {
	t.Parallel()

	fake := newFakeMeteredPricer()

	// A response without metering info is left untouched.
	plain := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("data")),
		Request:    httptest.NewRequest("POST", "/v1/x", nil),
		Header:     http.Header{},
	}
	attachUsageObserver(plain)
	_, isWrapped := plain.Body.(*usageObservingBody)
	require.False(t, isWrapped)

	// A response for an annotated request is wrapped and reports usage.
	info := &meteringInfo{
		tokenID:     "token",
		serviceName: "inference",
		path:        "/v1/x",
		pricer:      fake,
		tailBytes:   64,
	}
	req := httptest.NewRequest("POST", "/v1/x", nil)
	req = req.WithContext(context.WithValue(
		req.Context(), meteringContextKey{}, info,
	))

	header := http.Header{}
	header.Set("Content-Type", "text/event-stream")
	res := &http.Response{
		StatusCode: 200,
		Body: io.NopCloser(
			strings.NewReader(`data: {"usage":{"total":9}}`),
		),
		Request: req,
		Header:  header,
	}
	attachUsageObserver(res)

	_, isWrapped = res.Body.(*usageObservingBody)
	require.True(t, isWrapped)

	_, err := io.Copy(io.Discard, res.Body)
	require.NoError(t, err)

	select {
	case usage := <-fake.usageReports:
		require.True(t, usage.Complete)
		require.Equal(t, "token", usage.TokenID)
		require.Equal(t, "text/event-stream", usage.ContentType)
		require.Equal(t, 200, usage.HTTPStatus)
		require.Contains(
			t, string(usage.ResponseTail), `"usage":{"total":9}`,
		)
	case <-time.After(5 * time.Second):
		t.Fatal("no usage report received")
	}
}

// TestReleaseReservationOnAbandonedRequest checks that a metered request the
// backend never served still returns the estimate reserved at authorization
// time. The reservation is only released by a usage report, and the pricer
// holds no timer, so a leak here silently shrinks the buyer's usable balance
// until an unspent bundle reads as exhausted.
func TestReleaseReservationOnAbandonedRequest(t *testing.T) {
	t.Parallel()

	fake := &fakeMeteredPricer{
		usageReports: make(chan *pricer.Usage, 1),
	}
	info := &meteringInfo{
		tokenID:          "token",
		serviceName:      "inference",
		path:             "/v1/x",
		pricer:           fake,
		tailBytes:        64,
		reservedEstimate: 4096,
	}

	releaseReservation(info, http.StatusBadGateway)

	select {
	case usage := <-fake.usageReports:
		// The echoed reservation is what lets the pricer release the
		// exact amount it took, and an incomplete report with no body
		// debits nothing, which is right for a request never served.
		require.Equal(t, int64(4096), usage.ReservedEstimate)
		require.Equal(t, "token", usage.TokenID)
		require.False(t, usage.Complete)
		require.Empty(t, usage.ResponseTail)

	case <-time.After(5 * time.Second):
		t.Fatal("no usage report; the reservation leaked")
	}
}

// TestReleaseReservationSkipsUnreservedRequests checks that a request which
// reserved nothing produces no report, so an unmetered path cannot spam the
// pricer.
func TestReleaseReservationSkipsUnreservedRequests(t *testing.T) {
	t.Parallel()

	fake := &fakeMeteredPricer{
		usageReports: make(chan *pricer.Usage, 1),
	}
	info := &meteringInfo{
		tokenID: "token",
		pricer:  fake,
	}

	releaseReservation(info, http.StatusBadGateway)

	select {
	case usage := <-fake.usageReports:
		t.Fatalf("unexpected usage report: %+v", usage)

	case <-time.After(100 * time.Millisecond):
	}
}

// TestAttachUsageObserverReleasesOnProtocolUpgrade checks that a hijacked
// upgrade, which bypasses the body copy the observer rides on, still hands
// the reservation back.
func TestAttachUsageObserverReleasesOnProtocolUpgrade(t *testing.T) {
	t.Parallel()

	fake := &fakeMeteredPricer{
		usageReports: make(chan *pricer.Usage, 1),
	}
	info := &meteringInfo{
		tokenID:          "token",
		serviceName:      "inference",
		path:             "/v1/x",
		pricer:           fake,
		tailBytes:        64,
		reservedEstimate: 512,
	}
	req := httptest.NewRequest("GET", "/v1/x", nil)
	req = req.WithContext(context.WithValue(
		req.Context(), meteringContextKey{}, info,
	))

	res := &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
		Header:     http.Header{},
	}
	attachUsageObserver(res)

	// The body stays unwrapped, so the release has to come from elsewhere.
	_, isWrapped := res.Body.(*usageObservingBody)
	require.False(t, isWrapped)

	select {
	case usage := <-fake.usageReports:
		require.Equal(t, int64(512), usage.ReservedEstimate)
		require.False(t, usage.Complete)

	case <-time.After(5 * time.Second):
		t.Fatal("no usage report; the reservation leaked")
	}
}

// paymentCredentialHeader builds an Authorization: Payment header for a charge
// credential whose preimage hashes to the returned payment hash. The metering
// layer runs after authentication, so the credential only has to parse; its
// HMAC and settlement have already been checked by the time metering sees it.
func paymentCredentialHeader(t *testing.T, intent string) (http.Header,
	string) {

	t.Helper()

	var preimage lntypes.Preimage
	_, err := rand.Read(preimage[:])
	require.NoError(t, err)
	paymentHash := preimage.Hash()

	chargeReq := &mpp.ChargeRequest{
		Amount:   "2000",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1fake",
			PaymentHash: paymentHash.String(),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	payloadJSON, err := json.Marshal(mpp.ChargePayload{
		Preimage: preimage.String(),
	})
	require.NoError(t, err)

	cred := &mpp.Credential{
		Challenge: mpp.ChallengeEcho{
			ID:      "test-challenge-id",
			Realm:   "test",
			Method:  mpp.MethodLightning,
			Intent:  intent,
			Request: encodedReq,
		},
		Payload: json.RawMessage(payloadJSON),
	}
	encoded, err := mpp.EncodeCredential(cred)
	require.NoError(t, err)

	header := make(http.Header)
	header.Set("Authorization", mpp.AuthScheme+" "+encoded)

	return header, paymentHash.String()
}

// TestMeteringTokenIDFromPaymentCredential verifies that a Payment charge
// credential meters under its invoice's payment hash, the same key its mint
// booking used, and that everything else resolves the way metering expects.
func TestMeteringTokenIDFromPaymentCredential(t *testing.T) {
	t.Parallel()

	t.Run("charge meters under the payment hash", func(t *testing.T) {
		header, wantHash := paymentCredentialHeader(
			t, mpp.IntentCharge,
		)

		tokenID, err := meteringTokenIDFromAuthHeader(&header)
		require.NoError(t, err)
		require.Equal(t, wantHash, tokenID)
	})

	t.Run("session is not metered here", func(t *testing.T) {
		// Sessions have their own draw-down accounting; double
		// metering a bearer request would charge the buyer twice.
		header, _ := paymentCredentialHeader(t, mpp.IntentSession)

		_, err := meteringTokenIDFromAuthHeader(&header)
		require.ErrorIs(t, err, errNoMeteredCredential)
	})

	t.Run("no credential at all is simply unmetered", func(t *testing.T) {
		header := make(http.Header)

		_, err := meteringTokenIDFromAuthHeader(&header)
		require.ErrorIs(t, err, errNoMeteredCredential)
	})

	t.Run("garbage payment credential is a hard error", func(t *testing.T) {
		// A malformed credential on a metered service must not become
		// free unmetered access.
		header := make(http.Header)
		header.Set("Authorization", "Payment not-base64url!!!")

		_, err := meteringTokenIDFromAuthHeader(&header)
		require.Error(t, err)
		require.NotErrorIs(t, err, errNoMeteredCredential)
	})
}

// TestCheckMeteredAccessPaymentCharge verifies the metering pipeline admits
// and annotates a request authenticated with a Payment charge credential,
// exactly as it does for an L402 token.
func TestCheckMeteredAccessPaymentCharge(t *testing.T) {
	t.Parallel()

	header, wantHash := paymentCredentialHeader(t, mpp.IntentCharge)

	newPaymentRequest := func() *http.Request {
		r := httptest.NewRequest(
			"POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"glm"}`),
		)
		r.Header.Set(
			"Authorization", header.Get("Authorization"),
		)

		return r
	}

	t.Run("charge request is annotated", func(t *testing.T) {
		fake := newFakeMeteredPricer()
		fake.authorizeResult = &pricer.AuthorizeResult{Allowed: true}
		svc := newMeteredService(fake)
		p := &Proxy{}

		w := httptest.NewRecorder()
		r, proceed := p.checkMeteredAccess(
			w, newPaymentRequest(), svc, "inference",
		)

		require.True(t, proceed)

		info, ok := r.Context().Value(
			meteringContextKey{},
		).(*meteringInfo)
		require.True(t, ok)
		require.Equal(t, wantHash, info.tokenID)
	})

	t.Run("session request passes through unmetered", func(t *testing.T) {
		sessHeader, _ := paymentCredentialHeader(
			t, mpp.IntentSession,
		)
		fake := newFakeMeteredPricer()
		svc := newMeteredService(fake)
		p := &Proxy{}

		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set(
			"Authorization", sessHeader.Get("Authorization"),
		)

		w := httptest.NewRecorder()
		annotated, proceed := p.checkMeteredAccess(
			w, r, svc, "inference",
		)

		require.True(t, proceed)

		_, ok := annotated.Context().Value(
			meteringContextKey{},
		).(*meteringInfo)
		require.False(t, ok)
	})

	t.Run("malformed payment credential is refused", func(t *testing.T) {
		fake := newFakeMeteredPricer()
		svc := newMeteredService(fake)
		p := &Proxy{}

		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Payment not-base64url!!!")

		w := httptest.NewRecorder()
		_, proceed := p.checkMeteredAccess(w, r, svc, "inference")

		require.False(t, proceed)
		require.Equal(t, http.StatusInternalServerError, w.Code)
	})
}

// TestMPPChargeHashesFromChallengeHeader verifies the mint-side booking key
// extraction: every well-formed Payment charge offer in a challenge header
// yields its payment hash, and everything else is skipped.
func TestMPPChargeHashesFromChallengeHeader(t *testing.T) {
	t.Parallel()

	var preimage lntypes.Preimage
	_, err := rand.Read(preimage[:])
	require.NoError(t, err)
	paymentHash := preimage.Hash()

	chargeReq := &mpp.ChargeRequest{
		Amount:   "2000",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1fake",
			PaymentHash: paymentHash.String(),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	t.Run("charge offer yields its payment hash", func(t *testing.T) {
		header := make(http.Header)
		header.Add(
			"WWW-Authenticate",
			`L402 macaroon="AGIAJEemVQUTEyNCR0exk7ek90Cg==", `+
				`invoice="lnbcrt1fake"`,
		)
		mpp.SetChallengeHeader(header, &mpp.ChallengeParams{
			ID:      "chal-id",
			Realm:   "test",
			Method:  mpp.MethodLightning,
			Intent:  mpp.IntentCharge,
			Request: encodedReq,
		})

		hashes := mppChargeHashesFromChallengeHeader(header)
		require.Equal(t, []string{paymentHash.String()}, hashes)
	})

	t.Run("session offers are skipped", func(t *testing.T) {
		header := make(http.Header)
		mpp.SetChallengeHeader(header, &mpp.ChallengeParams{
			ID:      "chal-id",
			Realm:   "test",
			Method:  mpp.MethodLightning,
			Intent:  mpp.IntentSession,
			Request: encodedReq,
		})

		require.Empty(t, mppChargeHashesFromChallengeHeader(header))
	})

	t.Run("no payment offer yields no hashes", func(t *testing.T) {
		header := make(http.Header)
		header.Add(
			"WWW-Authenticate",
			`L402 macaroon="AGIAJEemVQUTEyNCR0exk7ek90Cg==", `+
				`invoice="lnbcrt1fake"`,
		)

		require.Empty(t, mppChargeHashesFromChallengeHeader(header))
	})

	t.Run("offer with a bad hash is skipped", func(t *testing.T) {
		badReq, err := mpp.EncodeRequest(&mpp.ChargeRequest{
			Amount:   "2000",
			Currency: mpp.CurrencySat,
			MethodDetails: mpp.ChargeMethodDetails{
				Invoice:     "lnbcrt1fake",
				PaymentHash: "not-a-hash",
				Network:     "regtest",
			},
		})
		require.NoError(t, err)

		header := make(http.Header)
		mpp.SetChallengeHeader(header, &mpp.ChallengeParams{
			ID:      "chal-id",
			Realm:   "test",
			Method:  mpp.MethodLightning,
			Intent:  mpp.IntentCharge,
			Request: badReq,
		})

		require.Empty(t, mppChargeHashesFromChallengeHeader(header))
	})
}
