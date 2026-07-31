package aperturedb

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// newMPPSessionsStoreWithDB wraps a raw database handle in the session store.
func newMPPSessionsStoreWithDB(db *BaseDB) *MPPSessionsStore {
	dbTxer := NewTransactionExecutor(db,
		func(tx *sql.Tx) MPPSessionsDB {
			return db.WithTx(tx)
		},
	)

	return NewMPPSessionsStore(dbTxer)
}

// newMPPSessionsStore returns a session store over a fresh test database.
func newMPPSessionsStore(t *testing.T) *MPPSessionsStore {
	return newMPPSessionsStoreWithDB(NewTestDB(t).BaseDB)
}

// testSessionCtx returns a context bounded by the default test timeout.
func testSessionCtx(t *testing.T) context.Context {
	ctxt, cancel := context.WithTimeout(
		context.Background(), defaultTestTimeout,
	)
	t.Cleanup(cancel)

	return ctxt
}

// testSession builds a session with a deterministic payment hash derived from
// its index, so a test can hold several distinct sessions at once.
func testSession(idx byte, depositSats int64) *auth.Session {
	var hash lntypes.Hash
	for i := range hash {
		hash[i] = idx
	}

	return &auth.Session{
		SessionID:     hash.String(),
		PaymentHash:   hash,
		DepositSats:   depositSats,
		ReturnInvoice: "lnbcrt1refund",
		Status:        "open",
	}
}

// openSession creates a session in the store and returns it.
func openSession(t *testing.T, store *MPPSessionsStore, ctx context.Context,
	idx byte, depositSats int64) *auth.Session {

	t.Helper()

	session := testSession(idx, depositSats)
	require.NoError(t, store.CreateSession(ctx, session))

	return session
}

// TestMPPSessionLifecycle walks a session through the four actions the MPP
// session intent defines and checks the balance arithmetic at every step.
func TestMPPSessionLifecycle(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x01, 1000)

	// A freshly opened session reads back exactly as it was written.
	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1000), got.DepositSats)
	require.Equal(t, int64(0), got.SpentSats)
	require.Equal(t, "open", got.Status)
	require.Equal(t, session.PaymentHash, got.PaymentHash)
	require.Equal(t, session.ReturnInvoice, got.ReturnInvoice)

	// Bearer requests draw the balance down.
	require.NoError(t, store.DeductSessionBalance(ctx, session.SessionID, 300))
	require.NoError(t, store.DeductSessionBalance(ctx, session.SessionID, 200))

	// A top-up adds to the deposit rather than crediting the spend.
	require.NoError(t, store.UpdateSessionBalance(ctx, session.SessionID, 500))

	got, err = store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1500), got.DepositSats)
	require.Equal(t, int64(500), got.SpentSats)

	// Closing hands back the unspent remainder in the same transaction that
	// closes the session.
	refund, err := store.CloseSessionAndGetBalance(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1000), refund)

	got, err = store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, "closed", got.Status)
}

// TestMPPSessionRejectsInsufficientBalance asserts that a deduction larger than
// the remaining balance is refused whole. A partial deduction, or one that
// drives the balance negative, would hand out service the buyer has not paid
// for and leave a refund that cannot be honoured.
func TestMPPSessionRejectsInsufficientBalance(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x02, 100)

	// A deduction that exactly exhausts the balance is allowed: the
	// boundary belongs to the buyer.
	require.NoError(t, store.DeductSessionBalance(ctx, session.SessionID, 100))

	// One satoshi past it is not.
	err := store.DeductSessionBalance(ctx, session.SessionID, 1)
	require.ErrorContains(t, err, "insufficient balance")

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.SpentSats)
	require.Equal(t, int64(100), got.DepositSats)
}

// TestMPPSessionRejectsNonPositiveAmounts asserts that a zero or negative
// amount is refused on both balance mutations. A negative deduction would be a
// credit smuggled in through the debit path, which the SQL balance guard would
// happily accept.
func TestMPPSessionRejectsNonPositiveAmounts(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x03, 100)

	for _, amount := range []int64{0, -1, -1000} {
		require.Error(t, store.DeductSessionBalance(
			ctx, session.SessionID, amount,
		))
		require.Error(t, store.UpdateSessionBalance(
			ctx, session.SessionID, amount,
		))
	}

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(100), got.DepositSats)
	require.Equal(t, int64(0), got.SpentSats)
}

