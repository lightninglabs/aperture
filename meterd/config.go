package meterd

import (
	"fmt"
	"os"
	"time"

	"github.com/goccy/go-yaml"
	flags "github.com/jessevdk/go-flags"
)

const (
	// DefaultListenAddr is the address the daemon serves its gRPC
	// interface on when no other address is configured.
	DefaultListenAddr = "127.0.0.1:10010"

	// DefaultBundleTokens is the number of LLM tokens sold per bundle
	// when no other bundle size is configured.
	DefaultBundleTokens = 1_000_000

	// DefaultEstimatedTokens is the per-request token estimate reserved on
	// authorization when the request body carries no max_tokens hint. It
	// bounds how far concurrent requests on a near-empty bundle can
	// overdraw.
	DefaultEstimatedTokens = 4_096

	// DefaultMaxUnauthorizedBundles caps the number of never-authorized
	// (un-paid) bundles retained at once, bounding the state an
	// unauthenticated challenge-mint spammer can accumulate.
	DefaultMaxUnauthorizedBundles = 10_000

	// DefaultUnauthorizedBundleTTL is how long a never-authorized bundle is
	// retained before the janitor reaps it. Un-paid mint spam is reaped far
	// sooner than a paid, drawn-down bundle.
	DefaultUnauthorizedBundleTTL = 10 * time.Minute

	// MaxUsageTailBytes is the upper bound accepted for the configured
	// usage tail size, guarding against an unbounded per-request capture.
	MaxUsageTailBytes = 1 << 20

	// maxMsatPrice is the ceiling accepted for the millisatoshi price of a
	// full token bundle. It guards the bundleTokens times per-token-rate
	// arithmetic against int64 overflow.
	maxMsatPrice = int64(1) << 60
)

// ModelConfig holds the per-token rates of a single model.
type ModelConfig struct {
	// InputMsatPerToken is the price in millisatoshi of a single input
	// (prompt) token.
	InputMsatPerToken int64 `yaml:"inputmsatpertoken" long:"inputmsatpertoken" description:"Price in millisatoshi per input token"`

	// OutputMsatPerToken is the price in millisatoshi of a single output
	// (completion) token.
	OutputMsatPerToken int64 `yaml:"outputmsatpertoken" long:"outputmsatpertoken" description:"Price in millisatoshi per output token"`
}

// Config holds the daemon configuration. Values are read from an optional
// YAML config file and can be overridden through command line flags. The
// models map can only be configured through the YAML file.
type Config struct {
	// ConfigFile points to an optional YAML config file.
	ConfigFile string `yaml:"-" long:"config" description:"Path to a YAML config file"`

	// ListenAddr is the address the gRPC server listens on.
	ListenAddr string `yaml:"listenaddr" long:"listenaddr" description:"Address to serve the prices gRPC interface on"`

	// BundleTokens is the number of LLM tokens sold per bundle. One L402
	// payment purchases one bundle.
	BundleTokens int64 `yaml:"bundletokens" long:"bundletokens" description:"Number of LLM tokens sold per bundle"`

	// DefaultModel is the model used to price requests whose model is
	// missing or not present in the models map. When empty, such requests
	// cannot be priced.
	DefaultModel string `yaml:"defaultmodel" long:"defaultmodel" description:"Model to price requests with an unknown or missing model as"`

	// EstimatedTokens is the per-request token estimate reserved on
	// authorization when the request carries no max_tokens hint. It bounds
	// concurrent overdraw on a near-empty bundle.
	EstimatedTokens int64 `yaml:"estimatedtokens" long:"estimatedtokens" description:"Per-request token estimate reserved on authorization to bound concurrent overdraw"`

	// MaxUnauthorizedBundles caps the number of never-authorized (un-paid)
	// bundles retained at once. When zero, the default is used.
	MaxUnauthorizedBundles int `yaml:"maxunauthorizedbundles" long:"maxunauthorizedbundles" description:"Maximum number of never-authorized bundles retained, bounding challenge-mint spam"`

	// UnauthorizedBundleTTL is how long a never-authorized bundle is
	// retained before the janitor reaps it. When zero, the default is
	// used.
	UnauthorizedBundleTTL time.Duration `yaml:"unauthorizedbundlettl" long:"unauthorizedbundlettl" description:"How long a never-authorized bundle is retained before it is reaped"`

	// Models maps a model identifier to its per-token rates.
	Models map[string]*ModelConfig `yaml:"models"`

	// TLSCertPath is the path to the TLS certificate to serve with. The
	// server speaks plaintext when unset, matching aperture's
	// dynamicprice.insecure mode.
	TLSCertPath string `yaml:"tlscertpath" long:"tlscertpath" description:"Path to the TLS certificate to serve with, plaintext when unset"`

	// TLSKeyPath is the path to the TLS key belonging to the certificate.
	TLSKeyPath string `yaml:"tlskeypath" long:"tlskeypath" description:"Path to the TLS key to serve with"`

	// StatePath is the path to a JSON file the bundle balances are
	// persisted to on every change. Persistence is disabled when empty.
	StatePath string `yaml:"statepath" long:"statepath" description:"Path to a JSON file bundle balances are persisted to, no persistence when empty"`
}

