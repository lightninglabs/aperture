package meterd

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// sseContentType is the content type of a streamed chat completion.
const sseContentType = "text/event-stream; charset=utf-8"

// sseChunk renders a single SSE data line with the given JSON payload.
func sseChunk(payload string) string {
	return "data: " + payload + "\n\n"
}

// TestExtractUsage exercises the usage extraction across SSE and JSON
// response tails, including truncated and malformed inputs.
func TestExtractUsage(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		contentType string
		tail        string
		wantFound   bool
		want        usageCounts
	}{{
		// Streamed completions emit content chunks with a null usage
		// field and a final chunk carrying the usage object. The last
		// usage-bearing chunk must win.
		name:        "SSE last usage chunk wins",
		contentType: sseContentType,
		tail: sseChunk(`{"choices":[{"delta":{"content":"a"}}],`+
			`"usage":null}`) +
			sseChunk(`{"choices":[],"usage":{"prompt_tokens":1,`+
				`"completion_tokens":2,"total_tokens":3}}`) +
			sseChunk(`{"choices":[],"usage":{"prompt_tokens":10,`+
				`"completion_tokens":20,"total_tokens":30}}`),
		wantFound: true,
		want: usageCounts{
			promptTokens:     10,
			completionTokens: 20,
			totalTokens:      30,
			hasSplit:         true,
		},
	}, {
		name:        "SSE with DONE sentinel after usage",
		contentType: sseContentType,
		tail: sseChunk(`{"choices":[{"delta":{}}],"usage":null}`) +
			sseChunk(`{"usage":{"prompt_tokens":7,`+
				`"completion_tokens":11,"total_tokens":18}}`) +
			sseChunk(`[DONE]`),
		wantFound: true,
		want: usageCounts{
			promptTokens:     7,
			completionTokens: 11,
			totalTokens:      18,
			hasSplit:         true,
		},
	}, {
		// When the tail is truncated so that the final data line lost
		// its prefix, line scanning fails and the brace-matching
		// fallback must locate the usage object.
		name:        "SSE truncated mid-line falls back to braces",
		contentType: sseContentType,
		tail: `okens":5},"usage":{"prompt_tokens":42,` +
			`"completion_tokens":13,"total_tokens":55}}` + "\n\n" +
			sseChunk(`[DONE]`),
		wantFound: true,
		want: usageCounts{
			promptTokens:     42,
			completionTokens: 13,
			totalTokens:      55,
			hasSplit:         true,
		},
	}, {
		name:        "plain JSON body",
		contentType: "application/json",
		tail: `{"id":"chatcmpl-1","choices":[{"message":` +
			`{"content":"hello"}}],"usage":{"prompt_tokens":100,` +
			`"completion_tokens":50,"total_tokens":150}}`,
		wantFound: true,
		want: usageCounts{
			promptTokens:     100,
			completionTokens: 50,
			totalTokens:      150,
			hasSplit:         true,
		},
	}, {
		// A bounded tail of a large JSON body is cut off at the
		// front, so the document as a whole is not parseable.
		name:        "front-truncated JSON tail",
		contentType: "application/json",
		tail: `l of a very long content string"}}],` +
			`"usage":{"prompt_tokens":200,` +
			`"completion_tokens":300,"total_tokens":500}}`,
		wantFound: true,
		want: usageCounts{
			promptTokens:     200,
			completionTokens: 300,
			totalTokens:      500,
			hasSplit:         true,
		},
	}, {
		// A usage object whose values live inside a string must not
		// confuse the brace matcher.
		name:        "usage key inside string content",
		contentType: "application/json",
		tail: `{"choices":[{"message":{"content":"the \"usage\": ` +
			`{legend} continues"}}],"usage":{"prompt_tokens":4,` +
			`"completion_tokens":6,"total_tokens":10}}`,
		wantFound: true,
		want: usageCounts{
			promptTokens:     4,
			completionTokens: 6,
			totalTokens:      10,
			hasSplit:         true,
		},
	}, {
		// Only the total count is present, so the split is unknown.
		name:        "total tokens only",
		contentType: "application/json",
		tail:        `{"usage":{"total_tokens":77}}`,
		wantFound:   true,
		want: usageCounts{
			totalTokens: 77,
		},
	}, {
		// A null usage as the last occurrence must fall back to an
		// earlier real usage object.
		name:        "null usage after real usage",
		contentType: "application/json",
		tail: `{"a":{"usage":{"prompt_tokens":1,` +
			`"completion_tokens":2,"total_tokens":3}},` +
			`"b":{"usage":null}}`,
		wantFound: true,
		want: usageCounts{
			promptTokens:     1,
			completionTokens: 2,
			totalTokens:      3,
			hasSplit:         true,
		},
	}, {
		// Pretty-printed JSON puts whitespace around the colon and the
		// object, which the brace matcher must tolerate.
		name:        "whitespace around usage object",
		contentType: "application/json",
		tail: "{\"usage\" :\n\t{\"prompt_tokens\": 8,\n" +
			"\t\"completion_tokens\": 9,\n" +
			"\t\"total_tokens\": 17}\n}",
		wantFound: true,
		want: usageCounts{
			promptTokens:     8,
			completionTokens: 9,
			totalTokens:      17,
			hasSplit:         true,
		},
	}, {
		name:        "no usage present",
		contentType: "application/json",
		tail: `{"choices":[{"message":{"content":"no accounting ` +
			`here"}}]}`,
		wantFound: false,
	}, {
		name:        "SSE without usage",
		contentType: sseContentType,
		tail: sseChunk(`{"choices":[{"delta":{"content":"x"}}]}`) +
			sseChunk(`[DONE]`),
		wantFound: false,
	}, {
		name:        "garbage",
		contentType: "application/json",
		tail:        "\x00\x01\x02 not json at all {{{",
		wantFound:   false,
	}, {
		name:        "empty tail",
		contentType: sseContentType,
		tail:        "",
		wantFound:   false,
	}, {
		// The usage object itself may be cut off at the end of the
		// tail, in which case brace matching cannot complete.
		name:        "usage object truncated at end",
		contentType: "application/json",
		tail:        `{"usage":{"prompt_tokens":100,"comp`,
		wantFound:   false,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			counts, found := extractUsage(
				tc.contentType, []byte(tc.tail),
			)
			require.Equal(t, tc.wantFound, found)

			if tc.wantFound {
				require.Equal(t, tc.want, counts)
			}
		})
	}
}

