package mpp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProblem402 verifies that each problem type a 402 response can carry is
// rendered with its own type, and that an empty or unknown type falls back to
// payment-required.
func TestProblem402(t *testing.T) {
	testCases := []struct {
		name     string
		in       string
		wantType string
	}{{
		name:     "empty",
		in:       "",
		wantType: ProblemPaymentRequired,
	}, {
		name:     "payment required",
		in:       ProblemPaymentRequired,
		wantType: ProblemPaymentRequired,
	}, {
		name:     "malformed credential",
		in:       ProblemMalformedCredential,
		wantType: ProblemMalformedCredential,
	}, {
		name:     "invalid challenge",
		in:       ProblemInvalidChallenge,
		wantType: ProblemInvalidChallenge,
	}, {
		name:     "payment expired",
		in:       ProblemPaymentExpired,
		wantType: ProblemPaymentExpired,
	}, {
		name:     "verification failed",
		in:       ProblemVerificationFailed,
		wantType: ProblemVerificationFailed,
	}, {
		name:     "not a 402 problem",
		in:       ProblemMethodUnsupported,
		wantType: ProblemPaymentRequired,
	}, {
		name:     "unknown",
		in:       "https://example.com/problems/other",
		wantType: ProblemPaymentRequired,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var p ProblemDetails
			require.NoError(t, json.Unmarshal(Problem402(tc.in), &p))
			require.Equal(t, tc.wantType, p.Type)
			require.Equal(t, 402, p.Status)
			require.NotEmpty(t, p.Title)
			require.NotEmpty(t, p.Detail)
		})
	}

	// The default body is unchanged.
	require.Equal(t, Problem402(ProblemPaymentRequired),
		PaymentRequiredProblem())
}