// TestMPPSessionClosedIsTerminal asserts that a closed session accepts nothing
// further. Once the refund has been computed and paid, a late bearer request or
// top-up landing on the same session would move money against a balance that
// has already been settled.
func TestMPPSessionClosedIsTerminal(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x04, 1000)

	refund, err := store.CloseSessionAndGetBalance(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1000), refund)

	require.Error(t, store.DeductSessionBalance(ctx, session.SessionID, 1))
	require.Error(t, store.UpdateSessionBalance(ctx, session.SessionID, 1))
	require.Error(t, store.CloseSession(ctx, session.SessionID))

	// Closing twice must not report a second refund to pay out.
	_, err = store.CloseSessionAndGetBalance(ctx, session.SessionID)
	require.Error(t, err)
}

// TestMPPSessionUnknownSession asserts that every operation against a session
// that was never created fails rather than silently succeeding.
func TestMPPSessionUnknownSession(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	const unknown = "0000000000000000000000000000000000000000000000000000000000000000"

	_, err := store.GetSession(ctx, unknown)
	require.Error(t, err)

	require.Error(t, store.DeductSessionBalance(ctx, unknown, 1))
	require.Error(t, store.UpdateSessionBalance(ctx, unknown, 1))
	require.Error(t, store.CloseSession(ctx, unknown))

	_, err = store.CloseSessionAndGetBalance(ctx, unknown)
	require.Error(t, err)
}

// TestMPPSessionConcurrentDeductions is the test that earns the word "atomic"
// in the SessionStore interface. Many bearer requests against one session run
// at once, and together they ask for more than the deposit holds. The store has
// to let exactly as many through as the deposit covers, no more, and the
// survivors' spend must sum to exactly what they were granted: a read-then-write
// implementation would let two requests read the same remaining balance and
// both proceed.
func TestMPPSessionConcurrentDeductions(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	const (
		deposit    = 1000
		perRequest = 100
		requests   = 40
	)

	session := openSession(t, store, ctx, 0x05, deposit)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)

	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			err := store.DeductSessionBalance(
				ctx, session.SessionID, perRequest,
			)
			if err != nil {
				return
			}

			mu.Lock()
			succeeded++
			mu.Unlock()
		}()
	}
	wg.Wait()

	// The deposit covers exactly ten requests, so exactly ten may win.
	require.Equal(t, deposit/perRequest, succeeded)

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(succeeded*perRequest), got.SpentSats)
	require.GreaterOrEqual(t, got.DepositSats-got.SpentSats, int64(0))
}

// TestMPPSessionConcurrentCloseAndDeduct asserts the close path cannot be
// straddled. A bearer request racing a close either draws down before the close
// and is reflected in the refund, or is refused because the session is already
// closed. What must never happen is a deduction that lands after the refund
// amount has been decided: those satoshis would be paid out to the buyer and
// spent on service both.
func TestMPPSessionConcurrentCloseAndDeduct(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	const (
		deposit    = 1000
		perRequest = 50
		requests   = 20
	)

	session := openSession(t, store, ctx, 0x06, deposit)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int

		refund    int64
		refundErr error
	)

	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			err := store.DeductSessionBalance(
				ctx, session.SessionID, perRequest,
			)
			if err != nil {
				return
			}

			mu.Lock()
			succeeded++
			mu.Unlock()
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		refund, refundErr = store.CloseSessionAndGetBalance(
			ctx, session.SessionID,
		)
	}()

	wg.Wait()
	require.NoError(t, refundErr)

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, "closed", got.Status)

	// Every satoshi is accounted for exactly once: what was spent by the
	// requests that got in before the close, plus what was refunded, is the
	// deposit. Nothing was spent twice and nothing vanished.
	require.Equal(t, int64(succeeded*perRequest), got.SpentSats)
	require.Equal(t, int64(deposit), got.SpentSats+refund)
}

