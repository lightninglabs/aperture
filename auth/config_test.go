package auth

import (
	"testing"

	"github.com/lightninglabs/aperture/freebie"
	"github.com/stretchr/testify/require"
)

// TestParseLevel checks that authentication levels are normalized and that
// malformed values are rejected before they reach request handling.
func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  Level
		count freebie.Count
		err   bool
	}{
		{name: "default", want: ""},
		{name: "on", value: "ON", want: "on"},
		{name: "off", value: "False", want: "false"},
		{
			name: "freebie", value: "Freebie 2", want: "freebie 2",
			count: 2,
		},
		{
			name: "max count", value: "freebie 65535",
			want: "freebie 65535", count: 65535,
		},
		{name: "unknown", value: "none", err: true},
		{name: "missing count", value: "freebie", err: true},
		{name: "zero count", value: "freebie 0", err: true},
		{name: "negative count", value: "freebie -1", err: true},
		{name: "invalid count", value: "freebie many", err: true},

		// Larger counts would wrap around in freebie.Count.
		{name: "count overflow", value: "freebie 65536", err: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			level, err := ParseLevel(test.value)
			if test.err {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.want, level)
			if level.IsFreebie() {
				require.Equal(
					t, test.count, level.FreebieCount(),
				)
			}
		})
	}
}
