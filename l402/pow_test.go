package l402

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSolveAndVerifyPoW(t *testing.T) {
	var tokenID TokenID
	copy(tokenID[:], []byte("test-token-id-for-pow-testing!!"))

	// Use a low difficulty for fast tests.
	difficulty := uint32(8)
	nonce, err := SolvePoW(tokenID, difficulty)
	require.NoError(t, err)

	// Verify the solution.
	require.True(t, VerifyPoW(tokenID, nonce, difficulty))

	// Wrong nonce should fail.
	require.False(t, VerifyPoW(tokenID, nonce+1, difficulty))

	// Wrong token ID should fail.
	var wrongID TokenID
	copy(wrongID[:], []byte("wrong-token-id-for-pow-testing!"))
	require.False(t, VerifyPoW(wrongID, nonce, difficulty))
}

func TestSolvePoWDifficultyZero(t *testing.T) {
	var tokenID TokenID
	_, err := SolvePoW(tokenID, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "difficulty must be greater than 0")
}

func TestSolvePoWDifficultyTooHigh(t *testing.T) {
	var tokenID TokenID
	_, err := SolvePoW(tokenID, 257)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum")
}

func TestVerifyPoWBoundary(t *testing.T) {
	// Difficulty 0 should return false.
	var tokenID TokenID
	require.False(t, VerifyPoW(tokenID, 0, 0))

	// Difficulty > 256 should return false.
	require.False(t, VerifyPoW(tokenID, 0, 257))
}

func TestPoWSatisfier(t *testing.T) {
	var tokenID TokenID
	copy(tokenID[:], []byte("test-token-id-for-pow-testing!!"))

	difficulty := uint32(8)
	nonce, err := SolvePoW(tokenID, difficulty)
	require.NoError(t, err)

	satisfier := NewPoWSatisfier(tokenID, difficulty)
	require.Equal(t, CondPoW, satisfier.Condition)

	// Valid caveat should satisfy.
	caveatValue := FormatPoWCaveatValue(difficulty, nonce)
	err = satisfier.SatisfyFinal(Caveat{
		Condition: CondPoW,
		Value:     caveatValue,
	})
	require.NoError(t, err)

	// Invalid nonce should fail.
	badValue := FormatPoWCaveatValue(difficulty, nonce+999)
	err = satisfier.SatisfyFinal(Caveat{
		Condition: CondPoW,
		Value:     badValue,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "pow verification failed")

	// Lower difficulty than required should fail.
	lowerDifficulty := uint32(4)
	lowerNonce, err := SolvePoW(tokenID, lowerDifficulty)
	require.NoError(t, err)
	lowValue := FormatPoWCaveatValue(lowerDifficulty, lowerNonce)
	err = satisfier.SatisfyFinal(Caveat{
		Condition: CondPoW,
		Value:     lowValue,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "below required")
}

func TestPoWSatisfierInvalidFormats(t *testing.T) {
	var tokenID TokenID
	satisfier := NewPoWSatisfier(tokenID, 8)

	// No colon separator.
	err := satisfier.SatisfyFinal(Caveat{
		Condition: CondPoW,
		Value:     "invalid",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid pow caveat format")

	// Invalid difficulty.
	err = satisfier.SatisfyFinal(Caveat{
		Condition: CondPoW,
		Value:     "abc:0000000000000000",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid pow difficulty")

	// Wrong nonce length.
	err = satisfier.SatisfyFinal(Caveat{
		Condition: CondPoW,
		Value:     "8:00",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid pow nonce length")

	// Invalid hex.
	err = satisfier.SatisfyFinal(Caveat{
		Condition: CondPoW,
		Value:     "8:zzzzzzzzzzzzzzzz",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid pow nonce hex")
}

func TestFormatPoWCaveatValue(t *testing.T) {
	value := FormatPoWCaveatValue(20, 12345)
	require.Equal(t, "20:0000000000003039", value)
}

func TestHasLeadingZeroBits(t *testing.T) {
	tests := []struct {
		data     []byte
		n        uint32
		expected bool
	}{
		{[]byte{0x00, 0x00}, 16, true},
		{[]byte{0x00, 0x00}, 15, true},
		{[]byte{0x00, 0x01}, 16, false},
		{[]byte{0x00, 0x01}, 15, true},
		{[]byte{0x0F}, 4, true},
		{[]byte{0x0F}, 5, false},
		{[]byte{0x00}, 8, true},
		{[]byte{0x80}, 1, false},
		{[]byte{0x00}, 0, true}, // 0 leading zeros is always true
	}

	for _, tc := range tests {
		result := hasLeadingZeroBits(tc.data, tc.n)
		require.Equal(t, tc.expected, result, "data=%x n=%d",
			tc.data, tc.n)
	}
}

func TestPoWSatisfierPrevious(t *testing.T) {
	var tokenID TokenID
	satisfier := NewPoWSatisfier(tokenID, 8)

	// SatisfyPrevious should always succeed (only one PoW caveat expected).
	err := satisfier.SatisfyPrevious(
		Caveat{Condition: CondPoW, Value: "8:0000000000000000"},
		Caveat{Condition: CondPoW, Value: "8:0000000000000001"},
	)
	require.NoError(t, err)
}
