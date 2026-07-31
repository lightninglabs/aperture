// Package mppvectors builds the cross-implementation test vectors for the
// Payment HTTP Authentication Scheme.
//
// The vectors are produced by calling the exported mpp functions, so the file
// they generate is a record of what this implementation actually does rather
// than a hand-written description of what it is supposed to do. An independent
// implementation that replays the file agrees with us byte for byte or it does
// not, with no room in between.
//
// Every vector is deterministic. Nothing here reads a clock, a random source or
// the environment, so regenerating the file on a different machine has to
// produce identical bytes. Any diff is a real behaviour change and the
// staleness check in this package's test will say so.
package mppvectors

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/lightninglabs/aperture/mpp"
)

// Version is the schema version of the generated file. Bump it whenever the
// shape of the document changes, as opposed to the values inside it.
const Version = 1

// hmacSecret is the fixed secret the challenge identifier vectors are computed
// under. It is a test constant and carries no security meaning.
const hmacSecret = "mpp-test-vector-secret"

// File is the generated vector document.
type File struct {
	// Version is the schema version of this document.
	Version int `json:"version"`

	// Description tells a reader of the file where it came from.
	Description string `json:"description"`

	// HMACSecretHex is the secret the challengeIds vectors were computed
	// under, hex encoded.
	HMACSecretHex string `json:"hmacSecretHex"`

	// Canonicalization holds JSON canonicalization vectors.
	Canonicalization []CanonicalVector `json:"canonicalization"`

	// Base64URL holds base64url encoding and decoding vectors.
	Base64URL []Base64Vector `json:"base64url"`

	// ChallengeHeaders holds emit-and-reparse vectors for a single
	// challenge.
	ChallengeHeaders []ChallengeHeaderVector `json:"challengeHeaders"`

	// ChallengeLists holds parse vectors for field values that carry more
	// than one challenge.
	ChallengeLists []ChallengeListVector `json:"challengeLists"`

	// ChallengeIDs holds HMAC binding vectors.
	ChallengeIDs []ChallengeIDVector `json:"challengeIds"`

	// Credentials holds Authorization header vectors.
	Credentials []CredentialVector `json:"credentials"`

	// Receipts holds Payment-Receipt header vectors.
	Receipts []ReceiptVector `json:"receipts"`
}

// CanonicalVector is one RFC 8785 canonicalization case.
type CanonicalVector struct {
	// Name identifies the case.
	Name string `json:"name"`

	// Note explains what the case is guarding, where that is not obvious.
	Note string `json:"note,omitempty"`

	// Input is the JSON text to canonicalize.
	Input string `json:"input"`

	// Canonical is the expected canonical form, or empty when Error is set.
	Canonical string `json:"canonical,omitempty"`

	// Error is set when canonicalizing the input has to fail.
	Error bool `json:"error,omitempty"`
}

// Base64Vector is one base64url case. Decoding vectors that must be rejected
// leave DecodedHex empty and set Error.
type Base64Vector struct {
	// Name identifies the case.
	Name string `json:"name"`

	// Note explains what the case is guarding.
	Note string `json:"note,omitempty"`

	// Encoded is the encoded form.
	Encoded string `json:"encoded"`

	// DecodedHex is the hex of the bytes Encoded decodes to.
	DecodedHex string `json:"decodedHex,omitempty"`

	// Canonical says whether Encoded is what encoding DecodedHex produces,
	// as opposed to merely an accepted spelling of it.
	Canonical bool `json:"canonical"`

	// Error is set when decoding Encoded has to fail.
	Error bool `json:"error,omitempty"`
}

// ChallengeParamsJSON mirrors mpp.ChallengeParams with wire names, so the
// vector file does not depend on Go field names.
type ChallengeParamsJSON struct {
	ID          string `json:"id"`
	Realm       string `json:"realm"`
	Method      string `json:"method"`
	Intent      string `json:"intent"`
	Request     string `json:"request"`
	Expires     string `json:"expires,omitempty"`
	Digest      string `json:"digest,omitempty"`
	Description string `json:"description,omitempty"`
	Opaque      string `json:"opaque,omitempty"`
}

// ChallengeHeaderVector is one emit-and-reparse case.
type ChallengeHeaderVector struct {
	// Name identifies the case.
	Name string `json:"name"`

	// Note explains what the case is guarding.
	Note string `json:"note,omitempty"`

	// Params are the parameters handed to the emitter.
	Params ChallengeParamsJSON `json:"params"`

	// Header is the WWW-Authenticate field value the emitter produced.
	Header string `json:"header"`

	// Parsed is what parsing Header yields. It equals Params whenever the
	// values are ones the grammar can carry.
	Parsed ChallengeParamsJSON `json:"parsed"`
}

// ChallengeListVector is one multi-challenge parse case.
type ChallengeListVector struct {
	// Name identifies the case.
	Name string `json:"name"`

	// Note explains what the case is guarding.
	Note string `json:"note,omitempty"`

	// Header is the raw WWW-Authenticate field value.
	Header string `json:"header"`

	// Challenges are the Payment challenges it carries, in order.
	Challenges []ChallengeParamsJSON `json:"challenges,omitempty"`

	// Error is set when parsing Header has to fail.
	Error bool `json:"error,omitempty"`
}

