package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/lightninglabs/aperture/proxy"
	"gopkg.in/macaroon.v2"
)

// fakeMinter records the parameters of the last mint and returns a real
// macaroon carrying the supplied caveats.
type fakeMinter struct {
	price       int64
	serviceName string
	caveats     []l402.Caveat
	err         error
}

func (f *fakeMinter) MintL402WithCaveats(_ context.Context, price int64,
	serviceName string, caveats ...l402.Caveat) (*macaroon.Macaroon, string,
	error) {

	if f.err != nil {
		return nil, "", f.err
	}

	f.price = price
	f.serviceName = serviceName
	f.caveats = caveats

	var buf bytes.Buffer
	if err := l402.EncodeIdentifier(&buf, &l402.Identifier{
		Version: l402.LatestVersion,
	}); err != nil {
		return nil, "", err
	}

	var rootKey [l402.SecretSize]byte
	mac, err := macaroon.New(
		rootKey[:], buf.Bytes(), "lsat", macaroon.LatestVersion,
	)
	if err != nil {
		return nil, "", err
	}
	if err := l402.AddFirstPartyCaveats(mac, caveats...); err != nil {
		return nil, "", err
	}

	return mac, "lnbc100n1pexample", nil
}

func fixedTime() time.Time {
	return time.Unix(1_700_000_000, 0)
}

func newServer(minter Minter, services ...*proxy.Service) *Server {
	return New(Config{
		Services: func() []*proxy.Service { return services },
		Minter:   minter,
		Provider: Provider{Name: "test"},
		Now:      fixedTime,
	})
}

func weatherService() *proxy.Service {
	return &proxy.Service{
		Name:         "weather",
		PathRegexp:   "/v1/forecast",
		Capabilities: "forecast,historical",
		Price:        21,
		Constraints: map[string]string{
			"weather_monthly_requests": "1000000",
		},
		Timeout: 3600,
	}
}

func formulaService() *proxy.Service {
	return &proxy.Service{
		Name:         "weather",
		PathRegexp:   "/v1/forecast",
		Capabilities: "forecast",
		Formula: pricer.FormulaConfig{
			Enabled:  true,
			BaseMsat: 1000,
			Components: []*pricer.FormulaComponent{
				{
					Constraint:       "forecast_monthly_requests",
					PriceMsatPerUnit: 10,
					Unit:             1,
				},
			},
		},
		Constraints: map[string]string{
			"forecast_monthly_requests": "1000000",
		},
	}
}

func TestManifestHandler(t *testing.T) {
	t.Parallel()

	srv := newServer(&fakeMinter{}, weatherService())

	req := httptest.NewRequest(http.MethodGet, ManifestPath, nil)
	rec := httptest.NewRecorder()
	srv.ManifestService().(http.Handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("missing permissive CORS header")
	}

	var m Manifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if m.Version != ManifestVersion {
		t.Fatalf("version = %q", m.Version)
	}
	if m.QuoteEndpoint != QuotePath {
		t.Fatalf("quote_endpoint = %q", m.QuoteEndpoint)
	}
	if len(m.Services) != 1 || m.Services[0].Name != "weather" {
		t.Fatalf("unexpected services: %+v", m.Services)
	}
	if got := m.Services[0].Resources[0].Pricing; got.Model != pricingFixed ||
		got.PriceMsat != 21*msatPerSat {

		t.Fatalf("pricing = %+v", got)
	}
	if _, ok := m.Caveats["weather_capabilities"]; !ok {
		t.Fatalf("missing capabilities caveat spec")
	}
	if _, ok := m.Caveats["weather_valid_until"]; !ok {
		t.Fatalf("missing timeout caveat spec")
	}
}

func TestManifestFormulaPricing(t *testing.T) {
	t.Parallel()

	srv := newServer(&fakeMinter{}, formulaService())
	m := BuildManifest(srv.cfg.Provider, QuotePath, "", srv.cfg.Services())

	pr := m.Services[0].Resources[0].Pricing
	if pr.Model != pricingFormula {
		t.Fatalf("model = %q, want formula", pr.Model)
	}
	if pr.BaseMsat != 1000 || len(pr.Components) != 1 {
		t.Fatalf("formula = %+v", pr)
	}
	if pr.Components[0].Constraint != "forecast_monthly_requests" {
		t.Fatalf("component = %+v", pr.Components[0])
	}
}

func postQuote(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost, QuotePath, strings.NewReader(body),
	)
	rec := httptest.NewRecorder()
	srv.QuoteService().(http.Handler).ServeHTTP(rec, req)
	return rec
}

