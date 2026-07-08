package mpp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Canonicalize produces a JSON Canonicalization Scheme (JCS) output per RFC
// 8785 for the given value. JCS defines a deterministic JSON serialization
// that ensures identical logical values produce identical byte sequences.
//
// This is a minimal implementation sufficient for the flat and simply-nested
// JSON objects used in the Payment HTTP Authentication Scheme. It handles:
//   - Sorting object keys lexicographically (by Unicode code point).
//   - No whitespace between tokens.
//   - Strings serialized with standard JSON escaping.
//   - Numbers serialized per the ES6 specification (JSON default for integers).
//   - Null, boolean values serialized as-is.
//   - Nested objects and arrays are recursed into.
func Canonicalize(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := canonicalWrite(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CanonicalizeJSON takes a raw JSON byte slice, unmarshals it into a generic
// representation, and re-serializes it using JCS canonicalization.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("jcs: failed to unmarshal JSON: %w", err)
	}
	return Canonicalize(v)
}

// canonicalWrite writes the canonical JSON representation of v to buf.
func canonicalWrite(buf *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")

	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}

	case string:
		// Use json.Marshal for correct string escaping per RFC 8259.
		escaped, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("jcs: failed to marshal string: %w",
				err)
		}
		buf.Write(escaped)

	case json.Number:
		// JSON numbers are written as-is per JCS. For integers this
		// is the standard decimal representation.
		buf.WriteString(val.String())

	case float64:
		// Per JCS (RFC 8785 §3.2.2.3), numbers use the ES6/IEEE 754
		// serialization. For integers (no fractional part), output
		// without decimal point. For non-integers, use Go's default
		// float formatting which matches ES6 for typical values.
		if val == float64(int64(val)) {
			fmt.Fprintf(buf, "%d", int64(val))
		} else {
			// Use json.Marshal for float64 to get correct
			// representation.
			encoded, err := json.Marshal(val)
			if err != nil {
				return fmt.Errorf("jcs: failed to marshal "+
					"float: %w", err)
			}
			buf.Write(encoded)
		}

	case map[string]any:
		if err := canonicalWriteObject(buf, val); err != nil {
			return err
		}

	case []any:
		if err := canonicalWriteArray(buf, val); err != nil {
			return err
		}

	default:
		// Fallback: marshal with standard json package. This handles
		// types we don't explicitly cover.
		encoded, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("jcs: unsupported type %T: %w",
				val, err)
		}
		buf.Write(encoded)
	}

	return nil
}

// canonicalWriteObject writes a JSON object with keys sorted
// lexicographically by Unicode code point value, per JCS.
func canonicalWriteObject(buf *bytes.Buffer, obj map[string]any) error {
	// Collect and sort keys lexicographically. Go's default string
	// comparison is byte-wise which matches Unicode code point ordering
	// for valid UTF-8 strings.
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}

		// Write key as a JSON string.
		escaped, err := json.Marshal(k)
		if err != nil {
			return fmt.Errorf("jcs: failed to marshal key %q: %w",
				k, err)
		}
		buf.Write(escaped)
		buf.WriteByte(':')

		// Write value recursively.
		if err := canonicalWrite(buf, obj[k]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')

	return nil
}

// canonicalWriteArray writes a JSON array, recursing into each element.
func canonicalWriteArray(buf *bytes.Buffer, arr []any) error {
	buf.WriteByte('[')
	for i, elem := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := canonicalWrite(buf, elem); err != nil {
			return err
		}
	}
	buf.WriteByte(']')

	return nil
}