// NewConfig returns a Config populated with all default values.
func NewConfig() *Config {
	return &Config{
		ListenAddr:             DefaultListenAddr,
		BundleTokens:           DefaultBundleTokens,
		EstimatedTokens:        DefaultEstimatedTokens,
		MaxUnauthorizedBundles: DefaultMaxUnauthorizedBundles,
	}
}

// LoadConfig parses the given command line arguments, merges in the YAML
// config file when one is provided and validates the result. Command line
// flags take precedence over values from the config file.
func LoadConfig(args []string) (*Config, error) {
	// Pre-parse the command line to learn about a custom config file
	// location.
	cfg := NewConfig()
	parser := flags.NewParser(cfg, flags.Default)
	if _, err := parser.ParseArgs(args); err != nil {
		return nil, err
	}

	if cfg.ConfigFile != "" {
		b, err := os.ReadFile(cfg.ConfigFile)
		if err != nil {
			return nil, fmt.Errorf("unable to read config file "+
				"%s: %w", cfg.ConfigFile, err)
		}

		if err := yaml.Unmarshal(b, cfg); err != nil {
			return nil, fmt.Errorf("unable to parse config file "+
				"%s: %w", cfg.ConfigFile, err)
		}

		// Parse the command line again so that flags take precedence
		// over the config file.
		parser = flags.NewParser(cfg, flags.Default)
		if _, err := parser.ParseArgs(args); err != nil {
			return nil, err
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// validate checks the configuration for consistency.
func (c *Config) validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("listenaddr must be set")
	}

	if c.BundleTokens <= 0 {
		return fmt.Errorf("bundletokens must be positive, got %d",
			c.BundleTokens)
	}

	if c.EstimatedTokens < 0 {
		return fmt.Errorf("estimatedtokens must not be negative, got "+
			"%d", c.EstimatedTokens)
	}

	if c.MaxUnauthorizedBundles < 0 {
		return fmt.Errorf("maxunauthorizedbundles must not be "+
			"negative, got %d", c.MaxUnauthorizedBundles)
	}

	if (c.TLSCertPath == "") != (c.TLSKeyPath == "") {
		return fmt.Errorf("tlscertpath and tlskeypath must be set " +
			"together")
	}

	for name, model := range c.Models {
		if model == nil {
			return fmt.Errorf("model %s has no rates configured",
				name)
		}

		if model.InputMsatPerToken < 0 ||
			model.OutputMsatPerToken < 0 {

			return fmt.Errorf("model %s has negative rates", name)
		}

		// Guard the bundleTokens times per-token-rate arithmetic in
		// bundleQuoteSats against int64 overflow. The intermediate
		// product bundleTokens*(input+output) must stay within a safe
		// ceiling.
		rateSum := model.InputMsatPerToken + model.OutputMsatPerToken
		if rateSum < 0 {
			return fmt.Errorf("model %s rates overflow when summed",
				name)
		}
		if rateSum > 0 && c.BundleTokens > maxMsatPrice/rateSum {
			return fmt.Errorf("model %s price overflows: "+
				"bundletokens %d times rate sum %d exceeds the "+
				"ceiling %d", name, c.BundleTokens, rateSum,
				maxMsatPrice)
		}
	}

	if c.DefaultModel != "" {
		if _, ok := c.Models[c.DefaultModel]; !ok {
			return fmt.Errorf("defaultmodel %s is not present in "+
				"the models map", c.DefaultModel)
		}
	}

	return nil
}
