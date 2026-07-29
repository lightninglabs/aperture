package meterd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// testYAMLConfig is a config file exercising every YAML field.
const testYAMLConfig = `
listenaddr: 127.0.0.1:11111
bundletokens: 500000
defaultmodel: gpt-test
statepath: /tmp/meterd-test-state.json
models:
  gpt-test:
    inputmsatpertoken: 1000
    outputmsatpertoken: 2000
  claude-test:
    inputmsatpertoken: 3000
    outputmsatpertoken: 15000
`

// writeConfigFile writes the given YAML to a temp file and returns its path.
func writeConfigFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "meterd.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	return path
}

// TestLoadConfig verifies loading defaults, YAML values and flag overrides.
func TestLoadConfig(t *testing.T) {
	t.Parallel()

	// Without any arguments, the defaults apply.
	cfg, err := LoadConfig(nil)
	require.NoError(t, err)
	require.Equal(t, DefaultListenAddr, cfg.ListenAddr)
	require.EqualValues(t, DefaultBundleTokens, cfg.BundleTokens)

	// A YAML config file overrides the defaults.
	path := writeConfigFile(t, testYAMLConfig)
	cfg, err = LoadConfig([]string{"--config", path})
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:11111", cfg.ListenAddr)
	require.EqualValues(t, 500_000, cfg.BundleTokens)
	require.Equal(t, "gpt-test", cfg.DefaultModel)
	require.Equal(t, "/tmp/meterd-test-state.json", cfg.StatePath)
	require.Len(t, cfg.Models, 2)
	require.EqualValues(
		t, 15000, cfg.Models["claude-test"].OutputMsatPerToken,
	)

	// Command line flags take precedence over the config file.
	cfg, err = LoadConfig([]string{
		"--config", path,
		"--listenaddr", "127.0.0.1:22222",
		"--bundletokens", "42",
	})
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:22222", cfg.ListenAddr)
	require.EqualValues(t, 42, cfg.BundleTokens)
	require.Equal(t, "gpt-test", cfg.DefaultModel)

	// A missing config file is an error when explicitly pointed to.
	_, err = LoadConfig([]string{"--config", "/does/not/exist.yaml"})
	require.ErrorContains(t, err, "unable to read config file")

	// A config file with invalid YAML is an error too.
	badPath := writeConfigFile(t, "models: [not: a: map")
	_, err = LoadConfig([]string{"--config", badPath})
	require.ErrorContains(t, err, "unable to parse config file")
}

// TestConfigValidate exercises the consistency checks of the configuration.
func TestConfigValidate(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{{
		name:   "valid defaults",
		mutate: func(*Config) {},
	}, {
		name: "empty listen address",
		mutate: func(c *Config) {
			c.ListenAddr = ""
		},
		wantErr: "listenaddr must be set",
	}, {
		name: "non-positive bundle size",
		mutate: func(c *Config) {
			c.BundleTokens = 0
		},
		wantErr: "bundletokens must be positive",
	}, {
		name: "cert without key",
		mutate: func(c *Config) {
			c.TLSCertPath = "/some/cert.pem"
		},
		wantErr: "must be set together",
	}, {
		name: "model without rates",
		mutate: func(c *Config) {
			c.Models = map[string]*ModelConfig{"m": nil}
		},
		wantErr: "has no rates",
	}, {
		name: "negative rate",
		mutate: func(c *Config) {
			c.Models = map[string]*ModelConfig{
				"m": {InputMsatPerToken: -1},
			}
		},
		wantErr: "has negative rates",
	}, {
		name: "unknown default model",
		mutate: func(c *Config) {
			c.DefaultModel = "ghost"
		},
		wantErr: "not present in the models map",
	}, {
		name: "negative estimated tokens",
		mutate: func(c *Config) {
			c.EstimatedTokens = -1
		},
		wantErr: "estimatedtokens must not be negative",
	}, {
		name: "negative max unauthorized bundles",
		mutate: func(c *Config) {
			c.MaxUnauthorizedBundles = -1
		},
		wantErr: "maxunauthorizedbundles must not be negative",
	}, {
		name: "price arithmetic overflows",
		mutate: func(c *Config) {
			c.BundleTokens = 1 << 40
			c.Models = map[string]*ModelConfig{
				"huge": {
					InputMsatPerToken:  1 << 40,
					OutputMsatPerToken: 1 << 40,
				},
			}
		},
		wantErr: "price overflows",
	}, {
		name: "large but in-bounds price is allowed",
		mutate: func(c *Config) {
			c.BundleTokens = 1_000_000
			c.Models = map[string]*ModelConfig{
				"pricey": {
					InputMsatPerToken:  1_000_000,
					OutputMsatPerToken: 1_000_000,
				},
			}
		},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := NewConfig()
			tc.mutate(cfg)

			err := cfg.validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}