// ChallengeIDVector is one HMAC binding case.
type ChallengeIDVector struct {
	// Name identifies the case.
	Name string `json:"name"`

	// Note explains what the case is guarding.
	Note string `json:"note,omitempty"`

	// Params are the parameters the identifier binds. Its id field is the
	// computed value.
	Params ChallengeParamsJSON `json:"params"`

	// HMACInput is the pipe-delimited seven-slot string the identifier is
	// computed over, so that a mismatch can be localized.
	HMACInput string `json:"hmacInput"`
}

// CredentialVector is one Authorization header case.
type CredentialVector struct {
	// Name identifies the case.
	Name string `json:"name"`

	// Note explains what the case is guarding.
	Note string `json:"note,omitempty"`

	// CredentialJSON is the credential document before encoding.
	CredentialJSON string `json:"credentialJson"`

	// Header is the complete Authorization field value.
	Header string `json:"header"`

	// Error is set when parsing Header has to fail.
	Error bool `json:"error,omitempty"`
}

// ReceiptVector is one Payment-Receipt header case.
type ReceiptVector struct {
	// Name identifies the case.
	Name string `json:"name"`

	// Note explains what the case is guarding.
	Note string `json:"note,omitempty"`

	// ReceiptJSON is the receipt document before encoding.
	ReceiptJSON string `json:"receiptJson"`

	// Header is the Payment-Receipt field value.
	Header string `json:"header"`
}

// Generate builds the vector file and renders it as indented JSON with a
// trailing newline, which is the exact byte sequence that belongs on disk.
func Generate() ([]byte, error) {
	file, err := Build()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")

	// The vector file is data, not markup, so leave the characters
	// encoding/json would escape for HTML safety alone as they are. The
	// canonicalization vectors depend on that: escaping an ampersand here
	// would hide the very divergence those vectors exist to pin down.
	enc.SetEscapeHTML(false)

	if err := enc.Encode(file); err != nil {
		return nil, fmt.Errorf("encoding vectors: %w", err)
	}

	return buf.Bytes(), nil
}

// Build assembles every vector group.
func Build() (*File, error) {
	canonical, err := canonicalVectors()
	if err != nil {
		return nil, err
	}

	challengeHeaders, err := challengeHeaderVectors()
	if err != nil {
		return nil, err
	}

	challengeLists, err := challengeListVectors()
	if err != nil {
		return nil, err
	}

	credentials, err := credentialVectors()
	if err != nil {
		return nil, err
	}

	receipts, err := receiptVectors()
	if err != nil {
		return nil, err
	}

	return &File{
		Version: Version,
		Description: "Cross-implementation test vectors for the " +
			"Payment HTTP Authentication Scheme, generated by " +
			"github.com/lightninglabs/aperture/mpp/mppvectors. " +
			"Do not edit by hand; run " +
			"go run ./cmd/gen-mpp-vectors instead.",
		HMACSecretHex:    hex.EncodeToString([]byte(hmacSecret)),
		Canonicalization: canonical,
		Base64URL:        base64Vectors(),
		ChallengeHeaders: challengeHeaders,
		ChallengeLists:   challengeLists,
		ChallengeIDs:     challengeIDVectors(),
		Credentials:      credentials,
		Receipts:         receipts,
	}, nil
}

