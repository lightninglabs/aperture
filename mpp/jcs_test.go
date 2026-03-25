package mpp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCanonicalizeKeyOrdering verifies that object keys are sorted
// lexicographically by Unicode code point value per JCS (RFC 8785).
func TestCanonicalizeKeyOrdering(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected string
	}{
		{
			name: "alphabetical keys",
			input: map[string]any{
				"c": "3",
				"a": "1",
				"b": "2",
			},
			expected: `{"a":"1","b":"2","c":"3"}`,
		},
		{
			name: "mixed case keys sorted by code point",
			input: map[string]any{
				"b": "2",
				"A": "1",
				"a": "3",
			},
			// Uppercase 'A' (0x41) comes before lowercase 'a'
			// (0x61) and 'b' (0x62).
			expected: `{"A":"1","a":"3","b":"2"}`,
		},
		{
			name:     "empty object",
			input:    map[string]any{},
			expected: `{}`,
		},
		{
			name: "single key",
			input: map[string]any{
				"key": "value",
			},
			expected: `{"key":"value"}`,
		},
		{
			name: "numeric string keys",
			input: map[string]any{
				"10": "ten",
				"2":  "two",
				"1":  "one",
			},
			// Lexicographic: "1" < "10" < "2".
			expected: `{"1":"one","10":"ten","2":"two"}`,
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

// TestCanonicalizeValueTypes verifies correct serialization of various JSON
// value types per JCS.
func TestCanonicalizeValueTypes(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{
			name:     "null",
			input:    nil,
			expected: "null",
		},
		{
			name:     "true",
			input:    true,
			expected: "true",
		},
		{
			name:     "false",
			input:    false,
			expected: "false",
		},
		{
			name:     "string",
			input:    "hello",
			expected: `"hello"`,
		},
		{
			name:     "string with escapes",
			input:    "hello\nworld",
			expected: `"hello\nworld"`,
		},
		{
			name:     "string with unicode",
			input:    "café",
			expected: `"café"`,
		},
		{
			name:     "integer as float64",
			input:    float64(42),
			expected: "42",
		},
		{
			name:     "zero",
			input:    float64(0),
			expected: "0",
		},
		{
			name:     "negative integer",
			input:    float64(-1),
			expected: "-1",
		},
		{
			name:     "large integer",
			input:    float64(1000000),
			expected: "1000000",
		},
		{
			name:     "empty string",
			input:    "",
			expected: `""`,
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

// TestCanonicalizeNested verifies correct handling of nested objects and
// arrays.
func TestCanonicalizeNested(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{
			name: "nested object",
			input: map[string]any{
				"outer": map[string]any{
					"b": "2",
					"a": "1",
				},
			},
			expected: `{"outer":{"a":"1","b":"2"}}`,
		},
		{
			name: "array of strings",
			input: map[string]any{
				"items": []any{"c", "a", "b"},
			},
			// Arrays preserve order per JCS.
			expected: `{"items":["c","a","b"]}`,
		},
		{
			name: "array of objects",
			input: map[string]any{
				"list": []any{
					map[string]any{
						"z": "1",
						"a": "2",
					},
				},
			},
			expected: `{"list":[{"a":"2","z":"1"}]}`,
		},
		{
			name:     "empty array",
			input:    []any{},
			expected: `[]`,
		},
		{
			name: "deeply nested",
			input: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": "deep",
					},
				},
			},
			expected: `{"a":{"b":{"c":"deep"}}}`,
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

// TestCanonicalizeJSON verifies round-trip canonicalization from raw JSON
// bytes.
func TestCanonicalizeJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "reorder keys",
			input:    `{"z":"last","a":"first","m":"middle"}`,
			expected: `{"a":"first","m":"middle","z":"last"}`,
		},
		{
			name:     "strip whitespace",
			input:    `{ "b" : "2" , "a" : "1" }`,
			expected: `{"a":"1","b":"2"}`,
		},
		{
			name:     "nested reorder",
			input:    `{"outer":{"z":"1","a":"2"}}`,
			expected: `{"outer":{"a":"2","z":"1"}}`,
		},
		{
			name: "charge request example",
			input: `{
				"amount": "100",
				"currency": "sat",
				"description": "Weather report",
				"methodDetails": {
					"invoice": "lnbc1u1p...",
					"paymentHash": "bc230847...",
					"network": "mainnet"
				}
			}`,
			expected: `{"amount":"100","currency":"sat","description":"Weather report","methodDetails":{"invoice":"lnbc1u1p...","network":"mainnet","paymentHash":"bc230847..."}}`,
		},
		{
			name:     "idempotent on already canonical",
			input:    `{"a":"1","b":"2"}`,
			expected: `{"a":"1","b":"2"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := CanonicalizeJSON([]byte(tc.input))
			require.NoError(t, err)
			require.Equal(t, tc.expected, string(result))
		})
	}
}

// TestCanonicalizeJSONInvalid verifies error handling for invalid JSON input.
func TestCanonicalizeJSONInvalid(t *testing.T) {
	_, err := CanonicalizeJSON([]byte(`{invalid`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "jcs: failed to unmarshal JSON")
}
