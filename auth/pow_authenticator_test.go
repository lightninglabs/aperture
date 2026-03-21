package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// mockPoWChallenger implements mint.Challenger for PoW tests.
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

// mockPoWSecretStore implements mint.SecretStore for tests.
type mockPoWSecretStore struct {
	secrets map[[sha256.Size]byte][l402.SecretSize]byte
}

func newMockPoWSecretStore() *mockPoWSecretStore {
	return &mockPoWSecretStore{
		secrets: make(map[[sha256.Size]byte][l402.SecretSize]byte),
	}
}

func (s *mockPoWSecretStore) NewSecret(_ context.Context,
	id [sha256.Size]byte) ([l402.SecretSize]byte, error) {

	var secret [l402.SecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return secret, err
	}
	s.secrets[id] = secret
	return secret, nil
}

func (s *mockPoWSecretStore) GetSecret(_ context.Context,
	id [sha256.Size]byte) ([l402.SecretSize]byte, error) {

	secret, ok := s.secrets[id]
	if !ok {
		return secret, fmt.Errorf("secret not found")
	}
	return secret, nil
}

func (s *mockPoWSecretStore) RevokeSecret(_ context.Context,
	id [sha256.Size]byte) error {

	delete(s.secrets, id)
	return nil
}

// mockPoWServiceLimiter implements mint.ServiceLimiter.
type mockPoWServiceLimiter struct{}

func (l *mockPoWServiceLimiter) ServiceCapabilities(_ context.Context,
	_ ...l402.Service) ([]l402.Caveat, error) {

	return nil, nil
}

func (l *mockPoWServiceLimiter) ServiceConstraints(_ context.Context,
	_ ...l402.Service) ([]l402.Caveat, error) {

	return nil, nil
}

func (l *mockPoWServiceLimiter) ServiceTimeouts(_ context.Context,
	_ ...l402.Service) ([]l402.Caveat, error) {

	return nil, nil
}

func TestPoWAuthenticatorAccept(t *testing.T) {
	secretStore := newMockPoWSecretStore()
	m := mint.New(&mint.Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        secretStore,
		ServiceLimiter: &mockPoWServiceLimiter{},
		Now:            time.Now,
	})

	serviceName := "test-service"
	difficulty := uint32(8)
	difficulties := map[string]uint32{serviceName: difficulty}
	powAuth := auth.NewPoWAuthenticator(m, difficulties)

	// Mint a macaroon.
	service := l402.Service{
		Name:  serviceName,
		Tier:  l402.BaseTier,
		Price: 1,
	}
	mac, _, err := m.MintL402(context.Background(), service)
	require.NoError(t, err)

	// Decode the identifier.
	id, err := l402.DecodeIdentifier(bytes.NewReader(mac.Id()))
	require.NoError(t, err)

	// Solve PoW.
	nonce, err := l402.SolvePoW(id.TokenID, difficulty)
	require.NoError(t, err)

	// Add PoW caveat.
	solvedMac := mac.Clone()
	err = l402.AddFirstPartyCaveats(solvedMac, l402.NewCaveat(
		l402.CondPoW,
		l402.FormatPoWCaveatValue(difficulty, nonce),
	))
	require.NoError(t, err)

	// Create header with the solved macaroon.
	macBytes, err := solvedMac.MarshalBinary()
	require.NoError(t, err)
	macBase64 := base64.StdEncoding.EncodeToString(macBytes)

	header := http.Header{}
	header.Set("Authorization", fmt.Sprintf("L402 %s:POW", macBase64))

	// Accept should return true.
	require.True(t, powAuth.Accept(&header, serviceName))
}

func TestPoWAuthenticatorAcceptNoHeader(t *testing.T) {
	m := mint.New(&mint.Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        newMockPoWSecretStore(),
		ServiceLimiter: &mockPoWServiceLimiter{},
		Now:            time.Now,
	})

	difficulties := map[string]uint32{"test": 8}
	powAuth := auth.NewPoWAuthenticator(m, difficulties)

	// No header should be denied.
	header := http.Header{}
	require.False(t, powAuth.Accept(&header, "test"))
}

func TestPoWAuthenticatorAcceptWrongService(t *testing.T) {
	m := mint.New(&mint.Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        newMockPoWSecretStore(),
		ServiceLimiter: &mockPoWServiceLimiter{},
		Now:            time.Now,
	})

	// Only configure difficulty for "service-a".
	difficulties := map[string]uint32{"service-a": 8}
	powAuth := auth.NewPoWAuthenticator(m, difficulties)

	// Trying to authenticate for unconfigured service should fail.
	header := http.Header{}
	header.Set("Authorization", "L402 dummybase64:POW")
	require.False(t, powAuth.Accept(&header, "unknown-service"))
}

func TestPoWAuthenticatorFreshChallengeHeader(t *testing.T) {
	secretStore := newMockPoWSecretStore()
	m := mint.New(&mint.Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        secretStore,
		ServiceLimiter: &mockPoWServiceLimiter{},
		Now:            time.Now,
	})

	difficulties := map[string]uint32{"test": 16}
	powAuth := auth.NewPoWAuthenticator(m, difficulties)

	header, err := powAuth.FreshChallengeHeader("test", 1)
	require.NoError(t, err)

	// Should contain WWW-Authenticate header with pow parameter.
	authHeader := header.Get("WWW-Authenticate")
	require.Contains(t, authHeader, "L402")
	require.Contains(t, authHeader, "macaroon=")
	require.Contains(t, authHeader, "pow=")

	// Verify the macaroon can be decoded.
	require.Regexp(t, `macaroon="([^"]+)"`, authHeader)
}

func TestPoWAuthenticatorAcceptInvalidPoW(t *testing.T) {
	secretStore := newMockPoWSecretStore()
	m := mint.New(&mint.Config{
		Challenger:     &mockPoWChallenger{},
		Secrets:        secretStore,
		ServiceLimiter: &mockPoWServiceLimiter{},
		Now:            time.Now,
	})

	serviceName := "test-service"
	difficulty := uint32(8)
	difficulties := map[string]uint32{serviceName: difficulty}
	powAuth := auth.NewPoWAuthenticator(m, difficulties)

	// Mint a macaroon.
	service := l402.Service{
		Name:  serviceName,
		Tier:  l402.BaseTier,
		Price: 1,
	}
	mac, _, err := m.MintL402(context.Background(), service)
	require.NoError(t, err)

	// Add PoW caveat with a bad nonce (no actual PoW done).
	solvedMac := mac.Clone()
	err = l402.AddFirstPartyCaveats(solvedMac, l402.NewCaveat(
		l402.CondPoW,
		l402.FormatPoWCaveatValue(difficulty, 12345),
	))
	require.NoError(t, err)

	macBytes, err := solvedMac.MarshalBinary()
	require.NoError(t, err)
	macBase64 := base64.StdEncoding.EncodeToString(macBytes)

	header := http.Header{}
	header.Set("Authorization", fmt.Sprintf("L402 %s:POW", macBase64))

	// Should be rejected.
	require.False(t, powAuth.Accept(&header, serviceName))
}

// Prevent unused import warning.
var _ = (*macaroon.Macaroon)(nil)
