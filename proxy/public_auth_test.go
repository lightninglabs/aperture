package proxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// publicAuthToken contains the wire encodings of a token with a real L402
// identifier and a preimage caveat for the direct macaroon headers.
type publicAuthToken struct {
	id         l402.TokenID
	identifier []byte
	preimage   lntypes.Preimage
	header     http.Header
	macHex     string
	l402Value  string
	lsatValue  string
}

// newPublicAuthToken constructs a distinct token for each seed.
func newPublicAuthToken(t *testing.T, seed byte) publicAuthToken {
	t.Helper()

	var preimage lntypes.Preimage
	preimage[0] = seed
	var tokenID l402.TokenID
	tokenID[0] = seed
	identifier := l402.EncodeIdentifierBytes(preimage.Hash(), tokenID)

	mac, err := macaroon.New(
		[]byte("test root key"), identifier, "test",
		macaroon.LatestVersion,
	)
	require.NoError(t, err)
	require.NoError(t, l402.AddFirstPartyCaveats(mac, l402.Caveat{
		Condition: l402.PreimageKey,
		Value:     preimage.String(),
	}))
	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)
	header := make(http.Header)
	require.NoError(t, l402.SetHeader(&header, mac, preimage))
	values := header.Values(l402.HeaderAuthorization)

	return publicAuthToken{
		id:         tokenID,
		identifier: identifier,
		preimage:   preimage,
		header:     header,
		macHex:     hex.EncodeToString(macBytes),
		l402Value:  values[1],
		lsatValue:  values[0],
	}
}

// newMacaroonHex returns a macaroon in the hex form the Macaroon and
// Grpc-Metadata-Macaroon headers carry.
func newMacaroonHex(t *testing.T, rootKey string, id []byte,
	caveats ...l402.Caveat) string {

	t.Helper()

	mac, err := macaroon.New(
		[]byte(rootKey), id, "test", macaroon.LatestVersion,
	)
	require.NoError(t, err)
	require.NoError(t, l402.AddFirstPartyCaveats(mac, caveats...))
	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)

	return hex.EncodeToString(macBytes)
}

// newLndMacaroon returns a macaroon of the kind lnd mints: its identifier
// starts with a macaroon-bakery version byte instead of an L402 version.
func newLndMacaroon(t *testing.T) string {
	t.Helper()

	return newMacaroonHex(t, "lnd root key", []byte{3, 1, 2, 3})
}

// publicAuthRecorder records authentication attempts and accepts only the
// configured token IDs. The MPP instance records attempts without accepting.
type publicAuthRecorder struct {
	scheme     string
	accepted   map[l402.TokenID]bool
	headers    []http.Header
	resources  []string
	challenges int
	receipts   int
}

func (a *publicAuthRecorder) Scheme() string {
	return a.scheme
}

func (a *publicAuthRecorder) Accept(header *http.Header,
	resource string) bool {

	a.headers = append(a.headers, header.Clone())
	a.resources = append(a.resources, resource)
	if a.scheme != auth.AuthSchemeL402 {
		return false
	}

	mac, _, err := l402.FromHeader(header)
	if err != nil {
		return false
	}
	id, err := l402.DecodeIdentifier(bytes.NewReader(mac.Id()))
	return err == nil && a.accepted[id.TokenID]
}

func (a *publicAuthRecorder) FreshChallengeHeader(_ string,
	_ int64) (http.Header, error) {

	a.challenges++
	return http.Header{
		"Www-Authenticate": {`L402 macaroon="test", invoice="test"`},
	}, nil
}

func (a *publicAuthRecorder) ReceiptHeader(_ *http.Header,
	_ string) http.Header {

	a.receipts++
	return http.Header{"Payment-Receipt": {"unexpected receipt"}}
}

// publicAuthPricer records pricing and authorization calls while reusing the
// metering test's usage recorder.
type publicAuthPricer struct {
	*fakeMeteredPricer
	prices         int
	authorizations int
}

func (p *publicAuthPricer) GetPrice(_ context.Context,
	_ *http.Request) (int64, error) {

	p.prices++
	return 1, nil
}

func (p *publicAuthPricer) AuthorizeRequest(_ context.Context,
	_ *http.Request, _, _ string) (*pricer.AuthorizeResult, error) {

	p.authorizations++
	return &pricer.AuthorizeResult{Allowed: true}, nil
}

