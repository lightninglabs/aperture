package meterd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// usageCounts holds the token counts extracted from a response's usage
// object.
type usageCounts struct {
	// promptTokens is the number of input tokens consumed.
	promptTokens int64

	// completionTokens is the number of output tokens produced.
	completionTokens int64

	// totalTokens is the total number of tokens consumed.
	totalTokens int64

	// hasSplit is whether the prompt/completion split is known. When
	// false, only totalTokens carries information.
	hasSplit bool
}

// usageJSON mirrors the OpenAI usage object. Pointer fields distinguish
// absent fields from genuine zero counts.
type usageJSON struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
}

// countsFromUsage converts a parsed usage object into usage counts. It
// returns false when the object is nil or carries no token counts at all.
func countsFromUsage(u *usageJSON) (usageCounts, bool) {
	if u == nil {
		return usageCounts{}, false
	}

	var counts usageCounts
	switch {
	// The prompt/completion split is available, which allows exact
	// pricing at the per-direction rates.
	case u.PromptTokens != nil && u.CompletionTokens != nil:
		counts.promptTokens = *u.PromptTokens
		counts.completionTokens = *u.CompletionTokens
		counts.hasSplit = true

		counts.totalTokens = counts.promptTokens +
			counts.completionTokens
		if u.TotalTokens != nil {
			counts.totalTokens = *u.TotalTokens
		}

	// Only the total is available, so the caller has to fall back to the
	// blended rate.
	case u.TotalTokens != nil:
		counts.totalTokens = *u.TotalTokens

	default:
		return usageCounts{}, false
	}

	return counts, true
}

// extractUsage extracts the final usage object from the captured response
// tail. For SSE streams the data lines are scanned and the last parseable
// chunk with a non-null usage object wins. For plain JSON bodies, and as a
// fallback for truncated SSE tails, the last "usage" key in the tail is
// located and the object following it is brace-matched, since the tail is
// bounded and may be cut off at the front.
func extractUsage(contentType string, tail []byte) (usageCounts, bool) {
	if strings.Contains(contentType, "text/event-stream") {
		if counts, ok := usageFromSSELines(tail); ok {
			return counts, ok
		}

		// The tail may have been truncated mid-line, so fall back to
		// scanning for the raw usage object.
		return usageFromBraceMatch(tail)
	}

	return usageFromBraceMatch(tail)
}

