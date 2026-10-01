package mpp

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// TestCanonicalizeNumberShortestForm verifies that numbers are rendered as the
// shortest decimal that round-trips, per RFC 8785 section 3.2.2.3, rather than
// as their exact integer value.
//
// The two forms diverge for every integer above 2^53, because a double can no
// longer represent consecutive integers there and the shortest string that
// selects a given double stops being the exact value of that double.
func TestCanonicalizeNumberShortestForm(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected string
	}{
		{
			name: "first integer above the exactly " +
				"representable range",
			input:    36028797018963968,
			expected: "36028797018963970",
		},
		{
			name:     "large integer literal",
			input:    1234567890123456789,
			expected: "1234567890123456800",
		},
		{
			name:     "largest exactly representable integer",
			input:    9007199254740991,
			expected: "9007199254740991",
		},
		{
			name:     "one past the exactly representable integers",
			input:    9007199254740992,
			expected: "9007199254740992",
		},
		{
			name:     "odd integer that no double holds",
			input:    9007199254740993,
			expected: "9007199254740992",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Canonicalize(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, string(result))
		})
	}
}

// TestCanonicalizeNumberArchitectureIndependent pins the canonical form of the
// magnitudes whose conversion to int64 is architecture dependent.
//
// The Go specification leaves the result of converting an out-of-range float to
// an integer up to the implementation, and the implementations disagree: arm64
// saturates at MaxInt64 while amd64 wraps to MinInt64, flipping the sign. A
// canonicalizer that converts to int64 on the way to its output therefore mints
// a different challenge HMAC depending on which machine the server runs on,
// which is a self-inconsistency in a wire format. Canonicalization must never
// depend on that conversion, so none of these values may render as an int64
// boundary.
func TestCanonicalizeNumberArchitectureIndependent(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected string
	}{
		{
			name:     "just above MaxInt64",
			input:    9.223372036854776e18,
			expected: "9223372036854776000",
		},
		{
			name:     "just below MinInt64",
			input:    -9.223372036854776e18,
			expected: "-9223372036854776000",
		},
		{
			name:     "far above the int64 range",
			input:    1e21,
			expected: "1e+21",
		},
		{
			name:     "far below the int64 range",
			input:    -1e21,
			expected: "-1e+21",
		},
		{
			name:     "beyond any integer type",
			input:    1e100,
			expected: "1e+100",
		},
		{
			name:     "largest double",
			input:    math.MaxFloat64,
			expected: "1.7976931348623157e+308",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Canonicalize(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, string(result))

			// The saturated and the wrapped conversions are the two
			// ways the old implementation could go wrong, and both
			// of them produce one of these two strings.
			require.NotContains(
				t, string(result), "9223372036854775807",
			)
			require.NotContains(
				t, string(result), "9223372036854775808",
			)
		})
	}
}

// TestCanonicalizeNumberFormat covers the boundaries at which the ECMAScript
// Number::toString algorithm switches between positional and exponential
// notation, and the shape of the exponent it emits.
func TestCanonicalizeNumberFormat(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected string
	}{
		{
			name:     "zero",
			input:    0,
			expected: "0",
		},
		{
			// ECMAScript renders the negative zero as "0", so a
			// canonicalizer that simply hands the value to Go's
			// float formatter would emit "-0" and disagree.
			name:     "negative zero",
			input:    math.Copysign(0, -1),
			expected: "0",
		},
		{
			name:     "smallest positional magnitude",
			input:    1e-6,
			expected: "0.000001",
		},
		{
			// Go's exponent is zero padded to two digits here,
			// giving "1e-07", while ECMAScript emits "1e-7".
			name:     "largest exponential magnitude below one",
			input:    1e-7,
			expected: "1e-7",
		},
		{
			name:     "two digit negative exponent",
			input:    1e-10,
			expected: "1e-10",
		},
		{
			name:     "three digit negative exponent",
			input:    1e-100,
			expected: "1e-100",
		},
		{
			name:     "smallest subnormal",
			input:    math.SmallestNonzeroFloat64,
			expected: "5e-324",
		},
		{
			name:     "largest positional magnitude",
			input:    1e20,
			expected: "100000000000000000000",
		},
		{
			name:     "smallest exponential magnitude above one",
			input:    1e21,
			expected: "1e+21",
		},
		{
			name:     "fraction",
			input:    0.1,
			expected: "0.1",
		},
		{
			name:     "repeating fraction",
			input:    1.0 / 3.0,
			expected: "0.3333333333333333",
		},
		{
			name:     "negative fraction",
			input:    -4.35,
			expected: "-4.35",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Canonicalize(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, string(result))
		})
	}
}

