// Package discovery implements the server side of the L402 discovery layer: a
// free static manifest served at /.well-known/l402.json that advertises a
// provider's services, prices, and caveat vocabulary, and an optional quote
// endpoint at /l402/quote that turns a client-chosen bundle into a ready-to-pay
// L402 challenge. See the L402 discovery specification for the wire format.
package discovery

import (
	"strconv"
	"strings"

	"github.com/lightninglabs/aperture/proxy"
)

const (
	// ManifestPath is the well-known path the discovery manifest is served
	// at, per RFC 8615.
	ManifestPath = "/.well-known/l402.json"

	// QuotePath is the default path the quote endpoint is served at.
	QuotePath = "/l402/quote"

	// ManifestVersion is the schema version this implementation emits.
	ManifestVersion = "1.0"

	// pricingFixed, pricingFormula, and pricingDynamic are the pricing
	// models a resource can advertise.
	pricingFixed   = "fixed"
	pricingFormula = "formula"
	pricingDynamic = "dynamic"

	// msatPerSat is the number of millisatoshis in a satoshi. Aperture
	// prices internally in satoshis; the manifest and quotes are denominated
	// in millisatoshis per the discovery spec.
	msatPerSat = 1000
)

// Provider identifies the entity serving the manifest.
type Provider struct {
	Name       string `json:"name"`
	URI        string `json:"uri,omitempty"`
	NodePubKey string `json:"node_pubkey,omitempty"`
}

// Manifest is the top-level discovery document.
type Manifest struct {
	Version        string                `json:"version"`
	Provider       Provider              `json:"provider"`
	Currencies     []string              `json:"currencies"`
	MacaroonVers   []int                 `json:"macaroon_versions,omitempty"`
	PaymentMethods []string              `json:"payment_methods,omitempty"`
	QuoteEndpoint  string                `json:"quote_endpoint,omitempty"`
	OpenAPI        string                `json:"openapi,omitempty"`
	Services       []ServiceEntry        `json:"services"`
	Caveats        map[string]CaveatSpec `json:"caveats,omitempty"`
}

// ServiceEntry describes one L402-enabled service.
type ServiceEntry struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Tiers        []Tier          `json:"tiers,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Resources    []ResourceEntry `json:"resources"`
}

// Tier is one tier of a service. Aperture currently only mints the base tier.
type Tier struct {
	Tier int    `json:"tier"`
	Name string `json:"name,omitempty"`
}

// ResourceEntry binds an HTTP entry point to a price and constraint bounds.
type ResourceEntry struct {
	Path        string                `json:"path,omitempty"`
	Method      string                `json:"method,omitempty"`
	Capability  string                `json:"capability,omitempty"`
	Pricing     Pricing               `json:"pricing"`
	Constraints map[string]Constraint `json:"constraints,omitempty"`
}

// Pricing describes how a resource is priced.
type Pricing struct {
	Model      string             `json:"model"`
	PriceMsat  int64              `json:"price_msat,omitempty"`
	BaseMsat   int64              `json:"base_msat,omitempty"`
	Components []FormulaComponent `json:"components,omitempty"`
}

// FormulaComponent is one per-unit charge in a formula price.
type FormulaComponent struct {
	Constraint       string `json:"constraint"`
	PriceMsatPerUnit int64  `json:"price_msat_per_unit"`
	Unit             int64  `json:"unit,omitempty"`
}

// Constraint describes the permissible bounds of a constraint a client may set.
type Constraint struct {
	Type   string   `json:"type"`
	Max    *int64   `json:"max,omitempty"`
	Min    *int64   `json:"min,omitempty"`
	Values []string `json:"values,omitempty"`
}

// CaveatSpec describes a caveat condition the provider enforces.
type CaveatSpec struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Attenuation string `json:"attenuation,omitempty"`
}

// BuildManifest constructs the discovery manifest from the configured services.
func BuildManifest(provider Provider, quoteEndpoint, openAPI string,
	services []*proxy.Service) *Manifest {

	m := &Manifest{
		Version:        ManifestVersion,
		Provider:       provider,
		Currencies:     []string{"msat"},
		MacaroonVers:   []int{0},
		PaymentMethods: []string{"bolt11"},
		QuoteEndpoint:  quoteEndpoint,
		OpenAPI:        openAPI,
		Caveats: map[string]CaveatSpec{
			"services": {Type: "service_list", Attenuation: "subset"},
		},
	}

	for _, svc := range services {
		if svc.Name == "" {
			continue
		}

		entry := serviceEntry(svc)
		m.Services = append(m.Services, entry)

		// Add the caveat vocabulary contributed by this service.
		m.Caveats[svc.Name+"_capabilities"] = CaveatSpec{
			Type:        "string",
			Attenuation: "subset",
		}
		for cond, value := range svc.Constraints {
			m.Caveats[cond] = constraintCaveatSpec(value)
		}
		if svc.Timeout > 0 {
			m.Caveats[svc.Name+"_valid_until"] = CaveatSpec{
				Type:        "timestamp",
				Attenuation: "earlier",
			}
		}
	}

	return m
}

// serviceEntry maps a single proxy.Service to its manifest entry.
func serviceEntry(svc *proxy.Service) ServiceEntry {
	entry := ServiceEntry{
		Name:  svc.Name,
		Tiers: []Tier{{Tier: 0, Name: "base"}},
	}
	if svc.Capabilities != "" {
		entry.Capabilities = strings.Split(svc.Capabilities, ",")
	}

	resource := ResourceEntry{
		Path:        svc.PathRegexp,
		Pricing:     pricingFor(svc),
		Constraints: constraintBounds(svc.Constraints),
	}
	entry.Resources = []ResourceEntry{resource}

	return entry
}

// pricingFor determines the manifest pricing model for a service. Formula takes
// precedence (it is the discovery-specific model), then dynamic, then fixed.
func pricingFor(svc *proxy.Service) Pricing {
	switch {
	case svc.Formula.Enabled:
		components := make([]FormulaComponent, 0, len(svc.Formula.Components))
		for _, c := range svc.Formula.Components {
			components = append(components, FormulaComponent{
				Constraint:       c.Constraint,
				PriceMsatPerUnit: c.PriceMsatPerUnit,
				Unit:             c.Unit,
			})
		}
		return Pricing{
			Model:      pricingFormula,
			BaseMsat:   svc.Formula.BaseMsat,
			Components: components,
		}

	case svc.DynamicPrice.Enabled:
		return Pricing{Model: pricingDynamic}

	default:
		return Pricing{
			Model:     pricingFixed,
			PriceMsat: svc.Price * msatPerSat,
		}
	}
}

// constraintBounds maps a service's configured constraints to manifest
// constraint bounds. Aperture enforces a fixed value per constraint, which we
// surface as the upper bound a client may request.
func constraintBounds(constraints map[string]string) map[string]Constraint {
	if len(constraints) == 0 {
		return nil
	}

	bounds := make(map[string]Constraint, len(constraints))
	for cond, value := range constraints {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			max := n
			bounds[cond] = Constraint{Type: "integer", Max: &max}
		} else {
			bounds[cond] = Constraint{
				Type:   "string",
				Values: []string{value},
			}
		}
	}

	return bounds
}

// constraintCaveatSpec infers the caveat vocabulary entry for a constraint from
// its configured value.
func constraintCaveatSpec(value string) CaveatSpec {
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return CaveatSpec{Type: "integer", Attenuation: "decreasing"}
	}
	return CaveatSpec{Type: "string", Attenuation: "subset"}
}
