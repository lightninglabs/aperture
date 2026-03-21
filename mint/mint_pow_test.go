package mint

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// mockPoWChallenger returns a random hash for each challenge.
type mockPoWChallenger struct{}

func (c *mockPoWChallenger) NewChallenge(price int64) (string, lntypes.Hash,
	error) {

	var h lntypes.Hash
	if _, err := rand.Read(h[:]); err != nil {
		return "", h, err
	}
	return "16", h, nil
}

func (c *mockPoWChallenger) Stop() {}

func TestVerifyL402PoW(t *testing.T) {
	ctx := context.Background()

	// Set up a mint with PoW challenger.
	secretStore := newMockSecretStore()
	serviceLimiter := newMockServiceLimiter()
	m := New(&Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        secretStore,
		ServiceLimiter: serviceLimiter,
		Now:            time.Now,
	})

	// Mint an L402.
	serviceName := "test-service"
	service := l402.Service{
		Name:  serviceName,
		Tier:  l402.BaseTier,
		Price: 1,
	}
	mac, _, err := m.MintL402(ctx, service)
	require.NoError(t, err)

	// Extract the token ID from the macaroon identifier.
	id, err := l402.DecodeIdentifier(
		bytesReader(mac.Id()),
	)
	require.NoError(t, err)

	// Solve the PoW.
	difficulty := uint32(8)
	nonce, err := l402.SolvePoW(id.TokenID, difficulty)
	require.NoError(t, err)

	// Add the PoW caveat to the macaroon.
	solvedMac := mac.Clone()
	err = l402.AddFirstPartyCaveats(solvedMac, l402.NewCaveat(
		l402.CondPoW,
		l402.FormatPoWCaveatValue(difficulty, nonce),
	))
	require.NoError(t, err)

	// Verify should succeed.
	err = m.VerifyL402PoW(ctx, &PoWVerificationParams{
		Macaroon:      solvedMac,
		Difficulty:    difficulty,
		TargetService: serviceName,
	})
	require.NoError(t, err)
}

func TestVerifyL402PoWWrongNonce(t *testing.T) {
	ctx := context.Background()

	secretStore := newMockSecretStore()
	serviceLimiter := newMockServiceLimiter()
	m := New(&Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        secretStore,
		ServiceLimiter: serviceLimiter,
		Now:            time.Now,
	})

	serviceName := "test-service"
	service := l402.Service{
		Name:  serviceName,
		Tier:  l402.BaseTier,
		Price: 1,
	}
	mac, _, err := m.MintL402(ctx, service)
	require.NoError(t, err)

	// Add a PoW caveat with a wrong nonce.
	difficulty := uint32(8)
	solvedMac := mac.Clone()
	err = l402.AddFirstPartyCaveats(solvedMac, l402.NewCaveat(
		l402.CondPoW,
		l402.FormatPoWCaveatValue(difficulty, 99999999),
	))
	require.NoError(t, err)

	// Verify should fail.
	err = m.VerifyL402PoW(ctx, &PoWVerificationParams{
		Macaroon:      solvedMac,
		Difficulty:    difficulty,
		TargetService: serviceName,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "pow verification failed")
}

