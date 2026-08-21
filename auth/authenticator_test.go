package auth_test

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"gopkg.in/macaroon.v2"
)

// createDummyMacHex creates a valid macaroon with dummy content for our tests.
func createDummyMacHex(preimage string) string {
	return createDummyMacHexWithID(preimage, "AA==")
}

// createDummyMacHexWithID creates a valid macaroon with the given ID.
func createDummyMacHexWithID(preimage, id string) string {
	dummyMac, err := macaroon.New(
		[]byte("aabbccddeeff00112233445566778899"), []byte(id),
		"aperture", macaroon.LatestVersion,
	)
	if err != nil {
		panic(err)
	}
	preimageCaveat := l402.Caveat{Condition: l402.PreimageKey, Value: preimage}
	err = l402.AddFirstPartyCaveats(dummyMac, preimageCaveat)
	if err != nil {
		panic(err)
	}
	macBytes, err := dummyMac.MarshalBinary()
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(macBytes)
}

// TestL402Authenticator tests that the authenticator properly handles auth
// headers and the tokens contained in them.
func TestL402Authenticator(t *testing.T) {
	var (
		testPreimage = "49349dfea4abed3cd14f6d356afa83de" +
			"9787b609f088c8df09bacc7b4bd21b39"
		otherPreimage = "59349dfea4abed3cd14f6d356afa83de" +
			"9787b609f088c8df09bacc7b4bd21b39"
		testMacHex      = createDummyMacHex(testPreimage)
		otherIDMacHex   = createDummyMacHexWithID(testPreimage, "BB==")
		testMacBytes, _ = hex.DecodeString(testMacHex)
		testMacBase64   = base64.StdEncoding.EncodeToString(
			testMacBytes,
		)
		otherIDMacBytes, _ = hex.DecodeString(otherIDMacHex)
		otherIDMacBase64   = base64.StdEncoding.EncodeToString(
			otherIDMacBytes,
		)
		headerTests = []struct {
			id       string
			header   *http.Header
			checkErr error
			result   bool
		}{
			{
				id:     "empty header",
				header: &http.Header{},
				result: false,
			},
			{
				id: "no auth header",
				header: &http.Header{
					"Test": []string{"foo"},
				},
				result: false,
			},
			{
				id: "empty auth header",
				header: &http.Header{
					l402.HeaderAuthorization: []string{},
				},
				result: false,
			},
			{
				id: "zero length auth header",
				header: &http.Header{
					l402.HeaderAuthorization: []string{""},
				},
				result: false,
			},
			{
				id: "invalid auth header",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"foo",
					},
				},
				result: false,
			},
			{
				id: "invalid macaroon metadata header",
				header: &http.Header{
					l402.HeaderMacaroonMD: []string{"foo"},
				},
				result: false,
			},
			{
				id: "invalid macaroon header",
				header: &http.Header{
					l402.HeaderMacaroon: []string{"foo"},
				},
				result: false,
			},
			{
				id: "valid auth header (both LSAT and L402)",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
						"L402 " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: true,
			},
			{
				id: "valid auth header (both L402 and LSAT)",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":" +
							testPreimage,
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: true,
			},
			{
				id: "different macaroons (LSAT then L402)",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
						"L402 " + otherIDMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "different macaroons (L402 then LSAT)",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + otherIDMacBase64 + ":" +
							testPreimage,
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "different preimages (LSAT then L402)",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
						"L402 " + testMacBase64 + ":" +
							otherPreimage,
					},
				},
				result: false,
			},
			{
				id: "different preimages (L402 then LSAT)",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":" +
							otherPreimage,
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "case insensitive scheme and uppercase preimage",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"lSaT " + testMacBase64 + ":" +
							strings.ToUpper(testPreimage),
					},
				},
				result: true,
			},
			{
				id: "multiple spaces after scheme",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402   " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: true,
			},
			{
				id: "valid auth header (LSAT only)",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: true,
			},
			{
				id: "valid auth header (L402 only)",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: true,
			},
			{
				id: "valid auth header followed by unrelated value",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":" +
							testPreimage,
						"Bearer unrelated",
					},
				},
				result: false,
			},
			{
				id: "unrelated value followed by valid auth header",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"Bearer unrelated",
						"L402 " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "invalid macaroon followed by valid auth header",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 !!!!:" + testPreimage,
						"L402 " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "valid auth header followed by invalid macaroon",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":" +
							testPreimage,
						"LSAT !!!!:" + testPreimage,
					},
				},
				result: false,
			},
			{
				id: "invalid base64 followed by valid credential",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"LSAT A:" + testPreimage,
						"L402 " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "valid credential followed by invalid base64",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":" +
							testPreimage,
						"LSAT A:" + testPreimage,
					},
				},
				result: false,
			},
			{
				id: "invalid encoded macaroon followed by valid credential",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"LSAT YQ==:" + testPreimage,
						"L402 " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "valid credential followed by invalid encoded macaroon",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":" +
							testPreimage,
						"LSAT YQ==:" + testPreimage,
					},
				},
				result: false,
			},
			{
				id: "invalid preimage length",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":ab",
					},
				},
				result: false,
			},
			{
				id: "duplicate L402 auth headers",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"L402 " + testMacBase64 + ":" +
							testPreimage,
						"L402 " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "duplicate LSAT auth headers",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: false,
			},
			{
				id: "extra auth header value",
				header: &http.Header{
					l402.HeaderAuthorization: []string{
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
						"L402 " + testMacBase64 + ":" +
							testPreimage,
						"Bearer unrelated",
					},
				},
				result: false,
			},
			{
				id: "valid macaroon metadata header",
				header: &http.Header{
					l402.HeaderMacaroonMD: []string{
						testMacHex,
					}},
				result: true,
			},
			{
				id: "valid macaroon header",
				header: &http.Header{
					l402.HeaderMacaroon: []string{
						testMacHex,
					},
				},
				result: true,
			},
			{
				id: "valid macaroon header, wrong invoice state",
				header: &http.Header{
					l402.HeaderMacaroon: []string{
						testMacHex,
					},
				},
				checkErr: fmt.Errorf("nope"),
				result:   false,
			},
		}
	)

	c := &mockChecker{}
	a := auth.NewL402Authenticator(&mockMint{}, c)
	for _, testCase := range headerTests {
		c.err = testCase.checkErr
		result := a.Accept(testCase.header, "test")
		if result != testCase.result {
			t.Fatalf("test case %s failed. got %v expected %v",
				testCase.id, result, testCase.result)
		}
	}
}

