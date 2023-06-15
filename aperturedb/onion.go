package aperturedb

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/lightninglabs/aperture/aperturedb/sqlc"
	"github.com/lightningnetwork/lnd/clock"
	"github.com/lightningnetwork/lnd/tor"
)

type (
	NewOnionPrivateKey = sqlc.UpsertOnionParams
)

// OnionDB is an interface that defines the set of operations that can be
// executed against the onion database.
type OnionDB interface {
	// UpsertOnion inserts a new onion private key into the database. If
	// the onion private key already exists in the db this is a NOOP
	// operation.
	UpsertOnion(ctx context.Context, arg NewOnionPrivateKey) error

	// SelectOnionPrivateKey selects the onion private key from the
	// database.
	SelectOnionPrivateKey(ctx context.Context) ([]byte, error)

	// DeleteOnionPrivateKey deletes the onion private key from the
	// database.
	DeleteOnionPrivateKey(ctx context.Context) error
}

// OnionTxOptions defines the set of db txn options the OnionStore
// understands.
type OnionDBTxOptions struct {
	// readOnly governs if a read only transaction is needed or not.
	readOnly bool
}

// ReadOnly returns true if the transaction should be read only.
//
// NOTE: This implements the TxOptions
func (a *OnionDBTxOptions) ReadOnly() bool {
	return a.readOnly
}

// NewOnionDBReadTx creates a new read transaction option set.
func NewOnionDBReadTx() OnionDBTxOptions {
	return OnionDBTxOptions{
		readOnly: true,
	}
}

// BatchedOnionDB is a version of the OnionDB that's capable of batched
// database operations.
type BatchedOnionDB interface {
	OnionDB

	BatchedTx[OnionDB]
}

// OnionStore represents a storage backend.
type OnionStore struct {
	db    BatchedOnionDB
	clock clock.Clock
}

// NewOnionStore creates a new OnionStore instance given a open BatchedOnionDB
// storage backend.
func NewOnionStore(db BatchedOnionDB) *OnionStore {
	return &OnionStore{
		db:    db,
		clock: clock.NewDefaultClock(),
	}
}

// StorePrivateKey stores the private key according to the implementation of
// the OnionStore interface.
func (o *OnionStore) StorePrivateKey(privateKey []byte) error {
	ctxt, cancel := context.WithTimeout(
		context.Background(), DefaultStoreTimeout,
	)
	defer cancel()

	var writeTxOpts OnionDBTxOptions
	err := o.db.ExecTx(ctxt, &writeTxOpts, func(tx OnionDB) error {
		// Only store the private key if it doesn't already exist.
		dbPK, err := tx.SelectOnionPrivateKey(ctxt)
		switch {
		// If there is already a different private key stored in the
		/// database, return an error.
		case dbPK != nil && !bytes.Equal(dbPK, privateKey):
			return fmt.Errorf("private key already exists")

		case err != nil && err != sql.ErrNoRows:
			return err
		}

		params := NewOnionPrivateKey{
			PrivateKey: privateKey,
			CreatedAt:  o.clock.Now().UTC(),
		}

		return tx.UpsertOnion(ctxt, params)
	})

	if err != nil {
		return fmt.Errorf("failed to store private key: %v", err)
	}

	return nil
}

// PrivateKey retrieves a stored private key. If it is not found, then
// ErrNoPrivateKey should be returned.
func (o *OnionStore) PrivateKey() ([]byte, error) {
	ctxt, cancel := context.WithTimeout(
		context.Background(), DefaultStoreTimeout,
	)
	defer cancel()

	var (
		privateKey []byte
	)

	var readTxOpts OnionDBTxOptions
	err := o.db.ExecTx(ctxt, &readTxOpts, func(tx OnionDB) error {
		row, err := o.db.SelectOnionPrivateKey(ctxt)
		switch {
		case err == sql.ErrNoRows:
			return tor.ErrNoPrivateKey

		case err != nil:
			return err
		}

		privateKey = make([]byte, len(row))
		copy(privateKey, row)

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to retrieve private key: %w",
			err)
	}

	return privateKey, nil
}

// DeletePrivateKey securely removes the private key from the store.
func (o *OnionStore) DeletePrivateKey() error {
	ctxt, cancel := context.WithTimeout(
		context.Background(), DefaultStoreTimeout,
	)
	defer cancel()

	var writeTxOpts OnionDBTxOptions
	err := o.db.ExecTx(ctxt, &writeTxOpts, func(tx OnionDB) error {
		return tx.DeleteOnionPrivateKey(ctxt)
	})

	if err != nil {
		return fmt.Errorf("failed to delete private key: %v", err)
	}

	return nil
}
