package aperturedb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lightninglabs/aperture/aperturedb/sqlc"
	"github.com/lightningnetwork/lnd/clock"
)

type (
	// NewL402Transaction is a struct that contains the parameters required
	// to insert a new L402 transaction into the database.
	NewL402Transaction = sqlc.InsertL402TransactionParams

	// UpdateL402TxState is a struct that contains the parameters required
	// to update an L402 transaction's state.
	UpdateL402TxState = sqlc.UpdateL402TransactionStateParams

	// ListL402TxParams contains pagination parameters for listing
	// transactions.
	ListL402TxParams = sqlc.ListL402TransactionsParams

	// ListL402TxByServiceParams contains parameters for listing
	// transactions by service.
	ListL402TxByServiceParams = sqlc.ListL402TransactionsByServiceParams

	// ListL402TxByStateParams contains parameters for listing
	// transactions by state.
	ListL402TxByStateParams = sqlc.ListL402TransactionsByStateParams

	// ListL402TxByDateRangeParams contains parameters for listing
	// transactions by date range.
	ListL402TxByDateRangeParams = sqlc.ListL402TransactionsByDateRangeParams

	// RevenueByServiceAndDateRangeParams contains parameters for querying
	// revenue by service within a date range.
	RevenueByServiceAndDateRangeParams = sqlc.GetL402RevenueByServiceAndDateRangeParams

	// CountByDateRangeParams contains parameters for counting settled
	// transactions within a settlement-time date range.
	CountByDateRangeParams = sqlc.CountL402TransactionsByDateRangeParams

	// TotalRevenueByDateRangeParams contains parameters for querying total
	// settled revenue within a date range.
	TotalRevenueByDateRangeParams = sqlc.GetL402TotalRevenueByDateRangeParams

	// RevenueByServiceRow contains a service name and its total revenue.
	RevenueByServiceRow = sqlc.GetL402RevenueByServiceRow

	// RevenueByServiceAndDateRangeRow contains a service name and its
	// total revenue within a date range.
	RevenueByServiceAndDateRangeRow = sqlc.GetL402RevenueByServiceAndDateRangeRow

	// L402Transaction is the database model for an L402 transaction.
	L402Transaction = sqlc.L402Transaction
)

// L402TransactionsDB is an interface that defines the set of operations that
// can be executed against the L402 transactions database.
type L402TransactionsDB interface {
	// InsertL402Transaction inserts a new L402 transaction into the
	// database.
	InsertL402Transaction(ctx context.Context,
		arg NewL402Transaction) (int32, error)

	// UpdateL402TransactionState updates the state and settled_at
	// timestamp of a transaction identified by its payment hash.
	UpdateL402TransactionState(ctx context.Context,
		arg UpdateL402TxState) error

	// GetL402TransactionsByPaymentHash returns transactions matching the
	// given payment hash.
	GetL402TransactionsByPaymentHash(ctx context.Context,
		paymentHash []byte) ([]L402Transaction, error)

	// GetL402TransactionByIdentifierHash returns the transaction matching
	// the given identifier hash.
	GetL402TransactionByIdentifierHash(ctx context.Context,
		identifierHash []byte) (L402Transaction, error)

	// GetL402SettledTransactionByTokenID returns the settled transaction
	// matching the given token ID.
	GetL402SettledTransactionByTokenID(ctx context.Context,
		tokenID []byte) (L402Transaction, error)

	// ListL402Transactions returns a paginated list of all transactions,
	// ordered by created_at DESC.
	ListL402Transactions(ctx context.Context,
		arg ListL402TxParams) ([]L402Transaction, error)

	// ListL402TransactionsByService returns a paginated list of
	// transactions filtered by service name.
	ListL402TransactionsByService(ctx context.Context,
		arg ListL402TxByServiceParams) ([]L402Transaction, error)

	// ListL402TransactionsByState returns a paginated list of
	// transactions filtered by state.
	ListL402TransactionsByState(ctx context.Context,
		arg ListL402TxByStateParams) ([]L402Transaction, error)

	// ListL402TransactionsByDateRange returns a paginated list of
	// transactions within a date range.
	ListL402TransactionsByDateRange(ctx context.Context,
		arg ListL402TxByDateRangeParams) ([]L402Transaction, error)

	// CountL402Transactions returns the total number of settled
	// transactions.
	CountL402Transactions(ctx context.Context) (int64, error)

	// CountL402TransactionsByService returns the number of transactions
	// for a given service.
	CountL402TransactionsByService(ctx context.Context,
		serviceName string) (int64, error)

	// CountL402TransactionsByDateRange returns the total number of
	// settled transactions within a settlement-time date range.
	CountL402TransactionsByDateRange(ctx context.Context,
		arg CountByDateRangeParams) (int64, error)

	// GetL402RevenueByService returns the total settled revenue grouped
	// by service name.
	GetL402RevenueByService(ctx context.Context) (
		[]RevenueByServiceRow, error)

	// GetL402RevenueByServiceAndDateRange returns the total settled
	// revenue grouped by service name within a date range.
	GetL402RevenueByServiceAndDateRange(ctx context.Context,
		arg RevenueByServiceAndDateRangeParams) (
		[]RevenueByServiceAndDateRangeRow, error)

	// GetL402TotalRevenue returns the total settled revenue across all
	// services.
	GetL402TotalRevenue(ctx context.Context) (int64, error)

	// GetL402TotalRevenueByDateRange returns the total settled revenue
	// across all services within a date range.
	GetL402TotalRevenueByDateRange(ctx context.Context,
		arg TotalRevenueByDateRangeParams) (int64, error)

	// DeleteL402TransactionByTokenID deletes a transaction by its
	// token ID.
	DeleteL402TransactionByTokenID(ctx context.Context,
		tokenID []byte) (int64, error)
}

