package aperture

import (
	"bytes"
	"testing"

	"github.com/lightningnetwork/lnd/tor"
)

// assertPrivateKeyExists is a helper to determine if the private key for an
// onion service exists in the store. If it does, it's compared against what's
// expected.
func assertPrivateKeyExists(t *testing.T, store *onionStore,
	expPrivateKey *[]byte) {

	t.Helper()

	exists := expPrivateKey != nil
	privateKey, err := store.PrivateKey()
	switch {
	case exists && err != nil:
		t.Fatalf("unable to retrieve private key: %v", err)
	case !exists && err != tor.ErrNoPrivateKey:
		t.Fatalf("expected error ErrNoPrivateKey, got \"%v\"", err)
	case exists:
		if !bytes.Equal(privateKey, *expPrivateKey) {
			t.Fatalf("expected private key %v, got %v",
				string(*expPrivateKey), string(privateKey))
		}
	default:
		return
	}
}

// TestOnionStore ensures the different operations of the onionStore behave
// as expected.
func TestOnionStore(t *testing.T) {
	etcdClient, serverCleanup := etcdSetup(t)
	defer etcdClient.Close()
	defer serverCleanup()

	// Upon a fresh initialization of the store, no private keys should
	// exist for any onion service type.
	store := newOnionStore(etcdClient)
	assertPrivateKeyExists(t, store, nil)

	// Store a private key for an onion service and check it was stored
	// correctly.
	privateKey := []byte("hide_me_plz")
	if err := store.StorePrivateKey(privateKey); err != nil {
		t.Fatalf("unable to store private key for onion service: %v",
			err)
	}
	assertPrivateKeyExists(t, store, &privateKey)

	// Delete the private key for the onion service and check that it was
	// indeed successful.
	if err := store.DeletePrivateKey(); err != nil {
		t.Fatalf("unable to remove private key for onion service: %v",
			err)
	}
	assertPrivateKeyExists(t, store, nil)
}
