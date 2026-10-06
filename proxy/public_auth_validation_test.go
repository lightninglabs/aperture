package proxy_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// publicValidationSecretStore only implements lookup. Public requests must not
// mint or revoke credentials.
type publicValidationSecretStore struct {
	mint.SecretStore

	identifierHash [sha256.Size]byte
	key            [l402.SecretSize]byte
}

func (s *publicValidationSecretStore) GetSecret(_ context.Context,
	identifierHash [sha256.Size]byte) ([l402.SecretSize]byte, error) {

	if identifierHash != s.identifierHash {
		return [l402.SecretSize]byte{}, mint.ErrSecretNotFound
	}

	return s.key, nil
}

type publicValidationInvoiceChecker struct {
	paymentHash lntypes.Hash
	unsettled   bool
	calls       int
}

func (c *publicValidationInvoiceChecker) VerifyInvoiceStatus(hash lntypes.Hash,
	state lnrpc.Invoice_InvoiceState, _ time.Duration) error {

	c.calls++
	if hash != c.paymentHash || state != lnrpc.Invoice_SETTLED {
		return errors.New("unexpected invoice lookup")
	}
	if c.unsettled {
		return errors.New("invoice is not settled")
	}

	return nil
}

// TestPublicAuthenticationValidation verifies that a backend which extracts
// identity from L402 headers receives only credentials validated by Aperture.
func TestPublicAuthenticationValidation(t *testing.T) {
	var (
		rootKey  = [l402.SecretSize]byte{1}
		preimage = lntypes.Preimage{2}
		tokenID  = l402.TokenID{3}
		now      = func() time.Time { return time.Unix(1_800_000_000, 0) }
	)
	const serviceName = "public-quotes"
	identifier := l402.EncodeIdentifierBytes(preimage.Hash(), tokenID)

	tests := []struct {
		name             string
		forged           bool
		wrongPreimage    bool
		unsettled        bool
		expired          bool
		otherService     bool
		dynamic          bool
		protected        bool
		tokenService     string
		wantIdentity     bool
		wantInvoiceCalls int
	}{
		{
			name:             "valid paid token",
			wantIdentity:     true,
			wantInvoiceCalls: 1,
		},
		{
			name:             "valid dynamic resource token",
			dynamic:          true,
			wantIdentity:     true,
			wantInvoiceCalls: 1,
		},
		{
			name:      "dynamic token cannot authorize another resource",
			dynamic:   true,
			protected: true,
		},
		{
			name:         "dynamic token for another service",
			dynamic:      true,
			tokenService: "another-service",
		},
		{
			name:         "dynamic token sharing a name prefix",
			dynamic:      true,
			tokenService: serviceName + "x/paid",
		},
		{
			name:   "forged token claiming the same identity",
			forged: true,
		},
		{
			name:          "incorrect preimage",
			wrongPreimage: true,
		},
		{
			name:             "unsettled invoice",
			unsettled:        true,
			wantInvoiceCalls: 1,
		},
		{
			name:    "expired service caveat",
			expired: true,
		},
		{
			name:         "different service caveat",
			otherService: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// All tokens claim the same identity and payment. The
			// forged token differs only in its signing key.
			signingKey := rootKey
			if test.forged {
				signingKey[0]++
			}
			mac, err := macaroon.New(
				signingKey[:], identifier, "test",
				macaroon.LatestVersion,
			)
			require.NoError(t, err)

			allowedService := serviceName
			if test.dynamic {
				allowedService += "/paid"
			}
			if test.tokenService != "" {
				allowedService = test.tokenService
			}
			if test.otherService {
				allowedService = "another-service"
			}
			services, err := l402.NewServicesCaveat(l402.Service{
				Name: allowedService,
				Tier: l402.BaseTier,
			})
			require.NoError(t, err)
			timeout := int64(60)
			if test.expired {
				timeout = -1
			}
			require.NoError(t, l402.AddFirstPartyCaveats(
				mac, services,
				l402.NewTimeoutCaveat(allowedService, timeout, now),
			))

			checker := &publicValidationInvoiceChecker{
				paymentHash: preimage.Hash(),
				unsettled:   test.unsettled,
			}
			minter := mint.New(&mint.Config{
				Secrets: &publicValidationSecretStore{
					identifierHash: sha256.Sum256(identifier),
					key:            rootKey,
				},
				Now: now,
			})
			authenticator := auth.NewL402Authenticator(minter, checker)

			// Model a backend that parses an identity without
			// independently checking the macaroon's signature.
			seen := make(chan http.Header, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(
				w http.ResponseWriter, r *http.Request) {

				seen <- r.Header.Clone()
				mac, _, err := l402.FromHeader(&r.Header)
				if err == nil {
					id, err := l402.DecodeIdentifier(
						bytes.NewReader(mac.Id()),
					)
					if err != nil {
						w.WriteHeader(
							http.StatusInternalServerError,
						)
						return
					}
					w.Header().Set(
						"X-Backend-Token-ID", id.TokenID.String(),
					)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			t.Cleanup(backend.Close)

			service := &proxy.Service{
				Name:               serviceName,
				Address:            strings.TrimPrefix(backend.URL, "http://"),
				Protocol:           "http",
				HostRegexp:         ".*",
				PathRegexp:         "^/quote$",
				Auth:               "on",
				Price:              1,
				AuthWhitelistPaths: []string{"^/quote$"},
			}
			if test.protected {
				service.AuthWhitelistPaths = nil
				service.AuthSkipInvoiceCreationPaths = []string{
					"^/quote$",
				}
			}
			service.DynamicPrice.Enabled = test.dynamic
			service.DynamicPrice.Insecure = test.dynamic
			if test.dynamic {
				service.DynamicPrice.GRPCAddress = "unused"
			}
			p, err := proxy.New(
				authenticator, []*proxy.Service{service}, nil, nil,
			)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, p.Close()) })

			requestPreimage := preimage
			if test.wrongPreimage {
				requestPreimage[0]++
			}
			req := httptest.NewRequestWithContext(
				t.Context(), http.MethodGet,
				"http://example.com/quote", nil,
			)
			require.NoError(t, l402.SetHeader(
				&req.Header, mac, requestPreimage,
			))
			response := httptest.NewRecorder()
			p.ServeHTTP(response, req)

			if test.protected {
				require.Equal(
					t, http.StatusUnauthorized, response.Code,
				)
				require.Empty(t, seen)
				return
			}

			require.Equal(t, http.StatusNoContent, response.Code)
			require.Empty(t, response.Header().Values("WWW-Authenticate"))
			require.Equal(t, test.wantInvoiceCalls, checker.calls)
			if test.wantIdentity {
				require.Equal(t, tokenID.String(),
					response.Header().Get("X-Backend-Token-ID"))
				return
			}

			require.Empty(t, response.Header().Get("X-Backend-Token-ID"))
			select {
			case headers := <-seen:
				for _, name := range []string{
					l402.HeaderAuthorization,
					l402.HeaderMacaroon,
					l402.HeaderMacaroonMD,
				} {
					require.Empty(t, headers.Values(name))
				}
			default:
				t.Fatal("request did not reach the backend")
			}
		})
	}
}
