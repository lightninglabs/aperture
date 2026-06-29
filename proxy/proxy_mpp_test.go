package proxy_test

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightninglabs/aperture/proxy"
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
	p, err := proxy.New(mockAuth, services, "", []string{}, nil)
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

	p, err := proxy.New(mockAuth, services, "", []string{}, nil)
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