// usageFromSSELines scans the SSE data lines in the tail and returns the
// counts from the last parseable chunk that carries a non-null usage object
// with token counts.
func usageFromSSELines(tail []byte) (usageCounts, bool) {
	var (
		best  usageCounts
		found bool
	)

	for _, line := range bytes.Split(tail, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}

		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 ||
			bytes.Equal(payload, []byte("[DONE]")) {

			continue
		}

		var chunk struct {
			Usage *usageJSON `json:"usage"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			// The first line of the tail may be truncated at the
			// front, and providers may interleave non-JSON
			// events, so unparseable lines are simply skipped.
			continue
		}

		if counts, ok := countsFromUsage(chunk.Usage); ok {
			// The last usage-bearing chunk wins.
			best = counts
			found = true
		}
	}

	return best, found
}

// usageFromBraceMatch scans backwards for the last "usage" key in the given
// bytes and brace-matches the JSON object that follows it. This works on
// front-truncated tails where the enclosing JSON document can no longer be
// parsed as a whole. Occurrences whose value is not a parseable object with
// token counts (for example "usage":null) are skipped, moving to the next
// earlier occurrence.
func usageFromBraceMatch(data []byte) (usageCounts, bool) {
	key := []byte(`"usage"`)

	end := len(data)
	for end > 0 {
		idx := bytes.LastIndex(data[:end], key)
		if idx == -1 {
			return usageCounts{}, false
		}
		end = idx

		// Require the match to sit in JSON key position, so an
		// incidental "usage" substring inside a string value cannot
		// latch a crafted object. In an object, a key's opening quote
		// is preceded (ignoring whitespace) by the object's opening
		// brace or a comma separating it from the previous member.
		if !keyInObjectPosition(data, idx) {
			continue
		}

		obj, ok := jsonObjectAfterKey(data, idx+len(key))
		if !ok {
			continue
		}

		var u usageJSON
		if err := json.Unmarshal(obj, &u); err != nil {
			continue
		}

		if counts, ok := countsFromUsage(&u); ok {
			return counts, true
		}
	}

	return usageCounts{}, false
}

// keyInObjectPosition reports whether the key at the given index sits in JSON
// object-key position: its opening quote is preceded, ignoring whitespace, by
// the object's opening brace or a comma. A key at the very start of the tail
// is accepted, since a bounded tail may be front-truncated right up to the
// key.
func keyInObjectPosition(data []byte, keyStart int) bool {
	i := keyStart - 1
	for i >= 0 {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			i--
		case '{', ',':
			return true
		default:
			return false
		}
	}

	// The key runs to the front of the (possibly truncated) tail.
	return true
}

// jsonObjectAfterKey expects a colon followed by an opening brace after the
// given offset and returns the brace-matched JSON object starting there.
func jsonObjectAfterKey(data []byte, offset int) ([]byte, bool) {
	i := skipJSONSpace(data, offset)
	if i >= len(data) || data[i] != ':' {
		return nil, false
	}

	i = skipJSONSpace(data, i+1)
	if i >= len(data) || data[i] != '{' {
		return nil, false
	}

	return matchJSONObject(data[i:])
}

// skipJSONSpace returns the first index at or after the given offset that
// does not hold JSON whitespace.
func skipJSONSpace(data []byte, offset int) int {
	i := offset
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}

	return i
}

// matchJSONObject returns the shortest prefix of data that forms a balanced
// JSON object, honoring strings and escape sequences. The data must start
// with an opening brace.
func matchJSONObject(data []byte) ([]byte, bool) {
	var (
		depth    int
		inString bool
		escaped  bool
	)

	for i := 0; i < len(data); i++ {
		c := data[i]

		switch {
		case escaped:
			escaped = false

		case inString:
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}

		case c == '"':
			inString = true

		case c == '{':
			depth++

		case c == '}':
			depth--
			if depth == 0 {
				return data[:i+1], true
			}
		}
	}

	return nil, false
}

// modelFromRequestText extracts the model identifier from the JSON body of a
// serialized HTTP request, as produced by httputil.DumpRequest. An empty
// string is returned when no model can be extracted.
func modelFromRequestText(text string) string {
	body := requestBody(text)
	if len(body) == 0 {
		return ""
	}

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	return payload.Model
}

// maxTokensFromRequestText extracts the max_tokens hint from the JSON body of
// a serialized HTTP request, as produced by httputil.DumpRequest. Zero is
// returned when no positive max_tokens can be extracted. Both the OpenAI
// max_tokens field and its newer max_completion_tokens alias are recognized,
// preferring the larger of the two.
func maxTokensFromRequestText(text string) int64 {
	body := requestBody(text)
	if len(body) == 0 {
		return 0
	}

	var payload struct {
		MaxTokens           *int64 `json:"max_tokens"`
		MaxCompletionTokens *int64 `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}

	var maxTokens int64
	if payload.MaxTokens != nil && *payload.MaxTokens > maxTokens {
		maxTokens = *payload.MaxTokens
	}
	if payload.MaxCompletionTokens != nil &&
		*payload.MaxCompletionTokens > maxTokens {

		maxTokens = *payload.MaxCompletionTokens
	}

	return maxTokens
}

// requestBody returns the body of a serialized HTTP request. It first
// attempts a full HTTP parse, which honors framing such as Content-Length
// and chunked encoding, and falls back to splitting on the first blank line.
func requestBody(text string) []byte {
	reader := bufio.NewReader(strings.NewReader(text))
	if req, err := http.ReadRequest(reader); err == nil {
		defer func() {
			_ = req.Body.Close()
		}()

		if body, err := io.ReadAll(req.Body); err == nil &&
			len(body) > 0 {

			return body
		}
	}

	if i := strings.Index(text, "\r\n\r\n"); i != -1 {
		return []byte(text[i+4:])
	}
	if i := strings.Index(text, "\n\n"); i != -1 {
		return []byte(text[i+2:])
	}

	return nil
}
