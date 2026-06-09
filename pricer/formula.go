package pricer

// FormulaComponent prices one unit of a constraint value. The charge a
// component contributes is PriceMsatPerUnit * ceil(value / Unit), where value is
// the client-selected value for the named constraint.
type FormulaComponent struct {
	// Constraint is the caveat condition this component prices. It must
	// match a key in the service's constraint set.
	Constraint string `long:"constraint" description:"The constraint condition this component prices"`

	// PriceMsatPerUnit is the charge in millisatoshis per unit of the
	// constraint value.
	PriceMsatPerUnit int64 `long:"pricemsatperunit" description:"Charge in millisatoshis per unit of the constraint value"`

	// Unit is the size of a single unit. A value of zero is treated as one.
	Unit int64 `long:"unit" description:"Size of a single unit (defaults to 1)"`
}

// FormulaConfig describes a formula pricing model: a base price plus a set of
// per-unit components that scale with the client's chosen constraint values.
// The full price is computable by a client from the manifest alone, while the
// invoice the server ultimately issues remains authoritative.
type FormulaConfig struct {
	// Enabled indicates the service advertises formula pricing through the
	// L402 discovery layer.
	Enabled bool `long:"enabled" description:"Set to true to advertise formula pricing through L402 discovery"`

	// BaseMsat is the base price in millisatoshis before any per-unit
	// charges are added.
	BaseMsat int64 `long:"basemsat" description:"Base price in millisatoshis before per-unit charges"`

	// Components are the per-unit charges keyed by constraint.
	Components []*FormulaComponent `long:"components" description:"Per-unit charges keyed by constraint"`
}

// PriceMsat computes the formula price in millisatoshis for the given
// constraint values. A constraint that has no corresponding requested value
// contributes nothing.
func (f *FormulaConfig) PriceMsat(values map[string]int64) int64 {
	price := f.BaseMsat
	for _, c := range f.Components {
		value, ok := values[c.Constraint]
		if !ok || value <= 0 {
			continue
		}

		unit := c.Unit
		if unit <= 0 {
			unit = 1
		}

		// units = ceil(value / unit).
		units := (value + unit - 1) / unit
		price += c.PriceMsatPerUnit * units
	}

	return price
}
