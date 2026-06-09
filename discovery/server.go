package discovery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/proxy"
	"gopkg.in/macaroon.v2"
)

// defaultQuoteValidity is how long a quote (and the invoice it carries) is
// honored by default.
const defaultQuoteValidity = 60 * time.Second

// Minter mints an L402 committing to a fresh invoice with a caller-supplied set
// of caveats. It is satisfied by *mint.Mint.
type Minter interface {
	MintL402WithCaveats(ctx context.Context, price int64, serviceName string,
		caveats ...l402.Caveat) (*macaroon.Macaroon, string, error)
}

// Config holds the dependencies of the discovery server.
type Config struct {
	// Services returns the current set of configured services. It is a
	// function so the manifest reflects live service updates.
	Services func() []*proxy.Service

	// Minter mints quote challenges. May be nil to disable the quote
	// endpoint while still serving the manifest.
	Minter Minter

	// Provider identifies this provider in the manifest.
	Provider Provider

	// OpenAPI is the optional URI of an OpenAPI document to advertise.
	OpenAPI string

	// QuoteValidity is how long a quote is honored. Defaults to one minute.
	QuoteValidity time.Duration

	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
}

// Server serves the discovery manifest and quote endpoint.
type Server struct {
	cfg Config
}

// New creates a new discovery server.
func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.QuoteValidity <= 0 {
		cfg.QuoteValidity = defaultQuoteValidity
	}

	return &Server{cfg: cfg}
}

// ManifestService returns the manifest endpoint as a priority local service so
// it bypasses the 402 gate.
func (s *Server) ManifestService() proxy.LocalService {
	return proxy.NewLocalService(
		http.HandlerFunc(s.handleManifest),
		func(r *http.Request) bool {
			return r.URL.Path == ManifestPath
		},
	)
}

// QuoteService returns the quote endpoint as a priority local service. It is
// only handling requests if a minter is configured.
func (s *Server) QuoteService() proxy.LocalService {
	return proxy.NewLocalService(
		http.HandlerFunc(s.handleQuote),
		func(r *http.Request) bool {
			return s.cfg.Minter != nil && r.URL.Path == QuotePath
		},
	)
}

// handleManifest serves the static discovery manifest.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, newError(
			http.StatusMethodNotAllowed, "method_not_allowed",
			"manifest is fetched with GET", "",
		))
		return
	}

	quoteEndpoint := ""
	if s.cfg.Minter != nil {
		quoteEndpoint = QuotePath
	}

	manifest := BuildManifest(
		s.cfg.Provider, quoteEndpoint, s.cfg.OpenAPI, s.cfg.Services(),
	)
	writeJSON(w, http.StatusOK, manifest)
}

// QuoteRequest is the body of a quote request.
type QuoteRequest struct {
	Service       string                 `json:"service"`
	Tier          *int                   `json:"tier"`
	Capabilities  []string               `json:"capabilities"`
	Constraints   map[string]interface{} `json:"constraints"`
	MaxPriceMsat  int64                  `json:"max_price_msat"`
	Optimize      string                 `json:"optimize"`
	TokenID       string                 `json:"token_id"`
	PaymentMethod string                 `json:"payment_method"`
}

// QuoteResponse is the body of a successful quote.
type QuoteResponse struct {
	PriceMsat    int64         `json:"price_msat"`
	QuoteExpiry  int64         `json:"quote_expiry"`
	Macaroon     string        `json:"macaroon"`
	Invoice      string        `json:"invoice"`
	Alternatives []interface{} `json:"alternatives,omitempty"`
}

