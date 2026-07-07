package meterd

import "fmt"

// RateSource supplies the model rate table and the bundle sizing policy the
// server prices with. The default implementation is a static table built
// from the config's models map. A daemon that builds on meterd as a library
// can supply a dynamic implementation instead, e.g. one refreshed from an
// upstream provider's price list with a margin applied.
type RateSource interface {
	// ResolveModel maps a possibly empty or unknown model identifier to
	// a canonical model name and its per-token rates.
	ResolveModel(model string) (string, *ModelConfig, error)

	// BundleTokens returns the number of tokens sold per bundle for the
	// given canonical model.
	BundleTokens(model string) int64
}

// staticRates is the config-backed RateSource: a fixed models map and a flat
// bundle size shared by all models.
type staticRates struct {
	cfg *Config
}

// newStaticRates builds the default RateSource from the daemon config.
func newStaticRates(cfg *Config) *staticRates {
	return &staticRates{cfg: cfg}
}

// A compile-time check that staticRates satisfies RateSource.
var _ RateSource = (*staticRates)(nil)

// ResolveModel maps a possibly empty or unknown model identifier to a
// configured model and its rates, falling back to the default model.
func (r *staticRates) ResolveModel(model string) (string, *ModelConfig,
	error) {

	if model != "" {
		if rates, ok := r.cfg.Models[model]; ok {
			return model, rates, nil
		}
	}

	if r.cfg.DefaultModel != "" {
		if rates, ok := r.cfg.Models[r.cfg.DefaultModel]; ok {
			return r.cfg.DefaultModel, rates, nil
		}
	}

	return "", nil, fmt.Errorf("unknown model %q and no default model "+
		"configured", model)
}

// BundleTokens returns the flat configured bundle size regardless of the
// model.
func (r *staticRates) BundleTokens(string) int64 {
	return r.cfg.BundleTokens
}
