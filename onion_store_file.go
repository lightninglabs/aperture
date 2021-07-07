package aperture

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/lightningnetwork/lnd/tor"
)

// onionStoreFile is an file-based implementation of tor.OnionStore.
type onionStoreFile struct {
	rootDir string
}

// A compile-time constraint to ensure onionStore implements tor.OnionStore.
var _ tor.OnionStore = (*onionStoreFile)(nil)

// newOnionStoreFile creates an file-based implementation of tor.OnionStore.
func newOnionStoreFile(rootDir string) *onionStoreFile {
	return &onionStoreFile{rootDir: rootDir}
}

// onionFilePath returns the absolute filesystem path to an onion service's private key of the
// given type.
func (s *onionStoreFile) onionFilePath(onionType tor.OnionType) (string, error) {
	var typeName string
	switch onionType {
	case tor.V2:
		typeName = "v2"
	case tor.V3:
		typeName = "v3"
	default:
		return "", fmt.Errorf("unknown onion type %v", onionType)
	}

	return filepath.Join(s.rootDir, fmt.Sprintf("onion-%s.key", typeName)), nil
}

// StorePrivateKey stores the given private key.
func (s *onionStoreFile) StorePrivateKey(onionType tor.OnionType,
	privateKey []byte) error {

	name, err := s.onionFilePath(onionType)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(name, privateKey, 0400)
}

// PrivateKey retrieves a stored private key. If it is not found, then
// ErrNoPrivateKey should be returned.
func (s *onionStoreFile) PrivateKey(onionType tor.OnionType) ([]byte, error) {
	name, err := s.onionFilePath(onionType)
	if err != nil {
		return nil, err
	}

	data, err := ioutil.ReadFile(name)
	if err != nil && os.IsNotExist(err) {
		return nil, tor.ErrNoPrivateKey
	}

	return data, err
}

// DeletePrivateKey securely removes the private key from the store.
func (s *onionStoreFile) DeletePrivateKey(onionType tor.OnionType) error {
	name, err := s.onionFilePath(onionType)
	if err != nil {
		return err
	}

	err = os.Remove(name)
	if err != nil && os.IsNotExist(err) {
		return tor.ErrNoPrivateKey
	}

	return err
}