// TestCanonicalizeNumberRejectsNonFinite verifies that the values with no JSON
// representation are reported rather than silently mangled.
func TestCanonicalizeNumberRejectsNonFinite(t *testing.T) {
	for _, v := range []float64{
		math.NaN(), math.Inf(1), math.Inf(-1),
	} {
		_, err := Canonicalize(v)
		require.Error(t, err)
		require.Contains(t, err.Error(), "no JSON representation")
	}
}

// TestCanonicalizeJSONNumber verifies that a json.Number is re-rendered rather
// than copied through, since the literal text of the input is not canonical.
func TestCanonicalizeJSONNumber(t *testing.T) {
	tests := []struct {
		input    json.Number
		expected string
	}{
		{input: json.Number("1"), expected: "1"},
		{input: json.Number("1.0"), expected: "1"},
		{input: json.Number("1e0"), expected: "1"},
		{input: json.Number("1.000e3"), expected: "1000"},
		{input: json.Number("-0"), expected: "0"},
		{
			input:    json.Number("36028797018963968"),
			expected: "36028797018963970",
		},
	}

	for _, tc := range tests {
		t.Run(string(tc.input), func(t *testing.T) {
			result, err := Canonicalize(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, string(result))
		})
	}

	_, err := Canonicalize(json.Number("not a number"))
	require.Error(t, err)
}

// TestCanonicalizeStringEscaping verifies that strings are escaped exactly as
// ECMAScript's JSON.stringify escapes them, per RFC 8785 section 3.2.2.2.
//
// The interesting cases are the ones where Go's encoding/json escapes more than
// JSON.stringify does. It renders "<", ">" and "&" as unicode escapes so that
// its output is safe to inline in HTML, and it renders U+2028 and U+2029 as
// unicode escapes so that its output is safe to inline in JavaScript. Neither
// is canonical, and a request description as ordinary as "Q&A" would otherwise
// hash differently in Go than in a JavaScript peer.
func TestCanonicalizeStringEscaping(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "ampersand is not escaped",
			input:    "Q&A endpoint",
			expected: `"Q&A endpoint"`,
		},
		{
			name:     "angle brackets are not escaped",
			input:    "a<b>c",
			expected: `"a<b>c"`,
		},
		{
			name:     "line separator is not escaped",
			input:    "x\u2028y",
			expected: "\"x\u2028y\"",
		},
		{
			name:     "paragraph separator is not escaped",
			input:    "x\u2029y",
			expected: "\"x\u2029y\"",
		},
		{
			name:     "quote and backslash",
			input:    `say "hi" \ bye`,
			expected: `"say \"hi\" \\ bye"`,
		},
		{
			name:     "shorthand control escapes",
			input:    "\b\f\n\r\t",
			expected: `"\b\f\n\r\t"`,
		},
		{
			name:     "other control characters",
			input:    "\x00\x01\x1f",
			expected: `"\u0000\u0001\u001f"`,
		},
		{
			name:     "delete is not escaped",
			input:    "\x7f",
			expected: "\"\x7f\"",
		},
		{
			name:     "solidus is not escaped",
			input:    "a/b",
			expected: `"a/b"`,
		},
		{
			name:     "non-ascii passes through as utf-8",
			input:    "café",
			expected: `"café"`,
		},
		{
			name:     "astral plane passes through as utf-8",
			input:    "\U0001f600",
			expected: "\"\U0001f600\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Canonicalize(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.expected, string(result))
		})
	}
}

