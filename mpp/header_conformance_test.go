package mpp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// TestChallengeHeaderQuotedStringRoundTrip verifies that a challenge parameter
// containing the two characters an RFC 9110 quoted-string has to escape comes
// back unchanged.
//
// This is the property the whole scheme rests on. The server computes the
// challenge HMAC over the parameter values it minted, and it verifies that HMAC
// over the values the client echoed back, so any value that does not survive
// the header verbatim produces a credential the server rejects for reasons the
// client cannot see.
func TestChallengeHeaderQuotedStringRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{
			name:  "double quote",
			value: `api "quoted" example`,
		},
		{
			name:  "backslash",
			value: `a\b`,
		},
		{
			name:  "backslash before a quote",
			value: `a\"b`,
		},
		{
			name:  "trailing backslash",
			value: `trailing\`,
		},
		{
			name:  "comma, which also separates auth-params",
			value: "one, two",
		},
		{
			name:  "equals sign",
			value: "a=b",
		},
		{
			name:  "the scheme name itself",
			value: "Payment id=spoof",
		},
		{
			name:  "horizontal tab",
			value: "a\tb",
		},
		{
			name:  "non-ascii",
			value: "café über",
		},
		{
			name:  "empty-looking quoted content",
			value: `""`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := &ChallengeParams{
				ID:          "id-" + tc.name,
				Realm:       tc.value,
				Method:      MethodLightning,
				Intent:      IntentCharge,
				Request:     "eyJhIjoiMSJ9",
				Expires:     "2026-03-15T12:05:00Z",
				Digest:      "sha-256=:X48E9q:",
				Description: tc.value,
				Opaque:      tc.value,
			}

			// Every one of these values is representable, so the
			// validator has to accept them.
			require.NoError(t, ValidateChallengeParams(params))

			h := make(http.Header)
			SetChallengeHeader(h, params)

			parsed, err := ParseChallengeHeader(
				h.Get(headerWWWAuthenticate),
			)
			require.NoError(t, err)
			require.Equal(t, params, parsed)
		})
	}
}

// TestSetChallengeHeaderUsesRFC9110Quoting verifies that the emitted header
// escapes only the two characters RFC 9110 Section 5.6.4 calls for.
//
// Go's %q verb applies its own source-literal quoting instead, which escapes
// everything strconv.IsPrint rejects. A parser following RFC 9110 unescapes a
// quoted-pair by dropping the backslash and keeping the next octet, so Go's
// quoting of a tab arrives as the two characters "\" and "t".
func TestSetChallengeHeaderUsesRFC9110Quoting(t *testing.T) {
	h := make(http.Header)
	SetChallengeHeader(h, &ChallengeParams{
		ID:      "i",
		Realm:   "tab\there and café",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "r",
	})

	value := h.Get(headerWWWAuthenticate)
	require.Contains(t, value, "realm=\"tab\there and café\"")
	// Go's own quoting would have rendered the tab and the non-ASCII
	// rune as escapes, neither of which RFC 9110 knows how to undo.
	require.NotContains(t, value, `\t`)
	require.NotContains(t, value, `\u00e9`)
}

// TestSetChallengeHeaderRejectsHeaderInjection verifies that a parameter
// carrying a line ending cannot split the response header.
//
// The RFC 9110 quoted-string grammar has no way to carry a control character
// other than horizontal tab, not even as a quoted-pair, so these octets are
// dropped on emission. ValidateChallengeParams exists so that a caller can find
// out before that happens.
func TestSetChallengeHeaderRejectsHeaderInjection(t *testing.T) {
	injected := "evil\r\nX-Injected: yes"

	params := &ChallengeParams{
		ID:      "i",
		Realm:   injected,
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "r",
	}

	err := ValidateChallengeParams(params)
	require.Error(t, err)
	require.Contains(t, err.Error(), "realm")

	h := make(http.Header)
	SetChallengeHeader(h, params)

	value := h.Get(headerWWWAuthenticate)
	require.NotContains(t, value, "\r")
	require.NotContains(t, value, "\n")
	require.Contains(t, value, `realm="evilX-Injected: yes"`)
}

// TestParseChallengeListSplitsChallenges verifies that a field value carrying
// more than one challenge is split rather than flattened into one.
//
// A client built on fetch only ever sees this shape, because the Fetch standard
// joins repeated response header lines with ", " before handing them to the
// caller. Flattening them merges the challenges last-wins, so a 402 offering
// both a charge and a session yields the charge's id beside the session's
// invoice, and a client that pays it settles the session deposit while claiming
// to answer the charge.
func TestParseChallengeListSplitsChallenges(t *testing.T) {
	joined := `Payment id="charge-id", realm="api.example.com", ` +
		`method="lightning", intent="charge", request="CHARGE_REQ", ` +
		`Payment id="session-id", realm="api.example.com", ` +
		`method="lightning", intent="session", request="SESSION_REQ"`

	challenges, err := ParseChallengeList(joined)
	require.NoError(t, err)
	require.Len(t, challenges, 2)

	require.Equal(t, "charge-id", challenges[0].ID)
	require.Equal(t, IntentCharge, challenges[0].Intent)
	require.Equal(t, "CHARGE_REQ", challenges[0].Request)

	require.Equal(t, "session-id", challenges[1].ID)
	require.Equal(t, IntentSession, challenges[1].Intent)
	require.Equal(t, "SESSION_REQ", challenges[1].Request)

	// The single-challenge entry point has to return a coherent challenge
	// rather than a blend of the two.
	first, err := ParseChallengeHeader(joined)
	require.NoError(t, err)
	require.Equal(t, challenges[0], first)
}

// TestParseChallengeListCommaBoundaries checks the shapes where the comma that
// separates two challenges is hard to tell from the comma that separates two
// auth-params.
func TestParseChallengeListCommaBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		intent []string
	}{
		{
			name: "comma inside a quoted value",
			input: `Payment id="a,b", realm="r", ` +
				`method="lightning", intent="charge", ` +
				`request="q"`,
			intent: []string{IntentCharge},
		},
		{
			name: "the scheme name inside a quoted value",
			input: `Payment id="x", realm="Payment id=y", ` +
				`method="lightning", intent="charge", ` +
				`request="q"`,
			intent: []string{IntentCharge},
		},
		{
			name: "no space after the separating comma",
			input: `Payment id="a", realm="r", ` +
				`method="lightning", intent="charge",` +
				`Payment id="b", realm="r", ` +
				`method="lightning", intent="session", ` +
				`request="q"`,
			intent: []string{IntentSession},
		},
		{
			name: "empty list elements between challenges",
			input: `Payment id="a", realm="r", ` +
				`method="lightning", intent="charge", ` +
				`request="q" , , ` +
				`Payment id="b", realm="r", ` +
				`method="lightning", intent="session", ` +
				`request="q"`,
			intent: []string{IntentCharge, IntentSession},
		},
		{
			name: "a challenge for another scheme in between",
			input: `Basic realm="other", ` +
				`Payment id="a", realm="r", ` +
				`method="lightning", intent="charge", ` +
				`request="q"`,
			intent: []string{IntentCharge},
		},
		{
			name: "another scheme's params do not leak forward",
			input: `Payment id="a", realm="r", ` +
				`method="lightning", intent="charge", ` +
				`request="q", ` +
				`Digest realm="other", intent="bogus", ` +
				`Payment id="b", realm="r", ` +
				`method="lightning", intent="session", ` +
				`request="q"`,
			intent: []string{IntentCharge, IntentSession},
		},
		{
			name: "unquoted token values",
			input: `Payment id=a, realm=r, method=lightning, ` +
				`intent=charge, request=q, ` +
				`Payment id=b, realm=r, method=lightning, ` +
				`intent=session, request=q`,
			intent: []string{IntentCharge, IntentSession},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			challenges, err := ParseChallengeList(tc.input)
			require.NoError(t, err)

			intents := make([]string, len(challenges))
			for i, c := range challenges {
				intents[i] = c.Intent
			}
			require.Equal(t, tc.intent, intents)
		})
	}
}

// TestParseChallengeHeadersAcrossLines verifies that challenges are collected
// from every WWW-Authenticate field line, which is the shape a Go client sees
// when the server emits them separately.
func TestParseChallengeHeadersAcrossLines(t *testing.T) {
	h := make(http.Header)

	SetChallengeHeader(h, &ChallengeParams{
		ID:      "charge-id",
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJjaGFyZ2UiOiJ0cnVlIn0",
	})
	SetChallengeHeader(h, &ChallengeParams{
		ID:      "session-id",
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentSession,
		Request: "eyJzZXNzaW9uIjoidHJ1ZSJ9",
	})

	challenges, err := ParseChallengeHeaders(h)
	require.NoError(t, err)
	require.Len(t, challenges, 2)
	require.Equal(t, IntentCharge, challenges[0].Intent)
	require.Equal(t, IntentSession, challenges[1].Intent)

	// Comma-joining the same two lines, which is what fetch hands a
	// browser client, has to yield the same two challenges.
	joined := make(http.Header)
	joined.Set(
		headerWWWAuthenticate,
		strings.Join(h.Values(headerWWWAuthenticate), ", "),
	)

	fromJoined, err := ParseChallengeHeaders(joined)
	require.NoError(t, err)
	require.Equal(t, challenges, fromJoined)
}

// TestParseChallengeListSkipsUnusableChallenges verifies that one malformed
// offer does not deny the client the others, and that a value with no usable
// Payment challenge is still an error.
func TestParseChallengeListSkipsUnusableChallenges(t *testing.T) {
	challenges, err := ParseChallengeList(
		`Payment id="a", realm="r", ` +
			`Payment id="b", realm="r", method="lightning", ` +
			`intent="session", request="q"`,
	)
	require.NoError(t, err)
	require.Len(t, challenges, 1)
	require.Equal(t, "b", challenges[0].ID)

	_, err = ParseChallengeList(`Payment id="a", realm="r"`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing method")

	_, err = ParseChallengeList(`Basic realm="other"`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not use Payment scheme")
}

// TestParseChallengeHeaderCaseInsensitive verifies that the auth-scheme token
// and the auth-param names are matched case-insensitively, as RFC 9110 Sections
// 11.1 and 11.2 require.
func TestParseChallengeHeaderCaseInsensitive(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name: "lowercase scheme",
			input: `payment id="a", realm="r", ` +
				`method="lightning", intent="charge", ` +
				`request="q"`,
		},
		{
			name: "uppercase scheme",
			input: `PAYMENT id="a", realm="r", ` +
				`method="lightning", intent="charge", ` +
				`request="q"`,
		},
		{
			name: "uppercase parameter names",
			input: `Payment ID="a", REALM="r", ` +
				`METHOD="lightning", INTENT="charge", ` +
				`REQUEST="q"`,
		},
		{
			name: "mixed case parameter names",
			input: `Payment Id="a", Realm="r", ` +
				`Method="lightning", Intent="charge", ` +
				`Request="q"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := ParseChallengeHeader(tc.input)
			require.NoError(t, err)
			require.Equal(t, "a", parsed.ID)
			require.Equal(t, "r", parsed.Realm)
			require.Equal(t, MethodLightning, parsed.Method)
			require.Equal(t, IntentCharge, parsed.Intent)
			require.Equal(t, "q", parsed.Request)
		})
	}
}

