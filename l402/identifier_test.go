package l402

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

var (
	testPaymentHash lntypes.Hash
	testTokenID     [TokenIDSize]byte
)

// TestIdentifierSerialization ensures proper serialization of known identifier
// versions and failures for unknown versions.
func TestIdentifierSerialization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   Identifier
		err  error
	}{
		{
			name: "valid identifier",
			id: Identifier{
				Version:     LatestVersion,
				PaymentHash: testPaymentHash,
				TokenID:     testTokenID,
			},
			err: nil,
		},
		{
			name: "unknown version",
			id: Identifier{
				Version:     LatestVersion + 1,
				PaymentHash: testPaymentHash,
				TokenID:     testTokenID,
			},
			err: ErrUnknownVersion,
		},
	}

	for _, test := range tests {
		success := t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := EncodeIdentifier(&buf, &test.id)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected err \"%v\", got \"%v\"",
					test.err, err)
			}
			if test.err != nil {
				return
			}
			id, err := DecodeIdentifier(&buf)
			if err != nil {
				t.Fatalf("unable to decode identifier: %v", err)
			}
			if *id != test.id {
				t.Fatalf("expected id %v, got %v", test.id, *id)
			}
		})
		if !success {
			return
		}
	}
}

// TestEncodeIdentifierBytes tests that EncodeIdentifierBytes produces correct
// output that roundtrips through DecodeIdentifier and matches the output of
// EncodeIdentifier.
func TestEncodeIdentifierBytes(t *testing.T) {
	t.Parallel()

	paymentHash := lntypes.Hash{1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
		11, 12, 13, 14, 15, 16, 17, 18, 19, 20,
		21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

	tokenID := TokenID{32, 31, 30, 29, 28, 27, 26, 25, 24, 23,
		22, 21, 20, 19, 18, 17, 16, 15, 14, 13,
		12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}

	idBytes := EncodeIdentifierBytes(paymentHash, tokenID)

	// Verify roundtrip: decode the bytes back and check fields match.
	decoded, err := DecodeIdentifier(bytes.NewReader(idBytes))
	require.NoError(t, err)
	require.Equal(t, uint16(LatestVersion), decoded.Version)
	require.Equal(t, paymentHash, decoded.PaymentHash)
	require.Equal(t, tokenID, decoded.TokenID)

	// Verify output matches EncodeIdentifier written to buffer.
	id := &Identifier{
		Version:     LatestVersion,
		PaymentHash: paymentHash,
		TokenID:     tokenID,
	}
	var buf bytes.Buffer
	require.NoError(t, EncodeIdentifier(&buf, id))
	require.Equal(t, buf.Bytes(), idBytes)

	// Verify expected byte layout: 2-byte version + 32-byte hash +
	// 32-byte token ID.
	require.Len(t, idBytes, 2+32+32)
	require.Equal(t, []byte{0, 0}, idBytes[:2])
	require.Equal(t, paymentHash[:], idBytes[2:34])
	require.Equal(t, tokenID[:], idBytes[34:66])
}

// TestTokenIDString makes sure that TokenID is logged properly in Printf
// function family.
func TestTokenIDString(t *testing.T) {
	cases := []struct {
		token        TokenID
		formatString string
		wantText     string
	}{
		{
			token:        TokenID{1, 2, 3},
			formatString: "client %v paid",
			wantText: "client 01020300000000000000000000000000000" +
				"00000000000000000000000000000 paid",
		},
		{
			token:        TokenID{1, 2, 3},
			formatString: "client %s paid",
			wantText: "client 01020300000000000000000000000000000" +
				"00000000000000000000000000000 paid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.formatString, func(t *testing.T) {
			got := fmt.Sprintf(tc.formatString, tc.token)
			require.Equal(t, tc.wantText, got)

			got = fmt.Sprintf(tc.formatString, &tc.token)
			require.Equal(t, tc.wantText, got)
		})
	}
}
