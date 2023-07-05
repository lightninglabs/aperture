package aperturedb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/aperture/aperturedb/sqlc"
	"github.com/lightninglabs/aperture/lnc"
	"github.com/lightningnetwork/lnd/clock"
)

type (
	NewLNCSession = sqlc.InsertSessionParams

	SetRemoteParams = sqlc.SetRemotePubKeyParams

	SetExpiryParams = sqlc.SetExpiryParams
)

// LNCSessionsDB is an interface that defines the set of operations that can be
// executed agaist the lnc sessions database.
type LNCSessionsDB interface {
	// InsertLNCSession inserts a new session into the database.
	InsertSession(ctx context.Context, arg NewLNCSession) error

	// GetLNCSession returns the session tagged with the given passphrase
	// entropy.
	GetSession(ctx context.Context,
		passphraseEntropy []byte) (sqlc.LncSession, error)

	// SetRemotePubKey sets the remote public key for the session.
	SetRemotePubKey(ctx context.Context,
		arg SetRemoteParams) error

	// SetExpiry sets the expiry for the session.
	SetExpiry(ctx context.Context, arg SetExpiryParams) error
}

// LNCSessionsDBTxOptions defines the set of db txn options the LNCSessionsDB
// understands.
type LNCSessionsDBTxOptions struct {
	// readOnly governs if a read only transaction is needed or not.
	readOnly bool
}

// ReadOnly returns true if the transaction should be read only.
//
// NOTE: This implements the TxOptions
func (a *LNCSessionsDBTxOptions) ReadOnly() bool {
	return a.readOnly
}

// NewLNCSessionsDBReadTx creates a new read transaction option set.
func NewLNCSessionsDBReadTx() LNCSessionsDBTxOptions {
	return LNCSessionsDBTxOptions{
		readOnly: true,
	}
}

// BatchedLNCSessionsDB is a version of the LNCSecretsDB that's capable of
// batched database operations.
type BatchedLNCSessionsDB interface {
	LNCSessionsDB

	BatchedTx[LNCSessionsDB]
}

// LNCSessionsStore represents a storage backend.
type LNCSessionsStore struct {
	db    BatchedLNCSessionsDB
	clock clock.Clock
}

// NewSecretsStore creates a new SecretsStore instance given a open
// BatchedSecretsDB storage backend.
func NewLNCSessionsStore(db BatchedLNCSessionsDB) *LNCSessionsStore {
	return &LNCSessionsStore{
		db:    db,
		clock: clock.NewDefaultClock(),
	}
}

// AddSession adds a new session to the database.
func (l *LNCSessionsStore) AddSession(ctx context.Context,
	session *lnc.Session) error {

	if session.LocalStaticPrivKey == nil {
		return fmt.Errorf("local static private key is required")
	}

	localPrivKey := session.LocalStaticPrivKey.Serialize()
	createdAt := l.clock.Now().UTC().Truncate(time.Microsecond)

	var writeTxOpts LNCSessionsDBTxOptions
	err := l.db.ExecTx(ctx, &writeTxOpts, func(tx LNCSessionsDB) error {
		params := sqlc.InsertSessionParams{
			PassphraseWords:    session.PassphraseWords,
			PassphraseEntropy:  session.PassphraseEntropy,
			LocalStaticPrivKey: localPrivKey,
			MailboxAddr:        session.MailboxAddr,
			CreatedAt:          createdAt,
			DevServer:          session.DevServer,
		}

		return tx.InsertSession(ctx, params)
	})
	if err != nil {
		return fmt.Errorf("failed to insert new session: %v", err)
	}

	session.CreatedAt = createdAt

	return nil
}

// GetSession returns the session tagged with the given label.
func (l *LNCSessionsStore) GetSession(ctx context.Context,
	passphraseEntropy []byte) (*lnc.Session, error) {

	var session *lnc.Session

	readTx := NewLNCSessionsDBReadTx()
	err := l.db.ExecTx(ctx, &readTx, func(tx LNCSessionsDB) error {
		dbSession, err := tx.GetSession(ctx, passphraseEntropy)
		switch {
		case err == sql.ErrNoRows:
			return lnc.ErrSessionNotFound

		case err != nil:
			return err

		}

		privKey, _ := btcec.PrivKeyFromBytes(
			dbSession.LocalStaticPrivKey,
		)
		session = &lnc.Session{
			PassphraseWords:    dbSession.PassphraseWords,
			PassphraseEntropy:  dbSession.PassphraseEntropy,
			LocalStaticPrivKey: privKey,
			MailboxAddr:        dbSession.MailboxAddr,
			CreatedAt:          dbSession.CreatedAt,
			DevServer:          dbSession.DevServer,
		}

		if dbSession.RemoteStaticPubKey != nil {
			pubKey, err := btcec.ParsePubKey(
				dbSession.RemoteStaticPubKey,
			)
			if err != nil {
				return fmt.Errorf("failed to parse remote "+
					"public key for session(%x): %w",
					dbSession.PassphraseEntropy, err)
			}

			session.RemoteStaticPubKey = pubKey
		}

		if dbSession.Expiry.Valid {
			expiry := dbSession.Expiry.Time
			session.Expiry = &expiry
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	return session, nil
}

// SetRemotePubKey sets the remote public key for a session.
func (l *LNCSessionsStore) SetRemotePubKey(ctx context.Context,
	passphraseEntropy, remotePubKey []byte) error {

	var writeTxOpts LNCSessionsDBTxOptions
	err := l.db.ExecTx(ctx, &writeTxOpts, func(tx LNCSessionsDB) error {
		params := SetRemoteParams{
			PassphraseEntropy:  passphraseEntropy,
			RemoteStaticPubKey: remotePubKey,
		}
		return tx.SetRemotePubKey(ctx, params)
	})
	if err != nil {
		return fmt.Errorf("failed to set remote pub key to "+
			"session(%x): %w", passphraseEntropy, err)
	}

	return nil
}

// SetExpiry sets the expiry time for a session.
func (l *LNCSessionsStore) SetExpiry(ctx context.Context,
	passphraseEntropy []byte, expiry time.Time) error {

	var writeTxOpts LNCSessionsDBTxOptions
	err := l.db.ExecTx(ctx, &writeTxOpts, func(tx LNCSessionsDB) error {
		params := SetExpiryParams{
			PassphraseEntropy: passphraseEntropy,
			Expiry: sql.NullTime{
				Time:  expiry,
				Valid: true,
			},
		}

		return tx.SetExpiry(ctx, params)
	})
	if err != nil {
		return fmt.Errorf("failed to set expiry time to session(%x): "+
			"%w", passphraseEntropy, err)
	}

	return nil
}
