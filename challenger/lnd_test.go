package challenger

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

var (
	defaultTimeout = 100 * time.Millisecond
)

type invoiceStreamMock struct {
	lnrpc.Lightning_SubscribeInvoicesClient

	updateChan chan *lnrpc.Invoice
	errChan    chan error
	quit       chan struct{}
}

func (i *invoiceStreamMock) Recv() (*lnrpc.Invoice, error) {
	select {
	case msg := <-i.updateChan:
		return msg, nil

	case err := <-i.errChan:
		return nil, err

	case <-i.quit:
		return nil, context.Canceled
	}
}

type mockInvoiceClient struct {
	invoices   []*lnrpc.Invoice
	updateChan chan *lnrpc.Invoice
	errChan    chan error
	quit       chan struct{}

	lastAddIndex uint64
}

// ListInvoices returns a paginated list of all invoices known to lnd.
func (m *mockInvoiceClient) ListInvoices(_ context.Context,
	_ *lnrpc.ListInvoiceRequest,
	_ ...grpc.CallOption) (*lnrpc.ListInvoiceResponse, error) {

	return &lnrpc.ListInvoiceResponse{
		Invoices: m.invoices,
	}, nil
}

// SubscribeInvoices subscribes to updates on invoices.
func (m *mockInvoiceClient) SubscribeInvoices(_ context.Context,
	in *lnrpc.InvoiceSubscription, _ ...grpc.CallOption) (
	lnrpc.Lightning_SubscribeInvoicesClient, error) {

	m.lastAddIndex = in.AddIndex

	return &invoiceStreamMock{
		updateChan: m.updateChan,
		errChan:    m.errChan,
		quit:       m.quit,
	}, nil
}

// AddInvoice adds a new invoice to lnd.
func (m *mockInvoiceClient) AddInvoice(_ context.Context, in *lnrpc.Invoice,
	_ ...grpc.CallOption) (*lnrpc.AddInvoiceResponse, error) {

	m.invoices = append(m.invoices, in)

	return &lnrpc.AddInvoiceResponse{
		RHash:          in.RHash,
		PaymentRequest: in.PaymentRequest,
		AddIndex:       uint64(len(m.invoices) - 1),
	}, nil
}

func (m *mockInvoiceClient) stop() {
	close(m.quit)
}

func newChallenger() (*LndChallenger, *mockInvoiceClient, chan error) {
	mockClient := &mockInvoiceClient{
		updateChan: make(chan *lnrpc.Invoice),
		errChan:    make(chan error, 1),
		quit:       make(chan struct{}),
	}
	genInvoiceReq := func(price int64) (*lnrpc.Invoice, error) {
		return newInvoice(lntypes.ZeroHash, 99, lnrpc.Invoice_OPEN),
			nil
	}

	mainErrChan := make(chan error)
	quitChan := make(chan struct{})
	challenger := &LndChallenger{
		client:        mockClient,
		clientCtx:     context.Background,
		genInvoiceReq: genInvoiceReq,
		invoiceStore:  NewInvoiceStateStore(quitChan),
		quit:          quitChan,
		errChan:       mainErrChan,
	}

	return challenger, mockClient, mainErrChan
}

func newInvoice(hash lntypes.Hash, addIndex uint64,
	state lnrpc.Invoice_InvoiceState) *lnrpc.Invoice {

	return &lnrpc.Invoice{
		PaymentRequest: "foo",
		RHash:          hash[:],
		AddIndex:       addIndex,
		State:          state,
		CreationDate:   time.Now().Unix(),
		Expiry:         10,
	}
}