// newPublicAuthProxy creates a real reverse proxy and captures each request
// received by its backend, including all header values.
func newPublicAuthProxy(t *testing.T, service *Service,
	authenticator auth.Authenticator) (*Proxy, <-chan *http.Request) {

	t.Helper()

	received := make(chan *http.Request, 16)
	backend := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			received <- r.Clone(r.Context())
			w.WriteHeader(http.StatusNoContent)
		},
	))
	t.Cleanup(backend.Close)

	service.Name = "test-service"
	service.HostRegexp = ".*"
	service.Address = strings.TrimPrefix(backend.URL, "http://")
	service.Protocol = "http"
	p, err := New(authenticator, []*Service{service}, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, p.Close())
	})

	return p, received
}

// servePublicAuthRequest sends a request through ServeHTTP without starting a
// frontend listener. The backend still receives an actual HTTP request.
func servePublicAuthRequest(p *Proxy, path, remoteIP string,
	header http.Header) *httptest.ResponseRecorder {

	req := httptest.NewRequest(http.MethodGet, "http://proxy"+path, nil)
	req.RemoteAddr = remoteIP + ":1234"
	if header != nil {
		req.Header = header.Clone()
	}
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, req)
	return recorder
}

// TestPublicPathAuthentication checks which credentials survive a whitelist
// request, including ambiguous inputs that the L402 parser rejects.
func TestPublicPathAuthentication(t *testing.T) {
	valid := newPublicAuthToken(t, 1)
	forged := newPublicAuthToken(t, 2)
	tests := []struct {
		name       string
		authValues []string
		macaroon   string
		metadata   string
		verified   bool
	}{
		{name: "anonymous"},
		{
			name:       "valid L402",
			authValues: []string{valid.l402Value},
			verified:   true,
		},
		{
			name:       "valid LSAT",
			authValues: []string{valid.lsatValue},
			verified:   true,
		},
		{
			name:     "direct macaroon",
			macaroon: valid.macHex,
			verified: true,
		},
		{
			name:     "metadata macaroon",
			metadata: valid.macHex,
			verified: true,
		},
		{
			name:       "forged L402",
			authValues: []string{forged.l402Value},
		},
		{
			name:       "malformed L402",
			authValues: []string{"L402 garbage"},
		},
		{
			name:       "bare LSAT",
			authValues: []string{"LSAT"},
		},
		{
			name:       "lowercase scheme",
			authValues: []string{"l402 garbage"},
		},
		{
			name:     "malformed direct headers",
			macaroon: "garbage",
			metadata: "garbage",
		},
		{
			name:       "Payment is never spent",
			authValues: []string{"Payment credential"},
		},
		{
			name:       "mixed case Payment",
			authValues: []string{"pAyMeNt credential"},
		},
		{
			name:       "Bearer removed",
			authValues: []string{"Bearer backend-token"},
		},
		{
			name:       "Basic removed",
			authValues: []string{"Basic dXNlcjpwYXNz"},
		},
		{
			name: "Bearer followed by valid L402",
			authValues: []string{
				"Bearer backend-token", valid.l402Value,
			},
		},
		{
			name: "valid L402 followed by Bearer",
			authValues: []string{
				valid.l402Value, "Bearer backend-token",
			},
		},
		{
			name: "forged followed by valid L402",
			authValues: []string{
				forged.l402Value, valid.l402Value,
			},
		},
		{
			name: "valid followed by forged L402",
			authValues: []string{
				valid.l402Value, forged.l402Value,
			},
		},
		{
			name:       "valid L402 with shadow macaroons",
			authValues: []string{valid.l402Value},
			macaroon:   forged.macHex,
			metadata:   forged.macHex,
			verified:   true,
		},
		{
			name:       "forged L402 shadows valid macaroon",
			authValues: []string{forged.l402Value},
			macaroon:   valid.macHex,
		},
		{
			name:       "Bearer shadows direct macaroon",
			authValues: []string{"Bearer backend-token"},
			macaroon:   valid.macHex,
		},
		{
			name:     "metadata shadows direct macaroon",
			macaroon: valid.macHex,
			metadata: forged.macHex,
		},
		{
			name: "valid embedded L402",
			authValues: []string{
				"Bearer " + valid.l402Value,
			},
		},
		{
			name: "forged embedded L402",
			authValues: []string{
				"Bearer " + forged.l402Value,
			},
		},
		{
			name: "Payment followed by valid L402",
			authValues: []string{
				"Payment credential", valid.l402Value,
			},
		},
		{
			name: "valid L402 followed by Payment",
			authValues: []string{
				valid.l402Value, "Payment credential",
			},
		},
		{
			name: "empty value between valid credentials",
			authValues: []string{
				valid.l402Value, "", valid.l402Value,
			},
		},
		{
			name:       "empty authorization with macaroon",
			authValues: []string{""},
			macaroon:   valid.macHex,
		},
	}

	for _, level := range []auth.Level{"on", "freebie 1"} {
		for _, tc := range tests {
			t.Run(string(level)+"/"+tc.name, func(t *testing.T) {
				l402Auth := &publicAuthRecorder{
					scheme: auth.AuthSchemeL402,
					accepted: map[l402.TokenID]bool{
						valid.id: true,
					},
				}
				mppAuth := &publicAuthRecorder{
					scheme: auth.AuthSchemeMPP,
				}
				// Try MPP first to catch credentials leaking
				// into a payment-consuming authenticator.
				multi := auth.NewMultiAuthenticator(
					mppAuth, l402Auth,
				)
				service := &Service{
					Auth:       level,
					AuthScheme: auth.AuthSchemeL402MPP,
					AuthWhitelistPaths: []string{
						"^/public$",
					},
					AuthSkipInvoiceCreationPaths: []string{
						"^/public$",
					},
				}
				p, received := newPublicAuthProxy(
					t, service, multi,
				)
				header := make(http.Header)
				for _, value := range tc.authValues {
					header.Add("Authorization", value)
				}
				if tc.macaroon != "" {
					header.Set("Macaroon", tc.macaroon)
				}
				if tc.metadata != "" {
					header.Set(
						l402.HeaderMacaroonMD,
						tc.metadata,
					)
				}
				response := servePublicAuthRequest(
					p, "/public", "192.0.2.1", header,
				)
				require.Equal(
					t, http.StatusNoContent, response.Code,
				)
				resHeader := response.Header()
				challenge := resHeader.Get("WWW-Authenticate")
				require.Empty(t, challenge)
				require.Zero(t, l402Auth.challenges)
				require.Zero(t, mppAuth.challenges)
				require.Zero(t, l402Auth.receipts)
				require.Zero(t, mppAuth.receipts)

				forwarded := <-received
				expected := make(http.Header)
				if tc.verified {
					expected = header.Clone()
					expected["Authorization"] = []string{
						valid.lsatValue,
						valid.l402Value,
					}

					// Every macaroon in these cases is an L402,
					// and only the field the verified credential
					// was read from keeps its own.
					read := l402.CredentialHeader(&header)
					for _, name := range []string{
						l402.HeaderMacaroon,
						l402.HeaderMacaroonMD,
					} {
						if name != read {
							expected.Del(name)
						}
					}
				}
				for _, name := range []string{
					"Authorization", "Macaroon",
					l402.HeaderMacaroonMD,
				} {
					require.Equal(
						t, expected.Values(name),
						forwarded.Header.Values(name),
					)
				}

				require.Len(t, l402Auth.headers, 1)
				require.Equal(t, header, l402Auth.headers[0])
				require.Empty(t, mppAuth.headers)
				for _, resource := range l402Auth.resources {
					require.Equal(t, service.Name, resource)
				}
			})
		}
	}
}

