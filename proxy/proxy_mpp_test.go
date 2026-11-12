package proxy_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

const (
	testMPPProxyAddr   = "localhost:10029"
	testMPPBackendAddr = "localhost:8094"
	testMPPBackendBody = "MPP Hello"
)

// mppMockAuthenticator is a mock that simulates both L402 and MPP
// authentication, implementing ReceiptProvider for MPP credentials.
type mppMockAuthenticator struct {
	hmacSecret    []byte
	settledHashes map[lntypes.Hash]bool
}

var _ auth.Authenticator = (*mppMockAuthenticator)(nil)
var _ auth.ReceiptProvider = (*mppMockAuthenticator)(nil)

func newMPPMockAuth() *mppMockAuthenticator {
	return &mppMockAuthenticator{
		hmacSecret:    []byte("test-hmac-secret-key-32-bytes!!"),
		settledHashes: make(map[lntypes.Hash]bool),
	}
}

func (m *mppMockAuthenticator) Accept(header *http.Header,
	_ string) bool {

	cred, err := mpp.ParseCredential(header)
	if err != nil {
		return false
	}

	if cred.Challenge.Method != mpp.MethodLightning {
		return false
	}

	params := cred.Challenge.ToChallengeParams()
	if !mpp.VerifyChallengeID(m.hmacSecret, params, cred.Challenge.ID) {
		return false
	}

	if cred.Challenge.Intent == mpp.IntentCharge {
		var payload mpp.ChargePayload
		if err := json.Unmarshal(cred.Payload, &payload); err != nil {
			return false
		}

		preimage, err := lntypes.MakePreimageFromStr(payload.Preimage)
		if err != nil {
			return false
		}

		var chargeReq mpp.ChargeRequest
		if err := mpp.DecodeRequest(
			cred.Challenge.Request, &chargeReq,
		); err != nil {
			return false
		}

		paymentHash, err := lntypes.MakeHashFromStr(
			chargeReq.MethodDetails.PaymentHash,
		)
		if err != nil {
			return false
		}

		return preimage.Matches(paymentHash) &&
			m.settledHashes[paymentHash]
	}

	return false
}

func (m *mppMockAuthenticator) FreshChallengeHeader(_ string,
	_ int64) (http.Header, error) {

	var preimage lntypes.Preimage
	rand.Read(preimage[:]) //nolint:errcheck
	paymentHash := sha256.Sum256(preimage[:])

	chargeReq := &mpp.ChargeRequest{
		Amount:   "100",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1pmocktest...",
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     "regtest",
		},
	}
	encodedReq, _ := mpp.EncodeRequest(chargeReq)

	params := &mpp.ChallengeParams{
		Realm:   "test.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
	}
	params.ID = mpp.ComputeChallengeID(m.hmacSecret, params)

	header := make(http.Header)
	header.Set("WWW-Authenticate",
		`LSAT macaroon="mock", invoice="lnbcrt..."`)
	header.Add("WWW-Authenticate",
		`L402 macaroon="mock", invoice="lnbcrt..."`)
	mpp.SetChallengeHeader(header, params)

	return header, nil
}

func (m *mppMockAuthenticator) ReceiptHeader(header *http.Header,
	_ string) http.Header {

	cred, err := mpp.ParseCredential(header)
	if err != nil {
		return nil
	}

	var chargeReq mpp.ChargeRequest
	if err := mpp.DecodeRequest(
		cred.Challenge.Request, &chargeReq,
	); err != nil {
		return nil
	}

	receipt := &mpp.Receipt{
		Status:      mpp.ReceiptStatusSuccess,
		Method:      mpp.MethodLightning,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Reference:   chargeReq.MethodDetails.PaymentHash,
		ChallengeID: cred.Challenge.ID,
	}

	receiptHeader := make(http.Header)
	mpp.SetReceiptHeader(receiptHeader, receipt) //nolint:errcheck
	return receiptHeader
}

