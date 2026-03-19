package aperturedb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lightninglabs/aperture/aperturedb/sqlc"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightningnetwork/lnd/clock"
	"github.com/lightningnetwork/lnd/lntypes"
)

type (
	// NewMPPSession is a struct that contains the parameters required to
	// insert a new MPP session into the database.
	NewMPPSession = sqlc.InsertMPPSessionParams
)

// MPPSessionsDB is an interface that defines the set of operations that can be
// executed against the MPP sessions database.
type MPPSessionsDB interface {
	// InsertMPPSession inserts a new MPP session into the database.
	InsertMPPSession(ctx context.Context,
		arg NewMPPSession) (int32, error)

	// GetMPPSessionByID returns the MPP session with the given session ID.
	GetMPPSessionByID(ctx context.Context,
		sessionID string) (sqlc.MppSession, error)

	// UpdateMPPSessionDeposit atomically adds to the deposit balance.
	UpdateMPPSessionDeposit(ctx context.Context,
		arg sqlc.UpdateMPPSessionDepositParams) error

	// UpdateMPPSessionSpent atomically adds to the spent counter.
	UpdateMPPSessionSpent(ctx context.Context,
		arg sqlc.UpdateMPPSessionSpentParams) error

	// CloseMPPSession marks the session as closed.
	CloseMPPSession(ctx context.Context,
		arg sqlc.CloseMPPSessionParams) error
}

// MPPSessionsTxOptions defines the set of db txn options the
// MPPSessionsStore understands.
type MPPSessionsTxOptions struct {
	// readOnly governs if a read only transaction is needed or not.
	readOnly bool
}

// ReadOnly returns true if the transaction should be read only.
//
// NOTE: This implements the TxOptions interface.
func (a *MPPSessionsTxOptions) ReadOnly() bool {
	return a.readOnly
}

// NewMPPSessionsReadTx creates a new read transaction option set.
func NewMPPSessionsReadTx() MPPSessionsTxOptions {
	return MPPSessionsTxOptions{
		readOnly: true,
	}
}

// BatchedMPPSessionsDB is a version of the MPPSessionsDB that's capable of
// batched database operations.
type BatchedMPPSessionsDB interface {
	MPPSessionsDB

	BatchedTx[MPPSessionsDB]
}

// MPPSessionsStore represents a storage backend for MPP sessions.
type MPPSessionsStore struct {
	db    BatchedMPPSessionsDB
	clock clock.Clock
}

// NewMPPSessionsStore creates a new MPPSessionsStore instance given an open
// BatchedMPPSessionsDB storage backend.
func NewMPPSessionsStore(db BatchedMPPSessionsDB) *MPPSessionsStore {
	return &MPPSessionsStore{
		db:    db,
		clock: clock.NewDefaultClock(),
	}
}

// CreateSession creates a new session with the given initial state.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) CreateSession(ctx context.Context,
	session *auth.Session) error {

	now := s.clock.Now().UTC()

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		_, err := tx.InsertMPPSession(ctx, NewMPPSession{
			SessionID:     session.SessionID,
			PaymentHash:   session.PaymentHash[:],
			DepositSats:   session.DepositSats,
			SpentSats:     session.SpentSats,
			ReturnInvoice: session.ReturnInvoice,
			Status:        "open",
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		return err
	})

	if err != nil {
		return fmt.Errorf("unable to insert MPP session %s: %w",
			session.SessionID, err)
	}

	return nil
}

// GetSession returns the session with the given session ID.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) GetSession(ctx context.Context,
	sessionID string) (*auth.Session, error) {

	var session *auth.Session

	readOpts := NewMPPSessionsReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx MPPSessionsDB) error {
		row, err := tx.GetMPPSessionByID(ctx, sessionID)
		switch {
		case err == sql.ErrNoRows:
			return fmt.Errorf("session %s not found", sessionID)
		case err != nil:
			return err
		}

		var paymentHash lntypes.Hash
		copy(paymentHash[:], row.PaymentHash)

		session = &auth.Session{
			SessionID:     row.SessionID,
			PaymentHash:   paymentHash,
			DepositSats:   row.DepositSats,
			SpentSats:     row.SpentSats,
			ReturnInvoice: row.ReturnInvoice,
			Status:        row.Status,
			CreatedAt:     row.CreatedAt,
			UpdatedAt:     row.UpdatedAt,
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("unable to get MPP session %s: %w",
			sessionID, err)
	}

	return session, nil
}

// UpdateSessionBalance atomically adds the given amount to the session's
// deposit balance.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) UpdateSessionBalance(ctx context.Context,
	sessionID string, addSats int64) error {

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		return tx.UpdateMPPSessionDeposit(ctx,
			sqlc.UpdateMPPSessionDepositParams{
				DepositSats: addSats,
				UpdatedAt:   s.clock.Now().UTC(),
				SessionID:   sessionID,
			},
		)
	})

	if err != nil {
		return fmt.Errorf("unable to update balance for session "+
			"%s: %w", sessionID, err)
	}

	return nil
}

// DeductSessionBalance atomically adds the given amount to the session's
// spent counter. The caller is responsible for checking that the deduction
// does not exceed the deposit balance.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) DeductSessionBalance(ctx context.Context,
	sessionID string, amount int64) error {

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		// First check balance.
		session, err := tx.GetMPPSessionByID(ctx, sessionID)
		if err != nil {
			return err
		}
		if session.Status != "open" {
			return fmt.Errorf("session %s is closed", sessionID)
		}
		available := session.DepositSats - session.SpentSats
		if amount > available {
			return fmt.Errorf("insufficient balance: need %d, "+
				"have %d", amount, available)
		}

		return tx.UpdateMPPSessionSpent(ctx,
			sqlc.UpdateMPPSessionSpentParams{
				SpentSats: amount,
				UpdatedAt: s.clock.Now().UTC(),
				SessionID: sessionID,
			},
		)
	})

	if err != nil {
		return fmt.Errorf("unable to deduct balance for session "+
			"%s: %w", sessionID, err)
	}

	return nil
}

// CloseSession marks the session as closed. No further operations are accepted
// on a closed session.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) CloseSession(ctx context.Context,
	sessionID string) error {

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		return tx.CloseMPPSession(ctx, sqlc.CloseMPPSessionParams{
			UpdatedAt: s.clock.Now().UTC(),
			SessionID: sessionID,
		})
	})

	if err != nil {
		return fmt.Errorf("unable to close session %s: %w",
			sessionID, err)
	}

	return nil
}
