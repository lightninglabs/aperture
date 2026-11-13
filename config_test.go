package aperture

import (
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

// TestMPPChallengeExpiryConfig verifies the default, and that the expiry is
// only checked, and bounded, when MPP is enabled.
func TestMPPChallengeExpiryConfig(t *testing.T) {
	require.Equal(
		t, 15*time.Minute, NewConfig().Authenticator.MPPChallengeExpiry,
	)

	cfg := &AuthConfig{EnableMPP: true, Network: "regtest"}
	for _, d := range []time.Duration{0, -time.Hour, 366 * 24 * time.Hour} {
		cfg.MPPChallengeExpiry = d
		require.ErrorContains(t, cfg.validate(), "challenge expiry")
	}

	// 30 days passes the expiry check, so any error is about the unrelated
	// backend fields rather than the expiry.
	cfg.MPPChallengeExpiry = 720 * time.Hour
	if err := cfg.validate(); err != nil {
		require.NotContains(t, err.Error(), "challenge expiry")
	}
}

// TestTrustedProxiesConfig checks that trustedproxies is validated at startup,
// so a typo is an error rather than a silently untrusted proxy, and that the
// yaml key is read.
func TestTrustedProxiesConfig(t *testing.T) {
	newCfg := func(proxies ...string) *Config {
		cfg := NewConfig()
		cfg.ListenAddr = "127.0.0.1:8081"
		cfg.Authenticator.Disable = true
		cfg.TrustedProxies = proxies

		return cfg
	}

	require.NoError(t, newCfg().validate())
	require.NoError(t, newCfg("127.0.0.1", "10.0.0.0/8", "::1").validate())

	for _, bad := range []string{
		"not-an-ip", "10.0.0.0/33", "10.0.0.1/", "", "1.2.3",
	} {
		require.ErrorContains(
			t, newCfg(bad).validate(), "trusted proxy", bad,
		)
	}

	var cfg Config
	require.NoError(t, yaml.Unmarshal(
		[]byte("trustedproxies:\n  - 127.0.0.1\n  - 10.0.0.0/8\n"), &cfg,
	))
	require.Equal(t, []string{"127.0.0.1", "10.0.0.0/8"}, cfg.TrustedProxies)
}

// TestSharesChallengeBucket checks when the startup warning about a shared
// challenge bucket fires.
func TestSharesChallengeBucket(t *testing.T) {
	tests := []struct {
		name    string
		listen  string
		proxies []string
		limit   *proxy.ChallengeRateLimitConfig
		warn    bool
	}{
		{"loopback v4", "127.0.0.1:8000", nil, nil, true},
		{"loopback v4 range", "127.1.2.3:8000", nil, nil, true},
		{"loopback v6", "[::1]:8000", nil, nil, true},
		{"localhost", "localhost:8000", nil, nil, true},
		{"public listen", "0.0.0.0:8081", nil, nil, false},
		{"trusted set", "127.0.0.1:8000", []string{"127.0.0.1"}, nil, false},
		{
			"limiter off", "127.0.0.1:8000", nil,
			&proxy.ChallengeRateLimitConfig{}, false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := NewConfig()
			cfg.ListenAddr = test.listen
			cfg.TrustedProxies = test.proxies
			if test.limit != nil {
				cfg.ChallengeRateLimit = test.limit
			}
			require.Equal(t, test.warn, cfg.sharesChallengeBucket())
		})
	}
}