// handleQuote serves the quote endpoint.
func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, newError(
			http.StatusMethodNotAllowed, "method_not_allowed",
			"quotes are requested with POST", "",
		))
		return
	}

	var req QuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, newError(
			http.StatusBadRequest, "malformed_request",
			"invalid JSON body", "",
		))
		return
	}

	resp, apiErr := s.processQuote(r.Context(), &req)
	if apiErr != nil {
		writeError(w, apiErr)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// processQuote validates a bundle, prices it, mints a challenge, and assembles
// the quote response.
func (s *Server) processQuote(ctx context.Context,
	req *QuoteRequest) (*QuoteResponse, *apiError) {

	// Only BOLT 11 is supported; BOLT 12 binding is unspecified.
	if req.PaymentMethod != "" && req.PaymentMethod != "bolt11" {
		return nil, newError(
			http.StatusBadRequest, "unsupported_payment_method",
			"only bolt11 is supported", "payment_method",
		)
	}

	svc := s.findService(req.Service)
	if svc == nil {
		return nil, newError(
			http.StatusBadRequest, "unknown_service",
			"unknown service", "service",
		)
	}

	// Aperture only mints the base tier today.
	if req.Tier != nil && *req.Tier != 0 {
		return nil, newError(
			http.StatusBadRequest, "unknown_tier",
			"only the base tier (0) is offered", "tier",
		)
	}

	caps, apiErr := resolveCapabilities(svc, req.Capabilities)
	if apiErr != nil {
		return nil, apiErr
	}

	caveatValues, intValues, apiErr := resolveConstraints(svc, req.Constraints)
	if apiErr != nil {
		return nil, apiErr
	}

	priceSats, apiErr := s.priceBundle(ctx, svc, intValues)
	if apiErr != nil {
		return nil, apiErr
	}
	priceMsat := priceSats * msatPerSat

	if req.MaxPriceMsat > 0 && priceMsat > req.MaxPriceMsat {
		return nil, newError(
			http.StatusConflict, "unsatisfiable_budget",
			"no offer fits max_price_msat", "max_price_msat",
		)
	}

	caveats, err := bundleCaveats(svc, caps, caveatValues, s.cfg.Now)
	if err != nil {
		return nil, newError(
			http.StatusInternalServerError, "mint_failed",
			err.Error(), "",
		)
	}

	mac, invoice, err := s.cfg.Minter.MintL402WithCaveats(
		ctx, priceSats, svc.Name, caveats...,
	)
	if err != nil {
		return nil, newError(
			http.StatusInternalServerError, "mint_failed",
			err.Error(), "",
		)
	}

	macBytes, err := mac.MarshalBinary()
	if err != nil {
		return nil, newError(
			http.StatusInternalServerError, "mint_failed",
			err.Error(), "",
		)
	}

	expiry := s.cfg.Now().Add(s.cfg.QuoteValidity).Unix()

	return &QuoteResponse{
		PriceMsat:   priceMsat,
		QuoteExpiry: expiry,
		Macaroon:    base64.StdEncoding.EncodeToString(macBytes),
		Invoice:     invoice,
	}, nil
}

// findService returns the configured service with the given name, or nil.
func (s *Server) findService(name string) *proxy.Service {
	for _, svc := range s.cfg.Services() {
		if svc.Name == name {
			return svc
		}
	}
	return nil
}

// priceBundle prices a bundle in satoshis. Formula pricing takes precedence,
// then dynamic, then the static price.
func (s *Server) priceBundle(ctx context.Context, svc *proxy.Service,
	intValues map[string]int64) (int64, *apiError) {

	switch {
	case svc.Formula.Enabled:
		priceMsat := svc.Formula.PriceMsat(intValues)
		return ceilDiv(priceMsat, msatPerSat), nil

	case svc.DynamicPrice.Enabled:
		// Dynamic pricers price a concrete request. The quote has no
		// backend request, so we synthesize one against the service's
		// path. If pricing fails, the bundle is temporarily unpriceable.
		target := svc.PathRegexp
		if !strings.HasPrefix(target, "/") {
			target = "/"
		}
		pr, err := http.NewRequestWithContext(
			ctx, http.MethodGet, target, nil,
		)
		if err != nil {
			return 0, newError(
				http.StatusServiceUnavailable, "unpriceable",
				"unable to price dynamic service", "",
			)
		}
		price, err := svc.ResourcePrice(ctx, pr)
		if err != nil {
			return 0, newError(
				http.StatusServiceUnavailable, "unpriceable",
				"unable to price dynamic service", "",
			)
		}
		return price, nil

	default:
		return svc.Price, nil
	}
}

// resolveCapabilities validates the requested capabilities against the service
// and returns the effective capability set.
func resolveCapabilities(svc *proxy.Service,
	requested []string) ([]string, *apiError) {

	configured := splitNonEmpty(svc.Capabilities)
	if len(requested) == 0 {
		return configured, nil
	}

	allowed := make(map[string]struct{}, len(configured))
	for _, c := range configured {
		allowed[c] = struct{}{}
	}
	for _, c := range requested {
		if _, ok := allowed[c]; !ok {
			return nil, newError(
				http.StatusBadRequest, "unknown_capability",
				"capability not offered by service", c,
			)
		}
	}

	return requested, nil
}

// resolveConstraints validates the requested constraints against the service's
// configured bounds and formula. It returns the caveat values (as strings) and
// the integer values used for formula pricing.
func resolveConstraints(svc *proxy.Service,
	requested map[string]interface{}) (map[string]string, map[string]int64,
	*apiError) {

	caveatValues := make(map[string]string)
	intValues := make(map[string]int64)

	// The set of constraint conditions a client may request is the union of
	// the configured constraints and any formula component constraints.
	allowed := make(map[string]struct{})
	for cond := range svc.Constraints {
		allowed[cond] = struct{}{}
	}
	if svc.Formula.Enabled {
		for _, c := range svc.Formula.Components {
			allowed[c.Constraint] = struct{}{}
		}
	}

	for cond, raw := range requested {
		if _, ok := allowed[cond]; !ok {
			return nil, nil, newError(
				http.StatusBadRequest, "invalid_constraint",
				"unknown constraint", cond,
			)
		}

		value, ok := stringifyValue(raw)
		if !ok {
			return nil, nil, newError(
				http.StatusBadRequest, "invalid_constraint",
				"unsupported constraint value type", cond,
			)
		}
		caveatValues[cond] = value

		// If the requested value is an integer, record it for formula
		// pricing and enforce the configured upper bound, if any.
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			intValues[cond] = n

			if cfg, ok := svc.Constraints[cond]; ok {
				if max, err := strconv.ParseInt(cfg, 10, 64); err == nil &&
					n > max {

					return nil, nil, newError(
						http.StatusBadRequest,
						"constraint_out_of_bounds",
						"value exceeds the advertised "+
							"maximum", cond,
					)
				}
			}
		}
	}

	return caveatValues, intValues, nil
}