// TestL402AuthenticatorRejectsInvalidCredentialOrder verifies that a
// structurally valid but unauthorized credential cannot bypass validation
// when placed before a valid credential.
func TestL402AuthenticatorRejectsInvalidCredentialOrder(t *testing.T) {
	const (
		validPreimage = "49349dfea4abed3cd14f6d356afa83de" +
			"9787b609f088c8df09bacc7b4bd21b39"
		invalidPreimage = "59349dfea4abed3cd14f6d356afa83de" +
			"9787b609f088c8df09bacc7b4bd21b39"
	)

	validMacHex := createDummyMacHex(validPreimage)
	invalidMacHex := createDummyMacHexWithID(invalidPreimage, "BB==")
	validMacBytes, _ := hex.DecodeString(validMacHex)
	invalidMacBytes, _ := hex.DecodeString(invalidMacHex)
	validMacBase64 := base64.StdEncoding.EncodeToString(validMacBytes)
	invalidMacBase64 := base64.StdEncoding.EncodeToString(invalidMacBytes)

	headerValues := [][]string{
		{
			"LSAT " + invalidMacBase64 + ":" + invalidPreimage,
			"L402 " + validMacBase64 + ":" + validPreimage,
		},
		{
			"L402 " + validMacBase64 + ":" + validPreimage,
			"LSAT " + invalidMacBase64 + ":" + invalidPreimage,
		},
	}

	for i, values := range headerValues {
		header := http.Header{
			l402.HeaderAuthorization: values,
		}
		a := auth.NewL402Authenticator(
			&mockMint{rejectMacaroonHex: invalidMacHex},
			&mockChecker{},
		)

		if a.Accept(&header, "test") {
			t.Errorf("header order %d unexpectedly authenticated", i)
		}
	}
}
