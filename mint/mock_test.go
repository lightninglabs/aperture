package mint

import (
	"context"
	"crypto/rand"
	"crypto/sha256"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightningnetwork/lnd/lntypes"
)

var (
	testPreimage = lntypes.Preimage{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17,
		18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
	}
	testHash   = testPreimage.Hash()
	testPayReq = "lnsb1..."
)

type mockChallenger struct{}

var _ Challenger = (*mockChallenger)(nil)

func newMockChallenger() *mockChallenger {
	return &mockChallenger{}
}

func (d *mockChallenger) Start() error {
	return nil
}

func (d *mockChallenger) Stop() {
	// Nothing to do here.
}

func (d *mockChallenger) NewChallenge(price int64) (string, lntypes.Hash,
	error) {

	return testPayReq, testHash, nil
}

type mockSecretStore struct {
	secrets map[[sha256.Size]byte][lsat.SecretSize]byte
}

var _ SecretStore = (*mockSecretStore)(nil)

func (s *mockSecretStore) NewSecret(ctx context.Context,
	id [sha256.Size]byte) ([lsat.SecretSize]byte, error) {

	var secret [lsat.SecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return secret, err
	}
	s.secrets[id] = secret
	return secret, nil
}

func (s *mockSecretStore) GetSecret(ctx context.Context,
	id [sha256.Size]byte) ([lsat.SecretSize]byte, error) {

	secret, ok := s.secrets[id]
	if !ok {
		return secret, ErrSecretNotFound
	}
	return secret, nil
}

func (s *mockSecretStore) RevokeSecret(ctx context.Context,
	id [sha256.Size]byte) error {

	delete(s.secrets, id)
	return nil
}

func newMockSecretStore() *mockSecretStore {
	return &mockSecretStore{
		secrets: make(map[[sha256.Size]byte][lsat.SecretSize]byte),
	}
}

type mockServiceLimiter struct {
	capabilities map[lsat.Service]lsat.Caveat
	constraints  map[lsat.Service][]lsat.Caveat
	timeouts     map[lsat.Service]lsat.Caveat
}

var _ ServiceLimiter = (*mockServiceLimiter)(nil)

func newMockServiceLimiter() *mockServiceLimiter {
	return &mockServiceLimiter{
		capabilities: make(map[lsat.Service]lsat.Caveat),
		constraints:  make(map[lsat.Service][]lsat.Caveat),
		timeouts:     make(map[lsat.Service]lsat.Caveat),
	}
}

func (l *mockServiceLimiter) ServiceCapabilities(ctx context.Context,
	services ...lsat.Service) ([]lsat.Caveat, error) {

	res := make([]lsat.Caveat, 0, len(services))
	for _, service := range services {
		capabilities, ok := l.capabilities[service]
		if !ok {
			continue
		}
		res = append(res, capabilities)
	}
	return res, nil
}

func (l *mockServiceLimiter) ServiceConstraints(ctx context.Context,
	services ...lsat.Service) ([]lsat.Caveat, error) {

	res := make([]lsat.Caveat, 0, len(services))
	for _, service := range services {
		constraints, ok := l.constraints[service]
		if !ok {
			continue
		}
		res = append(res, constraints...)
	}
	return res, nil
}

func (l *mockServiceLimiter) ServiceTimeouts(ctx context.Context,
	services ...lsat.Service) ([]lsat.Caveat, error) {

	res := make([]lsat.Caveat, 0, len(services))
	for _, service := range services {
		timeouts, ok := l.timeouts[service]
		if !ok {
			continue
		}
		res = append(res, timeouts)
	}
	return res, nil
}