// TestPublicPathDoesNotCharge checks that public access never asks a metered
// pricer to authorize or bill a request, even with a verified L402 token.
func TestPublicPathDoesNotCharge(t *testing.T) {
	valid := newPublicAuthToken(t, 1)
	a := &publicAuthRecorder{
		scheme:   auth.AuthSchemeL402,
		accepted: map[l402.TokenID]bool{valid.id: true},
	}
	service := &Service{
		Auth:               "on",
		AuthWhitelistPaths: []string{"^/public$"},
	}
	p, _ := newPublicAuthProxy(t, service, a)
	price := &publicAuthPricer{fakeMeteredPricer: newFakeMeteredPricer()}
	service.pricer = price
	service.DynamicPrice.Enabled = true
	service.DynamicPrice.Metered = true

	for _, header := range []http.Header{
		nil, valid.header, {"Authorization": {"Payment credential"}},
	} {
		response := servePublicAuthRequest(
			p, "/public", "192.0.2.1", header,
		)
		require.Equal(t, http.StatusNoContent, response.Code)
		require.Empty(t, response.Header().Get("Payment-Receipt"))
	}
	require.Zero(t, price.prices)
	require.Zero(t, price.authorizations)
	require.Empty(t, price.usageReports)
	require.Empty(t, price.mintedTokens)
	require.Zero(t, a.receipts)
	require.Zero(t, a.challenges)
	require.Len(t, a.resources, 3)
	for _, resource := range a.resources {
		require.Equal(t, service.Name+"/public", resource)
	}
}

