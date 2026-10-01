package aperturedb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lightninglabs/aperture/aperturedb/sqlc"
	"github.com/lightningnetwork/lnd/clock"
	"github.com/lightningnetwork/lnd/lntypes"
)

// MPPChargesDB is an interface that defines the set of operations that can be
// executed against the store of consumed MPP charge credentials.
type MPPChargesDB interface {
	// InsertMPPConsumedCharge claims a payment hash for the one request it
	// bought, doing nothing if the hash has already been claimed. The
	// number of rows affected says which of the two happened.
	InsertMPPConsumedCharge(ctx context.Context,
		arg sqlc.InsertMPPConsumedChargeParams) (sql.Result, error)

	// GetMPPConsumedCharge returns the record of a consumed payment hash.
	GetMPPConsumedCharge(ctx context.Context,
		paymentHash []byte) (sqlc.MppConsumedCharge, error)

	// DeleteExpiredMPPConsumedCharges removes every record whose challenge
	// expired before the given instant.
	DeleteExpiredMPPConsumedCharges(ctx context.Context,
		expiresAt time.Time) (sql.Result, error)
}

// MPPChargesTxOptions defines the set of db txn options the MPPChargesStore
// understands.
type MPPChargesTxOptions struct {
	// readOnly governs if a read only transaction is needed or not.
	readOnly bool
}

// ReadOnly returns true if the transaction should be read only.
//
// NOTE: This implements the TxOptions interface.
func (a *MPPChargesTxOptions) ReadOnly() bool {
	return a.readOnly
}

// NewMPPChargesReadTx creates a new read transaction option set.
func NewMPPChargesReadTx() MPPChargesTxOptions {
	return MPPChargesTxOptions{
		readOnly: true,
	}
}

// BatchedMPPChargesDB is a version of the MPPChargesDB that's capable of
// batched database operations.
type BatchedMPPChargesDB interface {
	MPPChargesDB

	BatchedTx[MPPChargesDB]
}

// MPPChargesStore records which Lightning payments have already been spent on
// a request under the MPP charge intent, so that one payment buys exactly one
// request.
type MPPChargesStore struct {
	db    BatchedMPPChargesDB
	clock clock.Clock
}

// NewMPPChargesStore creates a new MPPChargesStore instance given an open
// BatchedMPPChargesDB storage backend.
func NewMPPChargesStore(db BatchedMPPChargesDB) *MPPChargesStore {
	return &MPPChargesStore{
		db:    db,
		clock: clock.NewDefaultClock(),
	}
}

// ConsumeCharge claims the given payment hash for the request being authorized
// and reports whether this call is the one that claimed it. A hash some earlier
// request already claimed leaves the table untouched and returns false.
//
// The claim is a single insert against a unique index rather than a read
// followed by a write, so two concurrent presentations of the same credential
// cannot both come away believing they were first: the loser's insert affects
// no rows.
//
// NOTE: This implements the auth.ChargeStore interface.
func (s *MPPChargesStore) ConsumeCharge(ctx context.Context,
	paymentHash lntypes.Hash, challengeID string,
	expiresAt time.Time) (bool, error) {

	var consumed bool

	var writeTxOpts MPPChargesTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPChargesDB) error {
		// The executor retries a transaction that failed to serialize,
		// so the answer from an abandoned attempt must not survive into
		// the next one.
		consumed = false

		result, err := tx.InsertMPPConsumedCharge(
			ctx, sqlc.InsertMPPConsumedChargeParams{
				PaymentHash: paymentHash[:],
				ChallengeID: challengeID,
				ExpiresAt:   expiresAt.UTC(),
				ConsumedAt:  s.clock.Now().UTC(),
			},
		)
		if err != nil {
			return err
		}

		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}

		consumed = rows > 0

		return nil
	})

	if err != nil {
		return false, fmt.Errorf("unable to consume charge for "+
			"payment hash %x: %w", paymentHash[:], err)
	}

	return consumed, nil
}

// PruneConsumedCharges removes every record whose challenge expired before the
// given instant, and returns how many it removed.
//
// The caller decides how far back that instant is, and the safety of the whole
// exercise rests on the answer: a credential is refused once its challenge has
// expired whether or not a record of it survives, so a record may only be
// dropped after the last moment at which its absence could have let a replay
// through.
//
// NOTE: This implements the auth.ChargeStore interface.
func (s *MPPChargesStore) PruneConsumedCharges(ctx context.Context,
	expiredBefore time.Time) (int64, error) {

	var pruned int64

	var writeTxOpts MPPChargesTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx MPPChargesDB) error {
		pruned = 0

		result, err := tx.DeleteExpiredMPPConsumedCharges(
			ctx, expiredBefore.UTC(),
		)
		if err != nil {
			return err
		}

		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}

		pruned = rows

		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("unable to prune consumed charges: %w",
			err)
	}

	return pruned, nil
}

// IsChargeConsumed returns whether the given payment hash has already bought a
// request. It answers a question about the past and is not a substitute for
// ConsumeCharge, which is the only call that may decide a request: asking first
// and claiming afterwards would put the race back where a single insert took it
// out.
func (s *MPPChargesStore) IsChargeConsumed(ctx context.Context,
	paymentHash lntypes.Hash) (bool, error) {

	var consumed bool

	readOpts := NewMPPChargesReadTx()
	err := s.db.ExecTx(ctx, &readOpts, func(tx MPPChargesDB) error {
		_, err := tx.GetMPPConsumedCharge(ctx, paymentHash[:])
		switch {
		case err == sql.ErrNoRows:
			consumed = false
			return nil

		case err != nil:
			return err
		}

		consumed = true

		return nil
	})

	if err != nil {
		return false, fmt.Errorf("unable to look up consumed charge "+
			"%x: %w", paymentHash[:], err)
	}

	return consumed, nil
}