// TestParseCredentialCaseInsensitiveScheme verifies the same case-insensitivity
// on the Authorization side.
func TestParseCredentialCaseInsensitiveScheme(t *testing.T) {
	credJSON, err := json.Marshal(&Credential{
		Challenge: ChallengeEcho{
			ID:      "i",
			Realm:   "r",
			Method:  MethodLightning,
			Intent:  IntentCharge,
			Request: "q",
		},
		Payload: json.RawMessage(`{"preimage":"ab"}`),
	})
	require.NoError(t, err)
	token := Base64URLEncode(credJSON)

	for _, scheme := range []string{"Payment", "payment", "PAYMENT"} {
		h := make(http.Header)
		h.Set(headerAuthorization, scheme+" "+token)

		parsed, err := ParseCredential(&h)
		require.NoError(t, err)
		require.Equal(t, "i", parsed.Challenge.ID)
	}
}

// TestParseAuthParamsDuplicateIsFirstWins pins the resolution of a repeated
// auth-param name.
//
// RFC 9110 leaves the outcome undefined. Taking the first occurrence denies an
// on-path element the ability to override a value the origin server already set
// simply by appending its own copy, which is the only direction in which the
// choice has a security consequence.
func TestParseAuthParamsDuplicateIsFirstWins(t *testing.T) {
	parsed, err := ParseChallengeHeader(
		`Payment id="first", realm="r", method="lightning", ` +
			`intent="charge", request="q", id="second", ` +
			`intent="session"`,
	)
	require.NoError(t, err)
	require.Equal(t, "first", parsed.ID)
	require.Equal(t, IntentCharge, parsed.Intent)

	params := parseAuthParams(`a="1", a="2", A="3"`)
	require.Equal(t, map[string]string{"a": "1"}, params)
}