// TestPublicPathServiceSettings checks the service-level authentication
// controls and the backend's configured credentials and path rewrite.
func TestPublicPathServiceSettings(t *testing.T) {
	valid := newPublicAuthToken(t, 1)
	for _, scheme := range []string{
		"", auth.AuthSchemeL402, auth.AuthSchemeMPP,
	} {
		t.Run("scheme="+scheme, func(t *testing.T) {
			l402Auth := &publicAuthRecorder{
				scheme: auth.AuthSchemeL402,
				accepted: map[l402.TokenID]bool{
					valid.id: true,
				},
			}
			mppAuth := &publicAuthRecorder{
				scheme: auth.AuthSchemeMPP,
			}
			service := &Service{
				Auth:               "on",
				AuthScheme:         scheme,
				AuthWhitelistPaths: []string{"^/public$"},
			}
			p, received := newPublicAuthProxy(
				t, service,
				auth.NewMultiAuthenticator(l402Auth, mppAuth),
			)
			response := servePublicAuthRequest(
				p, "/public", "192.0.2.1", valid.header,
			)
			require.Equal(t, http.StatusNoContent, response.Code)
			forwarded := <-received
			values := forwarded.Header.Values("Authorization")
			require.Empty(t, mppAuth.headers)
			if scheme == auth.AuthSchemeMPP {
				require.Empty(t, values)
				require.Empty(t, l402Auth.headers)
			} else {
				require.Equal(
					t, valid.header.Values("Authorization"),
					values,
				)
				require.Len(t, l402Auth.headers, 1)
			}
		})
	}

	t.Run("standalone MPP authenticator", func(t *testing.T) {
		a := &publicAuthRecorder{scheme: auth.AuthSchemeMPP}
		service := &Service{
			Auth:               "on",
			AuthWhitelistPaths: []string{"^/public$"},
		}
		p, received := newPublicAuthProxy(t, service, a)
		response := servePublicAuthRequest(
			p, "/public", "192.0.2.1",
			http.Header{"Authorization": {"Payment credential"}},
		)
		require.Equal(t, http.StatusNoContent, response.Code)
		require.Empty(t, a.headers)
		forwarded := <-received
		require.Empty(t, forwarded.Header.Values("Authorization"))
	})

	for _, level := range []auth.Level{"off", "false"} {
		t.Run(string(level), func(t *testing.T) {
			a := &publicAuthRecorder{scheme: auth.AuthSchemeL402}
			service := &Service{
				Auth:               level,
				AuthWhitelistPaths: []string{"^/public$"},
			}
			p, received := newPublicAuthProxy(t, service, a)
			header := http.Header{
				"Authorization":       {"Payment backend"},
				"Macaroon":            {"backend-macaroon"},
				l402.HeaderMacaroonMD: {"backend-metadata"},
			}
			response := servePublicAuthRequest(
				p, "/public", "192.0.2.1", header,
			)
			require.Equal(t, http.StatusNoContent, response.Code)
			require.Empty(t, a.headers)
			forwarded := <-received
			for name, values := range header {
				require.Equal(
					t, values,
					forwarded.Header.Values(name),
				)
			}
		})
	}

	t.Run("backend headers and rewrite", func(t *testing.T) {
		a := &publicAuthRecorder{
			scheme: auth.AuthSchemeL402,
			accepted: map[l402.TokenID]bool{
				valid.id: true,
			},
		}
		service := &Service{
			Auth:               "on",
			AuthWhitelistPaths: []string{"^/public$"},
			Rewrite:            RewriteConfig{Prefix: "/internal"},
		}
		p, received := newPublicAuthProxy(t, service, a)
		header := valid.header.Clone()
		response := servePublicAuthRequest(
			p, "/public?query=value", "192.0.2.1", header,
		)
		require.Equal(t, http.StatusNoContent, response.Code)
		forwarded := <-received
		require.Equal(
			t, valid.header.Values("Authorization"),
			forwarded.Header.Values("Authorization"),
		)
		require.Equal(t, "/internal/public", forwarded.URL.Path)
		require.Equal(t, "query=value", forwarded.URL.RawQuery)

		service.Headers = map[string]string{
			"Authorization": "Bearer upstream-key",
		}
		header.Add("Authorization", "Bearer client-key")
		response = servePublicAuthRequest(
			p, "/public?query=value", "192.0.2.1", header,
		)
		require.Equal(t, http.StatusNoContent, response.Code)
		forwarded = <-received
		require.Equal(
			t, []string{"Bearer upstream-key"},
			forwarded.Header.Values("Authorization"),
		)
	})
}

