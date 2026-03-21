package l402

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	// CondPoW is the caveat condition for proof-of-work.
	CondPoW = "pow"

	// PowNonceSize is the size of the PoW nonce in bytes (uint64).
	PowNonceSize = 8
)

// SolvePoW iterates nonces from 0 until SHA256(tokenID || nonce_bytes) has
// the required number of leading zero bits.
func SolvePoW(tokenID TokenID, difficulty uint32) (uint64, error) {
	if difficulty == 0 {
		return 0, fmt.Errorf("difficulty must be greater than 0")
	}
	if difficulty > 256 {
		return 0, fmt.Errorf("difficulty %d exceeds maximum of 256",
			difficulty)
	}

	var nonceBuf [PowNonceSize]byte
	for nonce := uint64(0); ; nonce++ {
		binary.BigEndian.PutUint64(nonceBuf[:], nonce)

		hash := sha256.New()
		hash.Write(tokenID[:])
		hash.Write(nonceBuf[:])
		digest := hash.Sum(nil)

		if hasLeadingZeroBits(digest, difficulty) {
			return nonce, nil
		}

		// Prevent infinite loop for impossibly high difficulty.
		if nonce == ^uint64(0) {
			return 0, fmt.Errorf("no valid nonce found")
		}
	}
}

// VerifyPoW checks that SHA256(tokenID || nonce_bytes) has the required number
// of leading zero bits.
func VerifyPoW(tokenID TokenID, nonce uint64, difficulty uint32) bool {
	if difficulty == 0 || difficulty > 256 {
		return false
	}

	var nonceBuf [PowNonceSize]byte
	binary.BigEndian.PutUint64(nonceBuf[:], nonce)

	hash := sha256.New()
	hash.Write(tokenID[:])
	hash.Write(nonceBuf[:])
	digest := hash.Sum(nil)

	return hasLeadingZeroBits(digest, difficulty)
}

// NewPoWSatisfier returns a Satisfier for the "pow" caveat condition. The
// satisfier verifies that the caveat value contains a valid nonce for the given
// token ID and difficulty. Caveat format: pow=<difficulty>:<16-char-hex-nonce>.
func NewPoWSatisfier(tokenID TokenID, difficulty uint32) Satisfier {
	return Satisfier{
		Condition: CondPoW,
		SatisfyPrevious: func(prev, cur Caveat) error {
			// Only one PoW caveat should exist, but if there are
			// multiple, each must be independently valid.
			return nil
		},
		SatisfyFinal: func(c Caveat) error {
			parts := strings.SplitN(c.Value, ":", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid pow caveat format: "+
					"%s", c.Value)
			}

			caveatDifficulty, err := strconv.ParseUint(
				parts[0], 10, 32,
			)
			if err != nil {
				return fmt.Errorf("invalid pow difficulty: %w",
					err)
			}

			if uint32(caveatDifficulty) < difficulty {
				return fmt.Errorf("pow difficulty %d below "+
					"required %d", caveatDifficulty,
					difficulty)
			}

			nonceHex := parts[1]
			if len(nonceHex) != hex.EncodedLen(PowNonceSize) {
				return fmt.Errorf("invalid pow nonce length: "+
					"%d", len(nonceHex))
			}

			nonceBytes, err := hex.DecodeString(nonceHex)
			if err != nil {
				return fmt.Errorf("invalid pow nonce hex: %w",
					err)
			}

			nonce := binary.BigEndian.Uint64(nonceBytes)
			if !VerifyPoW(tokenID, nonce, uint32(caveatDifficulty)) {
				return fmt.Errorf("pow verification failed")
			}

			return nil
		},
	}
}

// FormatPoWCaveatValue formats a PoW caveat value string from difficulty and
// nonce.
func FormatPoWCaveatValue(difficulty uint32, nonce uint64) string {
	var nonceBuf [PowNonceSize]byte
	binary.BigEndian.PutUint64(nonceBuf[:], nonce)
	return fmt.Sprintf("%d:%s", difficulty, hex.EncodeToString(nonceBuf[:]))
}

// hasLeadingZeroBits checks whether the given byte slice has at least n
// leading zero bits.
func hasLeadingZeroBits(data []byte, n uint32) bool {
	// Check full zero bytes.
	fullBytes := n / 8
	remainingBits := n % 8

	for i := uint32(0); i < fullBytes; i++ {
		if i >= uint32(len(data)) {
			return false
		}
		if data[i] != 0 {
			return false
		}
	}

	// Check remaining bits in the next byte.
	if remainingBits > 0 {
		if fullBytes >= uint32(len(data)) {
			return false
		}
		// Create a mask for the remaining bits. For example, if
		// remainingBits is 3, the mask is 0b11100000 = 0xE0.
		mask := byte(0xFF << (8 - remainingBits))
		if data[fullBytes]&mask != 0 {
			return false
		}
	}

	return true
}
