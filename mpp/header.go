package mpp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	// headerAuthorization is the standard HTTP Authorization header.
	headerAuthorization = "Authorization"

	// headerWWWAuthenticate is the standard HTTP WWW-Authenticate header.
	headerWWWAuthenticate = "WWW-Authenticate"

	// maxCredentialSize is the largest decoded credential we are willing to
	// unmarshal.
	maxCredentialSize = 64 * 1024

	// maxEncodedCredentialSize is the largest encoded credential we accept.
	// It is the length of maxCredentialSize bytes in base64url including
	// padding, so that a padded credential of the full permitted size still
	// fits. Unpadded input of that size is two characters shorter and is
	// accepted as well.
	maxEncodedCredentialSize = (maxCredentialSize + 2) / 3 * 4
)

// Base64URLEncode encodes data using base64url encoding without padding per
// RFC 4648 Section 5, as required by the Payment HTTP Authentication Scheme.
func Base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// Base64URLDecode decodes a base64url-encoded string. Trailing padding is
// accepted, since the spec permits either form, but nothing else is: the input
// must consist only of base64url alphabet characters, the padding must bring
// the input to a multiple of four characters, and the bits that a final partial
// character cannot represent must be zero.
//
// The strictness is deliberate. base64.RawURLEncoding on its own silently skips
// embedded newlines and silently discards the non-zero trailing bits of an
// input like "AB", so several distinct strings decode to the same bytes. The
// encoded forms are compared and hashed as strings elsewhere in this protocol,
// so letting more than one of them stand for the same payload invites a peer to
// disagree with us about which credential it just received.
func Base64URLDecode(s string) ([]byte, error) {
	unpadded := strings.TrimRight(s, "=")

	padding := len(s) - len(unpadded)
	if padding > 0 {
		if padding > 2 {
			return nil, fmt.Errorf("mpp: base64url input has %d "+
				"padding characters, at most 2 are allowed",
				padding)
		}
		if len(s)%4 != 0 {
			return nil, fmt.Errorf("mpp: padded base64url input "+
				"has length %d, which is not a multiple of 4",
				len(s))
		}
	}

	for i := 0; i < len(unpadded); i++ {
		if !isBase64URLChar(unpadded[i]) {
			return nil, fmt.Errorf("mpp: invalid base64url "+
				"character %q at offset %d", unpadded[i], i)
		}
	}

	return base64.RawURLEncoding.Strict().DecodeString(unpadded)
}

// isBase64URLChar reports whether c belongs to the base64url alphabet of RFC
// 4648 Section 5.
func isBase64URLChar(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_':
		return true
	default:
		return false
	}
}

// SetChallengeHeader writes a WWW-Authenticate: Payment challenge header to
// the given http.Header using the auth-param syntax defined in
// draft-httpauth-payment-00 Section 5.1.
//
// Each value is emitted as an RFC 9110 quoted-string. Callers that put an octet
// in a parameter which the quoted-string grammar cannot carry, meaning a
// control character other than horizontal tab, will find it dropped; see
// ValidateChallengeParams for a way to catch that before it silently changes
// what the client echoes back.
func SetChallengeHeader(h http.Header, p *ChallengeParams) {
	var parts []string

	// Required parameters.
	parts = append(parts, "id="+quoteAuthParam(p.ID))
	parts = append(parts, "realm="+quoteAuthParam(p.Realm))
	parts = append(parts, "method="+quoteAuthParam(p.Method))
	parts = append(parts, "intent="+quoteAuthParam(p.Intent))
	parts = append(parts, "request="+quoteAuthParam(p.Request))

	// Optional parameters.
	if p.Expires != "" {
		parts = append(parts, "expires="+quoteAuthParam(p.Expires))
	}
	if p.Digest != "" {
		parts = append(parts, "digest="+quoteAuthParam(p.Digest))
	}
	if p.Description != "" {
		parts = append(parts,
			"description="+quoteAuthParam(p.Description))
	}
	if p.Opaque != "" {
		parts = append(parts, "opaque="+quoteAuthParam(p.Opaque))
	}

	value := AuthScheme + " " + strings.Join(parts, ", ")
	h.Add(headerWWWAuthenticate, value)
}