// TestMPPSessionSurvivesRestart asserts the property that makes a durable store
// worth having: a proxy that restarts must find the session, and its balance,
// exactly where it left them. An in-memory store loses the deposit, which is
// the buyer's money, and leaves nothing to refund.
func TestMPPSessionSurvivesRestart(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	dbFile := filepath.Join(t.TempDir(), "sessions.db")

	first, err := NewSqliteStore(&SqliteConfig{DatabaseFileName: dbFile})
	require.NoError(t, err)

	store := newMPPSessionsStoreWithDB(first.BaseDB)
	session := openSession(t, store, ctx, 0x07, 5000)

	require.NoError(t, store.DeductSessionBalance(ctx, session.SessionID, 1200))
	require.NoError(t, store.UpdateSessionBalance(ctx, session.SessionID, 300))

	// The proxy goes away.
	require.NoError(t, first.Close())

	// And comes back against the same file.
	second, err := NewSqliteStore(&SqliteConfig{DatabaseFileName: dbFile})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, second.Close())
	})

	restarted := newMPPSessionsStoreWithDB(second.BaseDB)

	got, err := restarted.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(5300), got.DepositSats)
	require.Equal(t, int64(1200), got.SpentSats)
	require.Equal(t, "open", got.Status)
	require.Equal(t, session.PaymentHash, got.PaymentHash)
	require.Equal(t, session.ReturnInvoice, got.ReturnInvoice)

	// The session is not merely readable after the restart, it is still
	// usable: the buyer can keep spending and can still be refunded.
	require.NoError(t, restarted.DeductSessionBalance(
		ctx, session.SessionID, 100,
	))

	refund, err := restarted.CloseSessionAndGetBalance(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(4000), refund)
}

// TestMPPSessionDuplicateOpen asserts a session ID cannot be opened twice. The
// session ID is the deposit payment hash, so a second open on the same hash
// would be a replay of one payment into a second balance.
func TestMPPSessionDuplicateOpen(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x08, 1000)
	require.NoError(t, store.DeductSessionBalance(ctx, session.SessionID, 400))

	require.Error(t, store.CreateSession(ctx, testSession(0x08, 9999)))

	// The replay must not have disturbed the real session's balance.
	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1000), got.DepositSats)
	require.Equal(t, int64(400), got.SpentSats)
}

// sessionOp is one balance mutation the property test can apply.
type sessionOp struct {
	// deposit is true for a top-up, false for a draw-down.
	deposit bool

	// amount is the satoshi amount of the operation.
	amount int64
}

// TestMPPSessionBalanceInvariants drives the store with arbitrary sequences of
// top-ups and draw-downs and checks the two invariants the money depends on:
// the balance never goes negative, and every satoshi that entered the session
// leaves it either as spend or as refund.
func TestMPPSessionBalanceInvariants(t *testing.T) {
	t.Parallel()

	// The context spans every rapid iteration rather than a single database
	// call, so it is given room well beyond the per-call timeout the other
	// tests use.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	store := newMPPSessionsStore(t)

	// Each iteration gets its own session inside the one database, so the
	// property runs against a fresh balance without paying to build a new
	// database every time.
	var iteration int

	rapid.Check(t, func(rt *rapid.T) {
		iteration++

		initial := rapid.Int64Range(0, 100_000).Draw(rt, "initial")

		session := &auth.Session{
			SessionID:     fmt.Sprintf("property-%d", iteration),
			DepositSats:   initial,
			ReturnInvoice: "lnbcrt1refund",
			Status:        "open",
		}
		require.NoError(rt, store.CreateSession(ctx, session))

		ops := rapid.SliceOfN(rapid.Custom(func(t *rapid.T) sessionOp {
			return sessionOp{
				deposit: rapid.Bool().Draw(t, "deposit"),
				amount: rapid.Int64Range(1, 20_000).Draw(
					t, "amount",
				),
			}
		}), 0, 30).Draw(rt, "ops")

		// The model tracks what the store should hold, applying the
		// same refusal rule the store does: a draw-down that does not
		// fit is refused whole.
		wantDeposit, wantSpent := initial, int64(0)

		for _, op := range ops {
			if op.deposit {
				err := store.UpdateSessionBalance(
					ctx, session.SessionID, op.amount,
				)
				require.NoError(rt, err)
				wantDeposit += op.amount

				continue
			}

			err := store.DeductSessionBalance(
				ctx, session.SessionID, op.amount,
			)
			if wantDeposit-wantSpent >= op.amount {
				require.NoError(rt, err)
				wantSpent += op.amount
			} else {
				require.Error(rt, err)
			}

			got, err := store.GetSession(ctx, session.SessionID)
			require.NoError(rt, err)

			// The invariant that matters most: a session can never
			// owe service it was not paid for.
			require.GreaterOrEqual(
				rt, got.DepositSats-got.SpentSats, int64(0),
			)
		}

		got, err := store.GetSession(ctx, session.SessionID)
		require.NoError(rt, err)
		require.Equal(rt, wantDeposit, got.DepositSats)
		require.Equal(rt, wantSpent, got.SpentSats)

		refund, err := store.CloseSessionAndGetBalance(
			ctx, session.SessionID,
		)
		require.NoError(rt, err)

		// Conservation: everything deposited is either spent or
		// refunded, and nothing else.
		require.Equal(rt, wantDeposit, got.SpentSats+refund)
	})
}