// TestGrpcMetadataAuthorization checks that clients cannot bypass Aperture's
// authentication through grpc-gateway's metadata header mapping.
func TestGrpcMetadataAuthorization(t *testing.T) {
	valid := newPublicAuthToken(t, 1)
	const clientValue = "L402 forged"

	tests := []struct {
		name       string
		auth       auth.Level
		path       string
		valid      bool
		configured string
		want       string
	}{
		{
			name: "anonymous public request",
			auth: "on",
			path: "/public",
		},
		{
			name:  "authenticated public request",
			auth:  "on",
			path:  "/public",
			valid: true,
		},
		{
			name:  "authenticated protected request",
			auth:  "on",
			path:  "/protected",
			valid: true,
		},
		{
			name: "authentication off",
			auth: "off",
			path: "/public",
			want: clientValue,
		},
		{
			name:       "configured backend credential",
			auth:       "on",
			path:       "/public",
			configured: "Bearer trusted",
			want:       "Bearer trusted",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := &publicAuthRecorder{
				scheme: auth.AuthSchemeL402,
				accepted: map[l402.TokenID]bool{
					valid.id: true,
				},
			}
			service := &Service{
				Auth: test.auth,
				AuthWhitelistPaths: []string{
					"^/public$",
				},
				AuthSkipInvoiceCreationPaths: []string{
					"^/protected$",
				},
			}
			if test.configured != "" {
				service.Headers = map[string]string{
					grpcMetadataAuthorization: test.configured,
				}
			}
			p, received := newPublicAuthProxy(t, service, a)

			header := make(http.Header)
			if test.valid {
				header = valid.header.Clone()
			}
			header.Set(grpcMetadataAuthorization, clientValue)
			response := servePublicAuthRequest(
				p, test.path, "192.0.2.1", header,
			)
			require.Equal(t, http.StatusNoContent, response.Code)
			forwarded := <-received
			require.Equal(
				t, test.want,
				forwarded.Header.Get(grpcMetadataAuthorization),
			)
		})
	}
}

// TestAnonymousFallbackCredentials checks that free requests reached after
// failed authentication cannot carry an unverified identity to the backend.
func TestAnonymousFallbackCredentials(t *testing.T) {
	forged := newPublicAuthToken(t, 2)
	for _, level := range []auth.Level{"on", "freebie 1", "freebie 2"} {
		t.Run(string(level), func(t *testing.T) {
			a := &publicAuthRecorder{scheme: auth.AuthSchemeL402}
			service := &Service{Auth: level}
			p, received := newPublicAuthProxy(t, service, a)
			// Model a dynamic pricer returning zero after failed
			// authentication without starting a gRPC server.
			if level != "freebie 2" {
				service.pricer = pricer.NewDefaultPricer(0)
			}

			// Use up the only freebie, so the request falls back to
			// the zero price.
			if level == "freebie 1" {
				ok, err := service.freebieDB.TakeFreebie(
					nil, net.ParseIP("192.0.2.1"),
				)
				require.NoError(t, err)
				require.True(t, ok)
			}
			header := forged.header.Clone()
			header.Set("Macaroon", forged.macHex)
			header.Set(l402.HeaderMacaroonMD, forged.macHex)
			response := servePublicAuthRequest(
				p, "/resource", "192.0.2.1", header,
			)
			require.Equal(t, http.StatusNoContent, response.Code)
			require.Len(t, a.headers, 1)
			require.Equal(t, header, a.headers[0])
			forwarded := <-received
			require.Empty(
				t, forwarded.Header.Values("Authorization"),
			)
			require.NotContains(t, forwarded.Header, "Macaroon")
			require.NotContains(
				t, forwarded.Header, l402.HeaderMacaroonMD,
			)
			require.Zero(t, a.challenges)
		})
	}
}

