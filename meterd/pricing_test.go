package meterd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFractionalRates verifies that rates below one msat per token retain
// their precision in quotes and settlement.
func TestFractionalRates(t *testing.T) {
	t.Parallel()

	rates := &ModelConfig{
		InputMsatPerThousandTokens:  213,
		OutputMsatPerThousandTokens: 427,
	}

	// One million tokens at the blended 0.32 msat rate cost 320 sats. The
	// integer per-token fields cannot express this rate.
	require.EqualValues(t, 320, bundleQuoteSats(1_000_000, rates))

	counts := usageCounts{
		promptTokens:     1_000_000,
		completionTokens: 1_000_000,
		hasSplit:         true,
	}
	require.EqualValues(t, 640, usageCostSats(&counts, rates))

	// One hundred prompt and two hundred completion tokens debit
	// ceil((100*213 + 200*427) / 320) = 334 bundle tokens.
	counts = usageCounts{
		promptTokens:     100,
		completionTokens: 200,
		hasSplit:         true,
	}
	debit, ok := weightedDebit(&counts, rates)
	require.True(t, ok)
	require.EqualValues(t, 334, debit)
	// The exact 106.7 msat total rounds up to one satoshi.
	require.EqualValues(t, 1, usageCostSats(&counts, rates))
}

// TestCachedInputPricing verifies cached-input discounts and the default that
// preserves the historical full-price behavior.
func TestCachedInputPricing(t *testing.T) {
	t.Parallel()

	counts := usageCounts{
		promptTokens:     1000,
		completionTokens: 100,
		cachedTokens:     800,
		hasSplit:         true,
	}
	rates := &ModelConfig{
		InputMsatPerToken:  6,
		OutputMsatPerToken: 28,
	}

	// Without a configured discount the debit remains
	// ceil(2*(1000*6 + 100*28) / (6+28)) = 518.
	debit, ok := weightedDebit(&counts, rates)
	require.True(t, ok)
	require.EqualValues(t, 518, debit)
	require.EqualValues(t, 9, usageCostSats(&counts, rates))

	rates.CachedInputPermille = 100

	// Two hundred fresh and eight hundred cached tokens cost 200*6 +
	// 800*0.6 msat. Together with the completions, this is 4480 msat.
	debit, ok = weightedDebit(&counts, rates)
	require.True(t, ok)
	require.EqualValues(t, 264, debit)
	require.EqualValues(t, 5, usageCostSats(&counts, rates))
}

// TestCachedInputRateIsNotRounded verifies that the permille is applied to
// the total rather than rounded into a per-thousand-token rate first.
func TestCachedInputRateIsNotRounded(t *testing.T) {
	t.Parallel()

	rates := &ModelConfig{
		InputMsatPerThousandTokens:  1,
		OutputMsatPerThousandTokens: 1,
		CachedInputPermille:         1,
	}
	counts := usageCounts{
		promptTokens: 1_000_000,
		cachedTokens: 1_000_000,
		hasSplit:     true,
	}

	// Cached tokens cost exactly 0.001 of the input rate: one sat overall.
	// Rounding their per-thousand-token rate first would make them free or
	// charge the full thousand sats.
	require.EqualValues(t, 1, usageCostSats(&counts, rates))
	debit, ok := weightedDebit(&counts, rates)
	require.True(t, ok)
	require.EqualValues(t, 1000, debit)
}

// TestLegacyRateRangeIsPreserved verifies that normalizing legacy rates does
// not introduce an int64 overflow that the old arithmetic did not have.
func TestLegacyRateRangeIsPreserved(t *testing.T) {
	t.Parallel()

	rates := &ModelConfig{
		InputMsatPerToken:  1 << 40,
		OutputMsatPerToken: 1 << 40,
	}
	counts := usageCounts{
		promptTokens:     1,
		completionTokens: 1,
		hasSplit:         true,
	}

	require.EqualValues(t, (2*(1<<40)+999)/1000, usageCostSats(&counts, rates))
	require.EqualValues(t, 1<<40, bundleQuoteSats(1000, rates))
	// At 1<<40 msat per token, 2,147,483 tokens are worth
	// floor(2_147_483 * (1<<40) / 1000) sats.
	require.EqualValues(
		t, int64(2_147_483)*(1<<40)/1000,
		remainingValueSats(2_147_483, rates),
	)
}