func TestVerifyL402PoWWrongService(t *testing.T) {
	ctx := context.Background()

	secretStore := newMockSecretStore()
	serviceLimiter := newMockServiceLimiter()
	m := New(&Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        secretStore,
		ServiceLimiter: serviceLimiter,
		Now:            time.Now,
	})

	service := l402.Service{
		Name:  "service-a",
		Tier:  l402.BaseTier,
		Price: 1,
	}
	mac, _, err := m.MintL402(ctx, service)
	require.NoError(t, err)

	id, err := l402.DecodeIdentifier(bytesReader(mac.Id()))
	require.NoError(t, err)

	difficulty := uint32(8)
	nonce, err := l402.SolvePoW(id.TokenID, difficulty)
	require.NoError(t, err)

	solvedMac := mac.Clone()
	err = l402.AddFirstPartyCaveats(solvedMac, l402.NewCaveat(
		l402.CondPoW,
		l402.FormatPoWCaveatValue(difficulty, nonce),
	))
	require.NoError(t, err)

	// Verify with wrong service name should fail.
	err = m.VerifyL402PoW(ctx, &PoWVerificationParams{
		Macaroon:      solvedMac,
		Difficulty:    difficulty,
		TargetService: "wrong-service",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not authorized")
}

func TestVerifyL402PoWTamperedMacaroon(t *testing.T) {
	ctx := context.Background()

	secretStore := newMockSecretStore()
	serviceLimiter := newMockServiceLimiter()
	m := New(&Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        secretStore,
		ServiceLimiter: serviceLimiter,
		Now:            time.Now,
	})

	// Create a macaroon with a different secret (simulating tampering).
	var secret [l402.SecretSize]byte
	_, _ = rand.Read(secret[:])

	var tokenID l402.TokenID
	_, _ = rand.Read(tokenID[:])
	var paymentHash lntypes.Hash
	_, _ = rand.Read(paymentHash[:])

	identifier := &l402.Identifier{
		Version:     l402.LatestVersion,
		PaymentHash: paymentHash,
		TokenID:     tokenID,
	}

	var buf bytesBuffer
	err := l402.EncodeIdentifier(&buf, identifier)
	require.NoError(t, err)

	// Store a real secret for this identifier.
	idHash := sha256.Sum256(buf.Bytes())
	_, err = secretStore.NewSecret(ctx, idHash)
	require.NoError(t, err)

	// Create the macaroon with a DIFFERENT secret, simulating tampering.
	var wrongSecret [l402.SecretSize]byte
	_, _ = rand.Read(wrongSecret[:])
	tamperedMac, err := macaroon.New(
		wrongSecret[:], buf.Bytes(), "lsat", macaroon.LatestVersion,
	)
	require.NoError(t, err)

	difficulty := uint32(8)
	nonce, err := l402.SolvePoW(tokenID, difficulty)
	require.NoError(t, err)

	err = l402.AddFirstPartyCaveats(tamperedMac, l402.NewCaveat(
		l402.CondPoW,
		l402.FormatPoWCaveatValue(difficulty, nonce),
	))
	require.NoError(t, err)

	err = m.VerifyL402PoW(ctx, &PoWVerificationParams{
		Macaroon:      tamperedMac,
		Difficulty:    difficulty,
		TargetService: "any",
	})
	require.Error(t, err)
}

func TestVerifyL402PoWRevokedSecret(t *testing.T) {
	ctx := context.Background()

	secretStore := newMockSecretStore()
	serviceLimiter := newMockServiceLimiter()
	m := New(&Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        secretStore,
		ServiceLimiter: serviceLimiter,
		Now:            time.Now,
	})

	serviceName := "test-service"
	service := l402.Service{
		Name:  serviceName,
		Tier:  l402.BaseTier,
		Price: 1,
	}
	mac, _, err := m.MintL402(ctx, service)
	require.NoError(t, err)

	id, err := l402.DecodeIdentifier(bytesReader(mac.Id()))
	require.NoError(t, err)

	difficulty := uint32(8)
	nonce, err := l402.SolvePoW(id.TokenID, difficulty)
	require.NoError(t, err)

	solvedMac := mac.Clone()
	err = l402.AddFirstPartyCaveats(solvedMac, l402.NewCaveat(
		l402.CondPoW,
		l402.FormatPoWCaveatValue(difficulty, nonce),
	))
	require.NoError(t, err)

	// Revoke the secret.
	idHash := sha256.Sum256(mac.Id())
	err = secretStore.RevokeSecret(ctx, idHash)
	require.NoError(t, err)

	// Verify should fail with secret not found.
	err = m.VerifyL402PoW(ctx, &PoWVerificationParams{
		Macaroon:      solvedMac,
		Difficulty:    difficulty,
		TargetService: serviceName,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSecretNotFound)
}

// bytesReader wraps a byte slice into an io.Reader.
func bytesReader(b []byte) *bytesReaderImpl {
	return &bytesReaderImpl{data: b, pos: 0}
}

type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

type bytesBuffer struct {
	data []byte
}

func (b *bytesBuffer) Write(p []byte) (n int, err error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *bytesBuffer) Bytes() []byte {
	return b.data
}
