package aperture

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReusableChargePolicyForServices pins the shape of the name the policy
// is asked about: the authenticator passes the resource name, which for a
// dynamically priced service is the service name with the request path
// appended. Matching on equality alone silently re-enables single-use
// charges on every metered service, which is exactly the failure a live run
// caught.
func TestReusableChargePolicyForServices(t *testing.T) {
	t.Parallel()

	policy := reusableChargePolicyForServices(map[string]struct{}{
		"inference": {},
	})

	// The resource name of a dynamically priced service carries the path.
	require.True(t, policy("inference/v1/chat/completions"))

	// A service without dynamic pricing is asked about by bare name.
	require.True(t, policy("inference"))

	// Prefix matching must not bleed across the path separator.
	require.False(t, policy("inference2/v1/chat/completions"))
	require.False(t, policy("inference2"))
	require.False(t, policy("other"))
}
