package l402

import (
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

var (
	testRootKey  = []byte("aabbccddeeff00112233445566778899")
	testPreimage = lntypes.Preimage{
		0x49, 0x34, 0x9d, 0xfe, 0xa4, 0xab, 0xed, 0x3c,
		0xd1, 0x4f, 0x6d, 0x35, 0x6a, 0xfa, 0x83, 0xde,
		0x97, 0x87, 0xb6, 0x09, 0xf0, 0x88, 0xc8, 0xdf,
		0x09, 0xba, 0xcc, 0x7b, 0x4b, 0xd2, 0x1b, 0x39,
	}
	testPreimageHex = "49349dfea4abed3cd14f6d356afa83de" +
		"9787b609f088c8df09bacc7b4bd21b39"
)

// newTestMacaroon creates a macaroon with a preimage caveat for testing.
func newTestMacaroon(t *testing.T) *macaroon.Macaroon {
	t.Helper()

	mac, err := macaroon.New(
		testRootKey, []byte("test-id"), "aperture",
		macaroon.LatestVersion,
	)
	require.NoError(t, err)

	return mac
}

// newTestMacaroonWithPreimage creates a macaroon with a preimage caveat
// embedded, suitable for the hex-only header formats.
func newTestMacaroonWithPreimage(t *testing.T) *macaroon.Macaroon {
	t.Helper()

	mac := newTestMacaroon(t)
	err := AddFirstPartyCaveats(mac, Caveat{
		Condition: PreimageKey,
		Value:     testPreimageHex,
	})
	require.NoError(t, err)

	return mac
}

// newTestDischarge creates a discharge macaroon bound to the given root
// macaroon's signature.
func newTestDischarge(t *testing.T, root *macaroon.Macaroon) *macaroon.Macaroon {
	t.Helper()

	thirdPartyKey := []byte("third-party-shared-secret-key!!!")
	caveatID := []byte("tp-caveat-id")

	err := root.AddThirdPartyCaveat(
		thirdPartyKey, caveatID, "https://thirdparty",
	)
	require.NoError(t, err)

	discharge, err := macaroon.New(
		thirdPartyKey, caveatID, "https://thirdparty",
		macaroon.LatestVersion,
	)
	require.NoError(t, err)

	discharge.Bind(root.Signature())

	return discharge
}

// TestFromHeaderAuthDischarges tests that discharge macaroons survive a
// round-trip through SetHeader/FromHeader for the Authorization header format.
func TestFromHeaderAuthDischarges(t *testing.T) {
	t.Parallel()

	mac := newTestMacaroon(t)
	discharge := newTestDischarge(t, mac)

	// Serialize root + discharge into the Authorization header.
	header := http.Header{}
	err := SetHeader(&header, mac, testPreimage, []*macaroon.Macaroon{
		discharge,
	})
	require.NoError(t, err)

	// Parse them back out.
	gotMac, gotPreimage, gotDischarges, err := FromHeader(&header)
	require.NoError(t, err)
	require.True(t, mac.Equal(gotMac))
	require.Equal(t, testPreimage, gotPreimage)
	require.Len(t, gotDischarges, 1)
	require.True(t, discharge.Equal(gotDischarges[0]))
}

// TestFromHeaderAuthNoDischarges tests that the Authorization header format
// still works when no discharges are present.
func TestFromHeaderAuthNoDischarges(t *testing.T) {
	t.Parallel()

	mac := newTestMacaroon(t)

	header := http.Header{}
	err := SetHeader(&header, mac, testPreimage, nil)
	require.NoError(t, err)

	gotMac, gotPreimage, gotDischarges, err := FromHeader(&header)
	require.NoError(t, err)
	require.True(t, mac.Equal(gotMac))
	require.Equal(t, testPreimage, gotPreimage)
	require.Nil(t, gotDischarges)
}

// TestFromHeaderHexDischarges tests that discharge macaroons are correctly
// extracted from the hex-encoded Grpc-Metadata-Macaroon header format.
func TestFromHeaderHexDischarges(t *testing.T) {
	t.Parallel()

	mac := newTestMacaroonWithPreimage(t)
	discharge := newTestDischarge(t, mac)

	// Manually build the hex-encoded header with a Slice containing
	// root + discharge.
	slice := macaroon.Slice{mac, discharge}
	sliceBytes, err := slice.MarshalBinary()
	require.NoError(t, err)

	header := http.Header{
		HeaderMacaroonMD: []string{hex.EncodeToString(sliceBytes)},
	}

	gotMac, gotPreimage, gotDischarges, err := FromHeader(&header)
	require.NoError(t, err)
	require.True(t, mac.Equal(gotMac))
	require.Equal(t, testPreimage, gotPreimage)
	require.Len(t, gotDischarges, 1)
	require.True(t, discharge.Equal(gotDischarges[0]))
}

// TestFromHeaderHexNoDischarges tests that the hex-encoded header format
// still works for a single macaroon without discharges.
func TestFromHeaderHexNoDischarges(t *testing.T) {
	t.Parallel()

	mac := newTestMacaroonWithPreimage(t)

	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)

	header := http.Header{
		HeaderMacaroon: []string{hex.EncodeToString(macBytes)},
	}

	gotMac, gotPreimage, gotDischarges, err := FromHeader(&header)
	require.NoError(t, err)
	require.True(t, mac.Equal(gotMac))
	require.Equal(t, testPreimage, gotPreimage)
	require.Nil(t, gotDischarges)
}

