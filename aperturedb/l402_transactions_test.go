package aperturedb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/clock"
	"github.com/stretchr/testify/require"
)

func newL402TransactionsStoreWithDB(db *BaseDB) *L402TransactionsStore {
	dbTxer := NewTransactionExecutor(db,
		func(tx *sql.Tx) L402TransactionsDB {
			return db.WithTx(tx)
		},
	)

	return NewL402TransactionsStore(dbTxer)
}

func TestRecordAndListTransactions(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// Record two transactions.
	tokenID1 := []byte("token1__________________________")
	paymentHash1 := []byte("hash1___________________________")
	idHash1 := sha256.Sum256([]byte("id1"))

	err := store.RecordTransaction(
		ctxt, tokenID1, paymentHash1, "service_a", 1000,
		idHash1[:],
	)
	require.NoError(t, err)

	tokenID2 := []byte("token2__________________________")
	paymentHash2 := []byte("hash2___________________________")
	idHash2 := sha256.Sum256([]byte("id2"))

	err = store.RecordTransaction(
		ctxt, tokenID2, paymentHash2, "service_b", 2000,
		idHash2[:],
	)
	require.NoError(t, err)

	// List all transactions.
	txns, err := store.ListTransactions(ctxt, 50, 0)
	require.NoError(t, err)
	require.Len(t, txns, 2)

	// Most recent first.
	require.Equal(t, "service_b", txns[0].ServiceName)
	require.Equal(t, "service_a", txns[1].ServiceName)
	require.Equal(t, "pending", txns[0].State)
}

func TestSettleTransaction(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	tokenID := []byte("token_settle____________________")
	paymentHash := []byte("hash_settle_____________________")

	err := store.RecordTransaction(
		ctxt, tokenID, paymentHash, "svc", 500, nil,
	)
	require.NoError(t, err)

	// Settle the transaction.
	err = store.SettleTransaction(ctxt, paymentHash)
	require.NoError(t, err)

	// Verify it's settled.
	txns, err := store.ListByState(ctxt, "settled", 50, 0)
	require.NoError(t, err)
	require.Len(t, txns, 1)
	require.Equal(t, "settled", txns[0].State)
	require.True(t, txns[0].SettledAt.Valid)
}

func TestListByService(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// Create transactions for two services.
	for i := 0; i < 3; i++ {
		tokenID := make([]byte, 32)
		tokenID[0] = byte(i)
		hash := make([]byte, 32)
		hash[0] = byte(i)

		err := store.RecordTransaction(
			ctxt, tokenID, hash, "alpha", 100, nil,
		)
		require.NoError(t, err)
	}

	betaTokenID := make([]byte, 32)
	betaTokenID[0] = byte(99)
	betaHash := make([]byte, 32)
	betaHash[0] = byte(99)
	err := store.RecordTransaction(
		ctxt, betaTokenID, betaHash, "beta", 200, nil,
	)
	require.NoError(t, err)

	txns, err := store.ListByService(ctxt, "alpha", 50, 0)
	require.NoError(t, err)
	require.Len(t, txns, 3)

	txns, err = store.ListByService(ctxt, "beta", 50, 0)
	require.NoError(t, err)
	require.Len(t, txns, 1)
}

func TestListByState(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	tokenID := []byte("token_state_____________________")
	paymentHash := []byte("hash_state______________________")

	err := store.RecordTransaction(
		ctxt, tokenID, paymentHash, "svc", 100, nil,
	)
	require.NoError(t, err)

	// Should be one pending, zero settled.
	pending, err := store.ListByState(ctxt, "pending", 50, 0)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	settled, err := store.ListByState(ctxt, "settled", 50, 0)
	require.NoError(t, err)
	require.Len(t, settled, 0)

	// Settle and recheck.
	err = store.SettleTransaction(ctxt, paymentHash)
	require.NoError(t, err)

	pending, err = store.ListByState(ctxt, "pending", 50, 0)
	require.NoError(t, err)
	require.Len(t, pending, 0)

	settled, err = store.ListByState(ctxt, "settled", 50, 0)
	require.NoError(t, err)
	require.Len(t, settled, 1)
}

func TestListByDateRange(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	tokenID := []byte("token_date______________________")
	paymentHash := []byte("hash_date_______________________")

	err := store.RecordTransaction(
		ctxt, tokenID, paymentHash, "svc", 100, nil,
	)
	require.NoError(t, err)

	// Settle the transaction so it has a settled_at timestamp for
	// date range filtering.
	err = store.SettleTransaction(ctxt, paymentHash)
	require.NoError(t, err)

	now := time.Now().UTC()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	txns, err := store.ListByDateRange(ctxt, from, to, 50, 0)
	require.NoError(t, err)
	require.Len(t, txns, 1)

	// Date range in the past should return nothing.
	txns, err = store.ListByDateRange(
		ctxt,
		now.Add(-48*time.Hour),
		now.Add(-24*time.Hour),
		50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 0)
}