func TestQuoteFixedSuccess(t *testing.T) {
	t.Parallel()

	minter := &fakeMinter{}
	srv := newServer(minter, weatherService())

	rec := postQuote(t, srv, `{
		"service": "weather",
		"capabilities": ["forecast"],
		"constraints": {"weather_monthly_requests": 500000}
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp QuoteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PriceMsat != 21*msatPerSat {
		t.Fatalf("price_msat = %d", resp.PriceMsat)
	}
	if resp.Invoice == "" || resp.Macaroon == "" {
		t.Fatalf("missing macaroon/invoice")
	}
	if resp.QuoteExpiry != fixedTime().Add(defaultQuoteValidity).Unix() {
		t.Fatalf("quote_expiry = %d", resp.QuoteExpiry)
	}

	// The minted bundle must carry the services caveat, the narrowed
	// capability, the chosen constraint value, and the timeout.
	caveats := caveatMap(minter.caveats)
	if caveats["services"] != "weather:0" {
		t.Fatalf("services caveat = %q", caveats["services"])
	}
	if caveats["weather_capabilities"] != "forecast" {
		t.Fatalf("capabilities caveat = %q",
			caveats["weather_capabilities"])
	}
	if caveats["weather_monthly_requests"] != "500000" {
		t.Fatalf("constraint caveat = %q",
			caveats["weather_monthly_requests"])
	}
	if _, ok := caveats["weather_valid_until"]; !ok {
		t.Fatalf("missing timeout caveat")
	}
}

func TestQuoteFormulaPrice(t *testing.T) {
	t.Parallel()

	minter := &fakeMinter{}
	srv := newServer(minter, formulaService())

	rec := postQuote(t, srv, `{
		"service": "weather",
		"constraints": {"forecast_monthly_requests": 100000}
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp QuoteResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	// base 1000 + 10 * 100000 = 1_001_000 msat -> 1001 sats -> 1_001_000 msat.
	wantMsat := int64(1_001_000)
	if resp.PriceMsat != wantMsat {
		t.Fatalf("price_msat = %d, want %d", resp.PriceMsat, wantMsat)
	}
	if minter.price != 1001 {
		t.Fatalf("minted price (sats) = %d, want 1001", minter.price)
	}
}

func TestQuoteErrors(t *testing.T) {
	t.Parallel()

	srv := newServer(&fakeMinter{}, weatherService())

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "unknown service",
			body:       `{"service": "nope"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "unknown_service",
		},
		{
			name:       "unknown capability",
			body:       `{"service": "weather", "capabilities": ["nope"]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "unknown_capability",
		},
		{
			name: "constraint out of bounds",
			body: `{"service": "weather",
				"constraints": {"weather_monthly_requests": 2000000}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "constraint_out_of_bounds",
		},
		{
			name: "invalid constraint",
			body: `{"service": "weather",
				"constraints": {"unknown_thing": 5}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_constraint",
		},
		{
			name:       "unsatisfiable budget",
			body:       `{"service": "weather", "max_price_msat": 1}`,
			wantStatus: http.StatusConflict,
			wantCode:   "unsatisfiable_budget",
		},
		{
			name:       "unsupported payment method",
			body:       `{"service": "weather", "payment_method": "bolt12"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "unsupported_payment_method",
		},
		{
			name:       "unknown tier",
			body:       `{"service": "weather", "tier": 5}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "unknown_tier",
		},
		{
			name:       "malformed",
			body:       `{not json`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "malformed_request",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := postQuote(t, srv, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)",
					rec.Code, tc.wantStatus, rec.Body.String())
			}
			var eb errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &eb); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if eb.Error.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", eb.Error.Code,
					tc.wantCode)
			}
		})
	}
}

func TestQuoteMethodNotAllowed(t *testing.T) {
	t.Parallel()

	srv := newServer(&fakeMinter{}, weatherService())
	req := httptest.NewRequest(http.MethodGet, QuotePath, nil)
	rec := httptest.NewRecorder()
	srv.QuoteService().(http.Handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestQuoteDisabledWithoutMinter(t *testing.T) {
	t.Parallel()

	srv := New(Config{
		Services: func() []*proxy.Service {
			return []*proxy.Service{weatherService()}
		},
		Provider: Provider{Name: "test"},
	})

	req := httptest.NewRequest(http.MethodPost, QuotePath, nil)
	if srv.QuoteService().IsHandling(req) {
		t.Fatalf("quote endpoint should be disabled without a minter")
	}

	// The manifest should also omit the quote endpoint.
	m := BuildManifest(srv.cfg.Provider, "", "", srv.cfg.Services())
	if m.QuoteEndpoint != "" {
		t.Fatalf("quote_endpoint should be empty, got %q",
			m.QuoteEndpoint)
	}
}

// caveatMap decodes a list of caveats into a condition->value map (last wins).
func caveatMap(caveats []l402.Caveat) map[string]string {
	out := make(map[string]string, len(caveats))
	for _, c := range caveats {
		out[c.Condition] = c.Value
	}
	return out
}