// TestBase64URLDecodeStrict verifies that the decoder accepts exactly one
// encoding of any given payload.
//
// base64.RawURLEncoding on its own is lenient in two ways that let distinct
// strings decode to the same bytes: it discards the trailing bits a final
// partial character cannot represent, and it silently skips embedded line
// endings. The encoded forms are compared and hashed as strings elsewhere in
// this protocol, so more than one spelling of a payload is a way for two peers
// to disagree about which credential arrived.
func TestBase64URLDecodeStrict(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		errMsg string
	}{
		{
			name:   "non-zero trailing bits",
			input:  "AB",
			errMsg: "illegal base64 data",
		},
		{
			name:   "embedded newline",
			input:  "aGVs\nbG8",
			errMsg: "invalid base64url character",
		},
		{
			name:   "embedded carriage return",
			input:  "aGVs\rbG8",
			errMsg: "invalid base64url character",
		},
		{
			name:   "standard base64 alphabet",
			input:  "a+b/",
			errMsg: "invalid base64url character",
		},
		{
			name:   "internal padding",
			input:  "aGV=bG8",
			errMsg: "invalid base64url character",
		},
		{
			name:   "excess padding",
			input:  "aGVsbG8===",
			errMsg: "padding characters",
		},
		{
			name:   "padding to a length that is not a multiple of four",
			input:  "aGVsbG=",
			errMsg: "not a multiple of 4",
		},
		{
			name:   "dangling character",
			input:  "aGVsb",
			errMsg: "illegal base64 data",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Base64URLDecode(tc.input)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errMsg)
		})
	}

	// The canonical spellings still decode.
	decoded, err := Base64URLDecode("aGVsbG8")
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), decoded)

	decoded, err = Base64URLDecode("aGVsbG8=")
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), decoded)

	decoded, err = Base64URLDecode("")
	require.NoError(t, err)
	require.Empty(t, decoded)
}

