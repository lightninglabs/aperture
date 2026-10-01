package mpp

import (
	"net/http"
	"strings"
	"testing"
)

// htmlEscapes are the three escapes encoding/json applies by default so its
// output is safe to inline in an HTML document. ECMAScript's JSON.stringify
// applies none of them, so a document we put on the wire whole must not carry
// them either.
var htmlEscapes = []string{"\\u0026", "\\u003c", "\\u003e"}

// TestEncodersDoNotEscapeForHTML checks the documents that go on the wire
// whole, rather than through the canonicalizer, are spelled the way a
// JavaScript peer spells them. A description as ordinary as "Q&A endpoint" is
// enough to make the bytes differ, and both a receipt and a credential are
// compared and stored as bytes.
func TestEncodersDoNotEscapeForHTML(t *testing.T) {
	t.Parallel()

	const raw = "Q&A <b> endpoint"

	t.Run("receipt", func(t *testing.T) {
		t.Parallel()

		h := make(http.Header)
		err := SetReceiptHeader(h, &Receipt{
			Status:      ReceiptStatusSuccess,
			Method:      MethodLightning,
			Timestamp:   "2025-01-15T12:00:00Z",
			Reference:   raw,
			ChallengeID: "cid",
		})
		if err != nil {
			t.Fatalf("SetReceiptHeader: %v", err)
		}

		decoded, err := Base64URLDecode(h.Get(HeaderPaymentReceipt))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}

		assertPlainJSON(t, string(decoded), raw)
	})

	t.Run("credential", func(t *testing.T) {
		t.Parallel()

		encoded, err := EncodeCredential(&Credential{
			Challenge: ChallengeEcho{
				ID:          "cid",
				Realm:       raw,
				Method:      MethodLightning,
				Intent:      IntentCharge,
				Request:     "cmVx",
				Description: raw,
			},
			Payload: []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("EncodeCredential: %v", err)
		}

		decoded, err := Base64URLDecode(encoded)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}

		assertPlainJSON(t, string(decoded), raw)

		// The blessed encoder has to survive our own parser, or we have
		// traded one divergence for another.
		hdr := make(http.Header)
		hdr.Set(headerAuthorization, AuthScheme+" "+encoded)
		if _, err := ParseCredential(&hdr); err != nil {
			t.Fatalf("ParseCredential: %v", err)
		}
	})
}

// TestPlainJSONStillEscapesLineSeparators pins the one place we knowingly
// differ from JSON.stringify. encoding/json escapes U+2028 and U+2029
// unconditionally so its output stays safe to inline in a script, and
// SetEscapeHTML(false) does not turn that off.
//
// We accept it rather than hand-rolling a second string encoder. Unlike the
// HTML escapes, neither character is plausible in a realm or a description,
// and neither of these documents feeds the challenge HMAC: the two slots that
// do, request and opaque, are base64url and cannot contain either. A peer that
// spells them literally still parses to the same document. This test exists so
// the next person comparing bytes across implementations finds the reason
// rather than a surprise.
func TestPlainJSONStillEscapesLineSeparators(t *testing.T) {
	t.Parallel()

	encoded, err := MarshalPlainJSON(map[string]string{
		"s": "a b c",
	})
	if err != nil {
		t.Fatalf("MarshalPlainJSON: %v", err)
	}

	const want = `{"s":"a\u2028b\u2029c"}`
	if string(encoded) != want {
		t.Fatalf("got %s, want %s", encoded, want)
	}
}

// TestPlainJSONHasNoTrailingNewline guards the one hazard in reaching for
// json.Encoder, which appends a newline where json.Marshal does not. A stray
// byte here would change every encoded credential and receipt.
func TestPlainJSONHasNoTrailingNewline(t *testing.T) {
	t.Parallel()

	encoded, err := MarshalPlainJSON(map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("MarshalPlainJSON: %v", err)
	}

	if strings.HasSuffix(string(encoded), "\n") {
		t.Fatalf("trailing newline in %q", encoded)
	}
}

// assertPlainJSON fails when s carries an HTML escape, and confirms the raw
// text survived rather than being dropped.
func assertPlainJSON(t *testing.T, s, raw string) {
	t.Helper()

	for _, esc := range htmlEscapes {
		if strings.Contains(s, esc) {
			t.Fatalf("found %s in %s", esc, s)
		}
	}

	if !strings.Contains(s, raw) {
		t.Fatalf("%q missing from %s", raw, s)
	}
}