// ValidateChallengeParams reports whether every parameter value survives a
// round trip through the WWW-Authenticate header unchanged.
//
// A challenge is only useful if the client can echo it back byte for byte,
// because the server recomputes the challenge HMAC over what was echoed. The
// quoted-string grammar of RFC 9110 Section 5.6.4 cannot carry a control
// character other than horizontal tab, so a parameter containing one can never
// come back intact and the resulting credential would fail verification for
// reasons the client has no way to diagnose.
func ValidateChallengeParams(p *ChallengeParams) error {
	values := map[string]string{
		"id":          p.ID,
		"realm":       p.Realm,
		"method":      p.Method,
		"intent":      p.Intent,
		"request":     p.Request,
		"expires":     p.Expires,
		"digest":      p.Digest,
		"description": p.Description,
		"opaque":      p.Opaque,
	}

	for name, value := range values {
		for i := 0; i < len(value); i++ {
			c := value[i]
			if c == '\t' || (c >= 0x20 && c != 0x7f) {
				continue
			}

			return fmt.Errorf("mpp: challenge parameter %q "+
				"contains byte %#x at offset %d, which an "+
				"RFC 9110 quoted-string cannot carry", name,
				c, i)
		}
	}

	return nil
}

// quoteAuthParam renders s as an RFC 9110 Section 5.6.4 quoted-string.
//
// Only the reverse solidus and the double quote are turned into a quoted-pair;
// every other permitted octet, including horizontal tab and the whole of
// obs-text, is emitted as-is. This is deliberately not fmt.Sprintf("%q"), which
// applies Go's source-literal quoting: it escapes everything strconv.IsPrint
// rejects, so a tab becomes a literal backslash followed by "t". A recipient
// following RFC 9110 unescapes a quoted-pair by dropping the backslash and
// keeping the next octet verbatim, so Go's quoting does not survive the round
// trip, not even against our own parser.
//
// Octets the grammar cannot carry at all, the control characters other than
// horizontal tab and DEL, are dropped rather than escaped. Emitting them
// verbatim would let a caller-supplied realm carry a CRLF and split the
// response header.
func quoteAuthParam(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)

	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' || c == '"':
			b.WriteByte('\\')
			b.WriteByte(c)

		case c == '\t' || (c >= 0x20 && c != 0x7f):
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')

	return b.String()
}

// ParseCredential extracts and decodes a Payment credential from the
// Authorization header. The credential is a base64url-encoded JSON object per
// draft-httpauth-payment-00 Section 5.2.
//
// Returns nil and an error if the header does not contain a valid Payment
// credential.
func ParseCredential(h *http.Header) (*Credential, error) {
	authHeader := h.Get(headerAuthorization)
	if authHeader == "" {
		return nil, fmt.Errorf("mpp: no Authorization header")
	}

	// Split off the scheme. RFC 9110 Section 11.1 makes the auth-scheme
	// token case-insensitive, so match it that way.
	scheme, token, found := strings.Cut(authHeader, " ")
	if !found || !strings.EqualFold(scheme, AuthScheme) {
		return nil, fmt.Errorf("mpp: authorization header does not "+
			"use %s scheme", AuthScheme)
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("mpp: empty credential token")
	}

	// Reject oversized credentials before decoding to avoid allocating
	// memory for payloads that will be rejected anyway.
	if len(token) > maxEncodedCredentialSize {
		return nil, fmt.Errorf("mpp: credential too large "+
			"(%d encoded bytes, max %d)", len(token),
			maxEncodedCredentialSize)
	}

	// Decode from base64url.
	decoded, err := Base64URLDecode(token)
	if err != nil {
		return nil, fmt.Errorf("mpp: failed to decode credential "+
			"token: %w", err)
	}

	// Unmarshal the JSON credential.
	var cred Credential
	if err := json.Unmarshal(decoded, &cred); err != nil {
		return nil, fmt.Errorf("mpp: failed to unmarshal "+
			"credential: %w", err)
	}

	// Validate required fields.
	if cred.Challenge.ID == "" {
		return nil, fmt.Errorf("mpp: credential missing " +
			"challenge.id")
	}
	if cred.Challenge.Realm == "" {
		return nil, fmt.Errorf("mpp: credential missing " +
			"challenge.realm")
	}
	if cred.Challenge.Method == "" {
		return nil, fmt.Errorf("mpp: credential missing " +
			"challenge.method")
	}
	if cred.Challenge.Intent == "" {
		return nil, fmt.Errorf("mpp: credential missing " +
			"challenge.intent")
	}
	if cred.Challenge.Request == "" {
		return nil, fmt.Errorf("mpp: credential missing " +
			"challenge.request")
	}
	if len(cred.Payload) == 0 {
		return nil, fmt.Errorf("mpp: credential missing payload")
	}

	return &cred, nil
}