// canonicalVectors covers RFC 8785 section 3.2: member ordering, string
// escaping and number rendering.
func canonicalVectors() ([]CanonicalVector, error) {
	cases := []struct {
		name  string
		note  string
		input string
		fail  bool
	}{
		{
			name:  "empty object",
			input: `{}`,
		},
		{
			name:  "empty array",
			input: `[]`,
		},
		{
			name:  "member ordering",
			input: `{"z":1,"m":2,"a":3,"A":4,"10":5,"2":6}`,
			note: "Member names sort by code unit, so \"10\" " +
				"precedes \"2\" and \"A\" precedes \"a\".",
		},
		{
			name: "member ordering across the surrogate range",
			note: "RFC 8785 section 3.2.3 orders member names by " +
				"their UTF-16 code units, not their code " +
				"points. A code point at or above U+10000 " +
				"encodes as a surrogate pair starting at " +
				"U+D800, so it sorts before U+E000 rather " +
				"than after it. Sorting UTF-8 bytes, which " +
				"is what a naive Go implementation does, " +
				"gets this backwards.",
			input: `{"\ue000":1,"\ud800\udc00":2,` +
				`"\uffff":3,"\ud7ff":4}`,
		},
		{
			name:  "nested objects and arrays",
			input: `{"b":{"d":1,"c":2},"a":[3,{"f":4,"e":5}]}`,
			note:  "Array order is significant and is preserved.",
		},
		{
			name:  "literals",
			input: `{"t":true,"f":false,"n":null}`,
		},
		{
			name: "ampersand and angle brackets are not escaped",
			note: "Go's encoding/json escapes \"<\", \">\" and " +
				"\"&\" so its output is safe to inline in " +
				"HTML. ECMAScript's JSON.stringify, which " +
				"RFC 8785 section 3.2.2.2 defers to, does " +
				"not. A description as ordinary as \"Q&A\" " +
				"hashes differently if this is wrong.",
			input: `{"description":"Q&A <b> endpoint"}`,
		},
		{
			name: "line and paragraph separators are not escaped",
			note: "Go's encoding/json escapes U+2028 and U+2029 " +
				"so its output is safe to inline in " +
				"JavaScript. JSON.stringify does not.",
			input: `{"s":"x\u2028y\u2029z"}`,
		},
		{
			name:  "shorthand control escapes",
			input: `{"s":"\b\f\n\r\t"}`,
		},
		{
			name:  "control characters without a shorthand",
			note:  "Escaped as lowercase four-digit \\u escapes.",
			input: `{"s":"\u0000\u0001\u001f"}`,
		},
		{
			name:  "delete and the solidus are not escaped",
			input: `{"s":"a/b\u007f"}`,
		},
		{
			name:  "quote and reverse solidus",
			input: `{"s":"he said \"go\" \\ home"}`,
		},
		{
			name:  "non-ascii passes through as utf-8",
			input: `{"s":"café あ 😀"}`,
		},
		{
			name: "integers above the exactly representable range",
			note: "RFC 8785 section 3.2.2.3 requires the " +
				"shortest decimal that round-trips, not the " +
				"exact value of the double. Above 2^53 the " +
				"two part ways.",
			input: `{"a":36028797018963968,` +
				`"b":1234567890123456789,` +
				`"c":9007199254740991,"d":9007199254740993}`,
		},
		{
			name: "magnitudes beyond the int64 range",
			note: "An implementation that converts to a 64-bit " +
				"integer on the way to its output is not " +
				"merely wrong here, it is architecture " +
				"dependent: arm64 saturates at MaxInt64 " +
				"while amd64 wraps to MinInt64 and flips " +
				"the sign.",
			input: `{"a":9.223372036854776e18,` +
				`"b":-9.223372036854776e18,"c":1e21,` +
				`"d":-1e21,"e":1e100}`,
		},
		{
			name: "notation boundaries",
			note: "Positional notation is used when the decimal " +
				"exponent lies in (-6, 21], exponential " +
				"notation otherwise, and the exponent " +
				"carries no leading zero, so 1e-7 renders " +
				"as \"1e-7\" and not \"1e-07\".",
			input: `{"a":1e-6,"b":1e-7,"c":1e-10,"d":1e20,` +
				`"e":1e21,"f":5e-324,` +
				`"g":1.7976931348623157e308}`,
		},
		{
			name:  "signed zero",
			note:  "ECMAScript renders both zeroes as \"0\".",
			input: `{"a":0,"b":-0,"c":0.0,"d":-0.0}`,
		},
		{
			name: "number literals are re-rendered",
			note: "The literal text of the input is not " +
				"canonical, so it is parsed and rendered " +
				"afresh.",
			input: `{"a":1.0,"b":1e0,"c":1.000e3,"d":0.1,` +
				`"e":-4.35,"f":1.0000000000000002}`,
		},
		{
			name: "charge request",
			input: `{"amount":"100","currency":"sat",` +
				`"description":"Weather report",` +
				`"methodDetails":{"invoice":"lnbc1u1p",` +
				`"paymentHash":"bc230847",` +
				`"network":"mainnet"}}`,
		},
		{
			name: "session request",
			input: `{"amount":"3","currency":"sat",` +
				`"unitType":"token","depositInvoice":` +
				`"lnbc500n1p","paymentHash":"aa11bb22",` +
				`"depositAmount":"5000","idleTimeout":"900"}`,
		},
		{
			name: "opaque correlation data",
			note: "The opaque parameter is the one piece of " +
				"server-defined JSON that reaches the " +
				"challenge HMAC, so its canonical form is " +
				"load bearing.",
			input: `{"tier":"gold & silver",` +
				`"orderId":36028797018963968,` +
				`"note":"he said \"go\""}`,
		},
		{
			name:  "malformed input is rejected",
			input: `{"a":`,
			fail:  true,
		},
	}

	out := make([]CanonicalVector, 0, len(cases))
	for _, c := range cases {
		vector := CanonicalVector{
			Name:  c.name,
			Note:  c.note,
			Input: c.input,
		}

		canonical, err := mpp.CanonicalizeJSON([]byte(c.input))
		switch {
		case c.fail && err == nil:
			return nil, fmt.Errorf("canonicalization vector %q "+
				"was expected to fail", c.name)

		case c.fail:
			vector.Error = true

		case err != nil:
			return nil, fmt.Errorf("canonicalization vector %q: %w",
				c.name, err)

		default:
			vector.Canonical = string(canonical)
		}

		out = append(out, vector)
	}

	return out, nil
}

