package meterd

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClampTokenCount verifies that upstream-reported counts are bounded so
// downstream token-times-rate products cannot overflow int64.
func TestClampTokenCount(t *testing.T) {
	t.Parallel()

	require.EqualValues(t, 0, clampTokenCount(-5))
	require.EqualValues(t, 0, clampTokenCount(math.MinInt64))
	require.EqualValues(t, 0, clampTokenCount(0))
	require.EqualValues(t, 100, clampTokenCount(100))
	require.EqualValues(t, maxTokenCount, clampTokenCount(maxTokenCount))
	require.EqualValues(t, maxTokenCount, clampTokenCount(math.MaxInt64))
}

// TestAbsurdUsageDoesNotWrap verifies that a crafted usage tail claiming
// near-int64-max token counts cannot wrap the final satoshi or debit result
// into a negative or otherwise corrupted value.
func TestAbsurdUsageDoesNotWrap(t *testing.T) {
	t.Parallel()

	rates := &ModelConfig{
		InputMsatPerToken:  4,
		OutputMsatPerToken: 19,
	}
	counts := usageCounts{
		promptTokens:     clampTokenCount(math.MaxInt64),
		completionTokens: clampTokenCount(math.MaxInt64),
		cachedTokens:     clampTokenCount(math.MaxInt64),
		hasSplit:         true,
	}

	// With counts clamped to maxTokenCount, the cost is large but finite
	// and non-negative, never a wrapped negative number.
	cost := usageCostSats(&counts, rates)
	require.Greater(t, cost, int64(0))

	debit, ok := weightedDebit(&counts, rates)
	require.True(t, ok)
	require.Greater(t, debit, int64(0))

	// An absurd splitless total is likewise bounded.
	splitless := usageCounts{
		totalTokens: clampTokenCount(math.MaxInt64),
	}
	require.Greater(t, usageCostSats(&splitless, rates), int64(0))
}

// TestSaturatingConversion verifies that a degenerate config (ceiling rates
// with a one-token bundle) combined with a clamped absurd token count
// saturates at math.MaxInt64 rather than wrapping negative.
func TestSaturatingConversion(t *testing.T) {
	t.Parallel()

	// Push the rate sum to the ceiling for a one-token bundle.
	rates := &ModelConfig{
		InputMsatPerToken:  maxMsatPrice / 2,
		OutputMsatPerToken: maxMsatPrice / 2,
	}
	counts := usageCounts{
		totalTokens: maxTokenCount,
	}

	// The exact value far exceeds int64; it must saturate, never wrap.
	require.Equal(t, int64(math.MaxInt64), usageCostSats(&counts, rates))
}
