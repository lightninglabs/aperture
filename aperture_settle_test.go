package aperture

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/aperturedb"
	"github.com/lightninglabs/aperture/challenger"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// untrackedClient mints invoices but cannot track them, like the Wavelength
// client.
type untrackedClient struct{}

var _ challenger.InvoiceClient = (*untrackedClient)(nil)
var _ challenger.InvoiceTracker = (*untrackedClient)(nil)

func (untrackedClient) TracksInvoices() bool { return false }

func (untrackedClient) ListInvoices(context.Context,
	*lnrpc.ListInvoiceRequest, ...grpc.CallOption) (
	*lnrpc.ListInvoiceResponse, error) {

	return nil, fmt.Errorf("unsupported")
}

func (untrackedClient) SubscribeInvoices(context.Context,
	*lnrpc.InvoiceSubscription, ...grpc.CallOption) (
	lnrpc.Lightning_SubscribeInvoicesClient, error) {

	return nil, fmt.Errorf("unsupported")
}

func (untrackedClient) AddInvoice(context.Context, *lnrpc.Invoice,
	...grpc.CallOption) (*lnrpc.AddInvoiceResponse, error) {

	return nil, fmt.Errorf("unsupported")
}

// TestWavelengthPaidCredentialSettlesTransaction wires the real transaction
// store into the challenger the way the Wavelength authenticator branch does,
// and checks that verifying a paid credential settles exactly its row while
// an unpaid challenge, minted at the same time, stays pending.
func TestWavelengthPaidCredentialSettlesTransaction(t *testing.T) {
	t.Parallel()

	db := aperturedb.NewTestDB(t)
	store := aperturedb.NewL402TransactionsStore(
		aperturedb.NewTransactionExecutor(
			db.BaseDB, func(tx *sql.Tx) aperturedb.L402TransactionsDB {
				return db.WithTx(tx)
			},
		),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	paidHash := sha256.Sum256([]byte("paid"))
	unpaidHash := sha256.Sum256([]byte("unpaid"))
	for i, h := range [][32]byte{paidHash, unpaidHash} {
		tokenID := sha256.Sum256([]byte{byte(i)})
		require.NoError(t, store.RecordTransaction(
			ctx, tokenID[:], h[:], "svc", 100, nil,
		))
	}

	c, err := challenger.NewLndChallenger(
		untrackedClient{}, 10, func(int64) (*lnrpc.Invoice, error) {
			return &lnrpc.Invoice{Value: 100}, nil
		}, context.Background, make(chan error), false,
		buildChallengerOpts(store)...,
	)
	require.NoError(t, err)

	// Verifying twice must be harmless.
	for i := 0; i < 2; i++ {
		require.NoError(t, c.VerifyInvoiceStatus(
			lntypes.Hash(paidHash), lnrpc.Invoice_SETTLED,
			time.Second,
		))
	}

	state := func(h [32]byte) string {
		txns, err := store.ListTransactions(ctx, 10, 0)
		require.NoError(t, err)
		for _, txn := range txns {
			if string(txn.PaymentHash) == string(h[:]) {
				return txn.State
			}
		}
		t.Fatalf("transaction not found")

		return ""
	}

	// The write happens on the challenger's settlement worker.
	require.Eventually(t, func() bool {
		return state(paidHash) == "settled"
	}, 5*time.Second, 10*time.Millisecond)
	c.Stop()

	require.Equal(t, "pending", state(unpaidHash))
}