// L402TransactionsDBTxOptions defines the set of db txn options the
// L402TransactionsStore understands.
type L402TransactionsDBTxOptions struct {
	// readOnly governs if a read only transaction is needed or not.
	readOnly bool
}

// ReadOnly returns true if the transaction should be read only.
//
// NOTE: This implements the TxOptions interface.
func (a *L402TransactionsDBTxOptions) ReadOnly() bool {
	return a.readOnly
}

// NewL402TransactionsDBReadTx creates a new read transaction option set.
func NewL402TransactionsDBReadTx() L402TransactionsDBTxOptions {
	return L402TransactionsDBTxOptions{
		readOnly: true,
	}
}

// BatchedL402TransactionsDB is a version of the L402TransactionsDB that's
// capable of batched database operations.
type BatchedL402TransactionsDB interface {
	L402TransactionsDB

	BatchedTx[L402TransactionsDB]
}

// L402TransactionsStore represents a storage backend for L402 transactions.
type L402TransactionsStore struct {
	db    BatchedL402TransactionsDB
	clock clock.Clock
}

// NewL402TransactionsStore creates a new L402TransactionsStore instance given
// an open BatchedL402TransactionsDB storage backend.
func NewL402TransactionsStore(
	db BatchedL402TransactionsDB) *L402TransactionsStore {

	return &L402TransactionsStore{
		db:    db,
		clock: clock.NewDefaultClock(),
	}
}

// RecordTransaction records a new pending L402 transaction.
func (s *L402TransactionsStore) RecordTransaction(ctx context.Context,
	tokenID []byte, paymentHash []byte, serviceName string,
	priceSats int64, identifierHash []byte) error {

	var writeTxOpts L402TransactionsDBTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx L402TransactionsDB) error {
		_, err := tx.InsertL402Transaction(ctx, NewL402Transaction{
			TokenID:        tokenID,
			PaymentHash:    paymentHash,
			ServiceName:    serviceName,
			PriceSats:      priceSats,
			State:          "pending",
			CreatedAt:      s.clock.Now().UTC(),
			IdentifierHash: identifierHash,
		})
		return err
	})

	if err != nil {
		return fmt.Errorf("unable to record L402 transaction: %w", err)
	}

	return nil
}

// SettleTransaction marks all transactions with the given payment hash as
// settled.
func (s *L402TransactionsStore) SettleTransaction(ctx context.Context,
	paymentHash []byte) error {

	var writeTxOpts L402TransactionsDBTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx L402TransactionsDB) error {
		return tx.UpdateL402TransactionState(ctx, UpdateL402TxState{
			State: "settled",
			SettledAt: sql.NullTime{
				Time:  s.clock.Now().UTC(),
				Valid: true,
			},
			PaymentHash: paymentHash,
		})
	})

	if err != nil {
		return fmt.Errorf("unable to settle L402 transaction "+
			"(hash=%x): %w", paymentHash, err)
	}

	return nil
}

// ListTransactions returns a paginated list of all transactions.
func (s *L402TransactionsStore) ListTransactions(ctx context.Context,
	limit, offset int32) ([]L402Transaction, error) {

	var txns []L402Transaction
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		txns, err = tx.ListL402Transactions(ctx, ListL402TxParams{
			Limit:  limit,
			Offset: offset,
		})
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("unable to list L402 transactions: %w",
			err)
	}

	return txns, nil
}

// ListByService returns a paginated list of transactions for a given service.
func (s *L402TransactionsStore) ListByService(ctx context.Context,
	serviceName string, limit, offset int32) ([]L402Transaction, error) {

	var txns []L402Transaction
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		txns, err = tx.ListL402TransactionsByService(
			ctx, ListL402TxByServiceParams{
				ServiceName: serviceName,
				Limit:       limit,
				Offset:      offset,
			},
		)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("unable to list L402 transactions "+
			"by service(%s): %w", serviceName, err)
	}

	return txns, nil
}

// ListByState returns a paginated list of transactions with the given state.
func (s *L402TransactionsStore) ListByState(ctx context.Context,
	state string, limit, offset int32) ([]L402Transaction, error) {

	var txns []L402Transaction
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		txns, err = tx.ListL402TransactionsByState(
			ctx, ListL402TxByStateParams{
				State:  state,
				Limit:  limit,
				Offset: offset,
			},
		)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("unable to list L402 transactions "+
			"by state(%s): %w", state, err)
	}

	return txns, nil
}

