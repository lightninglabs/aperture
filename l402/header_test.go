package l402

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// TestFromHeader checks the macaroon and preimage that FromHeader extracts,
// and that it rejects values that only partly match the credential format.
func TestFromHeader(t *testing.T) {
	t.Parallel()

	preimage := lntypes.Preimage{1, 2, 3}
	var id bytes.Buffer
	err := EncodeIdentifier(&id, &Identifier{
		Version:     LatestVersion,
		PaymentHash: preimage.Hash(),
		TokenID:     TokenID{4},
	})
	require.NoError(t, err)

	// A minted macaroon has no preimage caveat, and that is what clients
	// send in the Authorization header. Our gRPC clients add the caveat
	// and send the macaroon alone.
	mac, err := macaroon.New(
		[]byte("root key"), id.Bytes(), "lsat", macaroon.LatestVersion,
	)
	require.NoError(t, err)
	paidMac := mac.Clone()
	err = AddFirstPartyCaveats(
		paidMac, NewCaveat(PreimageKey, preimage.String()),
	)
	require.NoError(t, err)

	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)
	paidMacBytes, err := paidMac.MarshalBinary()
	require.NoError(t, err)

	cred := base64.StdEncoding.EncodeToString(macBytes) + ":" +
		preimage.String()

	tests := []struct {
		name    string
		header  http.Header
		wantErr bool
	}{
		{
			name: "L402 without preimage caveat",
			header: http.Header{
				HeaderAuthorization: {"L402 " + cred},
			},
		},
		{
			name: "LSAT and L402",
			header: http.Header{
				HeaderAuthorization: {
					"LSAT " + cred, "L402 " + cred,
				},
			},
		},
		{
			name: "macaroon with preimage caveat",
			header: http.Header{
				HeaderMacaroon: {
					hex.EncodeToString(paidMacBytes),
				},
			},
		},
		{
			name: "text before the scheme",
			header: http.Header{
				HeaderAuthorization: {"Bearer L402 " + cred},
			},
			wantErr: true,
		},
		{
			name: "junk after the preimage",
			header: http.Header{
				HeaderAuthorization: {"L402 " + cred + ",x"},
			},
			wantErr: true,
		},
		{
			name: "LSAT and L402 folded into one value",
			header: http.Header{
				HeaderAuthorization: {
					"LSAT " + cred + ", L402 " + cred,
				},
			},
			wantErr: true,
		},
		{
			name: "overlong preimage",
			header: http.Header{
				HeaderAuthorization: {"L402 " + cred + "00"},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			gotMac, gotPreimage, err := FromHeader(&test.header)
			if test.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, mac.Id(), gotMac.Id())
			require.Equal(t, preimage, gotPreimage)
		})
	}
}

// TestContainsCredential checks which Authorization values contain an L402
// credential, including values that FromHeader rejects.
func TestContainsCredential(t *testing.T) {
	t.Parallel()

	token := "mac:" + strings.Repeat("0", 64)
	tests := []struct {
		value string
		want  bool
	}{
		{value: "L402 " + token, want: true},
		{value: "LSAT " + token, want: true},
		{value: "l402 " + token, want: true},

		// A credential after another scheme is found too.
		{value: "Bearer L402 " + token, want: true},

		{value: "Payment eyJjaGFsbGVuZ2UiOnt9fQ"},
		{value: "L402 garbage"},
	}

	for _, test := range tests {
		require.Equal(
			t, test.want, ContainsCredential(test.value),
			test.value,
		)
	}
}

// TestIsMacaroonCredential checks that an L402 macaroon in a macaroon header
// is found, with or without a preimage caveat, while a macaroon minted by
// another service, such as lnd, is not.
func TestIsMacaroonCredential(t *testing.T) {
	t.Parallel()

	encode := func(id []byte) string {
		mac, err := macaroon.New(
			[]byte("root key"), id, "test", macaroon.LatestVersion,
		)
		require.NoError(t, err)
		macBytes, err := mac.MarshalBinary()
		require.NoError(t, err)

		return hex.EncodeToString(macBytes)
	}

	preimage := lntypes.Preimage{1}
	l402Mac := encode(EncodeIdentifierBytes(preimage.Hash(), TokenID{2}))
	require.True(t, IsMacaroonCredential(l402Mac))
	require.True(t, IsMacaroonCredential(strings.ToUpper(l402Mac)))

	// macaroon-bakery, which lnd uses, starts its identifiers with a
	// version byte of 2 or 3.
	require.False(t, IsMacaroonCredential(encode([]byte{3, 1, 2, 3})))

	require.False(t, IsMacaroonCredential("not hex"))
	require.False(t, IsMacaroonCredential("00ff"))
	require.False(t, IsMacaroonCredential(""))
}

// TestCredentialHeader checks that CredentialHeader names the field FromHeader
// reads, which is the first one present even if its value is empty.
func TestCredentialHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		header http.Header
		want   string
	}{
		{header: http.Header{}, want: ""},
		{
			header: http.Header{
				HeaderAuthorization: {"L402 mac:preimage"},
				HeaderMacaroonMD:    {"aa"},
				HeaderMacaroon:      {"bb"},
			},
			want: HeaderAuthorization,
		},
		{
			header: http.Header{
				HeaderAuthorization: {""},
				HeaderMacaroonMD:    {"aa"},
			},
			want: HeaderAuthorization,
		},
		{
			header: http.Header{
				HeaderMacaroonMD: {"aa"},
				HeaderMacaroon:   {"bb"},
			},
			want: HeaderMacaroonMD,
		},
		{
			header: http.Header{HeaderMacaroon: {"bb"}},
			want:   HeaderMacaroon,
		},
	}

	for _, test := range tests {
		require.Equal(
			t, test.want, CredentialHeader(&test.header),
			test.header,
		)
	}
}
