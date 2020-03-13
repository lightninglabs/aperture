package kirin

import (
	"bytes"
	"testing"

	"github.com/lightningnetwork/lnd/tor"
)

// assertPrivateKeyExists is a helper to determine if the private key for an
// onion service exists in the store. If it does, it's compared against what's
// expected.
func assertPrivateKeyExists(t *testing.T, store *onionStore,
	onionType tor.OnionType, expPrivateKey *[]byte) {

	t.Helper()

	exists := expPrivateKey != nil
	privateKey, err := store.PrivateKey(onionType)
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

// TestOnionStore ensures the different operations of the onionStore behave as
// espected.
func TestOnionStore(t *testing.T) {
	etcdClient, serverCleanup := etcdSetup(t)
	defer etcdClient.Close()
	defer serverCleanup()

	// Upon a fresh initialization of the store, no private keys should
	// exist for any onion service type.
	store := newOnionStore(etcdClient)
	assertPrivateKeyExists(t, store, tor.V2, nil)
	assertPrivateKeyExists(t, store, tor.V3, nil)

	// Store a private key for a V2 onion service and check it was stored
	// correctly.
	privateKeyV2 := []byte("hide_me_plz_v2")
	if err := store.StorePrivateKey(tor.V2, privateKeyV2); err != nil {
		t.Fatalf("unable to store private key for v2 onion service: %v",
			err)
	}
	assertPrivateKeyExists(t, store, tor.V2, &privateKeyV2)

	// Store a private key for a V3 onion service and check it was stored
	// correctly.
	privateKeyV3 := []byte("hide_me_plz_v3")
	if err := store.StorePrivateKey(tor.V3, privateKeyV3); err != nil {
		t.Fatalf("unable to store private key for v3 onion service: %v",
			err)
	}
	assertPrivateKeyExists(t, store, tor.V3, &privateKeyV3)

	// Delete the private key for the V2 onion service and check that it was
	// indeed successful.
	if err := store.DeletePrivateKey(tor.V2); err != nil {
		t.Fatalf("unable to remove private key for v2 onion service: %v",
			err)
	}
	assertPrivateKeyExists(t, store, tor.V2, nil)

	// Delete the private key for the V3 onion service and check that it was
	// indeed successful.
	if err := store.DeletePrivateKey(tor.V3); err != nil {
		t.Fatalf("unable to remove private key for v3 onion service: %v",
			err)
	}
	assertPrivateKeyExists(t, store, tor.V3, nil)
}
