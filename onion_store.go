package aperture

import (
	"context"
	"strings"

	"github.com/lightningnetwork/lnd/tor"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	// onionDir is the directory we'll use to store all onion service
	// related information.
	onionDir = "onion"

	// onionV3Dir is the directory we'll use to store a v3 onion service's
	// private key, such that it can be restored after restarts.
	onionV3Dir = "v3"
)

// onionPath is the full path to an onion service's private key.
var onionPath = strings.Join(
	[]string{topLevelKey, onionDir, onionV3Dir}, etcdKeyDelimeter,
)

// onionStore is an etcd-based implementation of tor.OnionStore.
type onionStore struct {
	*clientv3.Client
}

// A compile-time constraint to ensure onionStore implements tor.OnionStore.
var _ tor.OnionStore = (*onionStore)(nil)

// newOnionStore creates an etcd-based implementation of tor.OnionStore.
func newOnionStore(client *clientv3.Client) *onionStore {
	return &onionStore{Client: client}
}

// StorePrivateKey stores the given private key.
func (s *onionStore) StorePrivateKey(privateKey []byte) error {
	_, err := s.Client.Put(context.Background(), onionPath, string(privateKey))
	return err
}

// PrivateKey retrieves a stored private key. If it is not found, then
// ErrNoPrivateKey should be returned.
func (s *onionStore) PrivateKey() ([]byte, error) {
	resp, err := s.Get(context.Background(), onionPath)
	if err != nil {
		return nil, err
	}
	if len(resp.Kvs) == 0 {
		return nil, tor.ErrNoPrivateKey
	}

	return resp.Kvs[0].Value, nil
}

// DeletePrivateKey securely removes the private key from the store.
func (s *onionStore) DeletePrivateKey() error {
	_, err := s.Client.Delete(context.Background(), onionPath)
	return err
}
