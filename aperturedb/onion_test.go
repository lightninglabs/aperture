package aperturedb

import (
	"database/sql"
	"testing"

	"github.com/lightningnetwork/lnd/tor"
	"github.com/stretchr/testify/require"
)

func newOnionStoreWithDB(db *BaseDB) *OnionStore {
	dbTxer := NewTransactionExecutor(db,
		func(tx *sql.Tx) OnionDB {
			return db.WithTx(tx)
		},
	)

	return NewOnionStore(dbTxer)
}

func TestOnionDB(t *testing.T) {
	// First, create a new test database.
	db := NewTestDB(t)
	store := newOnionStoreWithDB(db.BaseDB)

	// Attempting to retrieve a private key when none is stored returns the
	// expected error.
	_, err := store.PrivateKey()
	require.ErrorIs(t, err, tor.ErrNoPrivateKey)

	// Store a private key.
	privateKey := []byte("private key")
	err = store.StorePrivateKey(privateKey)
	require.NoError(t, err)

	// Retrieving the private key should return the stored value.
	privateKeyDB, err := store.PrivateKey()
	require.NoError(t, err)
	require.Equal(t, privateKey, privateKeyDB)

	// Storing the same private key should not return an error.
	err = store.StorePrivateKey(privateKey)
	require.NoError(t, err)

	// We can only store one private key.
	newPrivateKey := []byte("second private key")
	err = store.StorePrivateKey(newPrivateKey)
	require.Error(t, err)

	// Remove the stored private key.
	err = store.DeletePrivateKey()
	require.NoError(t, err)

	err = store.StorePrivateKey(newPrivateKey)
	require.NoError(t, err)
}
