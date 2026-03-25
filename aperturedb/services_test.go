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
	err = store.UpsertService(ctxt, ServiceParams{
		Name:       "svc-alpha",
		Address:    "localhost:8080",
		Protocol:   "http",
		HostRegexp: ".*",
		PathRegexp: "/api/.*",
		Auth:       "",
		AuthScheme: "l402",
		Price:      100,
	})
	require.NoError(t, err)

	svcs, err = store.ListServices(ctxt)
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	require.Equal(t, "svc-alpha", svcs[0].Name)
	require.Equal(t, "localhost:8080", svcs[0].Address)
	require.Equal(t, int64(100), svcs[0].Price)
	require.Equal(t, "l402", svcs[0].AuthScheme)

	// Upsert (update) the same service with a different scheme.
	err = store.UpsertService(ctxt, ServiceParams{
		Name:       "svc-alpha",
		Address:    "localhost:9090",
		Protocol:   "https",
		HostRegexp: ".*",
		PathRegexp: "/api/.*",
		Auth:       "on",
		AuthScheme: "mpp",
		Price:      200,
	})
	require.NoError(t, err)

	svcs, err = store.ListServices(ctxt)
	require.NoError(t, err)
	require.Len(t, svcs, 1)
	require.Equal(t, "localhost:9090", svcs[0].Address)
	require.Equal(t, "https", svcs[0].Protocol)
	require.Equal(t, int64(200), svcs[0].Price)
	require.Equal(t, "on", svcs[0].Auth)
	require.Equal(t, "mpp", svcs[0].AuthScheme)
}

func TestDeleteService(t *testing.T) {
	db := NewTestDB(t)
	store := newServicesStoreWithDB(db.BaseDB)

	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	defer cancel()

	// Insert two services.
	err := store.UpsertService(ctxt, ServiceParams{
		Name:       "svc-a",
		Address:    "localhost:1111",
		Protocol:   "http",
		HostRegexp: ".*",
		AuthScheme: "l402",
		Price:      10,
	})
	require.NoError(t, err)
	err = store.UpsertService(ctxt, ServiceParams{
		Name:       "svc-b",
		Address:    "localhost:2222",
		Protocol:   "http",
		HostRegexp: ".*",
		AuthScheme: "l402+mpp",
		Price:      20,
	})
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
	require.Equal(t, "l402+mpp", svcs[0].AuthScheme)
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
	txns, err := store.ListFiltered(ctxt, TransactionFilter{
		Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, txns, 3)

	// Filter by service only.
	txns, err = store.ListFiltered(ctxt, TransactionFilter{
		Service: "alpha", Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, txns, 2)

	// Filter by state only.
	txns, err = store.ListFiltered(ctxt, TransactionFilter{
		State: "settled", Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, txns, 2)

	txns, err = store.ListFiltered(ctxt, TransactionFilter{
		State: "pending", Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, txns, 1)

	// Combined filter: service + state.
	txns, err = store.ListFiltered(ctxt, TransactionFilter{
		Service: "alpha", State: "settled", Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, txns, 2)

	txns, err = store.ListFiltered(ctxt, TransactionFilter{
		Service: "beta", State: "settled", Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, txns, 0)

	// Count filtered.
	count, err := store.CountFiltered(ctxt, TransactionFilter{
		Service: "alpha",
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	count, err = store.CountFiltered(ctxt, TransactionFilter{
		State: "settled",
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), count)

	// Date range filter.
	now := time.Now().UTC()
	txns, err = store.ListFiltered(ctxt, TransactionFilter{
		State: "settled", HasDateRange: true,
		From: now.Add(-time.Hour), To: now.Add(time.Hour),
		Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, txns, 2)

	// Future date range -- no results.
	txns, err = store.ListFiltered(ctxt, TransactionFilter{
		HasDateRange: true,
		From: now.Add(24 * time.Hour), To: now.Add(25 * time.Hour),
		Limit: 50,
	})
	require.NoError(t, err)
	require.Len(t, txns, 0)
}