// TestCanonicalizeStringEscapingInKeys verifies that member names get the same
// escaping treatment as values.
func TestCanonicalizeStringEscapingInKeys(t *testing.T) {
	result, err := Canonicalize(map[string]any{
		"a&b": "1",
		`c"d`: "2",
	})
	require.NoError(t, err)
	require.Equal(t, `{"a&b":"1","c\"d":"2"}`, string(result))
}

// TestCanonicalizeKeyOrderingUTF16 verifies that member names are ordered by
// their UTF-16 code units, per RFC 8785 section 3.2.3.
//
// Ordering by UTF-8 bytes, which is what Go's native string comparison does,
// orders by code point instead. The two disagree above the basic multilingual
// plane: a code point at or above U+10000 encodes in UTF-16 as a surrogate pair
// beginning at U+D800, so it sorts before U+E000 rather than after it. A
// JavaScript peer sorting with Array.prototype.sort compares UTF-16 code units.
func TestCanonicalizeKeyOrderingUTF16(t *testing.T) {
	result, err := Canonicalize(map[string]any{
		"\ue000":     "private use",
		"\uffff":     "noncharacter",
		"\U00010000": "astral",
		"\ud7ff":     "before the surrogates",
	})
	require.NoError(t, err)

	// The astral key comes after the last name in the basic multilingual
	// plane that sorts below U+D800, and before the two that sort above it.
	require.Equal(
		t,
		"{\"\ud7ff\":\"before the surrogates\","+
			"\"\U00010000\":\"astral\","+
			"\"\ue000\":\"private use\","+
			"\"\uffff\":\"noncharacter\"}",
		string(result),
	)
}

// TestCanonicalizeNormalizesNativeTypes verifies that a value which is not one
// of the generic JSON types still comes out canonical, rather than being handed
// straight to json.Marshal, which sorts nothing and renders numbers its own
// way.
func TestCanonicalizeNormalizesNativeTypes(t *testing.T) {
	type nested struct {
		Zulu  string `json:"zulu"`
		Alpha string `json:"alpha"`
	}
	type outer struct {
		Zebra  string `json:"zebra"`
		Apple  int64  `json:"apple"`
		Nested nested `json:"nested"`
		Amount int    `json:"amount"`
	}

	result, err := Canonicalize(&outer{
		Zebra:  "z",
		Apple:  36028797018963968,
		Nested: nested{Zulu: "z", Alpha: "a"},
		Amount: 7,
	})
	require.NoError(t, err)
	require.Equal(
		t,
		`{"amount":7,"apple":36028797018963970,`+
			`"nested":{"alpha":"a","zulu":"z"},"zebra":"z"}`,
		string(result),
	)
}

// TestCanonicalizeSessionFieldsExceedSafeRange exercises the int64 fields the
// session intent carries at magnitudes where an int64 and a double part ways.
// The canonical form has to be the double, since that is the only rendering a
// JavaScript peer can agree with.
func TestCanonicalizeSessionFieldsExceedSafeRange(t *testing.T) {
	event := &NeedTopUpEvent{
		SessionID:       "abc",
		BalanceSpent:    36028797018963968,
		BalanceRequired: 1234567890123456789,
	}

	result, err := Canonicalize(event)
	require.NoError(t, err)
	require.Equal(
		t,
		`{"balanceRequired":1234567890123456800,`+
			`"balanceSpent":36028797018963970,"sessionId":"abc"}`,
		string(result),
	)
}

