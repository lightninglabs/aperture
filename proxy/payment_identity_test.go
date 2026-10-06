package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

// paymentAuthenticator stands in for the MPP authenticator. Like the real one,
// it reads the credential from the first Authorization value only.
type paymentAuthenticator struct{}

func (paymentAuthenticator) Scheme() string {
	return auth.AuthSchemeMPP
}

func (paymentAuthenticator) Accept(header *http.Header, _ string) bool {
	_, err := mpp.ParseCredential(header)
	return err == nil
}

func (paymentAuthenticator) FreshChallengeHeader(_ string,
	_ int64) (http.Header, error) {

	return http.Header{}, nil
}

// meteringRecorder records the token each metered request is charged to.
type meteringRecorder struct {
	*fakeMeteredPricer
	tokenIDs []string
}

func (m *meteringRecorder) AuthorizeRequest(_ context.Context,
	_ *http.Request, tokenID, _ string) (*pricer.AuthorizeResult, error) {

	m.tokenIDs = append(m.tokenIDs, tokenID)
	return &pricer.AuthorizeResult{Allowed: true}, nil
}

func (m *meteringRecorder) ReportUsage(context.Context, *pricer.Usage) error {
	return nil
}

// newPaymentCredential returns the Authorization value of an MPP charge
// credential and the metering token it draws against.
func newPaymentCredential(t *testing.T) (string, string) {
	t.Helper()

	preimage := lntypes.Preimage{7}
	request, err := mpp.EncodeRequest(&mpp.ChargeRequest{
		Amount:   "1",
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			PaymentHash: preimage.Hash().String(),
		},
	})
	require.NoError(t, err)
	payload, err := json.Marshal(mpp.ChargePayload{
		Preimage: preimage.String(),
	})
	require.NoError(t, err)
	credential, err := json.Marshal(&mpp.Credential{
		Challenge: mpp.ChallengeEcho{
			ID:      "challenge",
			Realm:   "test",
			Method:  mpp.MethodLightning,
			Intent:  mpp.IntentCharge,
			Request: request,
		},
		Payload: payload,
	})
	require.NoError(t, err)

	return mpp.AuthScheme + " " + mpp.Base64URLEncode(credential),
		preimage.Hash().String()
}

// newPaymentProxy serves a metered service that accepts L402 and MPP, where
// only the given L402 tokens verify.
func newPaymentProxy(t *testing.T, limits []*RateLimitConfig,
	valid ...l402.TokenID) (*Proxy, <-chan *http.Request,
	*meteringRecorder) {

	t.Helper()

	l402Auth := &publicAuthRecorder{
		scheme:   auth.AuthSchemeL402,
		accepted: make(map[l402.TokenID]bool),
	}
	for _, id := range valid {
		l402Auth.accepted[id] = true
	}
	service := &Service{
		Auth:       "on",
		AuthScheme: auth.AuthSchemeL402MPP,
		RateLimits: limits,
	}
	p, received := newPublicAuthProxy(
		t, service,
		auth.NewMultiAuthenticator(l402Auth, paymentAuthenticator{}),
	)
	metering := &meteringRecorder{fakeMeteredPricer: newFakeMeteredPricer()}
	service.pricer = metering
	service.DynamicPrice.Enabled = true
	service.DynamicPrice.Metered = true

	return p, received, metering
}

// TestPaymentCredentialCarriesNoL402Identity checks that a request paid for
// with a Payment credential cannot also present an L402 that nothing verified.
// The MPP authenticator reads only the first Authorization value and the L402
// parser rejects a mix of schemes, yet a backend may still read the token. It
// must not be forwarded as the caller's identity, key the rate limit or be
// charged.
func TestPaymentCredentialCarriesNoL402Identity(t *testing.T) {
	payment, paymentTokenID := newPaymentCredential(t)
	victim := newPublicAuthToken(t, 9)

	for _, forged := range []struct {
		name  string
		value string
	}{
		{name: "L402", value: victim.l402Value},
		{name: "LSAT", value: victim.lsatValue},

		// An embedded token is also removed before forwarding.
		{name: "embedded L402", value: "Bearer " + victim.l402Value},
	} {
		t.Run(forged.name, func(t *testing.T) {
			p, received, metering := newPaymentProxy(t, nil)
			response := servePublicAuthRequest(
				p, "/resource", "192.0.2.1", http.Header{
					"Authorization": {
						payment, forged.value,
					},
				},
			)
			require.Equal(t, http.StatusNoContent, response.Code)

			forwarded := <-received
			require.Equal(
				t, []string{payment},
				forwarded.Header.Values("Authorization"),
			)
			require.Equal(
				t, []string{paymentTokenID}, metering.tokenIDs,
			)
		})
	}

	// The L402 parser rejects a mix of schemes, so even a valid L402 beside
	// a Payment value is unverified and removed.
	t.Run("valid L402 beside Payment", func(t *testing.T) {
		valid := newPublicAuthToken(t, 30)
		p, received, metering := newPaymentProxy(t, nil, valid.id)
		response := servePublicAuthRequest(
			p, "/resource", "192.0.2.1", http.Header{
				"Authorization": {payment, valid.l402Value},
			},
		)
		require.Equal(t, http.StatusNoContent, response.Code)

		forwarded := <-received
		require.Equal(
			t, []string{payment},
			forwarded.Header.Values("Authorization"),
		)
		require.Equal(
			t, []string{paymentTokenID}, metering.tokenIDs,
		)
	})

	// The macaroon headers lose their L402s too, while another service's
	// macaroon, such as lnd's, is forwarded for the backend to check.
	t.Run("macaroon headers", func(t *testing.T) {
		lndMacaroon := newLndMacaroon(t)
		p, received, metering := newPaymentProxy(t, nil)
		response := servePublicAuthRequest(
			p, "/resource", "192.0.2.1", http.Header{
				"Authorization":       {payment},
				l402.HeaderMacaroon:   {victim.macHex},
				l402.HeaderMacaroonMD: {lndMacaroon},
			},
		)
		require.Equal(t, http.StatusNoContent, response.Code)

		forwarded := <-received
		require.Equal(
			t, []string{payment},
			forwarded.Header.Values("Authorization"),
		)
		require.Empty(t, forwarded.Header.Values(l402.HeaderMacaroon))
		require.Equal(
			t, []string{lndMacaroon},
			forwarded.Header.Values(l402.HeaderMacaroonMD),
		)
		require.Equal(
			t, []string{paymentTokenID}, metering.tokenIDs,
		)
	})

	// Rotating forged tokens must not buy fresh rate limit buckets.
	t.Run("rate limit", func(t *testing.T) {
		p, _, _ := newPaymentProxy(t, []*RateLimitConfig{{
			Requests: 1,
			Per:      24 * time.Hour,
			Burst:    1,
		}})
		for i, status := range []int{
			http.StatusNoContent, http.StatusTooManyRequests,
		} {
			forged := newPublicAuthToken(t, byte(20+i))
			response := servePublicAuthRequest(
				p, "/resource", "192.0.2.1", http.Header{
					"Authorization": {
						payment, forged.l402Value,
					},
				},
			)
			require.Equal(t, status, response.Code)
		}
	})
}