// bundleCaveats maps a validated bundle to the macaroon's first-party caveats:
// the services caveat, a capabilities caveat, one caveat per constraint (the
// client's value overriding the configured one where provided), and the
// service timeout.
func bundleCaveats(svc *proxy.Service, caps []string,
	caveatValues map[string]string, now func() time.Time) ([]l402.Caveat,
	error) {

	servicesCaveat, err := l402.NewServicesCaveat(l402.Service{
		Name: svc.Name,
		Tier: l402.BaseTier,
	})
	if err != nil {
		return nil, err
	}
	caveats := []l402.Caveat{servicesCaveat}

	if len(caps) > 0 {
		caveats = append(caveats, l402.NewCapabilitiesCaveat(
			svc.Name, strings.Join(caps, ","),
		))
	}

	// Emit configured constraints, letting the client's chosen value
	// override the configured default where supplied.
	emitted := make(map[string]struct{})
	for cond, configured := range svc.Constraints {
		value := configured
		if v, ok := caveatValues[cond]; ok {
			value = v
		}
		caveats = append(caveats, l402.NewCaveat(cond, value))
		emitted[cond] = struct{}{}
	}

	// Emit any requested constraints not part of the configured set (for
	// example formula-only constraints), in deterministic order.
	for _, cond := range sortedKeys(caveatValues) {
		if _, done := emitted[cond]; done {
			continue
		}
		caveats = append(caveats, l402.NewCaveat(cond, caveatValues[cond]))
	}

	if svc.Timeout > 0 {
		caveats = append(caveats, l402.NewTimeoutCaveat(
			svc.Name, svc.Timeout, now,
		))
	}

	return caveats, nil
}

// apiError is an error to be rendered as the discovery error envelope.
type apiError struct {
	status  int
	code    string
	message string
	field   string
}

func newError(status int, code, message, field string) *apiError {
	return &apiError{status: status, code: code, message: message, field: field}
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// writeError writes the discovery error envelope.
func writeError(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.status, errorBody{
		Error: errorDetail{
			Code:    e.code,
			Message: e.message,
			Field:   e.field,
		},
	})
}

// writeJSON writes a JSON response with discovery's default headers.
func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// stringifyValue renders a JSON-decoded constraint value as the string used in
// a caveat. Integers are rendered without a decimal point.
func stringifyValue(raw interface{}) (string, bool) {
	switch v := raw.(type) {
	case string:
		return v, true

	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10), true
		}
		return strconv.FormatFloat(v, 'f', -1, 64), true

	case json.Number:
		return v.String(), true

	default:
		return "", false
	}
}

// splitNonEmpty splits a comma-separated list, dropping empty entries.
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sortedKeys returns the keys of a string-keyed map in sorted order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// ceilDiv divides a by b rounding up, for non-negative inputs.
func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}
