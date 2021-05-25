package aperture

import (
	"context"
	"fmt"
	"strings"

	"github.com/lightningnetwork/lnd/tor"
	"go.etcd.io/etcd/clientv3"
)

const (
	// onionDir is the directory we'll use to store all onion service
	// related information.
	onionDir = "onion"

	// onionV2Dir is the directory we'll use to store a v2 onion service's
	// private key, such that it can be restored after restarts.
	onionV2Dir = "v2"

	// onionV2Dir is the directory we'll use to store a v3 onion service's
	// private key, such that it can be restored after restarts.
	onionV3Dir = "v3"
)

// onionPath returns the full path to an onion service's private key of the
// given type.
func onionPath(onionType tor.OnionType) (string, error) {
	var typeDir string
	switch onionType {
	case tor.V2:
		typeDir = onionV2Dir
	case tor.V3:
		typeDir = onionV3Dir
	default:
		return "", fmt.Errorf("unknown onion type %v", onionType)
	}

	return strings.Join(
		[]string{topLevelKey, onionDir, typeDir}, etcdKeyDelimeter,
	), nil
}

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
func (s *onionStore) StorePrivateKey(onionType tor.OnionType,
	privateKey []byte) error {

	onionPath, err := onionPath(onionType)
	if err != nil {
		return err
	}

	_, err = s.Client.Put(context.Background(), onionPath, string(privateKey))
	return err
}

// PrivateKey retrieves a stored private key. If it is not found, then
// ErrNoPrivateKey should be returned.
func (s *onionStore) PrivateKey(onionType tor.OnionType) ([]byte, error) {
	onionPath, err := onionPath(onionType)
	if err != nil {
		return nil, err
	}

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
func (s *onionStore) DeletePrivateKey(onionType tor.OnionType) error {
	onionPath, err := onionPath(onionType)
	if err != nil {
		return err
	}

	_, err = s.Client.Delete(context.Background(), onionPath)
	return err
}