// TestMPPSessionsAreIsolated asserts that operations on one session never touch
// another. The balance guard lives in a WHERE clause keyed by session ID, and a
// guard that matched too broadly would let one buyer spend another's deposit.
func TestMPPSessionsAreIsolated(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	const numSessions = 5

	for i := 0; i < numSessions; i++ {
		openSession(t, store, ctx, byte(0x10+i), 1000)
	}

	var wg sync.WaitGroup
	for i := 0; i < numSessions; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			sessionID := testSession(byte(0x10+idx), 0).SessionID
			for j := 0; j <= idx; j++ {
				require.NoError(t, store.DeductSessionBalance(
					ctx, sessionID, 100,
				))
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < numSessions; i++ {
		sessionID := testSession(byte(0x10+i), 0).SessionID

		got, err := store.GetSession(ctx, sessionID)
		require.NoError(t, err, fmt.Sprintf("session %d", i))
		require.Equal(t, int64((i+1)*100), got.SpentSats)
		require.Equal(t, int64(1000), got.DepositSats)
	}
}

// TestMPPSessionSettleClamps asserts the reconciliation clamps rather than
// refuses. Metered pricing charges an estimate before the response exists, so
// the true cost can land either side of it, and both directions have to settle
// without leaving the session's books inconsistent.
func TestMPPSessionSettleClamps(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x20, 1000)
	require.NoError(t, store.DeductSessionBalance(ctx, session.SessionID, 100))

	// An under-estimate charges the shortfall.
	spent, err := store.SettleSessionBalance(ctx, session.SessionID, 40)
	require.NoError(t, err)
	require.Equal(t, int64(140), spent)

	// An over-estimate gives the excess back.
	spent, err = store.SettleSessionBalance(ctx, session.SessionID, -90)
	require.NoError(t, err)
	require.Equal(t, int64(50), spent)

	// A settlement larger than the balance is absorbed by the seller rather
	// than turning the refund into a claim against the buyer. Refusing it
	// instead would leave the books showing service rendered but unbilled.
	spent, err = store.SettleSessionBalance(ctx, session.SessionID, 10_000)
	require.NoError(t, err)
	require.Equal(t, int64(1000), spent)

	// And a give-back larger than the spend cannot manufacture a credit.
	spent, err = store.SettleSessionBalance(ctx, session.SessionID, -10_000)
	require.NoError(t, err)
	require.Equal(t, int64(0), spent)

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1000), got.DepositSats)
	require.Equal(t, int64(0), got.SpentSats)
}

// TestMPPSessionSettleRejectsClosed asserts a settlement cannot land on a
// closed session. The refund for a closed session has already been computed and
// possibly paid, so a late settlement would move money that is no longer there.
func TestMPPSessionSettleRejectsClosed(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x21, 1000)

	_, err := store.CloseSessionAndGetBalance(ctx, session.SessionID)
	require.NoError(t, err)

	_, err = store.SettleSessionBalance(ctx, session.SessionID, 10)
	require.Error(t, err)

	_, err = store.SettleSessionBalance(ctx, "no-such-session", 10)
	require.Error(t, err)
}

// TestMPPSessionConcurrentSettlements asserts that settlements racing each
// other, and racing draw-downs, never leave the spend outside the range the
// deposit allows. Settlements run detached from the request that produced them,
// so several land on one session at once as a matter of course.
func TestMPPSessionConcurrentSettlements(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	const (
		deposit  = 10_000
		requests = 40
	)

	session := openSession(t, store, ctx, 0x22, deposit)

	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Half the goroutines draw down, half settle in either
			// direction, so the spend is pushed at both bounds.
			if idx%2 == 0 {
				_ = store.DeductSessionBalance(
					ctx, session.SessionID, 400,
				)

				return
			}

			delta := int64(600)
			if idx%4 == 1 {
				delta = -600
			}

			_, err := store.SettleSessionBalance(
				ctx, session.SessionID, delta,
			)
			require.NoError(t, err)
		}(i)
	}
	wg.Wait()

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, got.SpentSats, int64(0))
	require.LessOrEqual(t, got.SpentSats, got.DepositSats)

	// The remaining balance is still exactly what a close would pay out.
	refund, err := store.CloseSessionAndGetBalance(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(deposit), got.SpentSats+refund)
}