// base64Vectors covers both directions, including the spellings a lenient
// decoder would wrongly accept.
func base64Vectors() []Base64Vector {
	encodes := []struct {
		name  string
		bytes []byte
	}{
		{name: "empty", bytes: []byte{}},
		{name: "one byte", bytes: []byte{0x00}},
		{name: "two bytes", bytes: []byte{0xff, 0xfe}},
		{name: "three bytes", bytes: []byte{0xff, 0xfe, 0xfd}},
		{name: "ascii", bytes: []byte("hello")},
		{
			name: "url-unsafe alphabet",
			bytes: []byte{
				0xfb, 0xff, 0xbf, 0x03, 0xef, 0xff,
			},
		},
		{
			name:  "canonical json",
			bytes: []byte(`{"amount":"100","currency":"sat"}`),
		},
	}

	rejects := []struct {
		name    string
		note    string
		encoded string
	}{
		{
			name: "non-zero trailing bits",
			note: "A lenient decoder discards the bits the " +
				"final character cannot represent, so " +
				"\"AB\" and \"AA\" decode alike and one " +
				"payload gains a second spelling.",
			encoded: "AB",
		},
		{
			name:    "embedded newline",
			note:    "A lenient decoder skips line endings.",
			encoded: "aGVs\nbG8",
		},
		{
			name:    "embedded carriage return",
			encoded: "aGVs\rbG8",
		},
		{
			name:    "standard rather than url alphabet",
			encoded: "a+b/",
		},
		{
			name:    "internal padding",
			encoded: "aGV=bG8",
		},
		{
			name:    "excess padding",
			encoded: "aGVsbG8===",
		},
		{
			name:    "padded to a length that is not a multiple of four",
			encoded: "aGVsbG=",
		},
		{
			name:    "dangling character",
			encoded: "aGVsb",
		},
		{
			name:    "not base64 at all",
			encoded: "!!!invalid!!!",
		},
	}

	out := make([]Base64Vector, 0, len(encodes)+len(rejects)+1)
	for _, c := range encodes {
		out = append(out, Base64Vector{
			Name:       "encode " + c.name,
			Encoded:    mpp.Base64URLEncode(c.bytes),
			DecodedHex: hex.EncodeToString(c.bytes),
			Canonical:  true,
		})
	}

	// Padding is accepted on input even though it is never emitted.
	out = append(out, Base64Vector{
		Name: "decode accepts padding",
		Note: "The scheme emits the unpadded form, but a padded " +
			"input decodes to the same bytes.",
		Encoded:    "aGVsbG8=",
		DecodedHex: hex.EncodeToString([]byte("hello")),
		Canonical:  false,
	})

	for _, c := range rejects {
		out = append(out, Base64Vector{
			Name:    "reject " + c.name,
			Note:    c.note,
			Encoded: c.encoded,
			Error:   true,
		})
	}

	return out
}