// MarshalPlainJSON serializes v the way every other implementation of this
// protocol does, which is to say without encoding/json's HTML and JavaScript
// escaping.
//
// json.Marshal escapes "<", ">" and "&" so its output is safe to inline in an
// HTML document, and U+2028 and U+2029 so its output is safe to inline in a
// script. None of those five is escaped by ECMAScript's JSON.stringify, so
// leaving the default on makes our bytes differ from a JavaScript peer's for
// any string containing one, over nothing more exotic than a description
// reading "Q&A endpoint". The documents this produces go on the wire whole
// rather than through the canonicalizer, so there is nothing downstream to
// undo it. See canonicalWriteString for the same argument on the path that
// does feed the challenge HMAC.
func MarshalPlainJSON(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	// Encode appends a newline that Marshal does not, and a stray byte
	// here would change every encoded credential and receipt.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// EncodeCredential serializes a credential and base64url-encodes it for the
// Authorization header, as the inverse of ParseCredential. Aperture is a seller
// and so never sends one itself, but the encoding is part of the protocol
// surface and buyers and test vectors need one blessed spelling of it.
func EncodeCredential(cred *Credential) (string, error) {
	data, err := MarshalPlainJSON(cred)
	if err != nil {
		return "", fmt.Errorf("mpp: failed to marshal credential: %w",
			err)
	}

	return Base64URLEncode(data), nil
}

// SetReceiptHeader writes a Payment-Receipt header to the given http.Header.
// The receipt is a base64url-encoded JSON object per draft-httpauth-payment-00
// Section 5.3.
func SetReceiptHeader(h http.Header, r *Receipt) error {
	data, err := MarshalPlainJSON(r)
	if err != nil {
		return fmt.Errorf("mpp: failed to marshal receipt: %w", err)
	}

	h.Set(HeaderPaymentReceipt, Base64URLEncode(data))
	return nil
}

// ParseReceiptHeader extracts and decodes a Payment-Receipt from the given
// http.Header.
func ParseReceiptHeader(h http.Header) (*Receipt, error) {
	encoded := h.Get(HeaderPaymentReceipt)
	if encoded == "" {
		return nil, fmt.Errorf("mpp: no Payment-Receipt header")
	}

	decoded, err := Base64URLDecode(encoded)
	if err != nil {
		return nil, fmt.Errorf("mpp: failed to decode receipt: %w",
			err)
	}

	var receipt Receipt
	if err := json.Unmarshal(decoded, &receipt); err != nil {
		return nil, fmt.Errorf("mpp: failed to unmarshal "+
			"receipt: %w", err)
	}

	return &receipt, nil
}

// EncodeRequest JCS-serializes and base64url-encodes a request object for use
// in the challenge's request parameter.
func EncodeRequest(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("mpp: failed to marshal request: %w",
			err)
	}

	canonical, err := CanonicalizeJSON(data)
	if err != nil {
		return "", fmt.Errorf("mpp: failed to canonicalize "+
			"request: %w", err)
	}

	return Base64URLEncode(canonical), nil
}

// DecodeRequest base64url-decodes and unmarshals a request parameter into the
// given target.
func DecodeRequest(encoded string, target any) error {
	decoded, err := Base64URLDecode(encoded)
	if err != nil {
		return fmt.Errorf("mpp: failed to decode request: %w", err)
	}

	if err := json.Unmarshal(decoded, target); err != nil {
		return fmt.Errorf("mpp: failed to unmarshal request: %w", err)
	}

	return nil
}

// ParseChallengeHeader parses a WWW-Authenticate field value and returns the
// first Payment challenge it carries.
//
// A single field value may carry several challenges, so prefer
// ParseChallengeList or ParseChallengeHeaders unless only one intent is ever
// expected. Taking the first one here means a server that offers a charge and a
// session in one response is answered with the charge.
func ParseChallengeHeader(headerValue string) (*ChallengeParams, error) {
	challenges, err := ParseChallengeList(headerValue)
	if err != nil {
		return nil, err
	}

	return challenges[0], nil
}