// mppJoinedChallengeAuth emits its Payment challenge as the trailing element
// of a single comma-joined WWW-Authenticate field value, under a caller chosen
// spelling of the auth-scheme token.
//
// This is the shape a challenge takes once anything on the path folds repeated
// header lines into one field value, which RFC 9110 Section 5.3 permits, and
// it is what the proxy itself has to recognize before it decides whether the
// body is Problem Details JSON.
type mppJoinedChallengeAuth struct {
	*mppMockAuthenticator

	// scheme is the spelling of the Payment auth-scheme token to emit.
	scheme string
}

var _ auth.Authenticator = (*mppJoinedChallengeAuth)(nil)

func (m *mppJoinedChallengeAuth) FreshChallengeHeader(service string,
	price int64) (http.Header, error) {

	base, err := m.mppMockAuthenticator.FreshChallengeHeader(
		service, price,
	)
	if err != nil {
		return nil, err
	}

	// Lift the Payment challenge out of the mock's header and re-emit it
	// under the scheme spelling under test.
	var payment string
	for _, v := range base.Values("WWW-Authenticate") {
		if strings.HasPrefix(v, mpp.AuthScheme+" ") {
			payment = m.scheme +
				strings.TrimPrefix(v, mpp.AuthScheme)

			break
		}
	}
	if payment == "" {
		return nil, fmt.Errorf("mock emitted no Payment challenge")
	}

	header := make(http.Header)
	header.Set("WWW-Authenticate",
		`L402 macaroon="mock", invoice="lnbcrt...", `+payment)

	return header, nil
}

// TestProxyMPPChallengeNotFirstInValue verifies that the proxy still answers
// with a Problem Details body when the Payment challenge is not the first
// element of its WWW-Authenticate field value, and that it matches the
// auth-scheme token case-insensitively as RFC 9110 Section 11.1 requires.
func TestProxyMPPChallengeNotFirstInValue(t *testing.T) {
	testCases := []struct {
		name   string
		scheme string
	}{{
		name:   "canonical scheme",
		scheme: "Payment",
	}, {
		name:   "lowercase scheme",
		scheme: "payment",
	}, {
		name:   "mixed case scheme",
		scheme: "PaYmEnT",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockAuth := &mppJoinedChallengeAuth{
				mppMockAuthenticator: newMPPMockAuth(),
				scheme:               tc.scheme,
			}

			services := []*proxy.Service{{
				Address:    testMPPBackendAddr,
				HostRegexp: ".*",
				PathRegexp: "^/http/.*$",
				Protocol:   "http",
				Auth:       "on",
				Price:      100,
			}}

			p, err := proxy.New(mockAuth, services, nil, nil)
			require.NoError(t, err)

			req := httptest.NewRequest(
				"GET", "http://localhost/http/test", nil,
			)
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)

			resp := rec.Result()
			defer resp.Body.Close()

			require.Equal(t, http.StatusPaymentRequired,
				resp.StatusCode)

			// The challenge has to reach the client as one field
			// value that leads with a challenge for another
			// scheme, or the case under test is not the one being
			// exercised.
			values := resp.Header.Values("Www-Authenticate")
			require.Len(t, values, 1)
			require.True(t, strings.HasPrefix(values[0], "L402 "),
				"expected the L402 challenge first, got %q",
				values[0])
			require.Contains(t, values[0], tc.scheme+" ")

			// The Payment challenge is present, so the body must
			// be RFC 9457 Problem Details rather than the plain
			// text of a bare L402 response.
			require.Equal(t, mpp.ProblemContentType,
				resp.Header.Get("Content-Type"))

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			var problem mpp.ProblemDetails
			require.NoError(t, json.Unmarshal(body, &problem))
			require.Equal(t, mpp.ProblemPaymentRequired,
				problem.Type)
			require.Equal(t, http.StatusPaymentRequired,
				problem.Status)
		})
	}
}

// mppProblemAuth reports a fixed problem type for every Payment credential the
// wrapped mock refuses, the way a ProblemAuthenticator names why it refused.
type mppProblemAuth struct {
	*mppMockAuthenticator

	// problem is the problem type reported for a refused credential.
	problem string
}

var _ auth.ProblemAuthenticator = (*mppProblemAuth)(nil)

func (m *mppProblemAuth) AcceptWithProblem(header *http.Header,
	service string) (bool, string) {

	if m.Accept(header, service) {
		return true, ""
	}

	_, err := mpp.ParseCredential(header)
	if errors.Is(err, mpp.ErrNoCredential) {
		return false, ""
	}

	return false, m.problem
}

