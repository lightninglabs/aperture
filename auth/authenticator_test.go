package auth_test

import (
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/loop/lsat"
	"gopkg.in/macaroon.v2"
)

// createDummyMacHex creates a valid macaroon with dummy content for our tests.
func createDummyMacHex(preimage string) string {
	dummyMac, err := macaroon.New(
		[]byte("aabbccddeeff00112233445566778899"), []byte("AA=="),
		"aperture", macaroon.LatestVersion,
	)
	if err != nil {
		panic(err)
	}
	preimageCaveat := lsat.Caveat{Condition: lsat.PreimageKey, Value: preimage}
	err = lsat.AddFirstPartyCaveats(dummyMac, preimageCaveat)
	if err != nil {
		panic(err)
	}
	macBytes, err := dummyMac.MarshalBinary()
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(macBytes)
}

// TestLsatAuthenticator tests that the authenticator properly handles auth
// headers and the tokens contained in them.
func TestLsatAuthenticator(t *testing.T) {
	var (
		testPreimage = "49349dfea4abed3cd14f6d356afa83de" +
			"9787b609f088c8df09bacc7b4bd21b39"
		testMacHex      = createDummyMacHex(testPreimage)
		testMacBytes, _ = hex.DecodeString(testMacHex)
		testMacBase64   = base64.StdEncoding.EncodeToString(
			testMacBytes,
		)
		headerTests = []struct {
			id     string
			header *http.Header
			result bool
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
					auth.HeaderAuthorization: []string{},
				},
				result: false,
			},
			{
				id: "zero length auth header",
				header: &http.Header{
					auth.HeaderAuthorization: []string{""},
				},
				result: false,
			},
			{
				id: "invalid auth header",
				header: &http.Header{
					auth.HeaderAuthorization: []string{
						"foo",
					},
				},
				result: false,
			},
			{
				id: "invalid macaroon metadata header",
				header: &http.Header{
					auth.HeaderMacaroonMD: []string{"foo"},
				},
				result: false,
			},
			{
				id: "invalid macaroon header",
				header: &http.Header{
					auth.HeaderMacaroon: []string{"foo"},
				},
				result: false,
			},
			{
				id: "valid auth header",
				header: &http.Header{
					auth.HeaderAuthorization: []string{
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: true,
			},
			{
				id: "valid macaroon metadata header",
				header: &http.Header{
					auth.HeaderMacaroonMD: []string{
						testMacHex,
					}},
				result: true,
			},
			{
				id: "valid macaroon header",
				header: &http.Header{
					auth.HeaderMacaroon: []string{
						testMacHex,
					},
				},
				result: true,
			},
		}
	)

	a := auth.NewLsatAuthenticator(&mockMint{})
	for _, testCase := range headerTests {
		result := a.Accept(testCase.header, "test")
		if result != testCase.result {
			t.Fatalf("test case %s failed. got %v expected %v",
				testCase.id, result, testCase.result)
		}
	}
}