// ParseChallengeList parses a WWW-Authenticate field value into every Payment
// challenge it carries, in the order they appear.
//
// RFC 9110 Section 11.6.1 allows a field value to hold a list of challenges,
// and a client built on fetch never sees anything else: the Fetch standard
// joins repeated response header lines with ", " before handing them to the
// caller, so a 402 that offers both a charge and a session arrives as one
// string holding two challenges. Challenges for other auth-schemes are skipped.
//
// Challenges that are missing a required parameter are skipped rather than
// rejected, so that one malformed offer does not deny the client the others.
// An error is returned only when no usable Payment challenge is left.
func ParseChallengeList(headerValue string) ([]*ChallengeParams, error) {
	paramSets, err := parseChallengeParamSets(headerValue)
	if err != nil {
		return nil, err
	}

	if len(paramSets) == 0 {
		return nil, fmt.Errorf("mpp: header does not use %s scheme",
			AuthScheme)
	}

	var firstErr error
	challenges := make([]*ChallengeParams, 0, len(paramSets))
	for _, params := range paramSets {
		challenge, err := challengeFromParams(params)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		challenges = append(challenges, challenge)
	}

	if len(challenges) == 0 {
		return nil, firstErr
	}

	return challenges, nil
}

// ParseChallengeHeaders parses every Payment challenge across all
// WWW-Authenticate field lines of a response, in the order they appear.
func ParseChallengeHeaders(h http.Header) ([]*ChallengeParams, error) {
	values := h.Values(headerWWWAuthenticate)
	if len(values) == 0 {
		return nil, fmt.Errorf("mpp: no %s header",
			headerWWWAuthenticate)
	}

	var (
		challenges []*ChallengeParams
		firstErr   error
	)
	for _, value := range values {
		parsed, err := ParseChallengeList(value)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		challenges = append(challenges, parsed...)
	}

	if len(challenges) == 0 {
		return nil, firstErr
	}

	return challenges, nil
}

// challengeFromParams lifts one challenge's auth-param set into
// ChallengeParams, checking that the required parameters are all present.
func challengeFromParams(params map[string]string) (*ChallengeParams, error) {
	p := &ChallengeParams{}

	var ok bool
	if p.ID, ok = params["id"]; !ok {
		return nil, fmt.Errorf("mpp: challenge missing id parameter")
	}
	if p.Realm, ok = params["realm"]; !ok {
		return nil, fmt.Errorf("mpp: challenge missing realm " +
			"parameter")
	}
	if p.Method, ok = params["method"]; !ok {
		return nil, fmt.Errorf("mpp: challenge missing method " +
			"parameter")
	}
	if p.Intent, ok = params["intent"]; !ok {
		return nil, fmt.Errorf("mpp: challenge missing intent " +
			"parameter")
	}
	if p.Request, ok = params["request"]; !ok {
		return nil, fmt.Errorf("mpp: challenge missing request " +
			"parameter")
	}

	// Extract optional parameters.
	p.Expires = params["expires"]
	p.Digest = params["digest"]
	p.Description = params["description"]
	p.Opaque = params["opaque"]

	return p, nil
}

// parseAuthParams parses a comma-separated list of auth-param key=value or
// key="value" pairs per RFC 9110 Section 11.2. Parameter names are lowercased,
// since the grammar makes them case-insensitive.
func parseAuthParams(s string) map[string]string {
	// Reuse the challenge scanner by presenting the list as the parameters
	// of a single Payment challenge.
	paramSets, err := parseChallengeParamSets(AuthScheme + " " + s)
	if err != nil || len(paramSets) == 0 {
		return map[string]string{}
	}

	return paramSets[0]
}

