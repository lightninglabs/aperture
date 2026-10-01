package mpp

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf16"
)

// Canonicalize produces a JSON Canonicalization Scheme (JCS) output per RFC
// 8785 for the given value. JCS defines a deterministic JSON serialization
// that ensures identical logical values produce identical byte sequences.
//
// The implementation covers the whole of RFC 8785 section 3.2:
//
//   - Object member names are sorted by their UTF-16 code unit sequence.
//   - No whitespace is emitted between tokens.
//   - Strings are escaped exactly as ECMAScript's JSON.stringify escapes them.
//   - Numbers are rendered with the ECMAScript Number::toString algorithm,
//     which is the shortest decimal that round-trips back to the same double.
//   - Null and the booleans are emitted literally.
//   - Nested objects and arrays are recursed into.
//
// Values that are not one of the six generic JSON types are first round-tripped
// through encoding/json so they arrive as generic types. That means every
// number is canonicalized as an IEEE 754 double, so an integer larger than 2^53
// is rounded to the nearest representable double before it is rendered. That
// loss is inherent to JCS, which defines numbers in terms of ECMAScript, and it
// is the only rendering that a JavaScript peer can agree with.
//
// NaN and the infinities have no JSON representation and are rejected.
func Canonicalize(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := canonicalWrite(&buf, v, false); err != nil {
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

// canonicalWrite writes the canonical JSON representation of v to buf. The
// normalized flag records whether v already came out of an encoding/json
// round-trip, which bounds the recursion in the default branch to a single
// pass.
func canonicalWrite(buf *bytes.Buffer, v any, normalized bool) error {
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
		canonicalWriteString(buf, val)

	case json.Number:
		// A json.Number carries the literal text from the input, which
		// is not canonical: "1.0", "1e0" and "1" all denote the same
		// value. Parse it and re-render it like any other number.
		f, err := strconv.ParseFloat(val.String(), 64)
		if err != nil {
			return fmt.Errorf("jcs: %q is not a valid JSON "+
				"number: %w", val.String(), err)
		}
		return canonicalWriteNumber(buf, f)

	case float64:
		return canonicalWriteNumber(buf, val)

	case map[string]any:
		return canonicalWriteObject(buf, val)

	case []any:
		return canonicalWriteArray(buf, val)

	default:
		if normalized {
			return fmt.Errorf("jcs: unsupported type %T", val)
		}

		// Anything else, a struct or a native integer or a
		// json.RawMessage, is round-tripped through encoding/json so
		// that it comes back as one of the generic types above.
		// Writing json.Marshal's output straight into the buffer would
		// be wrong, since it neither sorts struct fields nor renders
		// numbers the way ECMAScript does.
		encoded, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("jcs: unsupported type %T: %w",
				val, err)
		}

		var generic any
		if err := json.Unmarshal(encoded, &generic); err != nil {
			return fmt.Errorf("jcs: failed to normalize %T: %w",
				val, err)
		}

		return canonicalWrite(buf, generic, true)
	}

	return nil
}

// canonicalWriteString writes s as a JSON string, escaping exactly the
// characters that ECMAScript's JSON.stringify escapes, as required by RFC 8785
// section 3.2.2.2. That is the quotation mark, the reverse solidus and the C0
// controls, with the two-character shorthands preferred wherever one exists.
//
// We deliberately do not delegate to json.Marshal here. It additionally escapes
// "<", ">" and "&" so that its output is safe to inline in HTML, and it escapes
// U+2028 and U+2029 so that its output is safe to inline in JavaScript.
// JSON.stringify escapes none of those five, so json.Marshal's output diverges
// from the canonical form for any string that contains one. Since the canonical
// form of the request object feeds the challenge HMAC, a description as
// ordinary as "Q&A endpoint" would otherwise hash differently in Go than in
// every JavaScript implementation of this protocol.
func canonicalWriteString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)

		case '\\':
			buf.WriteString(`\\`)

		case '\b':
			buf.WriteString(`\b`)

		case '\f':
			buf.WriteString(`\f`)

		case '\n':
			buf.WriteString(`\n`)

		case '\r':
			buf.WriteString(`\r`)

		case '\t':
			buf.WriteString(`\t`)

		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
				continue
			}

			// WriteRune substitutes U+FFFD for an invalid encoding,
			// which is what encoding/json does as well.
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

