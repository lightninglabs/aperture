package pricer

import "testing"

func TestFormulaPriceMsat(t *testing.T) {
	t.Parallel()

	f := &FormulaConfig{
		BaseMsat: 1000,
		Components: []*FormulaComponent{
			{
				Constraint:       "forecast_monthly_requests",
				PriceMsatPerUnit: 10,
				Unit:             1,
			},
		},
	}

	tests := []struct {
		name   string
		values map[string]int64
		want   int64
	}{
		{
			name:   "base only when no values",
			values: nil,
			want:   1000,
		},
		{
			name:   "base plus per-unit",
			values: map[string]int64{"forecast_monthly_requests": 100000},
			want:   1000 + 10*100000,
		},
		{
			name:   "unknown constraint ignored",
			values: map[string]int64{"other": 5},
			want:   1000,
		},
		{
			name:   "non-positive value ignored",
			values: map[string]int64{"forecast_monthly_requests": 0},
			want:   1000,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := f.PriceMsat(tc.values); got != tc.want {
				t.Fatalf("PriceMsat = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFormulaUnitRounding(t *testing.T) {
	t.Parallel()

	// Two msat per 1000 units, rounding up partial units.
	f := &FormulaConfig{
		Components: []*FormulaComponent{
			{Constraint: "bytes", PriceMsatPerUnit: 2, Unit: 1000},
		},
	}

	// 1500 bytes = ceil(1500/1000) = 2 units = 4 msat.
	if got := f.PriceMsat(map[string]int64{"bytes": 1500}); got != 4 {
		t.Fatalf("PriceMsat = %d, want 4", got)
	}

	// 1000 bytes = exactly 1 unit = 2 msat.
	if got := f.PriceMsat(map[string]int64{"bytes": 1000}); got != 2 {
		t.Fatalf("PriceMsat = %d, want 2", got)
	}
}
