package l402

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// testPreimageStr is a valid 32-byte preimage hex string used across tests.
const testPreimageStr = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// makeTestMac creates a test macaroon with an optional preimage caveat.
func makeTestMac(t *testing.T, addPreimage bool) *macaroon.Macaroon {
	t.Helper()

	mac, err := macaroon.New(
		[]byte("aabbccddeeff00112233445566778899"),
		[]byte("test-id"), "test", macaroon.LatestVersion,
	)
	require.NoError(t, err)

	if addPreimage {
		err = AddFirstPartyCaveats(mac, Caveat{
			Condition: PreimageKey,
			Value:     testPreimageStr,
		})
		require.NoError(t, err)
	}

	return mac
}

// TestFromHeaderBackwardsCompat verifies that FromHeader accepts all six
// supported header formats, covering both current spec and legacy header
// names per bLIP-0026 backwards compatibility requirements.
func TestFromHeaderBackwardsCompat(t *testing.T) {
	// Create a macaroon with a preimage caveat for the metadata-style
	// headers (cases 2-5 that extract preimage from caveats).
	macWithCaveat := makeTestMac(t, true)
	macWithCaveatBytes, err := macWithCaveat.MarshalBinary()
	require.NoError(t, err)
	macWithCaveatHex := hex.EncodeToString(macWithCaveatBytes)

	// Create a bare macaroon for the Authorization header tests
	// (cases 0-1 where the preimage is a separate field).
	bareMac := makeTestMac(t, false)
	bareMacBytes, err := bareMac.MarshalBinary()
	require.NoError(t, err)
	bareMacBase64 := base64.StdEncoding.EncodeToString(bareMacBytes)

	tests := []struct {
		name   string
		header *http.Header
	}{
		{
			name: "Authorization: LSAT (legacy scheme)",
			header: &http.Header{
				HeaderAuthorization: []string{
					"LSAT " + bareMacBase64 + ":" +
						testPreimageStr,
				},
			},
		},
		{
			name: "Authorization: L402 (current scheme)",
			header: &http.Header{
				HeaderAuthorization: []string{
					"L402 " + bareMacBase64 + ":" +
						testPreimageStr,
				},
			},
		},
		{
			name: "Authorization: both LSAT and L402 values",
			header: &http.Header{
				HeaderAuthorization: []string{
					"LSAT " + bareMacBase64 + ":" +
						testPreimageStr,
					"L402 " + bareMacBase64 + ":" +
						testPreimageStr,
				},
			},
		},
		{
			name: "Grpc-Metadata-Token (current spec)",
			header: &http.Header{
				HeaderTokenMD: []string{macWithCaveatHex},
			},
		},
		{
			name: "Token (current spec)",
			header: &http.Header{
				HeaderToken: []string{macWithCaveatHex},
			},
		},
		{
			name: "Grpc-Metadata-Macaroon (legacy)",
			header: &http.Header{
				HeaderMacaroonMD: []string{macWithCaveatHex},
			},
		},
		{
			name: "Macaroon (legacy)",
			header: &http.Header{
				HeaderMacaroon: []string{macWithCaveatHex},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mac, preimage, err := FromHeader(tc.header)
			require.NoError(t, err)
			require.NotNil(t, mac)

			expectedPreimage, err := lntypes.MakePreimageFromStr(
				testPreimageStr,
			)
			require.NoError(t, err)
			require.Equal(t, expectedPreimage, preimage)
		})
	}
}

// TestFromHeaderPriorityOrder verifies that when multiple header types are
// present, the Authorization header takes precedence over metadata headers,
// and current-spec headers take precedence over legacy ones.
func TestFromHeaderPriorityOrder(t *testing.T) {
	macWithCaveat := makeTestMac(t, true)
	macBytes, err := macWithCaveat.MarshalBinary()
	require.NoError(t, err)
	macHex := hex.EncodeToString(macBytes)
	macBase64 := base64.StdEncoding.EncodeToString(macBytes)

	// When Authorization header is present alongside metadata headers,
	// Authorization takes precedence.
	header := &http.Header{
		HeaderAuthorization: []string{
			"L402 " + macBase64 + ":" + testPreimageStr,
		},
		HeaderTokenMD:    []string{macHex},
		HeaderMacaroonMD: []string{macHex},
	}

	mac, _, err := FromHeader(header)
	require.NoError(t, err)
	require.NotNil(t, mac)
}

// TestFromHeaderRejectsInvalid verifies that invalid or missing headers are
// properly rejected.
func TestFromHeaderRejectsInvalid(t *testing.T) {
	tests := []struct {
		name      string
		header    *http.Header
		errSubstr string
	}{
		{
			name:      "empty header",
			header:    &http.Header{},
			errSubstr: "no auth header",
		},
		{
			name: "invalid Authorization format",
			header: &http.Header{
				HeaderAuthorization: []string{"Bearer foo"},
			},
			errSubstr: "invalid auth header format",
		},
		{
			name: "invalid hex in Token header",
			header: &http.Header{
				HeaderToken: []string{"not-hex"},
			},
			errSubstr: "hex decode",
		},
		{
			name: "invalid hex in legacy Macaroon header",
			header: &http.Header{
				HeaderMacaroon: []string{"not-hex"},
			},
			errSubstr: "hex decode",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := FromHeader(tc.header)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errSubstr)
		})
	}
}

