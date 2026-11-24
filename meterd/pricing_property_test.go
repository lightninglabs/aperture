package meterd

import (
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// TestPricingProperties compares the billing paths against an independent
// rational model with rounding only at the point of conversion.
func TestPricingProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		count := rapid.Int64Range(0, maxTokenCount)
		rate := rapid.Int64Range(0, maxMsatPrice/2)
		in, out := rate.Draw(t, "input"), rate.Draw(t, "output")
		rates := &ModelConfig{}
		toSats := func(n int64, legacy, fractional *int64, key string) *big.Rat {
			if rapid.Bool().Draw(t, key) {
				*legacy = n
				return big.NewRat(n, 1000)
			}
			*fractional = n
			return big.NewRat(n, 1_000_000)
		}
		input := toSats(in, &rates.InputMsatPerToken,
			&rates.InputMsatPerThousandTokens, "legacy_input")
		output := toSats(out, &rates.OutputMsatPerToken,
			&rates.OutputMsatPerThousandTokens, "legacy_output")
		rates.CachedInputPermille = rapid.Int64Range(0, 1000).Draw(t, "permille")
		permille := rates.CachedInputPermille
		if permille == 0 {
			permille = 1000
		}

		prompt, completion := count.Draw(t, "prompt"), count.Draw(t, "completion")
		cached := rapid.Int64Range(0, prompt).Draw(t, "cached")
		split := usageCounts{promptTokens: prompt, completionTokens: completion,
			cachedTokens: cached, hasSplit: true}
		blended := new(big.Rat).Add(input, output)
		blended.Quo(blended, big.NewRat(2, 1))
		scaled := func(rate *big.Rat, n int64) *big.Rat {
			return new(big.Rat).Mul(rate, big.NewRat(n, 1))
		}
		exact := scaled(input, prompt-cached)
		discount := new(big.Rat).Mul(input, big.NewRat(permille, 1000))
		exact.Add(exact, scaled(discount, cached))
		exact.Add(exact, scaled(output, completion))
		require.Equal(t, roundedPrice(exact, true), usageCostSats(&split, rates))

		debit, ok := weightedDebit(&split, rates)
		require.Equal(t, blended.Sign() > 0, ok)
		if ok {
			debitExact := new(big.Rat).Quo(exact, blended)
			require.Equal(t, roundedPrice(debitExact, true), debit)
			require.LessOrEqual(t, debit, 2*(prompt+completion))
		}

		total := count.Draw(t, "total")
		unsplit := usageCounts{totalTokens: total}
		value := scaled(blended, total)
		quote := bundleQuoteSats(total, rates)
		require.Equal(t, roundedPrice(value, true), quote)
		require.Equal(t, quote, usageCostSats(&unsplit, rates))
		require.Equal(t, roundedPrice(value, false), remainingValueSats(total, rates))
	})
}

// roundedPrice rounds a nonnegative rational value into the RPC's range.
func roundedPrice(value *big.Rat, up bool) int64 {
	q := new(big.Int).Div(value.Num(), value.Denom())
	if up && new(big.Rat).SetInt(q).Cmp(value) < 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return math.MaxInt64
	}
	return q.Int64()
}
