package challenger

import (
	"testing"

	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

func TestPoWChallengerNewChallenge(t *testing.T) {
	difficulty := uint32(20)
	c := NewPoWChallenger(difficulty)

	challengeStr, paymentHash, err := c.NewChallenge(100)
	require.NoError(t, err)

	// The challenge string should be the difficulty as a string.
	require.Equal(t, "20", challengeStr)

	// The payment hash should be non-zero (random).
	require.NotEqual(t, lntypes.Hash{}, paymentHash)

	// Two challenges should produce different hashes.
	_, hash2, err := c.NewChallenge(100)
	require.NoError(t, err)
	require.NotEqual(t, paymentHash, hash2)
}

func TestPoWChallengerStop(t *testing.T) {
	c := NewPoWChallenger(20)
	// Stop should not panic.
	c.Stop()
}
