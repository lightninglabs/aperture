package aperturedb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/mint"
	"github.com/stretchr/testify/require"
)

var (
	defaultTestTimeout = 5 * time.Second
)

func newSecretsStoreWithDB(db *BaseDB) *SecretsStore {
	dbTxer := NewTransactionExecutor(db,
		func(tx *sql.Tx) SecretsDB {
			return db.WithTx(tx)
		},
	)

	return NewSecretsStore(dbTxer)
}

func TestSecretDB(t *testing.T) {
	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// First, create a new test database.
	db := NewTestDB(t)
	store := newSecretsStoreWithDB(db.BaseDB)

	// Create a random hash.
	hash := [sha256.Size]byte{}
	_, err := rand.Read(hash[:])
	require.NoError(t, err)

	// Trying to get a secret that doesn't exist should fail.
	_, err = store.GetSecret(ctxt, hash)
	require.ErrorIs(t, err, mint.ErrSecretNotFound)

	// Create a new secret.
	secret, err := store.NewSecret(ctxt, hash)
	require.NoError(t, err)

	// Get the secret from the db.
	dbSecret, err := store.GetSecret(ctxt, hash)
	require.NoError(t, err)
	require.Equal(t, secret, dbSecret)

	// Revoke the secret.
	err = store.RevokeSecret(ctxt, hash)
	require.NoError(t, err)

	// The secret should no longer exist.
	_, err = store.GetSecret(ctxt, hash)
	require.ErrorIs(t, err, mint.ErrSecretNotFound)
}