// chatRequestText renders a serialized OpenAI chat completion HTTP request
// with the given model in the JSON body, in the format aperture forwards it.
func chatRequestText(model string) string {
	body := fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`,
		model,
	)

	return "POST /v1/chat/completions HTTP/1.1\r\n" +
		"Host: backend.example\r\n" +
		"Content-Type: application/json\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
		"\r\n" + body
}

// TestModelFromRequestText checks the extraction of the model identifier
// from serialized HTTP requests.
func TestModelFromRequestText(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		text string
		want string
	}{{
		name: "well-formed request",
		text: chatRequestText("gpt-test"),
		want: "gpt-test",
	}, {
		name: "no body",
		text: "GET /v1/models HTTP/1.1\r\nHost: x\r\n\r\n",
		want: "",
	}, {
		name: "body without model",
		text: "POST / HTTP/1.1\r\nHost: x\r\n" +
			"Content-Length: 13\r\n\r\n" +
			`{"stream": 1}`,
		want: "",
	}, {
		// A request that fails the full HTTP parse still yields its
		// body through the blank line fallback.
		name: "malformed request line with JSON body",
		text: "NOT-HTTP\n\n" + `{"model":"claude-test"}`,
		want: "claude-test",
	}, {
		name: "garbage",
		text: "complete nonsense",
		want: "",
	}, {
		name: "empty",
		text: "",
		want: "",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(
				t, tc.want, modelFromRequestText(tc.text),
			)
		})
	}
}

// TestBundleQuoteSats checks the blended bundle pricing formula, including
// its rounding behavior.
func TestBundleQuoteSats(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name   string
		tokens int64
		rates  ModelConfig
		want   int64
	}{{
		// 1e6 tokens * (1000 + 2000) / 2 = 1.5e9 msat = 1.5e6 sats.
		name:   "exact division",
		tokens: 1_000_000,
		rates: ModelConfig{
			InputMsatPerToken:  1000,
			OutputMsatPerToken: 2000,
		},
		want: 1_500_000,
	}, {
		// 1000 * (1 + 0) / 2 = 500 msat, which rounds up to 1 sat.
		name:   "rounds up to whole sats",
		tokens: 1000,
		rates: ModelConfig{
			InputMsatPerToken:  1,
			OutputMsatPerToken: 0,
		},
		want: 1,
	}, {
		// 3 * (1 + 0) / 2 = 1.5 msat, which also rounds up to 1 sat.
		name:   "sub-msat rounds up",
		tokens: 3,
		rates: ModelConfig{
			InputMsatPerToken:  1,
			OutputMsatPerToken: 0,
		},
		want: 1,
	}, {
		name:   "free model",
		tokens: 1_000_000,
		rates:  ModelConfig{},
		want:   0,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(
				t, tc.want,
				bundleQuoteSats(tc.tokens, &tc.rates),
			)
		})
	}
}