func TestGetRevenueStats(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// Create and settle transactions for two services.
	tokenID1 := []byte("token_rev1______________________")
	hash1 := []byte("hash_rev1_______________________")
	err := store.RecordTransaction(
		ctxt, tokenID1, hash1, "svc_a", 500, nil,
	)
	require.NoError(t, err)
	err = store.SettleTransaction(ctxt, hash1)
	require.NoError(t, err)

	tokenID2 := []byte("token_rev2______________________")
	hash2 := []byte("hash_rev2_______________________")
	err = store.RecordTransaction(
		ctxt, tokenID2, hash2, "svc_b", 300, nil,
	)
	require.NoError(t, err)
	err = store.SettleTransaction(ctxt, hash2)
	require.NoError(t, err)

	rows, err := store.GetRevenueStats(ctxt, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestGetTotalRevenue(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// No transactions yet, revenue should be 0.
	total, err := store.GetTotalRevenue(ctxt)
	require.NoError(t, err)
	require.Equal(t, int64(0), total)

	// Create and settle a transaction.
	tokenID := []byte("token_total_____________________")
	hash := []byte("hash_total______________________")
	err = store.RecordTransaction(
		ctxt, tokenID, hash, "svc", 750, nil,
	)
	require.NoError(t, err)
	err = store.SettleTransaction(ctxt, hash)
	require.NoError(t, err)

	total, err = store.GetTotalRevenue(ctxt)
	require.NoError(t, err)
	require.Equal(t, int64(750), total)
}

func TestDeleteByTokenID(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	tokenID := []byte("token_del_______________________")
	hash := []byte("hash_del________________________")

	err := store.RecordTransaction(
		ctxt, tokenID, hash, "svc", 100, nil,
	)
	require.NoError(t, err)

	// Verify it exists.
	txns, err := store.ListTransactions(ctxt, 50, 0)
	require.NoError(t, err)
	require.Len(t, txns, 1)

	// Delete it.
	err = store.DeleteByTokenID(ctxt, tokenID)
	require.NoError(t, err)

	// Should be gone.
	txns, err = store.ListTransactions(ctxt, 50, 0)
	require.NoError(t, err)
	require.Len(t, txns, 0)
}

func TestRecordTransactionLargePriceSats(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	largePrice := int64(math.MaxInt32) + 12345
	tokenID := []byte("token_large_price______________")
	hash := []byte("hash_large_price_______________")

	err := store.RecordTransaction(
		ctxt, tokenID, hash, "svc", largePrice, nil,
	)
	require.NoError(t, err)

	txns, err := store.ListTransactions(ctxt, 50, 0)
	require.NoError(t, err)
	require.Len(t, txns, 1)
	require.Equal(t, largePrice, txns[0].PriceSats)
}

func TestGetSettledByTokenID(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	tokenID := []byte("token_get_by_id_______________")
	hash := []byte("hash_get_by_id________________")

	err := store.RecordTransaction(
		ctxt, tokenID, hash, "svc", 100, nil,
	)
	require.NoError(t, err)
	require.NoError(t, store.SettleTransaction(ctxt, hash))

	txn, err := store.GetSettledByTokenID(ctxt, tokenID)
	require.NoError(t, err)
	require.Equal(t, int64(100), txn.PriceSats)
	require.Equal(t, "settled", txn.State)
}

func TestDateRangeAggregates(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	tokenID := []byte("token_range___________________")
	hash := []byte("hash_range____________________")

	err := store.RecordTransaction(
		ctxt, tokenID, hash, "svc", 750, nil,
	)
	require.NoError(t, err)
	require.NoError(t, store.SettleTransaction(ctxt, hash))

	pastFrom := time.Now().UTC().Add(-time.Hour)
	pastTo := time.Now().UTC().Add(time.Hour)

	total, err := store.GetTotalRevenueByDateRange(ctxt, pastFrom, pastTo)
	require.NoError(t, err)
	require.Equal(t, int64(750), total)

	count, err := store.CountTransactionsByDateRange(ctxt, pastFrom, pastTo)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	futureFrom := time.Now().UTC().Add(24 * time.Hour)
	futureTo := futureFrom.Add(time.Hour)

	total, err = store.GetTotalRevenueByDateRange(
		ctxt, futureFrom, futureTo,
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), total)

	count, err = store.CountTransactionsByDateRange(
		ctxt, futureFrom, futureTo,
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), count)
}

func TestDateRangeAggregatesUseSettledAt(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	createdAt := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	settledAt := createdAt.Add(48 * time.Hour)
	testClock := clock.NewTestClock(createdAt)
	store.clock = testClock

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	tokenID := []byte("token_settle_range____________")
	hash := []byte("hash_settle_range_____________")

	err := store.RecordTransaction(
		ctxt, tokenID, hash, "svc", 900, nil,
	)
	require.NoError(t, err)

	testClock.SetTime(settledAt)
	require.NoError(t, store.SettleTransaction(ctxt, hash))

	createdWindowFrom := createdAt.Add(-time.Hour)
	createdWindowTo := createdAt.Add(time.Hour)

	total, err := store.GetTotalRevenueByDateRange(
		ctxt, createdWindowFrom, createdWindowTo,
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), total)

	count, err := store.CountTransactionsByDateRange(
		ctxt, createdWindowFrom, createdWindowTo,
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), count)

	settledWindowFrom := settledAt.Add(-time.Hour)
	settledWindowTo := settledAt.Add(time.Hour)

	total, err = store.GetTotalRevenueByDateRange(
		ctxt, settledWindowFrom, settledWindowTo,
	)
	require.NoError(t, err)
	require.Equal(t, int64(900), total)

	count, err = store.CountTransactionsByDateRange(
		ctxt, settledWindowFrom, settledWindowTo,
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
}