// challengeHeaderVectors emits a challenge and parses it back, which is the
// round trip the challenge HMAC depends on.
func challengeHeaderVectors() ([]ChallengeHeaderVector, error) {
	chargeRequest, err := mpp.EncodeRequest(&mpp.ChargeRequest{
		Amount:      "100",
		Currency:    mpp.CurrencySat,
		Description: "Weather report",
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     "lnbc1u1pexample",
			PaymentHash: "bc230847",
			Network:     "mainnet",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encoding charge request: %w", err)
	}

	sessionRequest, err := mpp.EncodeRequest(&mpp.SessionRequest{
		Amount:         "3",
		Currency:       mpp.CurrencySat,
		UnitType:       "token",
		DepositInvoice: "lnbc500n1pexample",
		PaymentHash:    "aa11bb22",
		DepositAmount:  "5000",
		IdleTimeout:    "900",
	})
	if err != nil {
		return nil, fmt.Errorf("encoding session request: %w", err)
	}

	cases := []struct {
		name   string
		note   string
		params *mpp.ChallengeParams
	}{
		{
			name: "minimal charge challenge",
			params: &mpp.ChallengeParams{
				ID:      "x7Tg2pLqR9mKvNwY3hBcZa",
				Realm:   "api.example.com",
				Method:  mpp.MethodLightning,
				Intent:  mpp.IntentCharge,
				Request: chargeRequest,
			},
		},
		{
			name: "session challenge with every parameter",
			params: &mpp.ChallengeParams{
				ID:          "kM9xPqWvT2nJrHsY4aDfEb",
				Realm:       "api.example.com",
				Method:      mpp.MethodLightning,
				Intent:      mpp.IntentSession,
				Request:     sessionRequest,
				Expires:     "2026-03-15T12:05:00Z",
				Digest:      "sha-256=:X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE=:",
				Description: "Inference session",
				Opaque:      "eyJvcmRlcklkIjoiMTIzIn0",
			},
		},
		{
			name: "quotation mark in a parameter",
			note: "RFC 9110 section 5.6.4 escapes it as a " +
				"quoted-pair. Go's %q verb happens to " +
				"produce the same bytes here, but it is not " +
				"the same rule, and a parser that stops at " +
				"the first quote truncates everything after " +
				"this parameter.",
			params: &mpp.ChallengeParams{
				ID:          "quote-id",
				Realm:       `api "quoted" example`,
				Method:      mpp.MethodLightning,
				Intent:      mpp.IntentCharge,
				Request:     chargeRequest,
				Description: `say "hi"`,
			},
		},
		{
			name: "reverse solidus in a parameter",
			note: "Escaped as a quoted-pair and unescaped by " +
				"dropping the backslash. Go's %q verb would " +
				"double it and a parser that does not " +
				"unescape would hand back the doubled form.",
			params: &mpp.ChallengeParams{
				ID:      "backslash-id",
				Realm:   `a\b\\c`,
				Method:  mpp.MethodLightning,
				Intent:  mpp.IntentCharge,
				Request: chargeRequest,
			},
		},
		{
			name: "separators inside a parameter",
			note: "A comma and an equals sign inside a quoted " +
				"value must not be read as structure.",
			params: &mpp.ChallengeParams{
				ID:          "separator-id",
				Realm:       "one, two=three",
				Method:      mpp.MethodLightning,
				Intent:      mpp.IntentCharge,
				Request:     chargeRequest,
				Description: `Payment id="spoofed"`,
			},
		},
		{
			name: "horizontal tab and obs-text",
			note: "Both are permitted verbatim inside a " +
				"quoted-string. Go's %q verb escapes the " +
				"tab and any non-printable rune, and RFC " +
				"9110 has no way to undo that.",
			params: &mpp.ChallengeParams{
				ID:          "obstext-id",
				Realm:       "tab\there",
				Method:      mpp.MethodLightning,
				Intent:      mpp.IntentCharge,
				Request:     chargeRequest,
				Description: "café über",
			},
		},
	}

	out := make([]ChallengeHeaderVector, 0, len(cases))
	for _, c := range cases {
		if err := mpp.ValidateChallengeParams(c.params); err != nil {
			return nil, fmt.Errorf("challenge header vector %q: %w",
				c.name, err)
		}

		h := make(http.Header)
		mpp.SetChallengeHeader(h, c.params)
		header := h.Get("WWW-Authenticate")

		parsed, err := mpp.ParseChallengeHeader(header)
		if err != nil {
			return nil, fmt.Errorf("challenge header vector %q: %w",
				c.name, err)
		}

		out = append(out, ChallengeHeaderVector{
			Name:   c.name,
			Note:   c.note,
			Params: toParamsJSON(c.params),
			Header: header,
			Parsed: toParamsJSON(parsed),
		})
	}

	return out, nil
}

// challengeListVectors covers field values that carry more than one challenge,
// which is what a client built on fetch always sees.
func challengeListVectors() ([]ChallengeListVector, error) {
	cases := []struct {
		name string
		note string
		in   string
		fail bool
	}{
		{
			name: "two intents comma-joined",
			note: "The Fetch standard joins repeated response " +
				"header lines with \", \" before handing " +
				"them to the caller, so a 402 offering both " +
				"a charge and a session arrives as one " +
				"string. A parser that does not split it " +
				"merges the two last-wins and produces a " +
				"challenge that pairs one offer's " +
				"identifier with the other's invoice.",
			in: `Payment id="charge-id", realm="api.example.com", ` +
				`method="lightning", intent="charge", ` +
				`request="Q0hBUkdF", ` +
				`Payment id="session-id", realm="api.example.com", ` +
				`method="lightning", intent="session", ` +
				`request="U0VTU0lPTg"`,
		},
		{
			name: "no space after the separating comma",
			in: `Payment id="a", realm="r", method="lightning", ` +
				`intent="charge", request="cQ",` +
				`Payment id="b", realm="r", method="lightning", ` +
				`intent="session", request="cQ"`,
		},
		{
			name: "empty list elements between challenges",
			note: "RFC 9110 section 5.6.1 tolerates empty list " +
				"elements.",
			in: `Payment id="a", realm="r", method="lightning", ` +
				`intent="charge", request="cQ" , , ` +
				`Payment id="b", realm="r", method="lightning", ` +
				`intent="session", request="cQ"`,
		},
		{
			name: "another scheme's parameters do not leak forward",
			note: "Parameters belonging to a challenge for a " +
				"scheme we do not implement must not be " +
				"attributed to the Payment challenge that " +
				"follows.",
			in: `Basic realm="other", ` +
				`Payment id="a", realm="r", method="lightning", ` +
				`intent="charge", request="cQ", ` +
				`Digest realm="other", intent="bogus", ` +
				`Payment id="b", realm="r", method="lightning", ` +
				`intent="session", request="cQ"`,
		},
		{
			name: "comma and scheme name inside a quoted value",
			in: `Payment id="a,b", realm="Payment id=c", ` +
				`method="lightning", intent="charge", ` +
				`request="cQ"`,
		},
		{
			name: "case-insensitive scheme and parameter names",
			note: "RFC 9110 sections 11.1 and 11.2 make both " +
				"case-insensitive.",
			in: `payment ID="a", Realm="r", METHOD="lightning", ` +
				`intent="charge", ReQuEsT="cQ"`,
		},
		{
			name: "unquoted token values",
			in: `Payment id=a, realm=r, method=lightning, ` +
				`intent=charge, request=cQ`,
		},
		{
			name: "repeated parameter takes the first occurrence",
			note: "RFC 9110 leaves the outcome undefined. " +
				"Taking the first denies an on-path element " +
				"the ability to override a value the origin " +
				"server already set by appending its own.",
			in: `Payment id="first", realm="r", ` +
				`method="lightning", intent="charge", ` +
				`request="cQ", id="second", intent="session"`,
		},
		{
			name: "an incomplete offer is skipped, not fatal",
			in: `Payment id="a", realm="r", ` +
				`Payment id="b", realm="r", ` +
				`method="lightning", intent="session", ` +
				`request="cQ"`,
		},
		{
			name: "no payment challenge",
			in:   `Basic realm="other"`,
			fail: true,
		},
		{
			name: "every offer is incomplete",
			in:   `Payment id="a", realm="r"`,
			fail: true,
		},
	}

	out := make([]ChallengeListVector, 0, len(cases))
	for _, c := range cases {
		vector := ChallengeListVector{
			Name:   c.name,
			Note:   c.note,
			Header: c.in,
		}

		challenges, err := mpp.ParseChallengeList(c.in)
		switch {
		case c.fail && err == nil:
			return nil, fmt.Errorf("challenge list vector %q was "+
				"expected to fail", c.name)

		case c.fail:
			vector.Error = true

		case err != nil:
			return nil, fmt.Errorf("challenge list vector %q: %w",
				c.name, err)

		default:
			for _, challenge := range challenges {
				vector.Challenges = append(
					vector.Challenges,
					toParamsJSON(challenge),
				)
			}
		}

		out = append(out, vector)
	}

	return out, nil
}

// challengeIDVectors pins the seven-slot HMAC binding, including the slot
// layout, so a mismatch can be localized to a slot rather than to the whole
// construction.
func challengeIDVectors() []ChallengeIDVector {
	secret := []byte(hmacSecret)

	cases := []struct {
		name   string
		note   string
		params *mpp.ChallengeParams
	}{
		{
			name: "required slots only",
			note: "The four optional slots are empty strings, " +
				"not omitted, so the pipe delimiters remain.",
			params: &mpp.ChallengeParams{
				Realm:   "api.example.com",
				Method:  mpp.MethodLightning,
				Intent:  mpp.IntentCharge,
				Request: "eyJhbW91bnQiOiIxMDAifQ",
			},
		},
		{
			name: "every slot populated",
			params: &mpp.ChallengeParams{
				Realm:       "api.example.com",
				Method:      mpp.MethodLightning,
				Intent:      mpp.IntentSession,
				Request:     "eyJhbW91bnQiOiIzIn0",
				Expires:     "2026-03-15T12:05:00Z",
				Digest:      "sha-256=:X48E9q:",
				Opaque:      "eyJvcmRlcklkIjoiMTIzIn0",
				Description: "not covered by the HMAC",
			},
		},
		{
			name: "pipe inside a slot value",
			note: "The delimiter is not escaped, so a value " +
				"carrying it is a way to make two different " +
				"parameter sets share an HMAC input. These " +
				"two vectors record what today's " +
				"construction does with that.",
			params: &mpp.ChallengeParams{
				Realm:   "a|b",
				Method:  mpp.MethodLightning,
				Intent:  mpp.IntentCharge,
				Request: "cQ",
			},
		},
		{
			name: "the same pipes split differently",
			params: &mpp.ChallengeParams{
				Realm:   "a",
				Method:  "b|" + mpp.MethodLightning,
				Intent:  mpp.IntentCharge,
				Request: "cQ",
			},
		},
	}

	out := make([]ChallengeIDVector, 0, len(cases))
	for _, c := range cases {
		params := *c.params
		params.ID = mpp.ComputeChallengeID(secret, &params)

		slots := []string{
			params.Realm, params.Method, params.Intent,
			params.Request, params.Expires, params.Digest,
			params.Opaque,
		}

		out = append(out, ChallengeIDVector{
			Name:      c.name,
			Note:      c.note,
			Params:    toParamsJSON(&params),
			HMACInput: strings.Join(slots, "|"),
		})
	}

	return out
}

// credentialVectors covers the Authorization header, including the malformed
// shapes that have to be rejected.
func credentialVectors() ([]CredentialVector, error) {
	accepted := []struct {
		name string
		note string
		cred *mpp.Credential
	}{
		{
			name: "charge credential",
			cred: &mpp.Credential{
				Challenge: mpp.ChallengeEcho{
					ID:      "kM9xPqWvT2nJrHsY4aDfEb",
					Realm:   "api.example.com",
					Method:  mpp.MethodLightning,
					Intent:  mpp.IntentCharge,
					Request: "eyJhbW91bnQiOiIxMDAifQ",
					Expires: "2026-03-15T12:05:00Z",
				},
				Payload: json.RawMessage(
					`{"preimage":"a3f1e2d4b5c6a7e8"}`,
				),
			},
		},
		{
			name: "session open credential with a source",
			cred: &mpp.Credential{
				Challenge: mpp.ChallengeEcho{
					ID:          "x7Tg2pLqR9mKvNwY3hBcZa",
					Realm:       "api.example.com",
					Method:      mpp.MethodLightning,
					Intent:      mpp.IntentSession,
					Request:     "eyJhbW91bnQiOiIzIn0",
					Description: "Q&A endpoint",
					Opaque:      "eyJvcmRlcklkIjoiMTIzIn0",
					Digest:      "sha-256=:X48E9q:",
				},
				Source: "did:key:z6MkhaXgBZDvotDkL5257" +
					"faiztiGiC2QtKLGpbnnEGta2doK",
				Payload: json.RawMessage(
					`{"action":"open","preimage":"aabb",` +
						`"returnInvoice":"lnbc1p"}`,
				),
			},
		},
		{
			name: "session bearer credential",
			cred: &mpp.Credential{
				Challenge: mpp.ChallengeEcho{
					ID:      "bearer-challenge",
					Realm:   "api.example.com",
					Method:  mpp.MethodLightning,
					Intent:  mpp.IntentSession,
					Request: "eyJhbW91bnQiOiIzIn0",
				},
				Payload: json.RawMessage(
					`{"action":"bearer",` +
						`"sessionId":"aa11bb22",` +
						`"preimage":"ccdd"}`,
				),
			},
		},
	}

	rejected := []struct {
		name   string
		note   string
		header string
	}{
		{
			name:   "wrong scheme",
			header: "Bearer abc123",
		},
		{
			name:   "empty token",
			header: "Payment ",
		},
		{
			name: "token that is not base64url",
			note: "The decoder is strict, so this is rejected " +
				"rather than partially decoded.",
			header: "Payment !!!invalid!!!",
		},
		{
			name:   "token with non-zero trailing bits",
			header: "Payment AB",
		},
		{
			name:   "token that is not json",
			header: "Payment " + mpp.Base64URLEncode([]byte("{oops}")),
		},
		{
			name: "missing challenge.id",
			header: "Payment " + mpp.Base64URLEncode([]byte(
				`{"challenge":{"realm":"r",`+
					`"method":"lightning",`+
					`"intent":"charge","request":"cQ"},`+
					`"payload":{}}`,
			)),
		},
		{
			name: "missing payload",
			header: "Payment " + mpp.Base64URLEncode([]byte(
				`{"challenge":{"id":"i","realm":"r",`+
					`"method":"lightning",`+
					`"intent":"charge","request":"cQ"}}`,
			)),
		},
	}

	out := make([]CredentialVector, 0, len(accepted)+len(rejected)+1)
	for _, c := range accepted {
		credJSON, err := json.Marshal(c.cred)
		if err != nil {
			return nil, fmt.Errorf("credential vector %q: %w",
				c.name, err)
		}

		header := mpp.AuthScheme + " " + mpp.Base64URLEncode(credJSON)

		h := make(http.Header)
		h.Set("Authorization", header)
		if _, err := mpp.ParseCredential(&h); err != nil {
			return nil, fmt.Errorf("credential vector %q does not "+
				"parse: %w", c.name, err)
		}

		out = append(out, CredentialVector{
			Name:           c.name,
			Note:           c.note,
			CredentialJSON: string(credJSON),
			Header:         header,
		})
	}

	// The scheme token is case-insensitive, so the same credential is
	// readable under a lowercased scheme.
	if len(out) > 0 {
		first := out[0]
		out = append(out, CredentialVector{
			Name: "lowercased scheme token",
			Note: "RFC 9110 section 11.1 makes the auth-scheme " +
				"case-insensitive.",
			CredentialJSON: first.CredentialJSON,
			Header: strings.Replace(
				first.Header, mpp.AuthScheme, "payment", 1,
			),
		})
	}

	for _, c := range rejected {
		h := make(http.Header)
		h.Set("Authorization", c.header)
		if _, err := mpp.ParseCredential(&h); err == nil {
			return nil, fmt.Errorf("credential vector %q was "+
				"expected to fail", c.name)
		}

		out = append(out, CredentialVector{
			Name:   "reject " + c.name,
			Note:   c.note,
			Header: c.header,
			Error:  true,
		})
	}

	return out, nil
}

// receiptVectors covers the Payment-Receipt header, which had no vectors at
// all, including the session receipt's int64 fields at magnitudes where an
// int64 and a double part ways.
func receiptVectors() ([]ReceiptVector, error) {
	base := []struct {
		name    string
		note    string
		receipt *mpp.Receipt
	}{
		{
			name: "charge receipt",
			receipt: &mpp.Receipt{
				Status:      mpp.ReceiptStatusSuccess,
				Method:      mpp.MethodLightning,
				Timestamp:   "2026-03-10T21:00:00Z",
				Reference:   "bc230847abcdef1234567890",
				ChallengeID: "kM9xPqWvT2nJrHsY4aDfEb",
			},
		},
		{
			name: "receipt without a challenge id",
			note: "challengeId is optional and is omitted when " +
				"empty.",
			receipt: &mpp.Receipt{
				Status:    mpp.ReceiptStatusSuccess,
				Method:    mpp.MethodLightning,
				Timestamp: "2026-03-10T21:00:00Z",
				Reference: "bc230847",
			},
		},
	}

	sessions := []struct {
		name    string
		note    string
		receipt *mpp.SessionReceipt
	}{
		{
			name: "session close with a refund",
			receipt: &mpp.SessionReceipt{
				Method:       mpp.MethodLightning,
				Reference:    "aa11bb22",
				Status:       mpp.ReceiptStatusSuccess,
				Timestamp:    "2026-03-10T21:00:00Z",
				RefundSats:   140,
				RefundStatus: mpp.RefundStatusSucceeded,
			},
		},
		{
			name: "session close with nothing to refund",
			note: "refundSats is omitted when it is zero.",
			receipt: &mpp.SessionReceipt{
				Method:       mpp.MethodLightning,
				Reference:    "aa11bb22",
				Status:       mpp.ReceiptStatusSuccess,
				Timestamp:    "2026-03-10T21:00:00Z",
				RefundSats:   0,
				RefundStatus: mpp.RefundStatusSkipped,
			},
		},
		{
			name: "refund beyond the exactly representable range",
			note: "refundSats is an int64 and is emitted as a " +
				"JSON number, so a reader that parses JSON " +
				"numbers as doubles cannot recover this " +
				"value exactly. Nothing in the protocol " +
				"produces a refund this large, but the " +
				"encoding has to be pinned all the same.",
			receipt: &mpp.SessionReceipt{
				Method:       mpp.MethodLightning,
				Reference:    "aa11bb22",
				Status:       mpp.ReceiptStatusSuccess,
				Timestamp:    "2026-03-10T21:00:00Z",
				RefundSats:   9007199254740993,
				RefundStatus: mpp.RefundStatusSucceeded,
			},
		},
		{
			name: "largest refund an int64 can hold",
			receipt: &mpp.SessionReceipt{
				Method:       mpp.MethodLightning,
				Reference:    "aa11bb22",
				Status:       mpp.ReceiptStatusSuccess,
				Timestamp:    "2026-03-10T21:00:00Z",
				RefundSats:   math.MaxInt64,
				RefundStatus: mpp.RefundStatusSucceeded,
			},
		},
	}

	events := []struct {
		name  string
		note  string
		event *mpp.NeedTopUpEvent
	}{
		{
			name: "need top-up event",
			note: "Emitted as SSE data when a streaming " +
				"response exhausts the session balance.",
			event: &mpp.NeedTopUpEvent{
				SessionID:       "aa11bb22",
				BalanceSpent:    4860,
				BalanceRequired: 3,
			},
		},
		{
			name: "need top-up beyond the safe integer range",
			event: &mpp.NeedTopUpEvent{
				SessionID:       "aa11bb22",
				BalanceSpent:    36028797018963968,
				BalanceRequired: 1234567890123456789,
			},
		},
	}

	out := make([]ReceiptVector, 0, len(base)+len(sessions)+len(events))
	for _, c := range base {
		encoded, err := json.Marshal(c.receipt)
		if err != nil {
			return nil, fmt.Errorf("receipt vector %q: %w",
				c.name, err)
		}

		h := make(http.Header)
		if err := mpp.SetReceiptHeader(h, c.receipt); err != nil {
			return nil, fmt.Errorf("receipt vector %q: %w",
				c.name, err)
		}

		out = append(out, ReceiptVector{
			Name:        c.name,
			Note:        c.note,
			ReceiptJSON: string(encoded),
			Header:      h.Get(mpp.HeaderPaymentReceipt),
		})
	}

	// The session receipt and the top-up event travel in the same encoding
	// but are not http.Header values, so encode them directly.
	for _, c := range sessions {
		encoded, err := json.Marshal(c.receipt)
		if err != nil {
			return nil, fmt.Errorf("receipt vector %q: %w",
				c.name, err)
		}

		out = append(out, ReceiptVector{
			Name:        c.name,
			Note:        c.note,
			ReceiptJSON: string(encoded),
			Header:      mpp.Base64URLEncode(encoded),
		})
	}

	for _, c := range events {
		encoded, err := json.Marshal(c.event)
		if err != nil {
			return nil, fmt.Errorf("event vector %q: %w",
				c.name, err)
		}

		out = append(out, ReceiptVector{
			Name:        c.name,
			Note:        c.note,
			ReceiptJSON: string(encoded),
			Header:      mpp.Base64URLEncode(encoded),
		})
	}

	return out, nil
}

// toParamsJSON converts challenge parameters into the wire-named shape the
// vector file uses.
func toParamsJSON(p *mpp.ChallengeParams) ChallengeParamsJSON {
	return ChallengeParamsJSON{
		ID:          p.ID,
		Realm:       p.Realm,
		Method:      p.Method,
		Intent:      p.Intent,
		Request:     p.Request,
		Expires:     p.Expires,
		Digest:      p.Digest,
		Description: p.Description,
		Opaque:      p.Opaque,
	}
}
