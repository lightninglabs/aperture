package challenger

import (
	"crypto/rand"
	"fmt"

	"github.com/lightningnetwork/lnd/lntypes"
)

// PoWChallenger is a challenger that issues proof-of-work challenges instead of
// Lightning invoices. It implements the mint.Challenger interface.
type PoWChallenger struct {
	difficulty uint32
}

// NewPoWChallenger creates a new PoW challenger with the given difficulty
// (number of leading zero bits required).
func NewPoWChallenger(difficulty uint32) *PoWChallenger {
	return &PoWChallenger{
		difficulty: difficulty,
	}
}

// NewChallenge returns the difficulty as the challenge string and a random
// 32-byte hash as the payment hash placeholder for the L402 identifier.
//
// NOTE: This is part of the mint.Challenger interface.
func (c *PoWChallenger) NewChallenge(price int64) (string, lntypes.Hash,
	error) {

	var paymentHash lntypes.Hash
	if _, err := rand.Read(paymentHash[:]); err != nil {
		return "", lntypes.Hash{}, fmt.Errorf("unable to generate "+
			"random hash: %w", err)
	}

	return fmt.Sprintf("%d", c.difficulty), paymentHash, nil
}

// Stop is a no-op for the PoW challenger.
//
// NOTE: This is part of the mint.Challenger interface.
func (c *PoWChallenger) Stop() {}
