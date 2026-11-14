package mint

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

func newTestMint(secrets SecretStore, challenger Challenger) *Mint {
	return New(&Config{
		Secrets:        secrets,
		Challenger:     challenger,
		ServiceLimiter: newMockServiceLimiter(),
		Now:            time.Now,
	})
}

type failingChallenger struct{}

func (failingChallenger) Start() error { return nil }
func (failingChallenger) Stop()        {}
func (failingChallenger) NewChallenge(int64) (string, lntypes.Hash, error) {
	return "", lntypes.Hash{}, errors.New("challenger unavailable")
}

// failingSecretStore fails every NewSecret and GetSecret call with err.
type failingSecretStore struct {
	SecretStore
	err error
}

func (s failingSecretStore) NewSecret(context.Context,
	[sha256.Size]byte) ([l402.SecretSize]byte, error) {

	return [l402.SecretSize]byte{}, s.err
}

func (s failingSecretStore) GetSecret(context.Context,
	[sha256.Size]byte) ([l402.SecretSize]byte, error) {

	return [l402.SecretSize]byte{}, s.err
}

// fixedSecretStore hands back the same secret for every identifier.
type fixedSecretStore struct {
	secret [l402.SecretSize]byte
}

func (s fixedSecretStore) NewSecret(context.Context,
	[sha256.Size]byte) ([l402.SecretSize]byte, error) {

	return s.secret, nil
}

func (s fixedSecretStore) GetSecret(context.Context,
	[sha256.Size]byte) ([l402.SecretSize]byte, error) {

	return s.secret, nil
}

func (fixedSecretStore) RevokeSecret(context.Context, [sha256.Size]byte) error {
	return nil
}

// TestMintL402Errors ensures each failing MintL402 step wraps its sentinel
// error around the original one.
func TestMintL402Errors(t *testing.T) {
	t.Parallel()

	storeErr := errors.New("db: connection reset")
	tests := []struct {
		name     string
		mint     *Mint
		sentinel error
	}{
		{
			name: "challenge",
			mint: newTestMint(
				newMockSecretStore(), failingChallenger{},
			),
			sentinel: ErrChallengeFailed,
		},
		{
			name: "secret",
			mint: newTestMint(
				failingSecretStore{err: storeErr},
				newMockChallenger(),
			),
			sentinel: ErrMintSecretFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := test.mint.MintL402(
				context.Background(), testService,
			)
			require.ErrorIs(t, err, test.sentinel)
		})
	}

	// The original error must stay reachable.
	m := newTestMint(failingSecretStore{err: storeErr}, newMockChallenger())
	_, _, err := m.MintL402(context.Background(), testService)
	require.ErrorIs(t, err, storeErr)
}

// TestVerifyL402Errors ensures each failing VerifyL402 check returns a
// VerifyError with the matching reason.
func TestVerifyL402Errors(t *testing.T) {
	t.Parallel()

	var mintSecret, otherSecret [l402.SecretSize]byte
	mintSecret[0] = 1
	otherSecret[0] = 2

	m := newTestMint(
		fixedSecretStore{secret: mintSecret}, newMockChallenger(),
	)
	mac, _, err := m.MintL402(context.Background(), testService)
	require.NoError(t, err)

	badID, err := macaroon.New(
		mintSecret[:], []byte("not-an-id"), "lsat",
		macaroon.LatestVersion,
	)
	require.NoError(t, err)

	wrongPreimage := testPreimage
	wrongPreimage[0] ^= 0xff

	tests := []struct {
		name     string
		secrets  SecretStore
		mac      *macaroon.Macaroon
		preimage lntypes.Preimage
		service  string
		reason   VerifyReason
	}{
		{
			name:     "malformed macaroon",
			mac:      badID,
			preimage: testPreimage,
			service:  testService.Name,
			reason:   VerifyMalformedMacaroon,
		},
		{
			name:     "bad preimage",
			mac:      mac,
			preimage: wrongPreimage,
			service:  testService.Name,
			reason:   VerifyBadPreimage,
		},
		{
			name: "secret not found",
			secrets: failingSecretStore{
				err: ErrSecretNotFound,
			},
			mac:      mac,
			preimage: testPreimage,
			service:  testService.Name,
			reason:   VerifySecretNotFound,
		},
		{
			name: "secret lookup error",
			secrets: failingSecretStore{
				err: errors.New("db: connection reset"),
			},
			mac:      mac,
			preimage: testPreimage,
			service:  testService.Name,
			reason:   VerifySecretLookupError,
		},
		{
			name:     "bad signature",
			secrets:  fixedSecretStore{secret: otherSecret},
			mac:      mac,
			preimage: testPreimage,
			service:  testService.Name,
			reason:   VerifyBadSignature,
		},
		{
			name:     "caveat unsatisfied",
			mac:      mac,
			preimage: testPreimage,
			service:  "other-service",
			reason:   VerifyCaveatUnsatisfied,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			secrets := test.secrets
			if secrets == nil {
				secrets = fixedSecretStore{secret: mintSecret}
			}
			verifier := newTestMint(secrets, newMockChallenger())

			err := verifier.VerifyL402(
				context.Background(), &VerificationParams{
					Macaroon:      test.mac,
					Preimage:      test.preimage,
					TargetService: test.service,
				},
			)

			var verifyErr *VerifyError
			require.ErrorAs(t, err, &verifyErr)
			require.Equal(t, test.reason, verifyErr.Reason)
		})
	}
}
