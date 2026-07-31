package aperturedb

import (
	"context"
	"crypto/rand"
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

// freshHash returns a payment hash no other credit has used, standing in for
// the hash of a top-up invoice the buyer has just paid.
func freshHash(t require.TestingT) lntypes.Hash {
	var hash lntypes.Hash
	_, err := rand.Read(hash[:])
	require.NoError(t, err)

	return hash
}

// creditTopUp pays a distinct top-up into the session and asserts the store
// treated it as a first-time credit. It returns the hash so a test can replay
// it.
func creditTopUp(t *testing.T, store *MPPSessionsStore, ctx context.Context,
	sessionID string, amount int64) lntypes.Hash {

	t.Helper()

	hash := freshHash(t)

	outcome, err := store.CreditSession(ctx, sessionID, hash, amount)
	require.NoError(t, err)
	require.Equal(t, auth.CreditApplied, outcome)

	return hash
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
	creditTopUp(t, store, ctx, session.SessionID, 500)

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
		_, err := store.CreditSession(
			ctx, session.SessionID, freshHash(t), amount,
		)
		require.Error(t, err)
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
	require.Error(t, store.CloseSession(ctx, session.SessionID))

	_, err = store.CreditSession(ctx, session.SessionID, freshHash(t), 1)
	require.Error(t, err)

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
	require.Error(t, store.CloseSession(ctx, unknown))

	_, err = store.CreditSession(ctx, unknown, freshHash(t), 1)
	require.Error(t, err)

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
	creditTopUp(t, store, ctx, session.SessionID, 300)

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
			PaymentHash:   freshHash(rt),
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
				// Every top-up here is a payment of its own,
				// so each gets a hash of its own. Replays are
				// the subject of their own property below.
				outcome, err := store.CreditSession(
					ctx, session.SessionID, freshHash(rt),
					op.amount,
				)
				require.NoError(rt, err)
				require.Equal(rt, auth.CreditApplied, outcome)
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

// TestMPPSessionCreditIsOnceOnly asserts that one payment funds a session once
// and only once. A settled invoice stays settled and a preimage keeps hashing
// to its payment hash forever, so every check a top-up credential can be made
// to pass passes again on the tenth presentation as readily as on the first.
// The store is the only place that remembers, and if it does not, a buyer pays
// one invoice and mints balance out of it for as long as they care to keep
// resending.
func TestMPPSessionCreditIsOnceOnly(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x30, 1000)

	hash := creditTopUp(t, store, ctx, session.SessionID, 500)

	// Replaying the very same payment adds nothing, however many times it
	// is presented.
	for i := 0; i < 10; i++ {
		outcome, err := store.CreditSession(
			ctx, session.SessionID, hash, 500,
		)
		require.NoError(t, err)
		require.Equal(t, auth.CreditReplayed, outcome)
	}

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1500), got.DepositSats)

	// A different payment still lands, so the guard refuses replays rather
	// than refusing top-ups.
	creditTopUp(t, store, ctx, session.SessionID, 250)

	got, err = store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1750), got.DepositSats)
}

// TestMPPSessionConcurrentCreditReplays asserts the once-only property holds
// under a genuine race. This is the case a check-then-act implementation loses:
// every replay reads the hash as unspent, every one of them decides it is the
// first, and the balance grows once per goroutine. Only a claim and a credit
// made atomic can let exactly one through.
func TestMPPSessionConcurrentCreditReplays(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	const (
		replays   = 32
		topUpSats = 700
	)

	session := openSession(t, store, ctx, 0x31, 1000)
	hash := freshHash(t)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		applied int

		// start holds every goroutine at the line until they can be
		// released together, so the replays genuinely overlap rather
		// than trickling through one after another.
		start = make(chan struct{})
	)

	for i := 0; i < replays; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			<-start

			outcome, err := store.CreditSession(
				ctx, session.SessionID, hash, topUpSats,
			)
			require.NoError(t, err)

			mu.Lock()
			defer mu.Unlock()

			if outcome == auth.CreditApplied {
				applied++
			}
		}()
	}

	close(start)
	wg.Wait()

	require.Equal(t, 1, applied)

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1000+topUpSats), got.DepositSats)
}

// TestMPPSessionCreditHashBindsToItsSession asserts that a payment credited to
// one session cannot be credited to another. Scoping the record per session
// instead would leave the weaker property: a buyer could open a second session
// and re-present a top-up they had already spent, since nothing about the
// credential names a session in a way the challenge HMAC covers.
func TestMPPSessionCreditHashBindsToItsSession(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	first := openSession(t, store, ctx, 0x32, 1000)
	second := openSession(t, store, ctx, 0x33, 1000)

	hash := creditTopUp(t, store, ctx, first.SessionID, 400)

	outcome, err := store.CreditSession(ctx, second.SessionID, hash, 400)
	require.NoError(t, err)
	require.Equal(t, auth.CreditForeign, outcome)

	// The foreign attempt moved nothing in either direction.
	got, err := store.GetSession(ctx, first.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1400), got.DepositSats)

	got, err = store.GetSession(ctx, second.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1000), got.DepositSats)
}

// TestMPPSessionDepositHashIsCredited asserts that the deposit which opened a
// session counts as a credit of that payment. Without this the buyer can open a
// session and then hand the very same settled payment back as a top-up: the
// action a credential names rides in its payload, which the challenge HMAC does
// not cover, so one deposit challenge serves as both.
func TestMPPSessionDepositHashIsCredited(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	session := openSession(t, store, ctx, 0x34, 1000)

	outcome, err := store.CreditSession(
		ctx, session.SessionID, session.PaymentHash, 1000,
	)
	require.NoError(t, err)
	require.Equal(t, auth.CreditReplayed, outcome)

	// Nor can that deposit be pointed at a second session.
	other := openSession(t, store, ctx, 0x35, 500)

	outcome, err = store.CreditSession(
		ctx, other.SessionID, session.PaymentHash, 1000,
	)
	require.NoError(t, err)
	require.Equal(t, auth.CreditForeign, outcome)

	got, err := store.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1000), got.DepositSats)

	got, err = store.GetSession(ctx, other.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(500), got.DepositSats)
}

