package l402

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTimeoutSatisfier tests that the Timeout Satisfier implementation behaves
// as expected and correctly accepts or rejects calls based on if the
// timeout has been reached or not.
func TestTimeoutSatisfier(t *testing.T) {
	t.Parallel()

	now := int64(0)

	var tests = []struct {
		name           string
		timeouts       []int64
		expectFinalErr bool
		expectPrevErr  bool
	}{
		{
			name:     "current time is before expiration",
			timeouts: []int64{now + 1000},
		},
		{
			name: "time passed is greater than " +
				"expiration",
			timeouts:       []int64{now - 1000},
			expectFinalErr: true,
		},
		{
			name: "successive caveats are increasingly " +
				"restrictive and not yet expired",
			timeouts: []int64{now + 1000, now + 500},
		},
		{
			name: "latter caveat is less restrictive " +
				"then previous",
			timeouts:      []int64{now + 500, now + 1000},
			expectPrevErr: true,
		},
	}

	var (
		service   = "restricted"
		condition = service + CondTimeoutSuffix
		satisfier = NewTimeoutSatisfier(service, func() time.Time {
			return time.Unix(now, 0)
		})
	)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var prev *Caveat
			for _, timeout := range test.timeouts {
				caveat := NewCaveat(
					condition, fmt.Sprintf("%d", timeout),
				)

				if prev != nil {
					err := satisfier.SatisfyPrevious(
						*prev, caveat,
					)
					if test.expectPrevErr {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				}

				err := satisfier.SatisfyFinal(caveat)
				if test.expectFinalErr {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}

				prev = &caveat
			}
		})
	}
}
