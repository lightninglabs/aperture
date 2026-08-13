package aperturedb

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

// newMPPChargesStoreWithDB wraps a raw database handle in the charges store.
func newMPPChargesStoreWithDB(db *BaseDB) *MPPChargesStore {
	dbTxer := NewTransactionExecutor(db,
		func(tx *sql.Tx) MPPChargesDB {
			return db.WithTx(tx)
		},
	)

	return NewMPPChargesStore(dbTxer)
}

// newMPPChargesStore returns a charges store over a fresh test database.
func newMPPChargesStore(t *testing.T) *MPPChargesStore {
	return newMPPChargesStoreWithDB(NewTestDB(t).BaseDB)
}

// liveExpiry returns an expiry a challenge issued now would carry.
func liveExpiry() time.Time {
	return time.Now().Add(15 * time.Minute).UTC()
}

// TestMPPChargeIsSpentOnce asserts the property the table exists for: a payment
// hash buys one request. A settled invoice stays settled, so the credential
// naming it looks exactly the same on its thousandth presentation as on its
// first, and only the record of what has been spent can tell them apart.
func TestMPPChargeIsSpentOnce(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPChargesStore(t)

	hash := freshHash(t)

	consumed, err := store.ConsumeCharge(
		ctx, hash, "challenge-1", liveExpiry(),
	)
	require.NoError(t, err)
	require.True(t, consumed)

	// Every later claim on the same payment loses, however it is dressed
	// up. A replay that echoes a different challenge ID is still the same
	// payment being spent twice.
	for i := 0; i < 10; i++ {
		consumed, err = store.ConsumeCharge(
			ctx, hash, "challenge-1", liveExpiry(),
		)
		require.NoError(t, err)
		require.False(t, consumed)
	}

	consumed, err = store.ConsumeCharge(
		ctx, hash, "some-other-challenge", liveExpiry(),
	)
	require.NoError(t, err)
	require.False(t, consumed)

	spent, err := store.IsChargeConsumed(ctx, hash)
	require.NoError(t, err)
	require.True(t, spent)

	// A different payment still buys a request, so the guard refuses
	// replays rather than refusing service.
	other := freshHash(t)
	consumed, err = store.ConsumeCharge(
		ctx, other, "challenge-2", liveExpiry(),
	)
	require.NoError(t, err)
	require.True(t, consumed)
}

// TestMPPChargeUnspentIsNotConsumed asserts that a payment nobody has spent
// reads as unspent, so the guard cannot refuse a first presentation.
func TestMPPChargeUnspentIsNotConsumed(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPChargesStore(t)

	spent, err := store.IsChargeConsumed(ctx, freshHash(t))
	require.NoError(t, err)
	require.False(t, spent)
}

// TestMPPChargeConcurrentClaims asserts the once-only property holds under a
// genuine race. This is the case a check-then-act implementation loses: every
// claimant reads the payment as unspent, every one of them decides it is the
// first, and one payment buys as many requests as there are threads. Only a
// claim made in a single statement against a unique index lets exactly one
// through.
func TestMPPChargeConcurrentClaims(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPChargesStore(t)

	const claims = 32

	hash := freshHash(t)

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		won int

		// start holds every goroutine at the line until they can be
		// released together, so the claims genuinely overlap rather
		// than trickling through one after another.
		start = make(chan struct{})
	)

	for i := 0; i < claims; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start

			consumed, err := store.ConsumeCharge(
				ctx, hash, "challenge-1", liveExpiry(),
			)
			require.NoError(t, err)

			mu.Lock()
			defer mu.Unlock()

			if consumed {
				won++
			}
		}()
	}

	close(start)
	wg.Wait()

	require.Equal(t, 1, won)
}

// TestMPPChargeConcurrentDistinctClaims asserts that the once-only guard is not
// a lock on the whole table: separately paid requests arriving at the same
// moment all succeed.
func TestMPPChargeConcurrentDistinctClaims(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPChargesStore(t)

	const payments = 16

	hashes := make([]lntypes.Hash, payments)
	for i := range hashes {
		hashes[i] = freshHash(t)
	}

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		won int

		start = make(chan struct{})
	)

	for i := 0; i < payments; i++ {
		wg.Add(1)
		go func(hash lntypes.Hash) {
			defer wg.Done()

			<-start

			consumed, err := store.ConsumeCharge(
				ctx, hash, "challenge", liveExpiry(),
			)
			require.NoError(t, err)

			mu.Lock()
			defer mu.Unlock()

			if consumed {
				won++
			}
		}(hashes[i])
	}

	close(start)
	wg.Wait()

	require.Equal(t, payments, won)
}

