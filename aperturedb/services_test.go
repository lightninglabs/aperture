package aperturedb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newServicesStoreWithDB(db *BaseDB) *ServicesStore {
	dbTxer := NewTransactionExecutor(db,
		func(tx *sql.Tx) ServicesDB {
			return db.WithTx(tx)
		},
	)

	return NewServicesStore(dbTxer)
}

func TestUpsertAndListServices(t *testing.T) {
	db := NewTestDB(t)
	store := newServicesStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// Initially no services.
	svcs, err := store.ListServices(ctxt)
	require.NoError(t, err)
	require.Len(t, svcs, 0)

	// Insert a service.
	err = store.UpsertService(
		ctxt, "svc-alpha", "localhost:8080", "http",
		".*", "/api/.*", "", 100,
	)
	require.NoError(t, err)

	svcs, err = store.ListServices(ctxt)
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	require.Equal(t, "svc-alpha", svcs[0].Name)
	require.Equal(t, "localhost:8080", svcs[0].Address)
	require.Equal(t, int64(100), svcs[0].Price)

	// Upsert (update) the same service.
	err = store.UpsertService(
		ctxt, "svc-alpha", "localhost:9090", "https",
		".*", "/api/.*", "on", 200,
	)
	require.NoError(t, err)

	svcs, err = store.ListServices(ctxt)
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	require.Equal(t, "localhost:9090", svcs[0].Address)
	require.Equal(t, "https", svcs[0].Protocol)
	require.Equal(t, int64(200), svcs[0].Price)
	require.Equal(t, "on", svcs[0].Auth)
}

func TestDeleteService(t *testing.T) {
	db := NewTestDB(t)
	store := newServicesStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// Insert two services.
	err := store.UpsertService(
		ctxt, "svc-a", "localhost:1111", "http",
		".*", "", "", 10,
	)
	require.NoError(t, err)
	err = store.UpsertService(
		ctxt, "svc-b", "localhost:2222", "http",
		".*", "", "", 20,
	)
	require.NoError(t, err)

	svcs, err := store.ListServices(ctxt)
	require.NoError(t, err)
	require.Len(t, svcs, 2)

	// Delete one.
	err = store.DeleteService(ctxt, "svc-a")
	require.NoError(t, err)

	svcs, err = store.ListServices(ctxt)
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	require.Equal(t, "svc-b", svcs[0].Name)
}

func TestListFilteredTransactions(t *testing.T) {
	db := NewTestDB(t)
	store := newL402TransactionsStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// Create transactions for two services in different states.
	tokenA := []byte("token_filter_a__________________")
	hashA := []byte("hash_filter_a___________________")
	err := store.RecordTransaction(
		ctxt, tokenA, hashA, "alpha", 100, nil,
	)
	require.NoError(t, err)
	require.NoError(t, store.SettleTransaction(ctxt, hashA))

	tokenB := []byte("token_filter_b__________________")
	hashB := []byte("hash_filter_b___________________")
	err = store.RecordTransaction(
		ctxt, tokenB, hashB, "beta", 200, nil,
	)
	require.NoError(t, err)

	tokenC := []byte("token_filter_c__________________")
	hashC := []byte("hash_filter_c___________________")
	err = store.RecordTransaction(
		ctxt, tokenC, hashC, "alpha", 300, nil,
	)
	require.NoError(t, err)
	require.NoError(t, store.SettleTransaction(ctxt, hashC))

	// No filters -- returns all 3.
	txns, err := store.ListFiltered(
		ctxt, "", "", false,
		time.Time{}, time.Time{}, 50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 3)

	// Filter by service only.
	txns, err = store.ListFiltered(
		ctxt, "alpha", "", false,
		time.Time{}, time.Time{}, 50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 2)

	// Filter by state only.
	txns, err = store.ListFiltered(
		ctxt, "", "settled", false,
		time.Time{}, time.Time{}, 50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 2)

	txns, err = store.ListFiltered(
		ctxt, "", "pending", false,
		time.Time{}, time.Time{}, 50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 1)

	// Combined filter: service + state.
	txns, err = store.ListFiltered(
		ctxt, "alpha", "settled", false,
		time.Time{}, time.Time{}, 50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 2)

	txns, err = store.ListFiltered(
		ctxt, "beta", "settled", false,
		time.Time{}, time.Time{}, 50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 0)

	// Count filtered.
	count, err := store.CountFiltered(
		ctxt, "alpha", "", false,
		time.Time{}, time.Time{},
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	count, err = store.CountFiltered(
		ctxt, "", "settled", false,
		time.Time{}, time.Time{},
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	// Date range filter.
	now := time.Now().UTC()
	txns, err = store.ListFiltered(
		ctxt, "", "settled", true,
		now.Add(-time.Hour), now.Add(time.Hour), 50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 2)

	// Future date range -- no results.
	txns, err = store.ListFiltered(
		ctxt, "", "", true,
		now.Add(24*time.Hour), now.Add(25*time.Hour), 50, 0,
	)
	require.NoError(t, err)
	require.Len(t, txns, 0)
}