// canonicalWriteNumber writes f using the ECMAScript Number::toString
// algorithm that RFC 8785 section 3.2.2.3 mandates: the shortest decimal string
// that parses back to exactly f.
//
// Note in particular that this is not the exact decimal value of f. The double
// nearest to 36028797018963968 is that integer exactly, but the shortest string
// that round-trips to it is "36028797018963970", and it is the shorter string
// that JCS requires.
func canonicalWriteNumber(buf *bytes.Buffer, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("jcs: %v has no JSON representation", f)
	}

	// ECMAScript renders both signed zeroes as "0", while Go's formatter
	// would render the negative one as "-0", so take it out of the picture
	// before formatting.
	if f == 0 {
		buf.WriteString("0")
		return nil
	}

	// Number::toString uses positional notation when the decimal exponent
	// of the shortest round-tripping digit string lies in (-6, 21], and
	// exponential notation otherwise. Those two bounds on the exponent are
	// the same as these two bounds on the magnitude.
	format := byte('f')
	if abs := math.Abs(f); abs < 1e-6 || abs >= 1e21 {
		format = 'e'
	}

	out := strconv.AppendFloat(nil, f, format, -1, 64)
	if format == 'e' {
		out = trimExponentZeros(out)
	}
	buf.Write(out)

	return nil
}

// trimExponentZeros strips the zero padding that Go puts in front of an
// exponent. Go's 'e' format always emits at least two exponent digits, so it
// renders 1e-7 as "1e-07", while ECMAScript emits the fewest digits that carry
// the value and renders it as "1e-7".
func trimExponentZeros(b []byte) []byte {
	e := bytes.IndexByte(b, 'e')
	if e < 0 {
		return b
	}

	// Step over the exponent sign, which Go always emits.
	start := e + 1
	if start < len(b) && (b[start] == '+' || b[start] == '-') {
		start++
	}

	// Leave the final digit alone so that an exponent of zero, which Go
	// would render as "e+00", does not lose all of its digits.
	digits := start
	for digits < len(b)-1 && b[digits] == '0' {
		digits++
	}
	if digits == start {
		return b
	}

	// Cap the prefix so that append copies rather than overwriting b.
	return append(b[:start:start], b[digits:]...)
}

// canonicalWriteObject writes a JSON object with its member names sorted, per
// RFC 8785 section 3.2.3.
func canonicalWriteObject(buf *bytes.Buffer, obj map[string]any) error {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareUTF16)

	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}

		canonicalWriteString(buf, k)
		buf.WriteByte(':')

		if err := canonicalWrite(buf, obj[k], false); err != nil {
			return err
		}
	}
	buf.WriteByte('}')

	return nil
}

// compareUTF16 orders two strings by their UTF-16 code unit sequences, which is
// the ordering RFC 8785 section 3.2.3 mandates for object member names.
//
// This is not the same as Go's native string comparison. Comparing UTF-8 bytes
// orders strings by code point, and the two orderings disagree above the basic
// multilingual plane: a code point at or above U+10000 encodes in UTF-16 as a
// surrogate pair starting at U+D800, so it sorts before U+E000 rather than
// after it. A JavaScript peer sorting the same names with Array.prototype.sort
// compares UTF-16 code units, so Go has to as well.
func compareUTF16(a, b string) int {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))

	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return cmp.Compare(ua[i], ub[i])
		}
	}

	return cmp.Compare(len(ua), len(ub))
}

// canonicalWriteArray writes a JSON array, recursing into each element. Array
// order is significant and is left untouched.
func canonicalWriteArray(buf *bytes.Buffer, arr []any) error {
	buf.WriteByte('[')
	for i, elem := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := canonicalWrite(buf, elem, false); err != nil {
			return err
		}
	}
	buf.WriteByte(']')

	return nil
}