// ListByDateRange returns a paginated list of transactions within a date
// range.
func (s *L402TransactionsStore) ListByDateRange(ctx context.Context,
	from, to time.Time, limit, offset int32) ([]L402Transaction, error) {

	var txns []L402Transaction
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		txns, err = tx.ListL402TransactionsByDateRange(
			ctx, ListL402TxByDateRangeParams{
				CreatedAt:   from,
				CreatedAt_2: to,
				Limit:       limit,
				Offset:      offset,
			},
		)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("unable to list L402 transactions "+
			"by date range: %w", err)
	}

	return txns, nil
}

// GetRevenueStats returns settled revenue grouped by service, optionally
// filtered by date range. If from is zero-valued, all settled revenue is
// returned.
func (s *L402TransactionsStore) GetRevenueStats(ctx context.Context,
	from, to time.Time) ([]RevenueByServiceRow, error) {

	var rows []RevenueByServiceRow
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		if from.IsZero() {
			var err error
			rows, err = tx.GetL402RevenueByService(ctx)
			return err
		}

		dateRows, err := tx.GetL402RevenueByServiceAndDateRange(
			ctx, RevenueByServiceAndDateRangeParams{
				SettledAt: sql.NullTime{
					Time:  from,
					Valid: true,
				},
				SettledAt_2: sql.NullTime{
					Time:  to,
					Valid: true,
				},
			},
		)
		if err != nil {
			return err
		}

		// Convert date range rows to the same type.
		rows = make([]RevenueByServiceRow, len(dateRows))
		for i, r := range dateRows {
			rows[i] = RevenueByServiceRow(r)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("unable to get L402 revenue stats: %w",
			err)
	}

	return rows, nil
}

// GetTotalRevenue returns the total settled revenue across all services.
func (s *L402TransactionsStore) GetTotalRevenue(
	ctx context.Context) (int64, error) {

	var total int64
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		total, err = tx.GetL402TotalRevenue(ctx)
		return err
	})

	if err != nil {
		return 0, fmt.Errorf("unable to get L402 total revenue: %w",
			err)
	}

	return total, nil
}

// GetTotalRevenueByDateRange returns the total settled revenue across all
// services within a date range.
func (s *L402TransactionsStore) GetTotalRevenueByDateRange(
	ctx context.Context, from, to time.Time) (int64, error) {

	var total int64
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		total, err = tx.GetL402TotalRevenueByDateRange(
			ctx, TotalRevenueByDateRangeParams{
				SettledAt: sql.NullTime{
					Time:  from,
					Valid: true,
				},
				SettledAt_2: sql.NullTime{
					Time:  to,
					Valid: true,
				},
			},
		)
		return err
	})

	if err != nil {
		return 0, fmt.Errorf("unable to get L402 total revenue by "+
			"date range: %w", err)
	}

	return total, nil
}

// CountTransactions returns the total number of settled transactions.
func (s *L402TransactionsStore) CountTransactions(
	ctx context.Context) (int64, error) {

	var count int64
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		count, err = tx.CountL402Transactions(ctx)
		return err
	})

	if err != nil {
		return 0, fmt.Errorf("unable to count L402 transactions: %w",
			err)
	}

	return count, nil
}

// CountTransactionsByDateRange returns the total number of settled
// transactions within a settlement-time date range.
func (s *L402TransactionsStore) CountTransactionsByDateRange(
	ctx context.Context, from, to time.Time) (int64, error) {

	var count int64
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		count, err = tx.CountL402TransactionsByDateRange(
			ctx, CountByDateRangeParams{
				SettledAt: sql.NullTime{
					Time:  from,
					Valid: true,
				},
				SettledAt_2: sql.NullTime{
					Time:  to,
					Valid: true,
				},
			},
		)
		return err
	})

	if err != nil {
		return 0, fmt.Errorf("unable to count L402 transactions by "+
			"date range: %w", err)
	}

	return count, nil
}

// GetSettledByTokenID returns the settled transaction for the given token ID.
func (s *L402TransactionsStore) GetSettledByTokenID(ctx context.Context,
	tokenID []byte) (L402Transaction, error) {

	var txn L402Transaction
	readOpts := NewL402TransactionsDBReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx L402TransactionsDB) error {
		var err error
		txn, err = tx.GetL402SettledTransactionByTokenID(ctx, tokenID)
		return err
	})

	if err != nil {
		return L402Transaction{}, fmt.Errorf("unable to get settled "+
			"L402 transaction by token_id(%x): %w", tokenID, err)
	}

	return txn, nil
}

// DeleteByTokenID deletes a transaction by its token ID.
func (s *L402TransactionsStore) DeleteByTokenID(ctx context.Context,
	tokenID []byte) error {

	var writeTxOpts L402TransactionsDBTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx L402TransactionsDB) error {
		nRows, err := tx.DeleteL402TransactionByTokenID(ctx, tokenID)
		if err != nil {
			return err
		}

		if nRows == 0 {
			log.Infof("Deleting L402 transaction by token_id(%x) "+
				"affected 0 rows", tokenID)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("unable to delete L402 transaction by "+
			"token_id(%x): %w", tokenID, err)
	}

	return nil
}
