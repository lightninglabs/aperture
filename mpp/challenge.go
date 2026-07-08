package mpp

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
)

// ComputeChallengeID computes the HMAC-SHA256 challenge ID from challenge
// parameters using the 7-slot positional scheme defined in
// draft-httpauth-payment-00 Section 5.1.2.1.1.
//
// The HMAC input is constructed from exactly seven fixed positional slots:
//
//	Slot 0: realm     (required, string value)
//	Slot 1: method    (required, string value)
//	Slot 2: intent    (required, string value)
//	Slot 3: request   (required, JCS-serialized then base64url-encoded)
//	Slot 4: expires   (optional, string value or empty string if absent)
//	Slot 5: digest    (optional, string value or empty string if absent)
//	Slot 6: opaque    (optional, JCS-serialized then base64url-encoded, or
//	                    empty string if absent)
//
// All seven slots are joined with the pipe character ("|") as delimiter. The
// result is the base64url-encoded (without padding) HMAC-SHA256 of the joined
// string.
func ComputeChallengeID(secret []byte, params *ChallengeParams) string {
	input := buildHMACInput(params)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(input))
	sum := mac.Sum(nil)

	return Base64URLEncode(sum)
}

// VerifyChallengeID verifies that a challenge ID matches the expected
// HMAC-SHA256 binding for the given parameters. Uses constant-time comparison
// to prevent timing attacks.
func VerifyChallengeID(secret []byte, params *ChallengeParams,
	id string) bool {

	expected := ComputeChallengeID(secret, params)
	return hmac.Equal([]byte(expected), []byte(id))
}

// buildHMACInput constructs the pipe-delimited HMAC input string from the
// seven positional slots. Optional fields use empty strings when absent.
func buildHMACInput(params *ChallengeParams) string {
	slots := [7]string{
		params.Realm,   // Slot 0: required.
		params.Method,  // Slot 1: required.
		params.Intent,  // Slot 2: required.
		params.Request, // Slot 3: required.
		params.Expires, // Slot 4: optional, empty if absent.
		params.Digest,  // Slot 5: optional, empty if absent.
		params.Opaque,  // Slot 6: optional, empty if absent.
	}

	return strings.Join(slots[:], "|")
}
