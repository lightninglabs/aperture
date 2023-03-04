package challenger

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

var (
	defaultTimeout = 100 * time.Millisecond
)

func newChallenger() (*LndChallenger, *MockInvoiceClient, chan error) {
	mockClient := &MockInvoiceClient{
		updateChan: make(chan *lnrpc.Invoice),
		errChan:    make(chan error, 1),
		quit:       make(chan struct{}),
	}
	genInvoiceReq := func(price int64) (*lnrpc.Invoice, error) {
		return newInvoice(lntypes.ZeroHash, 99, lnrpc.Invoice_OPEN),
			nil
	}
	invoicesMtx := &sync.Mutex{}
	mainErrChan := make(chan error)
	return &LndChallenger{
		client:        mockClient,
		genInvoiceReq: genInvoiceReq,
		invoiceStates: make(map[lntypes.Hash]lnrpc.Invoice_InvoiceState),
		quit:          make(chan struct{}),
		invoicesMtx:   invoicesMtx,
		invoicesCond:  sync.NewCond(invoicesMtx),
		errChan:       mainErrChan,
	}, mockClient, mainErrChan
}

func TestLndChallenger(t *testing.T) {
	t.Parallel()

	// First of all, test that the NewLndChallenger doesn't allow a nil
	// invoice generator function.
	errChan := make(chan error)
	_, err := NewLndChallenger(nil, nil, errChan)
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
	err = c.Start()
	require.NoError(t, err)
	require.Equal(t, 1, len(c.invoiceStates))
	require.Equal(t, lnrpc.Invoice_OPEN, c.invoiceStates[lntypes.ZeroHash])
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
	// challenger reports it on the main error channel to cause a shutdown
	// of aperture. The mock's error channel is buffered so we can send
	// directly.
	invoiceMock.errChan <- fmt.Errorf("an expected error")
	select {
	case err := <-mainErrChan:
		require.Error(t, err)

		// Make sure that the goroutine exited.
		done := make(chan struct{})
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

	invoiceMock.stop()
	c.Stop()
}