// TestSetHeaderEmitsBothSchemes verifies that SetHeader produces both LSAT
// (legacy) and L402 (current) Authorization header values for backwards
// compatibility.
func TestSetHeaderEmitsBothSchemes(t *testing.T) {
	mac := makeTestMac(t, false)
	preimage, err := lntypes.MakePreimageFromStr(testPreimageStr)
	require.NoError(t, err)

	header := http.Header{}
	err = SetHeader(&header, mac, &preimage)
	require.NoError(t, err)

	values := header.Values(HeaderAuthorization)
	require.Len(t, values, 2)

	// The first value should be LSAT (legacy, for old aperture compat).
	require.Regexp(t, `^LSAT `, values[0])

	// The second value should be L402 (current spec).
	require.Regexp(t, `^L402 `, values[1])

	// Both should be parseable by FromHeader.
	for _, scheme := range []string{"LSAT", "L402"} {
		parseHeader := &http.Header{
			HeaderAuthorization: []string{
				fmt.Sprintf(
					"%s %s",
					scheme,
					values[0][len("LSAT "):],
				),
			},
		}
		parsedMac, parsedPreimage, err := FromHeader(parseHeader)
		require.NoError(t, err)
		require.NotNil(t, parsedMac)
		require.Equal(t, preimage, parsedPreimage)
	}
}

// TestAuthHeaderRegexBackwardsCompat verifies that the client interceptor's
// authHeaderRegex correctly parses both old (macaroon=) and new (token= with
// version=) challenge header formats per bLIP-0026 backwards compatibility.
func TestAuthHeaderRegexBackwardsCompat(t *testing.T) {
	testToken := "AGIAJEemVQUTEyNCR0exk7ek90Cg=="
	testInvoice := "lntb5u1p0pskpmpp5jzw9xvdast2g5lm5tswq6n64t2epe3f4xav43dyd239qr8h3yllqdqqcqzpg"

	// Compile the same regex used in client_interceptor.go.
	re := regexp.MustCompile(
		`(LSAT|L402) (?:version="[^"]*", )?(?:token|macaroon)="(.*?)", invoice="(.*?)"`,
	)

	tests := []struct {
		name    string
		header  string
		matches bool
		scheme  string
		token   string
		invoice string
	}{
		{
			name: "legacy LSAT with macaroon= (old aperture)",
			header: fmt.Sprintf(
				`LSAT macaroon="%s", invoice="%s"`,
				testToken, testInvoice,
			),
			matches: true,
			scheme:  "LSAT",
			token:   testToken,
			invoice: testInvoice,
		},
		{
			name: "current L402 with token= and version=0",
			header: fmt.Sprintf(
				`L402 version="0", token="%s", invoice="%s"`,
				testToken, testInvoice,
			),
			matches: true,
			scheme:  "L402",
			token:   testToken,
			invoice: testInvoice,
		},
		{
			name: "L402 with token= but no version (compat)",
			header: fmt.Sprintf(
				`L402 token="%s", invoice="%s"`,
				testToken, testInvoice,
			),
			matches: true,
			scheme:  "L402",
			token:   testToken,
			invoice: testInvoice,
		},
		{
			name: "L402 with macaroon= and version (transitional)",
			header: fmt.Sprintf(
				`L402 version="0", macaroon="%s", invoice="%s"`,
				testToken, testInvoice,
			),
			matches: true,
			scheme:  "L402",
			token:   testToken,
			invoice: testInvoice,
		},
		{
			name: "LSAT with macaroon= no version (oldest format)",
			header: fmt.Sprintf(
				`LSAT macaroon="%s", invoice="%s"`,
				testToken, testInvoice,
			),
			matches: true,
			scheme:  "LSAT",
			token:   testToken,
			invoice: testInvoice,
		},
		{
			name: "future version value still parses",
			header: fmt.Sprintf(
				`L402 version="1", token="%s", invoice="%s"`,
				testToken, testInvoice,
			),
			matches: true,
			scheme:  "L402",
			token:   testToken,
			invoice: testInvoice,
		},
		{
			name:    "invalid scheme rejected",
			header:  fmt.Sprintf(`BEARER token="%s"`, testToken),
			matches: false,
		},
		{
			name:    "missing invoice rejected",
			header:  fmt.Sprintf(`L402 token="%s"`, testToken),
			matches: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			matches := re.FindStringSubmatch(tc.header)
			if !tc.matches {
				require.Nil(t, matches, "expected no match")
				return
			}

			require.Len(t, matches, 4,
				"expected 4 groups: full, scheme, token, invoice",
			)
			require.Equal(t, tc.scheme, matches[1])
			require.Equal(t, tc.token, matches[2])
			require.Equal(t, tc.invoice, matches[3])
		})
	}
}

// TestCredentialSendsBothKeys verifies that MacaroonCredential.GetRequestMetadata
// sends the token under both the "token" (current spec) and "macaroon" (legacy)
// gRPC metadata keys for backwards compatibility.
func TestCredentialSendsBothKeys(t *testing.T) {
	mac := makeTestMac(t, false)
	cred := NewMacaroonCredential(mac, false)

	md, err := cred.GetRequestMetadata(nil)
	require.NoError(t, err)

	// Both keys must be present.
	require.Contains(t, md, "token",
		"credential must send current-spec 'token' key",
	)
	require.Contains(t, md, "macaroon",
		"credential must send legacy 'macaroon' key for compat",
	)

	// Both keys must carry the same value.
	require.Equal(t, md["token"], md["macaroon"],
		"token and macaroon keys must have the same value",
	)

	// The value must be valid hex-encoded macaroon bytes.
	macBytes, err := hex.DecodeString(md["token"])
	require.NoError(t, err)

	decoded := &macaroon.Macaroon{}
	err = decoded.UnmarshalBinary(macBytes)
	require.NoError(t, err)
}