// TestSetHeaderRoundTrip tests that SetHeader and FromHeader are inverse
// operations for the Authorization header, preserving multiple discharges.
func TestSetHeaderRoundTrip(t *testing.T) {
	t.Parallel()

	mac := newTestMacaroon(t)

	// Add two third-party caveats with separate discharges.
	tpKey1 := []byte("third-party-key-one-32-bytes!!!!")
	caveatID1 := []byte("tp-caveat-1")
	require.NoError(t, mac.AddThirdPartyCaveat(
		tpKey1, caveatID1, "https://tp1",
	))
	discharge1, err := macaroon.New(
		tpKey1, caveatID1, "https://tp1", macaroon.LatestVersion,
	)
	require.NoError(t, err)
	discharge1.Bind(mac.Signature())

	tpKey2 := []byte("third-party-key-two-32-bytes!!!!")
	caveatID2 := []byte("tp-caveat-2")
	require.NoError(t, mac.AddThirdPartyCaveat(
		tpKey2, caveatID2, "https://tp2",
	))
	discharge2, err := macaroon.New(
		tpKey2, caveatID2, "https://tp2", macaroon.LatestVersion,
	)
	require.NoError(t, err)
	discharge2.Bind(mac.Signature())

	discharges := []*macaroon.Macaroon{discharge1, discharge2}

	header := http.Header{}
	err = SetHeader(&header, mac, testPreimage, discharges)
	require.NoError(t, err)

	gotMac, gotPreimage, gotDischarges, err := FromHeader(&header)
	require.NoError(t, err)
	require.True(t, mac.Equal(gotMac))
	require.Equal(t, testPreimage, gotPreimage)
	require.Len(t, gotDischarges, 2)
	require.True(t, discharge1.Equal(gotDischarges[0]))
	require.True(t, discharge2.Equal(gotDischarges[1]))
}

// TestSetHeaderBackwardsCompatible tests that a header set without discharges
// produces the same base64 blob as marshaling just the root macaroon, ensuring
// backwards compatibility with clients that don't know about discharges.
func TestSetHeaderBackwardsCompatible(t *testing.T) {
	t.Parallel()

	mac := newTestMacaroon(t)

	// Marshal the macaroon directly (old behavior).
	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)
	expectedBase64 := base64.StdEncoding.EncodeToString(macBytes)

	// SetHeader with nil discharges should produce the same base64.
	header := http.Header{}
	err = SetHeader(&header, mac, testPreimage, nil)
	require.NoError(t, err)

	// The L402 header (second value) should contain the expected base64.
	authValues := header.Values(HeaderAuthorization)
	require.Len(t, authValues, 2)

	// Check the L402 header (second one added).
	expected := "L402 " + expectedBase64 + ":" + testPreimage.String()
	require.Equal(t, expected, authValues[1])
}