// TestParseCredentialSizeLimit verifies that the encoded credential limit is
// exactly the encoded length of the largest payload we are willing to
// unmarshal, rather than an approximation of it.
func TestParseCredentialSizeLimit(t *testing.T) {
	// A payload of the full permitted size encodes to exactly the limit,
	// whether or not the client pads it.
	full := Base64URLEncode(make([]byte, maxCredentialSize))
	require.Equal(t, maxEncodedCredentialSize-2, len(full))

	h := make(http.Header)
	h.Set(headerAuthorization, AuthScheme+" "+full)

	// It gets past the size check and fails on its contents instead.
	_, err := ParseCredential(&h)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to unmarshal")

	oversized := Base64URLEncode(make([]byte, maxCredentialSize+8))
	h.Set(headerAuthorization, AuthScheme+" "+oversized)

	_, err = ParseCredential(&h)
	require.Error(t, err)
	require.Contains(t, err.Error(), "credential too large")
}

// TestChallengeHeaderPropertyRoundTrip asserts across arbitrary parameter
// values that whatever SetChallengeHeader emits, ParseChallengeHeader reads
// back unchanged, so long as the values are ones the grammar can carry.
func TestChallengeHeaderPropertyRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		params := drawChallengeParams(rt)

		h := make(http.Header)
		SetChallengeHeader(h, params)

		value := h.Get(headerWWWAuthenticate)
		require.NotContains(rt, value, "\r")
		require.NotContains(rt, value, "\n")

		parsed, err := ParseChallengeHeader(value)
		require.NoError(rt, err, "parsing %q", value)
		require.Equal(rt, params, parsed)
	})
}

