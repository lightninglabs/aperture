package meterd

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// TestUsageDetailsAreOptional checks that optional cache metadata cannot
// invalidate the counts that were billable before cache pricing was added.
func TestUsageDetailsAreOptional(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		prompt := rapid.Int64().Draw(t, "prompt")
		completion := rapid.Int64().Draw(t, "completion")
		total := rapid.Int64().Draw(t, "total")
		details := rapid.SampledFrom([]string{
			`[]`, `false`, `"other provider"`, `{"cached_tokens":"50"}`,
			`{"cached_tokens":2.5}`, `{"cached_tokens":9223372036854775808}`,
		}).Draw(t, "details")
		body := fmt.Sprintf(`"total_tokens":%d`, total)
		split := rapid.Bool().Draw(t, "split")
		if split {
			body += fmt.Sprintf(`,"prompt_tokens":%d,"completion_tokens":%d`,
				prompt, completion)
		}
		bare := `{"usage":{` + body + `}}`
		extra := `{"usage":{` + body + `,"prompt_tokens_details":` + details + `}}`
		contentType := "application/json"
		if rapid.Bool().Draw(t, "sse") {
			contentType = sseContentType
			bare, extra = sseChunk(bare), sseChunk(extra)
		}
		want, found := extractUsage(contentType, []byte(bare))
		require.True(t, found)
		got, found := extractUsage(contentType, []byte(extra))
		require.True(t, found)
		require.Equal(t, want, got)

		// Split counts are authoritative even when the same usage object
		// also reports a contradictory total.
		if split {
			require.Equal(t, clampTokenCount(prompt)+
				clampTokenCount(completion), got.tokenCount())
		} else {
			require.Equal(t, clampTokenCount(total), got.tokenCount())
		}
	})
}
