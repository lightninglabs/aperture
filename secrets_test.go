package aperture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/server/v3/embed"
)

// etcdSetup is a helper that instantiates a new etcd cluster along with a
// client connection to it. A cleanup closure is also returned to free any
// allocated resources required by etcd.
func etcdSetup(t *testing.T) (*clientv3.Client, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "etcd")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}

	cfg := embed.NewConfig()
	cfg.Dir = tempDir
	cfg.Logger = "zap"
	cfg.ListenClientUrls = []url.URL{{Host: "127.0.0.1:9125"}}
	cfg.ListenPeerUrls = []url.URL{{Host: "127.0.0.1:9126"}}

	etcd, err := embed.StartEtcd(cfg)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("unable to start etcd: %v", err)
	}

	select {
	case <-etcd.Server.ReadyNotify():
	case <-time.After(5 * time.Second):
		os.RemoveAll(tempDir)
		etcd.Server.Stop() // trigger a shutdown
		t.Fatal("server took too long to start")
	}

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{cfg.ListenClientUrls[0].Host},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("unable to connect to etcd: %v", err)
	}

	return client, func() {
		etcd.Close()
		os.RemoveAll(tempDir)
	}
}

// assertSecretExists is a helper to determine if a secret for the given
// identifier exists in the store. If it exists, its value is compared against
// the expected secret.
func assertSecretExists(t *testing.T, store *secretStore, id [sha256.Size]byte,
	expSecret *[l402.SecretSize]byte) {

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

// TestSecretStore ensures the different operations of the secretStore behave as
// expected.
func TestSecretStore(t *testing.T) {
	etcdClient, serverCleanup := etcdSetup(t)
	defer etcdClient.Close()
	defer serverCleanup()

	ctx := context.Background()
	store := newSecretStore(etcdClient)

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
