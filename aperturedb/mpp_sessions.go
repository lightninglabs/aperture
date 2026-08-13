package aperturedb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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

	// InsertMPPSessionCredit claims a payment hash for a session, doing
	// nothing if some session has already claimed it. The number of rows
	// affected says which of the two happened.
	InsertMPPSessionCredit(ctx context.Context,
		arg sqlc.InsertMPPSessionCreditParams) (sql.Result, error)

	// GetMPPSessionCreditOwner returns the session a payment hash has
	// already been credited to.
	GetMPPSessionCreditOwner(ctx context.Context,
		paymentHash []byte) (string, error)

	// UpdateMPPSessionDeposit atomically adds to the deposit balance.
	UpdateMPPSessionDeposit(ctx context.Context,
		arg sqlc.UpdateMPPSessionDepositParams) (sql.Result, error)

	// UpdateMPPSessionSpent atomically adds to the spent counter.
	// The query includes a balance check: deposit_sats - spent_sats >= amount.
	UpdateMPPSessionSpent(ctx context.Context,
		arg sqlc.UpdateMPPSessionSpentParams) (sql.Result, error)

	// SettleMPPSessionSpent adjusts the spent counter by a signed amount,
	// clamped to the range the deposit allows, and returns the resulting
	// spend.
	SettleMPPSessionSpent(ctx context.Context,
		arg sqlc.SettleMPPSessionSpentParams) (int64, error)

	// CloseMPPSession marks the session as closed.
	CloseMPPSession(ctx context.Context,
		arg sqlc.CloseMPPSessionParams) (sql.Result, error)

	// CloseMPPSessionReturningBalance atomically closes the session and
	// returns the remaining balance (deposit_sats - spent_sats).
	CloseMPPSessionReturningBalance(ctx context.Context,
		arg sqlc.CloseMPPSessionReturningBalanceParams) (int64, error)
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

// CreateSession creates a new session with the given initial state, claiming
// the deposit payment hash for it in the same transaction.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) CreateSession(ctx context.Context,
	session *auth.Session) error {

	now := s.clock.Now().UTC()

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		// The deposit that opens a session is a credit like any other,
		// so it claims its payment hash here. Without this a buyer
		// could open a session with a deposit and then re-present the
		// very same settled payment as a top-up, since the action a
		// credential names is not covered by the challenge HMAC.
		claimed, err := claimCreditHash(
			ctx, tx, session.SessionID, session.PaymentHash[:],
			session.DepositSats, now,
		)
		if err != nil {
			return err
		}
		if !claimed {
			return fmt.Errorf("deposit payment hash %x has "+
				"already been credited", session.PaymentHash[:])
		}

		_, err = tx.InsertMPPSession(ctx, NewMPPSession{
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

// claimCreditHash records that the given payment hash funded the given session,
// and reports whether this call is the one that recorded it. A hash some
// session has already claimed leaves the table untouched and returns false.
//
// The claim is an insert against a unique index rather than a read followed by
// a write, so two callers racing on the same hash cannot both come away
// believing they were first: the loser's insert affects no rows. It must run
// inside the same transaction as the balance change it guards, otherwise the
// race simply moves to the gap between them.
func claimCreditHash(ctx context.Context, tx MPPSessionsDB, sessionID string,
	paymentHash []byte, amountSats int64, now time.Time) (bool, error) {

	result, err := tx.InsertMPPSessionCredit(
		ctx, sqlc.InsertMPPSessionCreditParams{
			PaymentHash: paymentHash,
			SessionID:   sessionID,
			AmountSats:  amountSats,
			CreatedAt:   now,
		},
	)
	if err != nil {
		return false, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return rows > 0, nil
}

// CreditSession adds the given amount to the session's deposit if and only if
// the payment hash has not been credited before, and reports which of those
// happened.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) CreditSession(ctx context.Context, sessionID string,
	paymentHash lntypes.Hash, addSats int64) (auth.CreditOutcome, error) {

	if addSats <= 0 {
		return auth.CreditApplied, fmt.Errorf("credit must be "+
			"positive, got %d", addSats)
	}

	outcome := auth.CreditApplied

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		// The executor retries a transaction that failed to serialize,
		// so the outcome from an abandoned attempt must not survive
		// into the next one.
		outcome = auth.CreditApplied

		now := s.clock.Now().UTC()

		// Claim the payment hash before touching the balance. Losing
		// the claim means some earlier credit already spent this
		// payment, and the balance is left exactly as it was.
		claimed, err := claimCreditHash(
			ctx, tx, sessionID, paymentHash[:], addSats, now,
		)
		if err != nil {
			return err
		}

		if !claimed {
			owner, err := tx.GetMPPSessionCreditOwner(
				ctx, paymentHash[:],
			)

			// Losing the claim to a transaction that committed
			// after this one's snapshot was taken leaves the row
			// invisible to the read that follows. Nothing is wrong,
			// the answer simply is not readable yet, so ask the
			// executor for a retry: the next attempt begins with a
			// snapshot that includes the winner.
			if err == sql.ErrNoRows {
				return &ErrSerializationError{
					DBError: fmt.Errorf("credit for "+
						"payment hash %x is not yet "+
						"visible", paymentHash[:]),
				}
			}
			if err != nil {
				return err
			}

			// Whose credit it was decides what this is. The same
			// session means a client is retrying a top-up it has
			// already been given; a different one means a
			// credential is being pointed somewhere it was never
			// paid for.
			outcome = auth.CreditForeign
			if owner == sessionID {
				outcome = auth.CreditReplayed
			}

			return nil
		}

		result, err := tx.UpdateMPPSessionDeposit(ctx,
			sqlc.UpdateMPPSessionDepositParams{
				DepositSats: addSats,
				UpdatedAt:   now,
				SessionID:   sessionID,
			},
		)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			// The claim rolls back with the rest of the
			// transaction, so a payment that found no open session
			// to land on is not burned.
			return fmt.Errorf("session %s not found or "+
				"already closed", sessionID)
		}

		return nil
	})

	if err != nil {
		return auth.CreditApplied, fmt.Errorf("unable to credit "+
			"session %s: %w", sessionID, err)
	}

	return outcome, nil
}