// TestProxyMPPRefusalProblemType verifies that a 402 answering a refused
// Payment credential carries the problem type the authenticator reported, both
// when the authenticator is used directly and behind a MultiAuthenticator, and
// that a request without a credential still gets payment-required.
func TestProxyMPPRefusalProblemType(t *testing.T) {
	newDirect := func(problemAuth *mppProblemAuth) auth.Authenticator {
		return problemAuth
	}
	newMulti := func(problemAuth *mppProblemAuth) auth.Authenticator {
		return auth.NewMultiAuthenticator(problemAuth)
	}

	testCases := []struct {
		name    string
		newAuth func(*mppProblemAuth) auth.Authenticator
		authz   string
		problem string
	}{{
		name:    "direct, no credential",
		newAuth: newDirect,
		problem: mpp.ProblemPaymentRequired,
	}, {
		name:    "direct, refused credential",
		newAuth: newDirect,
		authz:   mpp.AuthScheme + " %%%",
		problem: mpp.ProblemPaymentExpired,
	}, {
		name:    "multi, no credential",
		newAuth: newMulti,
		problem: mpp.ProblemPaymentRequired,
	}, {
		name:    "multi, refused credential",
		newAuth: newMulti,
		authz:   mpp.AuthScheme + " %%%",
		problem: mpp.ProblemPaymentExpired,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			problemAuth := &mppProblemAuth{
				mppMockAuthenticator: newMPPMockAuth(),
				problem:              mpp.ProblemPaymentExpired,
			}

			services := []*proxy.Service{{
				Address:    testMPPBackendAddr,
				HostRegexp: ".*",
				PathRegexp: "^/http/.*$",
				Protocol:   "http",
				Auth:       "on",
				Price:      100,
			}}

			p, err := proxy.New(
				tc.newAuth(problemAuth), services, nil, nil,
			)
			require.NoError(t, err)

			req := httptest.NewRequest(
				"GET", "http://localhost/http/test", nil,
			)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)

			resp := rec.Result()
			defer resp.Body.Close()

			require.Equal(t, http.StatusPaymentRequired,
				resp.StatusCode)
			require.Equal(t, mpp.ProblemContentType,
				resp.Header.Get("Content-Type"))

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			var problem mpp.ProblemDetails
			require.NoError(t, json.Unmarshal(body, &problem))
			require.Equal(t, tc.problem, problem.Type)
			require.Equal(t, http.StatusPaymentRequired,
				problem.Status)
		})
	}
}

// refusalTestChallenger mints invoices for the refusal test and remembers the
// preimage of each, standing in for the buyer that pays them.
type refusalTestChallenger struct {
	mu        sync.Mutex
	preimages map[lntypes.Hash]lntypes.Preimage
}

func (c *refusalTestChallenger) NewChallenge(int64) (string, lntypes.Hash,
	error) {

	var preimage lntypes.Preimage
	if _, err := rand.Read(preimage[:]); err != nil {
		return "", lntypes.Hash{}, err
	}
	hash := lntypes.Hash(sha256.Sum256(preimage[:]))

	c.mu.Lock()
	c.preimages[hash] = preimage
	c.mu.Unlock()

	return "lnbcrt1000n1pmocktest...", hash, nil
}

func (c *refusalTestChallenger) Stop() {}

// refusalTestChecker reports every invoice as settled.
type refusalTestChecker struct{}

func (refusalTestChecker) VerifyInvoiceStatus(lntypes.Hash,
	lnrpc.Invoice_InvoiceState, time.Duration) error {

	return nil
}

// refusalTestStore is an in-memory charge store.
type refusalTestStore struct {
	mu       sync.Mutex
	consumed map[lntypes.Hash]bool
}

func (s *refusalTestStore) ConsumeCharge(_ context.Context,
	paymentHash lntypes.Hash, _ string, _ time.Time) (bool, error) {

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.consumed[paymentHash] {
		return false, nil
	}
	s.consumed[paymentHash] = true

	return true, nil
}

func (s *refusalTestStore) PruneConsumedCharges(context.Context,
	time.Time) (int64, error) {

	return 0, nil
}

