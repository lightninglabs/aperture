package meterd

import "math/big"

const (
	// perThousandTokens is the denominator of the normalized internal rates.
	perThousandTokens = 1000

	// wholeCachedInput selects the full input rate for cached prompt tokens.
	wholeCachedInput = 1000
)

// modelRates holds input and output rates in msat per thousand tokens. Big
// integers preserve the full range of legacy configurations after scaling and
// prevent token products from overflowing before the final division.
type modelRates struct {
	input          *big.Int
	output         *big.Int
	cachedPermille int64
}

// rates returns the model's normalized rates. The per-thousand-token fields
// take precedence for their respective directions, while legacy fields keep
// their historical meaning. Cached input defaults to the full input rate.
func (m *ModelConfig) rates() modelRates {
	input := big.NewInt(m.InputMsatPerToken)
	if m.InputMsatPerThousandTokens > 0 {
		input = big.NewInt(m.InputMsatPerThousandTokens)
	} else {
		input.Mul(input, big.NewInt(perThousandTokens))
	}

	output := big.NewInt(m.OutputMsatPerToken)
	if m.OutputMsatPerThousandTokens > 0 {
		output = big.NewInt(m.OutputMsatPerThousandTokens)
	} else {
		output.Mul(output, big.NewInt(perThousandTokens))
	}

	cachedPermille := int64(wholeCachedInput)
	if m.CachedInputPermille > 0 {
		cachedPermille = m.CachedInputPermille
	}

	return modelRates{
		input:          input,
		output:         output,
		cachedPermille: cachedPermille,
	}
}

// blendedRate returns twice the blended per-thousand-token rate.
func (r modelRates) blendedRate() *big.Int {
	return new(big.Int).Add(r.input, r.output)
}

// usageValue returns the value of a usage count in millionths of a msat. For
// an unsplit count it returns twice that value, since the blended rate is the
// average of the input and output rates. The cached-input permille stays in
// the product so it is never rounded to an intermediate per-token rate.
func (r modelRates) usageValue(counts *usageCounts) *big.Int {
	if !counts.hasSplit {
		value := new(big.Int).Mul(
			big.NewInt(counts.totalTokens), r.blendedRate(),
		)

		return value.Mul(value, big.NewInt(wholeCachedInput))
	}

	cached := min(max(counts.cachedTokens, 0), max(counts.promptTokens, 0))
	fresh := counts.promptTokens - cached
	value := new(big.Int).Mul(big.NewInt(fresh), r.input)
	value.Mul(value, big.NewInt(wholeCachedInput))
	cachedValue := new(big.Int).Mul(big.NewInt(cached), r.input)
	cachedValue.Mul(cachedValue, big.NewInt(r.cachedPermille))
	value.Add(value, cachedValue)
	completionValue := new(big.Int).Mul(
		big.NewInt(counts.completionTokens), r.output,
	)
	completionValue.Mul(completionValue, big.NewInt(wholeCachedInput))
	value.Add(value, completionValue)

	return value
}

// usageCostSats prices a usage count at a model's rates, in satoshis rounded
// up. When the prompt and completion split is known each direction is charged
// its own rate; otherwise the total is charged at the blended rate, which is
// the same assumption a bundle is priced under.
func usageCostSats(counts *usageCounts, rates *ModelConfig) int64 {
	value := rates.rates().usageValue(counts)
	denominator := int64(
		perThousandTokens * wholeCachedInput * 1000,
	)
	if !counts.hasSplit {
		denominator *= 2
	}

	return quotient(value, big.NewInt(denominator), true).Int64()
}

// bundleQuoteSats computes the flat price in satoshis of a full token bundle.
// The bundle is priced at the model's blended per-token rate, the average of
// the input and output rate, on the assumption that a bundle is drawn down by
// a roughly even mix of prompt and completion tokens.
func bundleQuoteSats(bundleTokens int64, rates *ModelConfig) int64 {
	r := rates.rates()
	value := new(big.Int).Mul(big.NewInt(bundleTokens), r.blendedRate())

	return quotient(
		value, big.NewInt(2*perThousandTokens*1000), true,
	).Int64()
}

// weightedDebit converts the value of a split usage count into bundle tokens,
// rounded up. A cached prompt token contributes its discounted value rather
// than the full fresh-input value.
func weightedDebit(counts *usageCounts, config *ModelConfig) (int64, bool) {
	r := config.rates()
	blendedRate := r.blendedRate()
	if blendedRate.Sign() <= 0 {
		return 0, false
	}

	valueTwice := new(big.Int).Mul(r.usageValue(counts), big.NewInt(2))
	denominator := new(big.Int).Mul(
		blendedRate, big.NewInt(wholeCachedInput),
	)
	debit := quotient(valueTwice, denominator, true)

	return debit.Int64(), true
}

// remainingValueSats values the remaining bundle tokens at the blended rate,
// rounding down so the reported balance never exceeds the tokens available.
func remainingValueSats(tokens int64, config *ModelConfig) int64 {
	r := config.rates()
	value := new(big.Int).Mul(big.NewInt(tokens), r.blendedRate())

	return quotient(
		value, big.NewInt(2*perThousandTokens*1000), false,
	).Int64()
}

// quotient divides value by denominator, optionally rounding up. Pricing uses
// integers throughout so fractional rates cannot overflow int64 intermediates
// or lose precision before the final result.
func quotient(value, denominator *big.Int, roundUp bool) *big.Int {
	if value.Sign() == 0 {
		return new(big.Int)
	}

	result, remainder := new(big.Int).QuoRem(value, denominator, new(big.Int))
	if roundUp && remainder.Sign() != 0 {
		result.Add(result, big.NewInt(1))
	}

	return result
}