func TestLndChallenger(t *testing.T) {
	t.Parallel()

	// First of all, test that the NewLndChallenger doesn't allow a nil
	// invoice generator function.
	errChan := make(chan error)
	_, err := NewLndChallenger(nil, 0, nil, nil, errChan)
	require.Error(t, err)

	// Now mock the lnd backend and create a challenger instance that we can
	// test.
	c, invoiceMock, mainErrChan := newChallenger()

	// Creating a new challenge should add an invoice to the lnd backend.
	req, hash, err := c.NewChallenge(1337)
	require.NoError(t, err)
	require.Equal(t, "foo", req)
	require.Equal(t, lntypes.ZeroHash, hash)
	require.Equal(t, 1, len(invoiceMock.invoices))
	require.Equal(t, uint64(0), invoiceMock.lastAddIndex)

	// Now we already have an invoice in our lnd mock. When starting the
	// challenger, we should have that invoice in the cache and a
	// subscription that only starts at our faked addIndex.
	// In the test setup, Start() is called after the challenger is created
	// by newChallenger, which already pre-populates the store.
	// We'll call Start() again here to ensure the subscription logic runs.
	c.Start()
	require.NoError(t, err)

	// Wait for the invoices to be loaded.
	c.invoiceStore.WaitForInitialLoad(time.Second * 3)

	// Verify the initial state using the public method, not direct access.
	require.Equal(t, uint64(99), invoiceMock.lastAddIndex)
	require.NoError(t, c.VerifyInvoiceStatus(
		lntypes.ZeroHash, lnrpc.Invoice_OPEN, defaultTimeout,
	))
	require.Error(t, c.VerifyInvoiceStatus(
		lntypes.ZeroHash, lnrpc.Invoice_SETTLED, defaultTimeout,
	))

	// Next, let's send an update for a new invoice and make sure it's added
	// to the map.
	hash = lntypes.Hash{77, 88, 99}
	invoiceMock.updateChan <- newInvoice(hash, 123, lnrpc.Invoice_SETTLED)
	require.NoError(t, c.VerifyInvoiceStatus(
		hash, lnrpc.Invoice_SETTLED, defaultTimeout,
	))
	require.Error(t, c.VerifyInvoiceStatus(
		hash, lnrpc.Invoice_OPEN, defaultTimeout,
	))

	// Finally, create a bunch of invoices but only settle the first 5 of
	// them. All others should get a failed invoice state after the timeout.
	var (
		numInvoices = 20
		errors      = make([]error, numInvoices)
		wg          sync.WaitGroup
	)
	for i := 0; i < numInvoices; i++ {
		hash := lntypes.Hash{77, 88, 99, byte(i)}
		invoiceMock.updateChan <- newInvoice(
			hash, 1000+uint64(i), lnrpc.Invoice_OPEN,
		)

		// The verification will block for a certain time. But we want
		// all checks to happen automatically to simulate many parallel
		// requests. So we spawn a goroutine for each invoice check.
		wg.Add(1)
		go func(errIdx int, hash lntypes.Hash) {
			defer wg.Done()

			errors[errIdx] = c.VerifyInvoiceStatus(
				hash, lnrpc.Invoice_SETTLED, defaultTimeout,
			)
		}(i, hash)
	}

	// With all 20 goroutines spinning and waiting for updates, we settle
	// the first 5 invoices.
	for i := 0; i < 5; i++ {
		hash := lntypes.Hash{77, 88, 99, byte(i)}
		invoiceMock.updateChan <- newInvoice(
			hash, 1000+uint64(i), lnrpc.Invoice_SETTLED,
		)
	}

	// Now wait for all checks to finish, then check that the last 15
	// invoices timed out.
	wg.Wait()
	for i := 0; i < numInvoices; i++ {
		if i < 5 {
			require.NoError(t, errors[i])
		} else {
			require.Error(t, errors[i])
			require.Contains(
				t, errors[i].Error(),
				"invoice status not correct before timeout",
			)
		}
	}

	// Finally test that if an error occurs in the invoice subscription the
	// challenger reports it on the main error channel to cause a shutdown.
	// The mock's error channel is buffered so we can send directly.
	expectedErr := fmt.Errorf("an expected error")
	invoiceMock.errChan <- expectedErr
	select {
	case err := <-mainErrChan:
		require.ErrorIs(t, err, expectedErr) // Check if it's the expected error

		// Make sure that the goroutine exited.
		done := make(chan struct{})
		require.Error(t, err)

		// Make sure that the goroutine exited.
		done = make(chan struct{})
		go func() {
			c.wg.Wait()
			done <- struct{}{}
		}()

		select {
		case <-done:

		case <-time.After(defaultTimeout):
			t.Fatalf("wait group didn't finish before timeout")
		}

	case <-time.After(defaultTimeout):
		t.Fatalf("error not received on main chan before the timeout")
	}

	// Stop the mock client first to close its quit channel used by the store
	invoiceMock.stop()
	// Then stop the challenger
	c.Stop()
}