// TestProxyMPPChargeRefusalProblemTypes runs the charge authenticator behind a
// MultiAuthenticator, as aperture composes it, and verifies that each refused
// credential is answered with the problem type the status code table of
// draft-httpauth-payment-00 gives it.
func TestProxyMPPChargeRefusalProblemTypes(t *testing.T) {
	secret := []byte("test-hmac-secret-key-32-bytes!!")
	challenger := &refusalTestChallenger{
		preimages: make(map[lntypes.Hash]lntypes.Preimage),
	}
	mppAuth, err := auth.NewMPPAuthenticator(
		challenger, refusalTestChecker{}, "test.example.com", secret,
		"regtest", nil,
		&refusalTestStore{consumed: make(map[lntypes.Hash]bool)},
	)
	require.NoError(t, err)

	backend := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, testMPPBackendBody)
		},
	))
	defer backend.Close()

	services := []*proxy.Service{{
		Name:       "test-service",
		Address:    backend.Listener.Addr().String(),
		HostRegexp: ".*",
		PathRegexp: "^/http/.*$",
		Protocol:   "http",
		Auth:       "on",
		Price:      100,
	}}
	p, err := proxy.New(
		auth.NewMultiAuthenticator(mppAuth), services, nil, nil,
	)
	require.NoError(t, err)

	// send makes a request with the given Authorization value and
	// returns the status code and the problem type of the response.
	send := func(authz string) (int, string, http.Header) {
		req := httptest.NewRequest(
			"GET", "http://localhost/http/test", nil,
		)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		resp := rec.Result()
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusPaymentRequired {
			return resp.StatusCode, "", resp.Header
		}

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var problem mpp.ProblemDetails
		require.NoError(t, json.Unmarshal(body, &problem))

		return resp.StatusCode, problem.Type, resp.Header
	}

	// challenge returns a freshly minted Payment challenge.
	challenge := func() *mpp.ChallengeParams {
		status, problem, header := send("")
		require.Equal(t, http.StatusPaymentRequired, status)
		require.Equal(t, mpp.ProblemPaymentRequired, problem)

		challenges, err := mpp.ParseChallengeHeaders(header)
		require.NoError(t, err)
		require.Len(t, challenges, 1)

		return challenges[0]
	}

	// credential answers the challenge with the given preimage.
	credential := func(c *mpp.ChallengeParams,
		preimage lntypes.Preimage) string {

		payload, err := json.Marshal(mpp.ChargePayload{
			Preimage: hex.EncodeToString(preimage[:]),
		})
		require.NoError(t, err)

		credJSON, err := json.Marshal(&mpp.Credential{
			Challenge: mpp.ChallengeEcho{
				ID:      c.ID,
				Realm:   c.Realm,
				Method:  c.Method,
				Intent:  c.Intent,
				Request: c.Request,
				Expires: c.Expires,
				Opaque:  c.Opaque,
			},
			Payload: payload,
		})
		require.NoError(t, err)

		return mpp.AuthScheme + " " + mpp.Base64URLEncode(credJSON)
	}

	// paid returns the preimage of the challenge's invoice.
	paid := func(c *mpp.ChallengeParams) lntypes.Preimage {
		var chargeReq mpp.ChargeRequest
		require.NoError(t, mpp.DecodeRequest(c.Request, &chargeReq))

		hash, err := lntypes.MakeHashFromStr(
			chargeReq.MethodDetails.PaymentHash,
		)
		require.NoError(t, err)

		challenger.mu.Lock()
		defer challenger.mu.Unlock()

		return challenger.preimages[hash]
	}

	// A paid credential is served once. Presenting it again names the
	// challenge as already used.
	c := challenge()
	valid := credential(c, paid(c))

	status, _, _ := send(valid)
	require.Equal(t, http.StatusOK, status)

	status, problem, _ := send(valid)
	require.Equal(t, http.StatusPaymentRequired, status)
	require.Equal(t, mpp.ProblemInvalidChallenge, problem)

	// A challenge past its expiry, bound with the server secret so only
	// the expiry is wrong.
	c = challenge()
	c.Expires = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	c.ID = mpp.ComputeChallengeID(secret, c)

	_, problem, _ = send(credential(c, paid(c)))
	require.Equal(t, mpp.ProblemPaymentExpired, problem)

	// A preimage that does not belong to the invoice.
	var wrongPreimage lntypes.Preimage
	_, err = rand.Read(wrongPreimage[:])
	require.NoError(t, err)

	_, problem, _ = send(credential(challenge(), wrongPreimage))
	require.Equal(t, mpp.ProblemVerificationFailed, problem)

	// A credential that is not base64url.
	_, problem, _ = send(mpp.AuthScheme + " %%%")
	require.Equal(t, mpp.ProblemMalformedCredential, problem)
}

