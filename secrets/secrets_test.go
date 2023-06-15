package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
)

// assertSecretExists is a helper to determine if a secret for the given
// identifier exists in the store. If it exists, its value is compared against
// the expected secret.
func assertSecretExists(t *testing.T, store *SecretStore, id [sha256.Size]byte,
	expSecret *[lsat.SecretSize]byte) {

	t.Helper()

	exists := expSecret != nil
	secret, err := store.GetSecret(context.Background(), id)
	switch {
	case exists && err != nil:
		t.Fatalf("unable to retrieve secret: %v", err)
	case !exists && err != mint.ErrSecretNotFound:
		t.Fatalf("expected error ErrSecretNotFound, got \"%v\"", err)
	case exists:
		if secret != *expSecret {
			t.Fatalf("expected secret %x, got %x", expSecret, secret)
		}
	default:
		return
	}
}

// TestSecretStore ensures the different operations of the SecretStore behave as
// expected.
func TestSecretStore(t *testing.T) {
	etcdClient, serverCleanup := EtcdSetup(t)
	defer etcdClient.Close()
	defer serverCleanup()

	ctx := context.Background()
	store := NewStore(etcdClient)

	// Create a test ID and ensure a secret doesn't exist for it yet as we
	// haven't created one.
	var id [sha256.Size]byte
	copy(id[:], bytes.Repeat([]byte("A"), 32))
	assertSecretExists(t, store, id, nil)

	// Create one and ensure we can retrieve it at a later point.
	secret, err := store.NewSecret(ctx, id)
	if err != nil {
		t.Fatalf("unable to generate new secret: %v", err)
	}
	assertSecretExists(t, store, id, &secret)

	// Once revoked, it should no longer exist.
	if err := store.RevokeSecret(ctx, id); err != nil {
		t.Fatalf("unable to revoke secret: %v", err)
	}
	assertSecretExists(t, store, id, nil)
}