// DeductSessionBalance atomically adds the given amount to the session's
// spent counter. The caller is responsible for checking that the deduction
// does not exceed the deposit balance.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) DeductSessionBalance(ctx context.Context,
	sessionID string, amount int64) error {

	if amount <= 0 {
		return fmt.Errorf("deduction must be positive, got %d",
			amount)
	}

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		// Atomic UPDATE with balance check in the WHERE clause.
		// This avoids the read-then-write TOCTOU race.
		result, err := tx.UpdateMPPSessionSpent(ctx,
			sqlc.UpdateMPPSessionSpentParams{
				SpentSats: amount,
				UpdatedAt: s.clock.Now().UTC(),
				SessionID: sessionID,
			},
		)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("session %s: not found, "+
				"closed, or insufficient balance",
				sessionID)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("unable to deduct balance for session "+
			"%s: %w", sessionID, err)
	}

	return nil
}

// SettleSessionBalance reconciles a request whose true cost is only known once
// its response completed. A positive delta charges the shortfall of an
// under-estimate, a negative delta gives back the excess of an over-estimate.
// It returns the spend the session settled on.
//
// The adjustment is clamped rather than refused. Metered pricing deducts an
// estimate before the response exists, so an under-estimate can settle to more
// than the balance holds, and the seller absorbs that difference: the service
// has already been rendered, and refusing the settlement would leave the
// session's books wrong instead of merely a fraction of one request short. The
// clamp is what keeps the shortfall bounded by the balance rather than driving
// it negative and turning a refund into a claim against the buyer.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) SettleSessionBalance(ctx context.Context,
	sessionID string, deltaSats int64) (int64, error) {

	var spentSats int64

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		spent, err := tx.SettleMPPSessionSpent(ctx,
			sqlc.SettleMPPSessionSpentParams{
				SpentSats: deltaSats,
				UpdatedAt: s.clock.Now().UTC(),
				SessionID: sessionID,
			},
		)
		if err != nil {
			return err
		}

		spentSats = spent

		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("unable to settle balance for session "+
			"%s: %w", sessionID, err)
	}

	return spentSats, nil
}

// CloseSession marks the session as closed. No further operations are accepted
// on a closed session.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) CloseSession(ctx context.Context,
	sessionID string) error {

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		result, err := tx.CloseMPPSession(ctx,
			sqlc.CloseMPPSessionParams{
				UpdatedAt: s.clock.Now().UTC(),
				SessionID: sessionID,
			},
		)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return fmt.Errorf("session %s not found or "+
				"already closed", sessionID)
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("unable to close session %s: %w",
			sessionID, err)
	}

	return nil
}

// CloseSessionAndGetBalance atomically closes the session and returns the
// remaining balance (deposit_sats - spent_sats). This prevents the TOCTOU race
// where a concurrent bearer request could deduct balance between a separate
// GetSession read and CloseSession write.
//
// NOTE: This implements the auth.SessionStore interface.
func (s *MPPSessionsStore) CloseSessionAndGetBalance(ctx context.Context,
	sessionID string) (int64, error) {

	var remainingBalance int64

	var writeTxOpts MPPSessionsTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPSessionsDB) error {
		balance, err := tx.CloseMPPSessionReturningBalance(ctx,
			sqlc.CloseMPPSessionReturningBalanceParams{
				UpdatedAt: s.clock.Now().UTC(),
				SessionID: sessionID,
			},
		)
		if err != nil {
			return err
		}
		remainingBalance = balance
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("unable to close session %s: %w",
			sessionID, err)
	}

	return remainingBalance, nil
}
