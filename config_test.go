package aperture

import (
	"testing"
	"time"

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
