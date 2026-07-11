package meterd

import "fmt"

// RateSource supplies the model rate table and the bundle sizing policy the
// server prices with. The default implementation is a static table built
// from the config's models map. A daemon that builds on meterd as a library
// can supply a dynamic implementation instead, e.g. one refreshed from an
// upstream provider's price list with a margin applied.
type RateSource interface {
	// ResolveModel maps a model identifier to a canonical model name and
	// its per-token rates. An empty identifier resolves to the default
	// model. A non-empty identifier the source does not know MUST return
	// an error rather than fall back: the server's model mismatch guard
	// relies on unknown models failing resolution, otherwise a bundle
	// bought for a cheap model could be spent on an upstream-hosted model
	// the seller never priced.
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

// ResolveModel maps a model identifier to a configured model and its
// rates. Only a missing (empty) model falls back to the default model; a
// non-empty identifier that is not in the models map fails closed, so a
// model the seller never priced is neither quoted nor served.
func (r *staticRates) ResolveModel(model string) (string, *ModelConfig,
	error) {

	if model != "" {
		if rates, ok := r.cfg.Models[model]; ok {
			return model, rates, nil
		}

		return "", nil, fmt.Errorf("unknown model %q", model)
	}

	if r.cfg.DefaultModel != "" {
		if rates, ok := r.cfg.Models[r.cfg.DefaultModel]; ok {
			return r.cfg.DefaultModel, rates, nil
		}
	}

	return "", nil, fmt.Errorf("no model named in the request and no " +
		"default model configured")
}

// BundleTokens returns the flat configured bundle size regardless of the
// model.
func (r *staticRates) BundleTokens(string) int64 {
	return r.cfg.BundleTokens
}
