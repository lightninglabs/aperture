package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// TestMPPChallengeBindingRoundTrip checks that arbitrary resource names survive
// the canonical opaque encoding exactly and cannot authorize a different
// resource name.
func TestMPPChallengeBindingRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		resource := rapid.StringN(0, 128, -1).Draw(rt, "resource")
		opaque, err := encodeMPPChallengeBinding(resource)
		require.NoError(rt, err)
		require.NoError(rt, verifyMPPChallengeBinding(opaque, resource))
		require.Error(rt, verifyMPPChallengeBinding(
			opaque, resource+"/different",
		))
	})
}

// TestMPPChallengeBindingRejectsMissingOpaque checks the activation boundary:
// credentials minted before resource binding existed are not accepted because
// they do not identify any resource they are authorized to access.
func TestMPPChallengeBindingRejectsMissingOpaque(t *testing.T) {
	require.Error(t, verifyMPPChallengeBinding("", "test-service"))
}