// parseChallengeParamSets walks a WWW-Authenticate field value and returns one
// auth-param map per Payment challenge found in it.
//
// Splitting the list is the delicate part, because the same comma separates two
// challenges and two auth-params of one challenge. What actually distinguishes
// them is what follows the token: an auth-param is a token followed by "=",
// while a new challenge is a token that is not. So the scanner reads a token,
// looks past any whitespace for the equals sign, and decides from that.
//
// Auth-params belonging to a challenge for some other auth-scheme are consumed
// into a map that is thrown away, so that they cannot leak into the Payment
// challenge that follows.
//
// The one shape this does not handle is the token68 alternative of RFC 9110
// Section 11.2, where a challenge carries a single unnamed value instead of
// auth-params. A token68 would be read as the start of a further challenge,
// which is harmless here because it will not match the Payment scheme and its
// parameters therefore go to the discard map.
func parseChallengeParamSets(s string) ([]map[string]string, error) {
	var (
		paymentSets []map[string]string
		current     map[string]string
		pos         int
	)

	for {
		// Both the separator between two auth-params and the separator
		// between two challenges is a comma, and RFC 9110 tolerates
		// empty list elements, so skip any run of them.
		for pos < len(s) && isListSeparator(s[pos]) {
			pos++
		}
		if pos >= len(s) {
			break
		}

		token, next := readToken(s, pos)
		if token == "" {
			return nil, fmt.Errorf("mpp: malformed "+
				"WWW-Authenticate value at offset %d", pos)
		}
		pos = next

		// Look past any whitespace for the equals sign that would make
		// this token an auth-param name rather than an auth-scheme.
		afterToken := pos
		for pos < len(s) && isOWS(s[pos]) {
			pos++
		}

		if pos < len(s) && s[pos] == '=' {
			if current == nil {
				return nil, fmt.Errorf("mpp: auth-param %q "+
					"appears before any auth-scheme", token)
			}

			var value string
			value, pos = readAuthParamValue(s, pos+1)

			// Resolve a repeated parameter name in favour of the
			// first occurrence. RFC 9110 leaves the outcome
			// undefined, and taking the first denies an on-path
			// element the ability to override a value the origin
			// server already set simply by appending its own.
			name := strings.ToLower(token)
			if _, seen := current[name]; !seen {
				current[name] = value
			}

			continue
		}

		// No equals sign, so the token opens a new challenge.
		pos = afterToken
		current = make(map[string]string)
		if strings.EqualFold(token, AuthScheme) {
			paymentSets = append(paymentSets, current)
		}
	}

	return paymentSets, nil
}

// readAuthParamValue reads the value half of an auth-param starting at pos,
// which is the offset just past the equals sign. It returns the unescaped value
// and the offset just past it.
func readAuthParamValue(s string, pos int) (string, int) {
	for pos < len(s) && isOWS(s[pos]) {
		pos++
	}

	if pos >= len(s) || s[pos] != '"' {
		// An unquoted value is a bare token, which ends at the first
		// character the token grammar does not allow.
		return readToken(s, pos)
	}

	// A quoted-string, in which a backslash escapes the octet after it.
	// RFC 9110 Section 5.6.4 defines the quoted-pair as carrying the second
	// octet verbatim, so unescaping is simply a matter of dropping the
	// backslash.
	var value strings.Builder
	pos++
	for pos < len(s) {
		switch {
		case s[pos] == '\\' && pos+1 < len(s):
			value.WriteByte(s[pos+1])
			pos += 2

		case s[pos] == '"':
			return value.String(), pos + 1

		default:
			value.WriteByte(s[pos])
			pos++
		}
	}

	// The closing quote is missing. Take what there is rather than
	// discarding the whole header; the caller validates the result.
	return value.String(), pos
}

// readToken reads an RFC 9110 Section 5.6.2 token starting at pos, returning it
// along with the offset just past it.
func readToken(s string, pos int) (string, int) {
	start := pos
	for pos < len(s) && isTChar(s[pos]) {
		pos++
	}

	return s[start:pos], pos
}

// isTChar reports whether c is one of the characters RFC 9110 Section 5.6.2
// permits in a token.
func isTChar(c byte) bool {
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`',
		'|', '~':

		return true
	}

	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= 'a' && c <= 'z':
		return true
	default:
		return false
	}
}

// isOWS reports whether c is optional whitespace per RFC 9110 Section 5.6.3.
func isOWS(c byte) bool {
	return c == ' ' || c == '\t'
}

// isListSeparator reports whether c separates two elements of a
// WWW-Authenticate list, counting the surrounding whitespace.
func isListSeparator(c byte) bool {
	return c == ',' || isOWS(c)
}