// TestMPPSessionCreditRefusedDoesNotBurnHash asserts that a credit which fails
// for a reason other than replay leaves the payment hash unclaimed. The claim
// and the balance change share a transaction precisely so that a top-up landing
// on a session that has just closed can still be credited to a session that is
// open, rather than the buyer's payment being consumed by an attempt that
// credited nothing.
func TestMPPSessionCreditRefusedDoesNotBurnHash(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	store := newMPPSessionsStore(t)

	closed := openSession(t, store, ctx, 0x36, 1000)
	_, err := store.CloseSessionAndGetBalance(ctx, closed.SessionID)
	require.NoError(t, err)

	hash := freshHash(t)

	_, err = store.CreditSession(ctx, closed.SessionID, hash, 300)
	require.Error(t, err)

	// The same payment still funds an open session.
	open := openSession(t, store, ctx, 0x37, 100)

	outcome, err := store.CreditSession(ctx, open.SessionID, hash, 300)
	require.NoError(t, err)
	require.Equal(t, auth.CreditApplied, outcome)

	got, err := store.GetSession(ctx, open.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(400), got.DepositSats)
}

// TestMPPSessionCreditSurvivesRestart asserts that the record of which payments
// have been credited is durable. Held in memory it would be worth nothing: a
// buyer who wanted their top-up counted twice would only have to wait for the
// proxy to restart, and a proxy restarts for reasons the buyer can often
// provoke.
func TestMPPSessionCreditSurvivesRestart(t *testing.T) {
	t.Parallel()

	ctx := testSessionCtx(t)
	dbFile := filepath.Join(t.TempDir(), "credits.db")

	first, err := NewSqliteStore(&SqliteConfig{DatabaseFileName: dbFile})
	require.NoError(t, err)

	store := newMPPSessionsStoreWithDB(first.BaseDB)
	session := openSession(t, store, ctx, 0x38, 1000)
	hash := creditTopUp(t, store, ctx, session.SessionID, 600)

	// The proxy goes away and comes back against the same file.
	require.NoError(t, first.Close())

	second, err := NewSqliteStore(&SqliteConfig{DatabaseFileName: dbFile})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, second.Close())
	})

	restarted := newMPPSessionsStoreWithDB(second.BaseDB)

	outcome, err := restarted.CreditSession(
		ctx, session.SessionID, hash, 600,
	)
	require.NoError(t, err)
	require.Equal(t, auth.CreditReplayed, outcome)

	// The deposit hash is remembered across the restart as well.
	outcome, err = restarted.CreditSession(
		ctx, session.SessionID, session.PaymentHash, 1000,
	)
	require.NoError(t, err)
	require.Equal(t, auth.CreditReplayed, outcome)

	got, err := restarted.GetSession(ctx, session.SessionID)
	require.NoError(t, err)
	require.Equal(t, int64(1600), got.DepositSats)
}

// TestMPPSessionCreditConservation is the arithmetic statement of the whole
// fix: however top-ups and replays of them are interleaved, the deposit a
// session ends up holding is the sum of the distinct payments made into it, and
// nothing else. Replays are drawn from the same pool as first presentations, so
// the property covers the orderings a handwritten test would not think to try.
func TestMPPSessionCreditConservation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	store := newMPPSessionsStore(t)

	var iteration int

	rapid.Check(t, func(rt *rapid.T) {
		iteration++

		initial := rapid.Int64Range(0, 100_000).Draw(rt, "initial")

		session := &auth.Session{
			SessionID:     fmt.Sprintf("conservation-%d", iteration),
			PaymentHash:   freshHash(rt),
			DepositSats:   initial,
			ReturnInvoice: "lnbcrt1refund",
			Status:        "open",
		}
		require.NoError(rt, store.CreateSession(ctx, session))

		// The pool stands for the invoices the buyer has actually paid.
		numPayments := rapid.IntRange(1, 8).Draw(rt, "numPayments")

		hashes := make([]lntypes.Hash, numPayments)
		amounts := make([]int64, numPayments)
		for i := range hashes {
			hashes[i] = freshHash(rt)
			amounts[i] = rapid.Int64Range(1, 20_000).Draw(
				rt, fmt.Sprintf("amount-%d", i),
			)
		}

		// Each presentation names one of those payments. Naming the
		// same one twice is a replay.
		presentations := rapid.SliceOfN(
			rapid.IntRange(0, numPayments-1), 0, 40,
		).Draw(rt, "presentations")

		credited := make(map[int]bool)

		for _, idx := range presentations {
			outcome, err := store.CreditSession(
				ctx, session.SessionID, hashes[idx],
				amounts[idx],
			)
			require.NoError(rt, err)

			if credited[idx] {
				require.Equal(rt, auth.CreditReplayed, outcome)

				continue
			}

			require.Equal(rt, auth.CreditApplied, outcome)
			credited[idx] = true
		}

		// The deposit is the opening balance plus each distinct payment
		// counted once, no matter how often it was presented.
		want := initial
		for idx := range credited {
			want += amounts[idx]
		}

		got, err := store.GetSession(ctx, session.SessionID)
		require.NoError(rt, err)
		require.Equal(rt, want, got.DepositSats)
	})
}