// TestProtectedPathStillRequiresAuthentication checks that the whitelist
// behavior does not relax authentication on neighboring paid paths.
func TestProtectedPathStillRequiresAuthentication(t *testing.T) {
	forged := newPublicAuthToken(t, 2)
	for _, skipInvoice := range []bool{false, true} {
		name := "payment required"
		if skipInvoice {
			name = "skip invoice"
		}
		t.Run(name, func(t *testing.T) {
			a := &publicAuthRecorder{scheme: auth.AuthSchemeL402}
			service := &Service{
				Auth:               "on",
				AuthWhitelistPaths: []string{"^/public$"},
			}
			if skipInvoice {
				service.AuthSkipInvoiceCreationPaths = []string{
					"^/protected$",
				}
			}
			p, received := newPublicAuthProxy(t, service, a)
			response := servePublicAuthRequest(
				p, "/protected", "192.0.2.1", forged.header,
			)
			require.Len(t, a.headers, 1)
			if skipInvoice {
				require.Equal(
					t, http.StatusUnauthorized,
					response.Code,
				)
				require.Zero(t, a.challenges)
			} else {
				require.Equal(
					t, http.StatusPaymentRequired,
					response.Code,
				)
				require.Equal(t, 1, a.challenges)
			}
			require.Empty(t, received)
		})
	}
}

// TestAuthenticatedRequestKeepsOnlyVerifiedL402 checks that an authenticated
// request reaches the backend with the L402 that was verified and no other. A
// macaroon in another header is removed even when it names the verified token,
// since nothing checked its signature or caveats, while a macaroon of another
// kind, such as lnd's, stays for the backend to check.
func TestAuthenticatedRequestKeepsOnlyVerifiedL402(t *testing.T) {
	valid := newPublicAuthToken(t, 1)

	// The forgery copies the verified token's identifier, but is signed
	// with another root key and claims more capabilities.
	forgery := newMacaroonHex(
		t, "another root key", valid.identifier,
		l402.Caveat{
			Condition: l402.PreimageKey,
			Value:     valid.preimage.String(),
		},
		l402.NewCapabilitiesCaveat("test-service", "read,write"),
	)
	lndMacaroon := newLndMacaroon(t)
	validAuthorization := valid.header.Values("Authorization")

	tests := []struct {
		name   string
		header http.Header

		// want holds the macaroon headers the backend receives.
		want http.Header
	}{
		{
			name: "forgery beside Authorization",
			header: http.Header{
				"Authorization":       validAuthorization,
				l402.HeaderMacaroonMD: {forgery},
				l402.HeaderMacaroon:   {forgery},
			},
			want: http.Header{},
		},
		{
			name: "forgery beside metadata macaroon",
			header: http.Header{
				l402.HeaderMacaroonMD: {valid.macHex},
				l402.HeaderMacaroon:   {forgery},
			},
			want: http.Header{l402.HeaderMacaroonMD: {valid.macHex}},
		},
		{
			name: "lnd macaroon beside Authorization",
			header: http.Header{
				"Authorization":       validAuthorization,
				l402.HeaderMacaroonMD: {lndMacaroon},
			},
			want: http.Header{l402.HeaderMacaroonMD: {lndMacaroon}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := &publicAuthRecorder{
				scheme: auth.AuthSchemeL402,
				accepted: map[l402.TokenID]bool{
					valid.id: true,
				},
			}
			p, received := newPublicAuthProxy(
				t, &Service{Auth: "on"}, a,
			)
			response := servePublicAuthRequest(
				p, "/resource", "192.0.2.1", test.header,
			)
			require.Equal(t, http.StatusNoContent, response.Code)

			forwarded := <-received
			require.Equal(
				t, validAuthorization,
				forwarded.Header.Values("Authorization"),
			)
			for _, name := range []string{
				l402.HeaderMacaroonMD, l402.HeaderMacaroon,
			} {
				require.Equal(
					t, test.want.Values(name),
					forwarded.Header.Values(name), name,
				)
			}
		})
	}
}