// TestMPPChargeSurvivesRestart asserts that a proxy restart does not hand the
// buyer a second request for the same payment. A proxy restarts for reasons the
// buyer can often bring about, so a guard held only in memory is a guard with
// an obvious way around it.
func TestMPPChargeSurvivesRestart(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	dbFile := filepath.Join(t.TempDir(), "charges.db")

	first, err := NewSqliteStore(&SqliteConfig{DatabaseFileName: dbFile})
	require.NoError(t, err)

	store := newMPPChargesStoreWithDB(first.BaseDB)

	hash := freshHash(t)
	expiresAt := liveExpiry()

	consumed, err := store.ConsumeCharge(
		ctx, hash, "challenge-1", expiresAt,
	)
	require.NoError(t, err)
	require.True(t, consumed)

	// The proxy goes away.
	require.NoError(t, first.Close())

	// And comes back against the same file.
	second, err := NewSqliteStore(&SqliteConfig{DatabaseFileName: dbFile})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, second.Close())
	})

	restarted := newMPPChargesStoreWithDB(second.BaseDB)

	spent, err := restarted.IsChargeConsumed(ctx, hash)
	require.NoError(t, err)
	require.True(t, spent)

	consumed, err = restarted.ConsumeCharge(
		ctx, hash, "challenge-1", expiresAt,
	)
	require.NoError(t, err)
	require.False(t, consumed)

	// The restarted proxy still serves payments it has not seen, so the
	// record is a memory of what was spent rather than a wall.
	consumed, err = restarted.ConsumeCharge(
		ctx, freshHash(t), "challenge-2", expiresAt,
	)
	require.NoError(t, err)
	require.True(t, consumed)
}

// TestMPPChargePruneDropsOnlyExpired asserts that the sweep removes exactly the
// records that can no longer change an answer. A record is needed for as long
// as the challenge it records could still be presented, so dropping one whose
// challenge is live would hand back the replay the record was written to
// prevent.
func TestMPPChargePruneDropsOnlyExpired(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPChargesStore(t)

	now := time.Now().UTC()

	// A record for a challenge that expired a day ago, one that expired a
	// moment ago, and one still live.
	var (
		longExpired = freshHash(t)
		justExpired = freshHash(t)
		live        = freshHash(t)
	)

	for _, c := range []struct {
		hash      lntypes.Hash
		expiresAt time.Time
	}{
		{longExpired, now.Add(-24 * time.Hour)},
		{justExpired, now.Add(-time.Second)},
		{live, now.Add(15 * time.Minute)},
	} {
		consumed, err := store.ConsumeCharge(
			ctx, c.hash, "challenge", c.expiresAt,
		)
		require.NoError(t, err)
		require.True(t, consumed)
	}

	// Sweep with an hour of margin behind us, as the authenticator does.
	pruned, err := store.PruneConsumedCharges(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), pruned)

	// Only the record that has been dead for a day is gone. The one that
	// expired a moment ago is still inside the margin, and the live one is
	// still doing its job.
	for _, c := range []struct {
		hash lntypes.Hash
		want bool
	}{
		{longExpired, false},
		{justExpired, true},
		{live, true},
	} {
		spent, err := store.IsChargeConsumed(ctx, c.hash)
		require.NoError(t, err)
		require.Equal(t, c.want, spent)
	}

	// The surviving records still refuse a replay.
	consumed, err := store.ConsumeCharge(
		ctx, live, "challenge", now.Add(15*time.Minute),
	)
	require.NoError(t, err)
	require.False(t, consumed)

	// A sweep that finds nothing to do says so rather than failing.
	pruned, err = store.PruneConsumedCharges(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(0), pruned)
}

// TestMPPChargePruneLeavesSessionCreditsAlone asserts that sweeping charge
// records cannot reopen the session replay. The two live in separate tables
// precisely because they are kept for different lengths of time: a charge
// record dies with its challenge, while a session credit is part of the
// session's books. Pruning one out of a table shared with the other would let a
// spent top-up be presented again.
func TestMPPChargePruneLeavesSessionCreditsAlone(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	db := NewTestDB(t).BaseDB

	charges := newMPPChargesStoreWithDB(db)
	sessions := newMPPSessionsStoreWithDB(db)

	session := openSession(t, sessions, ctx, 0x40, 1000)
	topUp := creditTopUp(t, sessions, ctx, session.SessionID, 500)

	// A charge record old enough to be swept away.
	charge := freshHash(t)
	consumed, err := charges.ConsumeCharge(
		ctx, charge, "challenge", time.Now().Add(-24*time.Hour).UTC(),
	)
	require.NoError(t, err)
	require.True(t, consumed)

	pruned, err := charges.PruneConsumedCharges(
		ctx, time.Now().Add(-time.Hour).UTC(),
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), pruned)

	// The session's own records are untouched, so replaying the top-up
	// still adds nothing, and so does replaying the deposit that opened the
	// session.
	outcome, err := sessions.CreditSession(
		ctx, session.SessionID, topUp, 500,
	)
	require.NoError(t, err)
	require.Equal(t, auth.CreditReplayed, outcome)

	outcome, err = sessions.CreditSession(
		ctx, session.SessionID, session.PaymentHash, 1000,
	)
	require.NoError(t, err)
	require.Equal(t, auth.CreditReplayed, outcome)

	got, err := sessions.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1500), got.DepositSats)
}