// TestCanonicalizeJSONRoundTripsOpaque verifies that the opaque parameter, the
// one piece of server-defined JSON that reaches the challenge HMAC, is
// canonicalized end to end.
func TestCanonicalizeJSONRoundTripsOpaque(t *testing.T) {
	opaque := []byte(`{
		"tier": "gold & silver",
		"orderId": 36028797018963968,
		"note": "he said \"go\""
	}`)

	encoded, err := func() (string, error) {
		canonical, err := CanonicalizeJSON(opaque)
		if err != nil {
			return "", err
		}
		return Base64URLEncode(canonical), nil
	}()
	require.NoError(t, err)

	decoded, err := Base64URLDecode(encoded)
	require.NoError(t, err)
	require.Equal(
		t,
		`{"note":"he said \"go\"","orderId":36028797018963970,`+
			`"tier":"gold & silver"}`,
		string(decoded),
	)
}

// TestCanonicalizePropertyNumberIsShortestRoundTrip is the property that the
// architecture dependent int64 conversion violated.
//
// Cross compiling inside a test is awkward, so rather than reproducing the
// symptom on a second architecture we assert the property that makes the
// symptom impossible: the canonical form of a double must be the shortest
// decimal that parses back to exactly that double. An implementation that
// reaches for int64 fails this on both architectures, because the saturated and
// the wrapped values are each nineteen digits long where sixteen suffice, and
// it fails the round trip outright on the architecture that wraps.
func TestCanonicalizePropertyNumberIsShortestRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		f := drawFinite(rt)

		canonical, err := Canonicalize(f)
		require.NoError(rt, err)

		// It has to parse back to exactly the double we started from.
		// The lone exception is the negative zero, whose sign
		// ECMAScript drops on the way out, so both zeroes render as
		// "0" and only one of them survives the trip.
		back, err := strconv.ParseFloat(string(canonical), 64)
		require.NoError(rt, err)
		if f == 0 {
			require.Equal(rt, "0", string(canonical))
		} else {
			require.Equal(
				rt, math.Float64bits(f), math.Float64bits(back),
				"canonical form %q does not round-trip",
				canonical,
			)
		}

		// It has to carry no more significant digits than the shortest
		// round-tripping form does. Go's 'e' format with a precision of
		// -1 is documented to produce that shortest form.
		require.Equal(
			rt, countSignificantDigits(
				strconv.FormatFloat(f, 'e', -1, 64),
			),
			countSignificantDigits(string(canonical)),
			"canonical form %q is not the shortest form of %v",
			canonical, f,
		)
	})
}

// TestCanonicalizePropertyNumberFormat asserts the shape rules of the
// ECMAScript Number::toString algorithm across the whole range of doubles:
// which notation is chosen, and that an exponent never carries a leading zero.
func TestCanonicalizePropertyNumberFormat(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		f := drawFinite(rt)

		canonical, err := Canonicalize(f)
		require.NoError(rt, err)
		out := string(canonical)

		// A signed zero is the one value that loses its sign.
		if f == 0 {
			require.Equal(rt, "0", out)
			return
		}

		abs := math.Abs(f)
		wantExponential := abs < 1e-6 || abs >= 1e21
		require.Equal(rt, wantExponential, strings.Contains(out, "e"),
			"wrong notation for %v: %q", f, out)

		if !wantExponential {
			return
		}

		// ECMAScript always signs the exponent and never pads it, so
		// the character after the sign is a nonzero digit unless the
		// exponent is a bare zero, which this range cannot produce.
		idx := strings.IndexByte(out, 'e')
		exponent := out[idx+1:]
		require.True(
			rt, strings.HasPrefix(exponent, "+") ||
				strings.HasPrefix(exponent, "-"),
			"exponent of %q is unsigned", out,
		)
		require.NotEqual(rt, byte('0'), exponent[1],
			"exponent of %q has a leading zero", out)
	})
}

// TestCanonicalizePropertyIdempotent asserts that canonicalizing an already
// canonical document changes nothing, which is the fixed point every peer has
// to converge on.
func TestCanonicalizePropertyIdempotent(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		doc := drawJSONValue(rt, 0)

		once, err := Canonicalize(doc)
		require.NoError(rt, err)

		twice, err := CanonicalizeJSON(once)
		require.NoError(rt, err)
		require.Equal(rt, string(once), string(twice))
	})
}

