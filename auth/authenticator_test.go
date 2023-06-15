package auth_test

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/lsat"
)

// TestLsatAuthenticator tests that the authenticator properly handles auth
// headers and the tokens contained in them.
func TestLsatAuthenticator(t *testing.T) {
	var (
		testPreimage = "49349dfea4abed3cd14f6d356afa83de" +
			"9787b609f088c8df09bacc7b4bd21b39"
		testMacHex      = auth.CreateDummyMacHex(testPreimage)
		testMacBytes, _ = hex.DecodeString(testMacHex)
		testMacBase64   = base64.StdEncoding.EncodeToString(
			testMacBytes,
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
					lsat.HeaderAuthorization: []string{},
				},
				result: false,
			},
			{
				id: "zero length auth header",
				header: &http.Header{
					lsat.HeaderAuthorization: []string{""},
				},
				result: false,
			},
			{
				id: "invalid auth header",
				header: &http.Header{
					lsat.HeaderAuthorization: []string{
						"foo",
					},
				},
				result: false,
			},
			{
				id: "invalid macaroon metadata header",
				header: &http.Header{
					lsat.HeaderMacaroonMD: []string{"foo"},
				},
				result: false,
			},
			{
				id: "invalid macaroon header",
				header: &http.Header{
					lsat.HeaderMacaroon: []string{"foo"},
				},
				result: false,
			},
			{
				id: "valid auth header",
				header: &http.Header{
					lsat.HeaderAuthorization: []string{
						"LSAT " + testMacBase64 + ":" +
							testPreimage,
					},
				},
				result: true,
			},
			{
				id: "valid macaroon metadata header",
				header: &http.Header{
					lsat.HeaderMacaroonMD: []string{
						testMacHex,
					}},
				result: true,
			},
			{
				id: "valid macaroon header",
				header: &http.Header{
					lsat.HeaderMacaroon: []string{
						testMacHex,
					},
				},
				result: true,
			},
			{
				id: "valid macaroon header, wrong invoice state",
				header: &http.Header{
					lsat.HeaderMacaroon: []string{
						testMacHex,
					},
				},
				checkErr: fmt.Errorf("nope"),
				result:   false,
			},
		}
	)

	c := &auth.MockChecker{}
	a := auth.NewLsatAuthenticator(&auth.MockMint{}, c)
	for _, testCase := range headerTests {
		c.Err = testCase.checkErr
		result := a.Accept(testCase.header, "test")
		if result != testCase.result {
			t.Fatalf("test case %s failed. got %v expected %v",
				testCase.id, result, testCase.result)
		}
	}
}
