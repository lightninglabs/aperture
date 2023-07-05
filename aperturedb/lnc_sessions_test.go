package aperturedb

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/aperture/lnc"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/stretchr/testify/require"
)

func newLNCSessionsStoreWithDB(db *BaseDB) *LNCSessionsStore {
	dbTxer := NewTransactionExecutor(db,
		func(tx *sql.Tx) LNCSessionsDB {
			return db.WithTx(tx)
		},
	)

	return NewLNCSessionsStore(dbTxer)
}

func TestLNCSessionsDB(t *testing.T) {
	t.Parallel()

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// First, create a new test database.
	db := NewTestDB(t)
	store := newLNCSessionsStoreWithDB(db.BaseDB)

	words, passphraseEntropy, err := mailbox.NewPassphraseEntropy()
	require.NoError(t, err, "error creating passphrase")

	passphrase := strings.Join(words[:], " ")
	mailboxAddr := "test-mailbox"
	devServer := true

	session, err := lnc.NewSession(passphrase, mailboxAddr, devServer)
	require.NoError(t, err, "error creating session")

	// A session needs to have a local static key set to be stored in the
	// database.
	err = store.AddSession(ctxt, session)
	require.Error(t, err)

	localStatic, err := btcec.NewPrivateKey()
	require.NoError(t, err, "error creating local static key")
	session.LocalStaticPrivKey = localStatic

	// The db has a precision of microseconds, so we need to truncate the
	// timestamp so we are able to capture that it was created AFTER this
	// timestamp.
	timestampBeforeCreation := time.Now().UTC().Truncate(time.Millisecond)

	err = store.AddSession(ctxt, session)
	require.NoError(t, err, "error adding session")
	require.True(t, session.CreatedAt.After(timestampBeforeCreation))

	// Get the session from the database.
	dbSession, err := store.GetSession(ctxt, passphraseEntropy[:])
	require.NoError(t, err, "error getting session")
	require.Equal(t, session, dbSession, "sessions do not match")

	// Set the remote static key.
	remoteStatic := localStatic.PubKey()
	session.RemoteStaticPubKey = remoteStatic

	err = store.SetRemotePubKey(
		ctxt, passphraseEntropy[:], remoteStatic.SerializeCompressed(),
	)
	require.NoError(t, err, "error setting remote static key")

	// Set expiration date.
	expiry := session.CreatedAt.Add(time.Hour).Truncate(time.Millisecond)
	session.Expiry = &expiry

	err = store.SetExpiry(ctxt, passphraseEntropy[:], expiry)
	require.NoError(t, err, "error setting expiry")

	// Next time we fetch the session, it should have the remote static key
	// and the expiry set.
	dbSession, err = store.GetSession(ctxt, passphraseEntropy[:])
	require.NoError(t, err, "error getting session")
	require.Equal(t, session, dbSession, "sessions do not match")

	// Trying to get a session that does not exist should return a specific
	// error.
	_, err = store.GetSession(ctxt, []byte("non-existent"))
	require.ErrorIs(t, err, lnc.ErrSessionNotFound)
}
