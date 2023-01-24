package aperture

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var (
	// secretsPrefix is the key we'll use to prefix all LSAT identifiers
	// with when storing secrets in an etcd cluster.
	secretsPrefix = "secrets"
)

// idKey returns the full key to store in the database for an LSAT identifier.
// The identifier is hex-encoded in order to prevent conflicts with the etcd key
// delimeter.
//
// The resulting path of the identifier bff4ee83 within etcd would look like:
// lsat/proxy/secrets/bff4ee83
func idKey(id [sha256.Size]byte) string {
	return strings.Join(
		[]string{topLevelKey, secretsPrefix, hex.EncodeToString(id[:])},
		etcdKeyDelimeter,
	)
}

// secretStore is a store of LSAT secrets backed by an etcd cluster.
type secretStore struct {
	*clientv3.Client
}

// A compile-time constraint to ensure secretStore implements mint.SecretStore.
var _ mint.SecretStore = (*secretStore)(nil)

// newSecretStore instantiates a new LSAT secrets store backed by an etcd
// cluster.
func newSecretStore(client *clientv3.Client) *secretStore {
	return &secretStore{Client: client}
}

// NewSecret creates a new cryptographically random secret which is keyed by the
// given hash.
func (s *secretStore) NewSecret(ctx context.Context,
	id [sha256.Size]byte) ([lsat.SecretSize]byte, error) {

	var secret [lsat.SecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return secret, err
	}

	_, err := s.Put(ctx, idKey(id), string(secret[:]))
	return secret, err
}

// GetSecret returns the cryptographically random secret that corresponds to the
// given hash. If there is no secret, then mint.ErrSecretNotFound is returned.
func (s *secretStore) GetSecret(ctx context.Context,
	id [sha256.Size]byte) ([lsat.SecretSize]byte, error) {

	resp, err := s.Get(ctx, idKey(id))
	if err != nil {
		return [lsat.SecretSize]byte{}, err
	}
	if len(resp.Kvs) == 0 {
		return [lsat.SecretSize]byte{}, mint.ErrSecretNotFound
	}
	if len(resp.Kvs[0].Value) != lsat.SecretSize {
		return [lsat.SecretSize]byte{}, fmt.Errorf("invalid secret "+
			"size %v", len(resp.Kvs[0].Value))
	}

	var secret [lsat.SecretSize]byte
	copy(secret[:], resp.Kvs[0].Value)
	return secret, nil
}

// RevokeSecret removes the cryptographically random secret that corresponds to
// the given hash. This acts as a NOP if the secret does not exist.
func (s *secretStore) RevokeSecret(ctx context.Context,
	id [sha256.Size]byte) error {

	_, err := s.Delete(ctx, idKey(id))
	return err
}