// TestProxyMPP402Response verifies that the proxy returns correct 402
// responses with both L402 and Payment scheme challenges, Cache-Control:
// no-store, and RFC 9457 Problem Details JSON body.
func TestProxyMPP402Response(t *testing.T) {
	proxyAddr := testMPPProxyAddr
	backendAddr := testMPPBackendAddr

	services := []*proxy.Service{{
		Address:    backendAddr,
		HostRegexp: "^localhost:.*$",
		PathRegexp: "^/http/.*$",
		Protocol:   "http",
		Auth:       "on",
		Price:      100,
	}}

	mockAuth := newMPPMockAuth()
	p, err := proxy.New(mockAuth, services, []string{}, nil)
	require.NoError(t, err)

	server := &http.Server{
		Addr:    proxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			t.Errorf("Error serving: %v", err)
		}
	}()
	defer closeOrFail(t, server)

	backendService := &http.Server{
		Addr: backendAddr,
		Handler: http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, testMPPBackendBody)
			},
		),
	}
	go func() {
		if err := backendService.ListenAndServe(); err != http.ErrServerClosed {
			t.Errorf("Backend error: %v", err)
		}
	}()
	defer closeOrFail(t, backendService)

	time.Sleep(100 * time.Millisecond)

	// Request without auth should get 402.
	resp, err := http.Get(
		fmt.Sprintf("http://%s/http/test", proxyAddr),
	)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equalf(t, http.StatusPaymentRequired, resp.StatusCode,
		"unexpected status, body: %s", string(body))

	// Should have Cache-Control: no-store.
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))

	// Should have multiple WWW-Authenticate headers.
	authHeaders := resp.Header.Values("Www-Authenticate")
	require.GreaterOrEqual(t, len(authHeaders), 3,
		"expected at least 3 WWW-Authenticate headers "+
			"(LSAT, L402, Payment)")

	hasLSAT := false
	hasL402 := false
	hasPayment := false
	for _, h := range authHeaders {
		if strings.HasPrefix(h, "LSAT ") {
			hasLSAT = true
		}
		if strings.HasPrefix(h, "L402 ") {
			hasL402 = true
		}
		if strings.HasPrefix(h, "Payment ") {
			hasPayment = true
		}
	}
	require.True(t, hasLSAT, "missing LSAT challenge")
	require.True(t, hasL402, "missing L402 challenge")
	require.True(t, hasPayment, "missing Payment challenge")

	// Should have Problem Details JSON body. Re-read body since we
	// already consumed it above for debugging.
	// body was already read above for the status assertion.

	require.Equal(t, mpp.ProblemContentType,
		resp.Header.Get("Content-Type"))

	var problem mpp.ProblemDetails
	require.NoError(t, json.Unmarshal(body, &problem))
	require.Equal(t, mpp.ProblemPaymentRequired, problem.Type)
	require.Equal(t, 402, problem.Status)
	require.Equal(t, "Payment Required", problem.Title)

	// Parse the Payment challenge and verify structure.
	var paymentChallenge string
	for _, h := range authHeaders {
		if strings.HasPrefix(h, "Payment ") {
			paymentChallenge = h
			break
		}
	}
	params, err := mpp.ParseChallengeHeader(paymentChallenge)
	require.NoError(t, err)
	require.NotEmpty(t, params.ID)
	require.NotEmpty(t, params.Realm)
	require.Equal(t, mpp.MethodLightning, params.Method)
	require.Equal(t, mpp.IntentCharge, params.Intent)
	require.NotEmpty(t, params.Request)

	// Verify HMAC binding.
	require.True(t, mpp.VerifyChallengeID(
		mockAuth.hmacSecret, params, params.ID,
	))

	// CORS should expose Payment-Receipt.
	exposeHeaders := resp.Header.Get("Access-Control-Expose-Headers")
	require.Contains(t, exposeHeaders, "Payment-Receipt")
}