// TestCanonicalizePropertyKeyOrderIsUTF16 asserts that whatever order the
// member names arrive in, they leave in UTF-16 code unit order.
func TestCanonicalizePropertyKeyOrderIsUTF16(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		names := rapid.SliceOfNDistinct(
			drawJSONString(), 1, 8, func(s string) string {
				return s
			},
		).Draw(rt, "names")

		obj := make(map[string]any, len(names))
		for i, name := range names {
			obj[name] = float64(i)
		}

		canonical, err := Canonicalize(obj)
		require.NoError(rt, err)

		emitted := memberNames(rt, canonical)
		require.Len(rt, emitted, len(names))

		for i := 1; i < len(emitted); i++ {
			require.Negative(
				rt, compareUTF16Units(
					emitted[i-1], emitted[i],
				),
				"member names %q and %q are out of UTF-16 "+
					"order in %s", emitted[i-1],
				emitted[i], canonical,
			)
		}
	})
}

// TestCanonicalizePropertyStringEscaping asserts that a canonicalized string
// escapes exactly the characters RFC 8785 calls for and no others, by checking
// that every backslash in the output introduces one of the permitted escapes
// and that the result still decodes back to the input.
func TestCanonicalizePropertyStringEscaping(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := drawJSONString().Draw(rt, "s")

		canonical, err := Canonicalize(s)
		require.NoError(rt, err)

		// Whatever escaping was applied has to be reversible.
		var back string
		require.NoError(rt, json.Unmarshal(canonical, &back))
		require.Equal(rt, s, back)

		// Every escape has to be one RFC 8785 permits, and the only
		// unicode escapes permitted are the C0 controls without a
		// shorthand.
		body := string(canonical[1 : len(canonical)-1])
		for i := 0; i < len(body); i++ {
			if body[i] != '\\' {
				require.NotEqual(rt, byte('"'), body[i],
					"unescaped quote in %s", canonical)
				continue
			}

			require.Less(rt, i+1, len(body))
			switch body[i+1] {
			case '"', '\\', 'b', 'f', 'n', 'r', 't':
				i++

			case 'u':
				require.Less(rt, i+5, len(body))
				code, err := strconv.ParseUint(
					body[i+2:i+6], 16, 32,
				)
				require.NoError(rt, err)
				require.Less(rt, code, uint64(0x20),
					"unicode escape %q in %s is not a C0 "+
						"control", body[i:i+6],
					canonical)
				i += 5

			default:
				rt.Fatalf("unexpected escape %q in %s",
					body[i:i+2], canonical)
			}
		}
	})
}

// drawFinite draws a finite float64, mixing the uniform bit pattern generator
// with magnitudes clustered around the boundaries that the format rules care
// about.
func drawFinite(rt *rapid.T) float64 {
	f := rapid.OneOf(
		rapid.Float64(),
		rapid.Float64Range(-1e22, 1e22),
		rapid.Float64Range(-1e-5, 1e-5),
		rapid.Custom(func(t *rapid.T) float64 {
			return float64(rapid.Int64().Draw(t, "i"))
		}),
		rapid.SampledFrom([]float64{
			0, math.Copysign(0, -1), 1e-7, 1e-6, 1e20, 1e21,
			9007199254740991, 9007199254740992, 36028797018963968,
			9.223372036854776e18, -9.223372036854776e18,
			math.MaxFloat64, math.SmallestNonzeroFloat64,
		}),
	).Draw(rt, "f")

	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}

	return f
}

