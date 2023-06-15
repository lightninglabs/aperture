package aperturedb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"github.com/lightninglabs/aperture/aperturedb/sqlc"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/clock"
)

type (
	// NewSecret is a struct that contains the parameters required to insert
	// a new secret into the database.
	NewSecret = sqlc.InsertSecretParams
)

// SecretsDB is an interface that defines the set of operations that can be
// executed against the secrets database.
type SecretsDB interface {
	// InsertSecret inserts a new secret into the database.
	InsertSecret(ctx context.Context, arg NewSecret) (int32, error)

	// GetSecretByHash returns the secret that corresponds to the given
	// hash.
	GetSecretByHash(ctx context.Context, hash []byte) ([]byte, error)

	// DeleteSecretByHash removes the secret that corresponds to the given
	// hash.
	DeleteSecretByHash(ctx context.Context, hash []byte) (int64, error)
}

// SecretsTxOptions defines the set of db txn options the SecretsStore
// understands.
type SecretsDBTxOptions struct {
	// readOnly governs if a read only transaction is needed or not.
	readOnly bool
}

// ReadOnly returns true if the transaction should be read only.
//
// NOTE: This implements the TxOptions
func (a *SecretsDBTxOptions) ReadOnly() bool {
	return a.readOnly
}

// NewSecretsDBReadTx creates a new read transaction option set.
func NewSecretsDBReadTx() SecretsDBTxOptions {
	return SecretsDBTxOptions{
		readOnly: true,
	}
}

// BatchedSecretsDB is a version of the SecretsDB that's capable of batched
// database operations.
type BatchedSecretsDB interface {
	SecretsDB

	BatchedTx[SecretsDB]
}

// SecretsStore represents a storage backend.
type SecretsStore struct {
	db    BatchedSecretsDB
	clock clock.Clock
}

// NewSecretsStore creates a new SecretsStore instance given a open
// BatchedSecretsDB storage backend.
func NewSecretsStore(db BatchedSecretsDB) *SecretsStore {
	return &SecretsStore{
		db:    db,
		clock: clock.NewDefaultClock(),
	}
}

// NewSecret creates a new cryptographically random secret which is
// keyed by the given hash.
func (s *SecretsStore) NewSecret(ctx context.Context,
	hash [sha256.Size]byte) ([lsat.SecretSize]byte, error) {

	var secret [lsat.SecretSize]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return [lsat.SecretSize]byte{}, err
	}

	var writeTxOpts SecretsDBTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx SecretsDB) error {
		_, err := tx.InsertSecret(ctx, NewSecret{
			Hash:      hash[:],
			Secret:    secret[:],
			CreatedAt: s.clock.Now().UTC(),
		})
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return [lsat.SecretSize]byte{}, fmt.Errorf("unable to insert "+
			"new secret for hash(%x): %w", hash, err)
	}

	return secret, nil
}

// GetSecret returns the cryptographically random secret that
// corresponds to the given hash. If there is no secret, then
// ErrSecretNotFound is returned.
func (s *SecretsStore) GetSecret(ctx context.Context,
	hash [sha256.Size]byte) ([lsat.SecretSize]byte, error) {

	var secret [lsat.SecretSize]byte
	readOpts := NewSecretsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(db SecretsDB) error {
		secretRow, err := db.GetSecretByHash(ctx, hash[:])
		switch {
		case err == sql.ErrNoRows:
			return mint.ErrSecretNotFound

		case err != nil:
			return err
		}

		copy(secret[:], secretRow)

		return nil
	})

	if err != nil {
		return [lsat.SecretSize]byte{}, fmt.Errorf("unable to get "+
			"secret for hash(%x): %w", hash, err)
	}

	return secret, nil
}

// RevokeSecret removes the cryptographically random secret that
// corresponds to the given hash. This acts as a NOP if the secret does
// not exist.
func (s *SecretsStore) RevokeSecret(ctx context.Context,
	hash [sha256.Size]byte) error {

	var writeTxOpts SecretsDBTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx SecretsDB) error {
		nRows, err := tx.DeleteSecretByHash(ctx, hash[:])
		if err != nil {
			return err
		}

		if nRows != 1 {
			log.Info("deleting secret(%x) did not affect %w rows",
				hash, nRows)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("unable to revoke secret for hash(%x): %w",
			hash, err)
	}

	return nil
}
