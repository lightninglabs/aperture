package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
)

// maxStreamUsageBodyBytes is the largest request body that is buffered to ask
// for streamed usage. A larger body is forwarded untouched, and a stream that
// then ends without usage settles at the pricer's estimate.
const maxStreamUsageBodyBytes = 4 << 20

// requestStreamUsage makes a metered streaming request ask the upstream for a
// final usage chunk, by setting stream_options.include_usage to true in an
// OpenAI-style JSON body with "stream": true that does not set it. Without that
// chunk the stream carries no usage, and the pricer can only settle it at an
// estimate, which overcharges a short generation and undercharges a long one.
//
// The edit is a byte splice, so the rest of the body, key order included, is
// forwarded exactly as the client sent it. An include_usage the client set
// explicitly, true or false, is left alone. A body that is not JSON, is
// content-encoded, is not a stream, or is larger than
// maxStreamUsageBodyBytes is never modified.
//
// Every JSON body read in full here is forwarded with a Content-Length that
// matches it, whether or not it changed. That covers a body edited here, and it
// gives the pricer the full size of a chunked body it would otherwise only see
// a prefix of, so the request is not reserved and settled at the whole bundle.
func requestStreamUsage(r *http.Request) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil
	}

	if enc := r.Header.Get("Content-Encoding"); enc != "" &&
		enc != "identity" {

		return nil
	}

	if r.ContentLength > maxStreamUsageBodyBytes {
		return nil
	}

	// Read one byte past the cap to tell a body that fits from one that
	// does not. Either way the bytes read are put back in front of the
	// unread remainder.
	original := r.Body
	body, err := io.ReadAll(
		io.LimitReader(original, maxStreamUsageBodyBytes+1),
	)
	if err != nil {
		_ = original.Close()

		return fmt.Errorf("unable to read request body: %w", err)
	}

	var reader io.Reader
	if len(body) > maxStreamUsageBodyBytes {
		reader = io.MultiReader(bytes.NewReader(body), original)
	} else {
		body = withStreamUsage(body)
		reader = bytes.NewReader(body)

		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}

	r.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: reader,
		Closer: original,
	}

	return nil
}

// withStreamUsage returns the JSON body with stream_options.include_usage set
// to true if the body is a stream request that does not set it, and the body
// unchanged otherwise.
func withStreamUsage(body []byte) []byte {
	fields, ok := topLevelFields(body)
	if !ok {
		return body
	}

	stream, ok := fields.spans["stream"]
	if !ok || !bytes.Equal(body[stream.start:stream.end], []byte("true")) {
		return body
	}

	const usageOption = `"include_usage":true`

	options, ok := fields.spans["stream_options"]
	if !ok {
		// The top-level object opens at its first byte, and a stream
		// request has at least one other field, so the option goes
		// first, followed by a comma.
		return splice(
			body, fields.open+1, fields.open+1,
			`"stream_options":{`+usageOption+`},`,
		)
	}

	value := body[options.start:options.end]
	switch {
	case bytes.Equal(value, []byte("null")):
		return splice(
			body, options.start, options.end, "{"+usageOption+"}",
		)

	case len(value) > 0 && value[0] == '{':
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(value, &inner); err != nil {
			return body
		}
		if _, set := inner["include_usage"]; set {
			return body
		}

		insert := usageOption
		if len(inner) > 0 {
			insert += ","
		}

		return splice(body, options.start+1, options.start+1, insert)
	}

	// A stream_options that is neither an object nor null is malformed,
	// and the upstream is left to reject it.
	return body
}

// fieldSpan is the byte range of a top-level field's value in a JSON body.
type fieldSpan struct {
	start int
	end   int
}

// bodyFields are the top-level fields of a JSON object body, keyed by name,
// with the offset of the object's opening brace. A repeated key keeps its last
// occurrence, which is the one JSON decoders use.
type bodyFields struct {
	open  int
	spans map[string]fieldSpan
}

// topLevelFields locates the value of every top-level field of a JSON object
// body. It reports false if the body is not a single valid JSON object.
func topLevelFields(body []byte) (bodyFields, bool) {
	if !json.Valid(body) {
		return bodyFields{}, false
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return bodyFields{}, false
	}

	index := bodyFields{
		open:  bytes.IndexByte(body, '{'),
		spans: make(map[string]fieldSpan),
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return bodyFields{}, false
		}
		key, ok := tok.(string)
		if !ok {
			return bodyFields{}, false
		}

		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return bodyFields{}, false
		}

		// The decoder stops right after the value, and the raw value
		// is the exact source bytes, so its span ends at the offset.
		end := int(dec.InputOffset())
		index.spans[key] = fieldSpan{start: end - len(value), end: end}
	}

	return index, true
}

// splice returns body with the bytes in [start, end) replaced by insert.
func splice(body []byte, start, end int, insert string) []byte {
	out := make([]byte, 0, len(body)-(end-start)+len(insert))
	out = append(out, body[:start]...)
	out = append(out, insert...)
	out = append(out, body[end:]...)

	return out
}