// drawJSONString draws a string out of an alphabet that concentrates on the
// characters where the escaping and ordering rules bite.
func drawJSONString() *rapid.Generator[string] {
	alphabet := []rune{
		'a', 'Z', '0', ' ', '<', '>', '&', '"', '\\', '/', '\'',
		'\b', '\f', '\n', '\r', '\t', 0x00, 0x01, 0x1f, 0x7f,
		'é', '\u2028', '\u2029', '\ud7ff', '\ue000', '\ufffd',
		'\uffff', '\U00010000', '\U0001f600', '\U0010ffff',
	}

	return rapid.Custom(func(t *rapid.T) string {
		runes := rapid.SliceOfN(
			rapid.SampledFrom(alphabet), 0, 6,
		).Draw(t, "runes")

		return string(runes)
	})
}

// drawJSONValue draws an arbitrary generic JSON value, bounded in depth.
func drawJSONValue(rt *rapid.T, depth int) any {
	if depth >= 3 {
		return drawJSONString().Draw(rt, "leaf")
	}

	switch rapid.IntRange(0, 6).Draw(rt, "kind") {
	case 0:
		return nil

	case 1:
		return rapid.Bool().Draw(rt, "bool")

	case 2:
		return drawFinite(rt)

	case 3:
		return drawJSONString().Draw(rt, "str")

	case 4:
		n := rapid.IntRange(0, 3).Draw(rt, "arrayLen")
		out := make([]any, n)
		for i := range out {
			out[i] = drawJSONValue(rt, depth+1)
		}
		return out

	default:
		n := rapid.IntRange(0, 4).Draw(rt, "objectLen")
		out := make(map[string]any, n)
		for i := 0; i < n; i++ {
			key := drawJSONString().Draw(rt, "key")
			out[key] = drawJSONValue(rt, depth+1)
		}
		return out
	}
}

// countSignificantDigits counts the digits of the mantissa of a rendered float,
// ignoring the sign, the decimal point, any exponent, and the leading and
// trailing zeroes that only place the decimal point.
func countSignificantDigits(s string) int {
	if idx := strings.IndexByte(s, 'e'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimPrefix(s, "-")
	s = strings.Replace(s, ".", "", 1)
	s = strings.TrimLeft(s, "0")
	s = strings.TrimRight(s, "0")

	return len(s)
}

// memberNames unmarshals a canonical object and returns its member names in the
// order they were emitted.
func memberNames(rt *rapid.T, canonical []byte) []string {
	var raw map[string]json.RawMessage
	require.NoError(rt, json.Unmarshal(canonical, &raw))

	// json.Unmarshal into a map loses the order, so recover it by scanning
	// the emitted bytes for the name of each member.
	names := make([]string, 0, len(raw))
	rest := string(canonical[1:])
	for len(rest) > 1 {
		name, remainder, err := scanCanonicalMemberName(rest)
		require.NoError(rt, err, "scanning %s", canonical)
		names = append(names, name)
		rest = remainder
	}

	return names
}

// scanCanonicalMemberName reads one member name and its value out of the body
// of a canonical object, returning the name and whatever follows the value.
func scanCanonicalMemberName(s string) (string, string, error) {
	dec := json.NewDecoder(strings.NewReader(s))

	var name string
	if err := dec.Decode(&name); err != nil {
		return "", "", err
	}

	rest := s[dec.InputOffset():]
	if !strings.HasPrefix(rest, ":") {
		return "", "", fmt.Errorf("expected a colon, found %q", rest)
	}
	rest = rest[1:]

	valueDec := json.NewDecoder(strings.NewReader(rest))
	var value json.RawMessage
	if err := valueDec.Decode(&value); err != nil {
		return "", "", err
	}

	return name, strings.TrimPrefix(
		rest[valueDec.InputOffset():], ",",
	), nil
}

// compareUTF16Units orders two strings by their UTF-16 code units. It is a
// deliberately separate transcription of the rule from the one the
// canonicalizer uses, so that the property test does not simply agree with the
// implementation by construction.
func compareUTF16Units(a, b string) int {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))

	for i := range ua {
		if i >= len(ub) {
			return 1
		}
		switch {
		case ua[i] < ub[i]:
			return -1
		case ua[i] > ub[i]:
			return 1
		}
	}

	if len(ua) < len(ub) {
		return -1
	}

	return 0
}
