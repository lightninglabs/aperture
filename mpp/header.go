package mpp

import (
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
)

// Base64URLEncode encodes data using base64url encoding without padding per
// RFC 4648 Section 5, as required by the Payment HTTP Authentication Scheme.
func Base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// Base64URLDecode decodes a base64url-encoded string. It accepts input with or
// without padding per the spec requirement.
func Base64URLDecode(s string) ([]byte, error) {
	// RawURLEncoding handles no-padding. If padding is present, strip it
	// first to be lenient.
	s = strings.TrimRight(s, "=")
	return base64.RawURLEncoding.DecodeString(s)
}

// SetChallengeHeader writes a WWW-Authenticate: Payment challenge header to
// the given http.Header using the auth-param syntax defined in
// draft-httpauth-payment-00 Section 5.1.
func SetChallengeHeader(h http.Header, p *ChallengeParams) {
	var parts []string

	// Required parameters.
	parts = append(parts, fmt.Sprintf("id=%q", p.ID))
	parts = append(parts, fmt.Sprintf("realm=%q", p.Realm))
	parts = append(parts, fmt.Sprintf("method=%q", p.Method))
	parts = append(parts, fmt.Sprintf("intent=%q", p.Intent))
	parts = append(parts, fmt.Sprintf("request=%q", p.Request))

	// Optional parameters.
	if p.Expires != "" {
		parts = append(parts, fmt.Sprintf("expires=%q", p.Expires))
	}
	if p.Digest != "" {
		parts = append(parts, fmt.Sprintf("digest=%q", p.Digest))
	}
	if p.Description != "" {
		parts = append(parts,
			fmt.Sprintf("description=%q", p.Description))
	}
	if p.Opaque != "" {
		parts = append(parts, fmt.Sprintf("opaque=%q", p.Opaque))
	}

	value := AuthScheme + " " + strings.Join(parts, ", ")
	h.Add(headerWWWAuthenticate, value)
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

	// Check for the Payment scheme prefix.
	prefix := AuthScheme + " "
	if !strings.HasPrefix(authHeader, prefix) {
		return nil, fmt.Errorf("mpp: authorization header does not "+
			"use %s scheme", AuthScheme)
	}

	// Extract the base64url-encoded token.
	token := strings.TrimPrefix(authHeader, prefix)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("mpp: empty credential token")
	}

	// Reject oversized credentials before decoding to avoid allocating
	// memory for payloads that will be rejected anyway. The encoded form
	// of maxCredentialSize bytes is roughly 4/3 of that size.
	const maxCredentialSize = 64 * 1024 // 64KB.
	maxEncodedLen := maxCredentialSize*4/3 + 4
	if len(token) > maxEncodedLen {
		return nil, fmt.Errorf("mpp: credential too large "+
			"(%d encoded bytes, max %d)", len(token),
			maxEncodedLen)
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

// SetReceiptHeader writes a Payment-Receipt header to the given http.Header.
// The receipt is a base64url-encoded JSON object per draft-httpauth-payment-00
// Section 5.3.
func SetReceiptHeader(h http.Header, r *Receipt) error {
	data, err := json.Marshal(r)
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

// ParseChallengeHeader parses a WWW-Authenticate: Payment header value into
// ChallengeParams. This is primarily used by clients to extract the challenge
// parameters from a 402 response.
func ParseChallengeHeader(headerValue string) (*ChallengeParams, error) {
	// Strip the scheme prefix.
	prefix := AuthScheme + " "
	if !strings.HasPrefix(headerValue, prefix) {
		return nil, fmt.Errorf("mpp: header does not use %s scheme",
			AuthScheme)
	}
	paramStr := strings.TrimPrefix(headerValue, prefix)

	// Parse the auth-param key=value pairs.
	params := parseAuthParams(paramStr)

	// Extract required parameters.
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
// key="value" pairs per RFC 9110 Section 11.
func parseAuthParams(s string) map[string]string {
	params := make(map[string]string)
	s = strings.TrimSpace(s)

	for s != "" {
		// Find the key.
		eqIdx := strings.IndexByte(s, '=')
		if eqIdx < 0 {
			break
		}
		key := strings.TrimSpace(s[:eqIdx])
		s = s[eqIdx+1:]

		var value string
		s = strings.TrimSpace(s)
		if len(s) > 0 && s[0] == '"' {
			// Quoted string value.
			s = s[1:]
			endQuote := strings.IndexByte(s, '"')
			if endQuote < 0 {
				// Malformed, take rest.
				value = s
				s = ""
			} else {
				value = s[:endQuote]
				s = s[endQuote+1:]
			}
		} else {
			// Token value (unquoted).
			commaIdx := strings.IndexByte(s, ',')
			if commaIdx < 0 {
				value = strings.TrimSpace(s)
				s = ""
			} else {
				value = strings.TrimSpace(s[:commaIdx])
				s = s[commaIdx:]
			}
		}

		params[key] = value

		// Skip comma and whitespace.
		s = strings.TrimSpace(s)
		if len(s) > 0 && s[0] == ',' {
			s = s[1:]
			s = strings.TrimSpace(s)
		}
	}

	return params
}