// TestProxyMPPChargeEndToEnd tests the full charge flow: 402 challenge → pay
// → credential → 200 with Payment-Receipt and Cache-Control: private.
func TestProxyMPPChargeEndToEnd(t *testing.T) {
	proxyAddr := "localhost:10039"
	backendAddr := "localhost:8099"

	var preimage lntypes.Preimage
	_, err := rand.Read(preimage[:])
	require.NoError(t, err)
	paymentHash := sha256.Sum256(preimage[:])

	services := []*proxy.Service{{
		Address:    backendAddr,
		HostRegexp: "^localhost:.*$",
		PathRegexp: "^/http/.*$",
		Protocol:   "http",
		Auth:       "on",
		Price:      100,
	}}

	mockAuth := newMPPMockAuth()
	mockAuth.settledHashes[paymentHash] = true

	p, err := proxy.New(mockAuth, services, []string{}, nil)
	require.NoError(t, err)

	server := &http.Server{
		Addr:    proxyAddr,
		Handler: http.HandlerFunc(p.ServeHTTP),
	}
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			t.Errorf("Error serving: %v", err)
		}
	}()
	defer closeOrFail(t, server)

	backendService := &http.Server{
		Addr: backendAddr,
		Handler: http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, testMPPBackendBody)
			},
		),
	}
	go func() {
		if err := backendService.ListenAndServe(); err != http.ErrServerClosed {
			t.Errorf("Backend error: %v", err)
		}
	}()
	defer closeOrFail(t, backendService)

	time.Sleep(100 * time.Millisecond)

	// Build a valid MPP charge credential.
	chargeReq := &mpp.ChargeRequest{
		Amount:   "100",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbcrt1000n1pmocktest...",
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     "regtest",
		},
	}
	encodedReq, err := mpp.EncodeRequest(chargeReq)
	require.NoError(t, err)

	params := &mpp.ChallengeParams{
		Realm:   "test.example.com",
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedReq,
	}
	params.ID = mpp.ComputeChallengeID(mockAuth.hmacSecret, params)

	payload := mpp.ChargePayload{
		Preimage: hex.EncodeToString(preimage[:]),
	}
	payloadJSON, err := json.Marshal(payload)
	require.NoError(t, err)

	cred := &mpp.Credential{
		Challenge: mpp.ChallengeEcho{
			ID:      params.ID,
			Realm:   params.Realm,
			Method:  params.Method,
			Intent:  params.Intent,
			Request: params.Request,
		},
		Payload: json.RawMessage(payloadJSON),
	}
	credJSON, err := json.Marshal(cred)
	require.NoError(t, err)

	// Send authenticated request.
	req, err := http.NewRequest(
		"GET",
		fmt.Sprintf("http://%s/http/test", proxyAddr),
		nil,
	)
	require.NoError(t, err)
	req.Header.Set("Authorization",
		mpp.AuthScheme+" "+mpp.Base64URLEncode(credJSON))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should get 200 OK.
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Should have Payment-Receipt header.
	receiptHdr := resp.Header.Get("Payment-Receipt")
	require.NotEmpty(t, receiptHdr, "missing Payment-Receipt header")

	// Parse the receipt and verify fields per spec.
	receipt, err := mpp.ParseReceiptHeader(resp.Header)
	require.NoError(t, err)
	require.Equal(t, mpp.ReceiptStatusSuccess, receipt.Status)
	require.Equal(t, mpp.MethodLightning, receipt.Method)
	require.Equal(t, hex.EncodeToString(paymentHash[:]),
		receipt.Reference)
	require.NotEmpty(t, receipt.Timestamp)

	// Should have Cache-Control: private per spec Section 11.10.
	require.Equal(t, "private", resp.Header.Get("Cache-Control"))

	// Read the body - should be the backend response.
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, testMPPBackendBody, string(body))
}