// TestChallengeHeaderPropertyListRoundTrip asserts that a comma-joined list of
// arbitrary challenges splits back into exactly the challenges that went into
// it, in order.
func TestChallengeHeaderPropertyListRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		count := rapid.IntRange(1, 4).Draw(rt, "count")

		h := make(http.Header)
		want := make([]*ChallengeParams, count)
		for i := range want {
			want[i] = drawChallengeParams(rt)
			SetChallengeHeader(h, want[i])
		}

		joined := make(http.Header)
		joined.Set(
			headerWWWAuthenticate,
			strings.Join(h.Values(headerWWWAuthenticate), ", "),
		)

		got, err := ParseChallengeHeaders(joined)
		require.NoError(rt, err)
		require.Equal(rt, want, got)
	})
}

// TestChallengeHeaderPropertyHMACSurvives ties the round trip to the thing that
// actually depends on it: the challenge identifier a server recomputes over the
// parameters the client echoed back.
func TestChallengeHeaderPropertyHMACSurvives(t *testing.T) {
	secret := []byte("a fixed test secret")

	rapid.Check(t, func(rt *rapid.T) {
		params := drawChallengeParams(rt)
		params.ID = ComputeChallengeID(secret, params)

		h := make(http.Header)
		SetChallengeHeader(h, params)

		parsed, err := ParseChallengeHeader(
			h.Get(headerWWWAuthenticate),
		)
		require.NoError(rt, err)

		// The client echoes what it parsed, so this is the credential
		// path in miniature.
		echoed := (&ChallengeEcho{
			ID:          parsed.ID,
			Realm:       parsed.Realm,
			Method:      parsed.Method,
			Intent:      parsed.Intent,
			Request:     parsed.Request,
			Expires:     parsed.Expires,
			Description: parsed.Description,
			Opaque:      parsed.Opaque,
			Digest:      parsed.Digest,
		}).ToChallengeParams()

		require.True(
			rt, VerifyChallengeID(secret, echoed, parsed.ID),
			"challenge %q does not verify after a round trip "+
				"through %q", params.ID,
			h.Get(headerWWWAuthenticate),
		)
	})
}

// TestParseChallengeListPropertyNoPanic asserts that arbitrary header-shaped
// input is either parsed or rejected, never a crash, since this parser runs on
// bytes a client took off the wire.
func TestParseChallengeListPropertyNoPanic(t *testing.T) {
	alphabet := []string{
		"Payment", "payment", "Basic", "id", "realm", "method",
		"intent", "request", "opaque", "=", ",", " ", "\t", `"`,
		`\`, `\"`, "abc", "", "==", "'", ";", "Payment ", "a=b",
	}

	rapid.Check(t, func(rt *rapid.T) {
		parts := rapid.SliceOfN(
			rapid.SampledFrom(alphabet), 0, 24,
		).Draw(rt, "parts")

		challenges, err := ParseChallengeList(strings.Join(parts, ""))
		if err != nil {
			require.Nil(rt, challenges)
			return
		}

		// Anything returned has to be a complete challenge.
		require.NotEmpty(rt, challenges)
		for _, c := range challenges {
			require.NotEmpty(rt, c.Realm)
			require.NotEmpty(rt, c.Method)
			require.NotEmpty(rt, c.Intent)
			require.NotEmpty(rt, c.Request)
		}
	})
}

// drawChallengeParams draws a challenge whose parameter values are all
// representable in a quoted-string, drawn from an alphabet that concentrates on
// the characters where the grammar bites.
func drawChallengeParams(rt *rapid.T) *ChallengeParams {
	alphabet := []string{
		"a", "Z", "0", " ", "\t", `"`, `\`, ",", "=", ";", "/",
		"Payment", "id=", "café", "ÿ", "-", "_", ".",
	}

	value := func(label string, allowEmpty bool) string {
		minLen := 1
		if allowEmpty {
			minLen = 0
		}

		parts := rapid.SliceOfN(
			rapid.SampledFrom(alphabet), minLen, 5,
		).Draw(rt, label)

		return strings.Join(parts, "")
	}

	return &ChallengeParams{
		ID:          value("id", false),
		Realm:       value("realm", false),
		Method:      value("method", false),
		Intent:      value("intent", false),
		Request:     value("request", false),
		Expires:     value("expires", true),
		Digest:      value("digest", true),
		Description: value("description", true),
		Opaque:      value("opaque", true),
	}
}
