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

	mu              sync.Mutex
	lastAddIndex    uint64
	subscribeCalled chan struct{}
}

// ListInvoices returns a paginated list of all invoices known to lnd.
func (m *mockInvoiceClient) ListInvoices(_ context.Context,
	r *lnrpc.ListInvoiceRequest,
	_ ...grpc.CallOption) (*lnrpc.ListInvoiceResponse, error) {

	if r.IndexOffset >= uint64(len(m.invoices)) {
		return &lnrpc.ListInvoiceResponse{}, nil
	}

	endIndex := r.IndexOffset + r.NumMaxInvoices
	if endIndex > uint64(len(m.invoices)) {
		endIndex = uint64(len(m.invoices))
	}

	return &lnrpc.ListInvoiceResponse{
		Invoices:        m.invoices[r.IndexOffset:endIndex],
		LastIndexOffset: endIndex,
	}, nil
}

// SubscribeInvoices subscribes to updates on invoices.
func (m *mockInvoiceClient) SubscribeInvoices(_ context.Context,
	in *lnrpc.InvoiceSubscription, _ ...grpc.CallOption) (
	lnrpc.Lightning_SubscribeInvoicesClient, error) {

	m.mu.Lock()
	m.lastAddIndex = in.AddIndex
	m.mu.Unlock()

	// Signal that SubscribeInvoices has been called.
	select {
	case <-m.subscribeCalled:
	default:
		close(m.subscribeCalled)
	}

	return &invoiceStreamMock{
		updateChan: m.updateChan,
		errChan:    m.errChan,
		quit:       m.quit,
	}, nil
}

// getLastAddIndex returns the last add index in a thread-safe manner.
func (m *mockInvoiceClient) getLastAddIndex() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastAddIndex
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
		updateChan:      make(chan *lnrpc.Invoice),
		errChan:         make(chan error, 1),
		quit:            make(chan struct{}),
		subscribeCalled: make(chan struct{}),
	}
	genInvoiceReq := func(price int64) (*lnrpc.Invoice, error) {
		return newInvoice(lntypes.ZeroHash, 99, lnrpc.Invoice_OPEN),
			nil
	}

	mainErrChan := make(chan error)
	quitChan := make(chan struct{})
	challenger := &LndChallenger{
		client:         mockClient,
		batchSize:      1,
		clientCtx:      context.Background,
		genInvoiceReq:  genInvoiceReq,
		invoiceStore:   NewInvoiceStateStore(quitChan),
		invoicesCancel: func() {},
		quit:           quitChan,
		errChan:        mainErrChan,
		strictVerify:   true,
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

// flexibleMockInvoiceClient is a more configurable mock that supports error
// injection for ListInvoices and SubscribeInvoices, and correctly handles the
// Reversed flag used by Start() for the initial index query.
type flexibleMockInvoiceClient struct {
	invoices   []*lnrpc.Invoice
	updateChan chan *lnrpc.Invoice
	errChan    chan error
	quit       chan struct{}

	lastAddIndex uint64

	// listInvoicesErr, if non-nil, is returned by ListInvoices.
	listInvoicesErr error

	// listCallCount tracks how many times ListInvoices has been called.
	listCallCount int

	// listInvoicesErrAfterN causes ListInvoices to return an error after
	// the Nth successful call.
	listInvoicesErrAfterN int

	// subscribeErr, if non-nil, is returned by SubscribeInvoices.
	subscribeErr error

	// subscribeCalled is closed when SubscribeInvoices is called. This
	// allows tests to synchronize with the subscription goroutine to
	// avoid races on fields like invoicesCancel.
	subscribeCalled chan struct{}

	mu sync.Mutex
}

// ListInvoices returns a paginated list of all invoices known to lnd.
// It correctly handles the Reversed flag used by Start() and supports
// error injection.
func (m *flexibleMockInvoiceClient) ListInvoices(_ context.Context,
	r *lnrpc.ListInvoiceRequest,
	_ ...grpc.CallOption) (*lnrpc.ListInvoiceResponse, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	m.listCallCount++

	if m.listInvoicesErr != nil {
		return nil, m.listInvoicesErr
	}

	if m.listInvoicesErrAfterN > 0 &&
		m.listCallCount > m.listInvoicesErrAfterN {

		return nil, fmt.Errorf("injected list invoices error")
	}

	// Handle the Reversed query used by Start() to get the latest
	// invoice.
	if r.Reversed {
		if len(m.invoices) == 0 {
			return &lnrpc.ListInvoiceResponse{}, nil
		}

		last := m.invoices[len(m.invoices)-1]
		return &lnrpc.ListInvoiceResponse{
			Invoices: []*lnrpc.Invoice{last},
		}, nil
	}

	if r.IndexOffset >= uint64(len(m.invoices)) {
		return &lnrpc.ListInvoiceResponse{}, nil
	}

	endIndex := r.IndexOffset + r.NumMaxInvoices
	if endIndex > uint64(len(m.invoices)) {
		endIndex = uint64(len(m.invoices))
	}

	return &lnrpc.ListInvoiceResponse{
		Invoices:        m.invoices[r.IndexOffset:endIndex],
		LastIndexOffset: endIndex,
	}, nil
}

// SubscribeInvoices subscribes to updates on invoices.
func (m *flexibleMockInvoiceClient) SubscribeInvoices(_ context.Context,
	in *lnrpc.InvoiceSubscription, _ ...grpc.CallOption) (
	lnrpc.Lightning_SubscribeInvoicesClient, error) {

	// Signal that SubscribeInvoices has been called, regardless of
	// success or failure.
	defer func() {
		select {
		case <-m.subscribeCalled:
		default:
			close(m.subscribeCalled)
		}
	}()

	if m.subscribeErr != nil {
		return nil, m.subscribeErr
	}

	m.mu.Lock()
	m.lastAddIndex = in.AddIndex
	m.mu.Unlock()

	return &invoiceStreamMock{
		updateChan: m.updateChan,
		errChan:    m.errChan,
		quit:       m.quit,
	}, nil
}

// getLastAddIndex returns the last add index passed to SubscribeInvoices in a
// thread-safe manner.
func (m *flexibleMockInvoiceClient) getLastAddIndex() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastAddIndex
}

// waitForSubscription blocks until SubscribeInvoices has been called or the
// timeout expires. This is useful for tests that need to synchronize with the
// subscription goroutine before sending updates or checking state. Returns
// true if the subscription was called, false on timeout.
func (m *flexibleMockInvoiceClient) waitForSubscription(
	timeout time.Duration) bool {

	select {
	case <-m.subscribeCalled:
		return true
	case <-time.After(timeout):
		return false
	}
}

// AddInvoice adds a new invoice to lnd.
func (m *flexibleMockInvoiceClient) AddInvoice(_ context.Context,
	in *lnrpc.Invoice,
	_ ...grpc.CallOption) (*lnrpc.AddInvoiceResponse, error) {

	m.mu.Lock()
	defer m.mu.Unlock()

	m.invoices = append(m.invoices, in)

	return &lnrpc.AddInvoiceResponse{
		RHash:          in.RHash,
		PaymentRequest: in.PaymentRequest,
		AddIndex:       uint64(len(m.invoices) - 1),
	}, nil
}

func (m *flexibleMockInvoiceClient) stop() {
	close(m.quit)
}

// newFlexibleChallenger creates a new LndChallenger with a flexibleMock for
// testing Start(), batch loading, and shutdown scenarios. It does NOT call
// Start() automatically so the caller controls when to start.
func newFlexibleChallenger(
	batchSize int, strictVerify bool,
) (*LndChallenger, *flexibleMockInvoiceClient, chan error) {

	mockClient := &flexibleMockInvoiceClient{
		updateChan:      make(chan *lnrpc.Invoice),
		errChan:         make(chan error, 1),
		quit:            make(chan struct{}),
		subscribeCalled: make(chan struct{}),
	}

	genInvoiceReq := func(price int64) (*lnrpc.Invoice, error) {
		return newInvoice(lntypes.ZeroHash, 99, lnrpc.Invoice_OPEN),
			nil
	}

	mainErrChan := make(chan error, 5)
	quitChan := make(chan struct{})
	challenger := &LndChallenger{
		client:         mockClient,
		batchSize:      batchSize,
		clientCtx:      context.Background,
		genInvoiceReq:  genInvoiceReq,
		invoiceStore:   NewInvoiceStateStore(quitChan),
		invoicesCancel: func() {},
		quit:           quitChan,
		errChan:        mainErrChan,
		strictVerify:   strictVerify,
	}

	return challenger, mockClient, mainErrChan
}

// TestLndChallengerStartStrictVerifyFalse verifies that when strictVerify is
// false, Start() marks the initial load as complete immediately without
// launching background goroutines or making any ListInvoices calls.
func TestLndChallengerStartStrictVerifyFalse(t *testing.T) {
	t.Parallel()

	c, mock, _ := newFlexibleChallenger(10, false)

	c.Start()

	// The initial load should be marked complete immediately.
	require.True(t, c.invoiceStore.IsInitialLoadComplete())

	// No ListInvoices calls should have been made since we skip loading.
	mock.mu.Lock()
	require.Equal(t, 0, mock.listCallCount)
	mock.mu.Unlock()

	// VerifyInvoiceStatus should return nil (skip check) for any hash.
	err := c.VerifyInvoiceStatus(
		lntypes.Hash{1}, lnrpc.Invoice_SETTLED,
		100*time.Millisecond,
	)
	require.NoError(t, err)

	c.Stop()
}

// TestLndChallengerPaginatedHistoricalLoad verifies that the historical
// invoice loader correctly paginates through multiple batches of invoices and
// populates the invoice store.
func TestLndChallengerPaginatedHistoricalLoad(t *testing.T) {
	t.Parallel()

	const batchSize = 3
	const numInvoices = 10

	c, mock, _ := newFlexibleChallenger(batchSize, true)

	// Pre-populate the mock with invoices.
	for i := 0; i < numInvoices; i++ {
		hash := lntypes.Hash{byte(i)}
		mock.invoices = append(
			mock.invoices,
			newInvoice(hash, uint64(i), lnrpc.Invoice_OPEN),
		)
	}

	c.Start()

	// Wait for the initial load to complete.
	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	// Verify all invoices were loaded into the store.
	for i := 0; i < numInvoices; i++ {
		hash := lntypes.Hash{byte(i)}
		state, exists := c.invoiceStore.GetState(hash)
		require.True(t, exists, "invoice %d should be in store", i)
		require.Equal(t, lnrpc.Invoice_OPEN, state)
	}

	// The mock should have been called multiple times for pagination.
	// 1 call for the initial reversed query in Start() +
	// ceil(10/3) = 4 batches for loading, but the last batch may be
	// partial and a final empty one triggers the stop condition.
	mock.mu.Lock()
	// At minimum we need 1 (reversed) + ceil(10/3) batches + 1 empty =
	// 1 + 4 + 1 = 6, but the exact count depends on the pagination
	// logic. We just verify it was called more than twice (pagination
	// occurred).
	require.Greater(t, mock.listCallCount, 2,
		"pagination should cause multiple ListInvoices calls")
	mock.mu.Unlock()

	mock.stop()
	c.Stop()
}

// TestLndChallengerHistoricalLoadSkipsIrrelevantInvoices verifies that the
// historical loader does not store canceled or expired invoices.
func TestLndChallengerHistoricalLoadSkipsIrrelevantInvoices(t *testing.T) {
	t.Parallel()

	c, mock, _ := newFlexibleChallenger(100, true)

	// Add a mix of relevant and irrelevant invoices.
	openHash := lntypes.Hash{1}
	settledHash := lntypes.Hash{2}
	canceledHash := lntypes.Hash{3}
	expiredHash := lntypes.Hash{4}
	nilHashInvoice := &lnrpc.Invoice{
		PaymentRequest: "foo",
		RHash:          nil,
		AddIndex:       5,
		State:          lnrpc.Invoice_OPEN,
		CreationDate:   time.Now().Unix(),
		Expiry:         10,
	}

	mock.invoices = []*lnrpc.Invoice{
		newInvoice(openHash, 1, lnrpc.Invoice_OPEN),
		newInvoice(settledHash, 2, lnrpc.Invoice_SETTLED),
		newInvoice(canceledHash, 3, lnrpc.Invoice_CANCELED),
		// An expired, non-settled invoice should be skipped.
		{
			PaymentRequest: "foo",
			RHash:          expiredHash[:],
			AddIndex:       4,
			State:          lnrpc.Invoice_OPEN,
			CreationDate:   time.Now().Add(-1 * time.Hour).Unix(),
			Expiry:         1,
		},
		nilHashInvoice,
	}

	c.Start()

	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	// Open and settled invoices should be present.
	_, exists := c.invoiceStore.GetState(openHash)
	require.True(t, exists, "open invoice should be in store")

	_, exists = c.invoiceStore.GetState(settledHash)
	require.True(t, exists, "settled invoice should be in store")

	// Canceled, expired-open, and nil-hash invoices should NOT be present.
	_, exists = c.invoiceStore.GetState(canceledHash)
	require.False(t, exists, "canceled invoice should not be in store")

	_, exists = c.invoiceStore.GetState(expiredHash)
	require.False(t, exists, "expired open invoice should not be in store")

	mock.stop()
	c.Stop()
}

// TestLndChallengerShutdownDuringHistoricalLoad verifies that closing the quit
// channel during the historical invoice loading process causes the loader to
// exit cleanly and still marks the initial load as complete so waiters do not
// block indefinitely.
func TestLndChallengerShutdownDuringHistoricalLoad(t *testing.T) {
	t.Parallel()

	// Use a large batch size so the load takes multiple rounds. We'll
	// inject a context.Canceled error to simulate shutdown.
	c, mock, _ := newFlexibleChallenger(2, true)

	// Add enough invoices for at least a few batches.
	for i := 0; i < 10; i++ {
		hash := lntypes.Hash{byte(i)}
		mock.invoices = append(
			mock.invoices,
			newInvoice(hash, uint64(i), lnrpc.Invoice_OPEN),
		)
	}

	// Set the error to fire after the first successful ListInvoices call
	// during the paginated load (the reversed call counts as 1).
	mock.mu.Lock()
	mock.listInvoicesErrAfterN = 2
	mock.mu.Unlock()

	c.Start()

	// Wait for the error to propagate or the load to complete.
	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	// The initial load should be marked complete regardless of the error,
	// preventing waiters from blocking forever.
	require.True(t, c.invoiceStore.IsInitialLoadComplete())

	mock.stop()
	c.Stop()
}

// TestLndChallengerShutdownViaQuitDuringLoad verifies that closing the quit
// channel while loadHistoricalInvoices is processing causes it to exit early
// and mark the initial load complete.
func TestLndChallengerShutdownViaQuitDuringLoad(t *testing.T) {
	t.Parallel()

	c, mock, _ := newFlexibleChallenger(1, true)

	// Add many invoices so the load takes many batches.
	for i := 0; i < 100; i++ {
		hash := lntypes.Hash{byte(i)}
		mock.invoices = append(
			mock.invoices,
			newInvoice(hash, uint64(i), lnrpc.Invoice_OPEN),
		)
	}

	c.Start()

	// Give the loader a moment to start, then shut down. We must close
	// the mock's quit channel first so that its Recv() unblocks (the
	// mock does not share the challenger's quit channel).
	time.Sleep(20 * time.Millisecond)
	mock.stop()
	c.Stop()

	// Even after shutdown, the initial load should be marked complete.
	require.True(t, c.invoiceStore.IsInitialLoadComplete())
}

// TestLndChallengerSubscriptionError verifies that when SubscribeInvoices
// fails immediately, the error is propagated to the errChan.
func TestLndChallengerSubscriptionError(t *testing.T) {
	t.Parallel()

	c, mock, mainErrChan := newFlexibleChallenger(100, true)

	// Set the subscription to fail.
	mock.subscribeErr = fmt.Errorf("subscription setup failed")

	c.Start()

	// The subscription error should be reported on the error channel.
	select {
	case err := <-mainErrChan:
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed invoice subscription")
	case <-time.After(3 * time.Second):
		t.Fatal("expected subscription error on errChan")
	}

	// The initial load should still complete despite the subscription
	// failure.
	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	c.Stop()
}

// TestLndChallengerListInvoicesError verifies that when ListInvoices fails
// during historical loading, the error is propagated to the errChan and
// the initial load is still marked complete.
func TestLndChallengerListInvoicesError(t *testing.T) {
	t.Parallel()

	c, mock, mainErrChan := newFlexibleChallenger(100, true)

	// Set ListInvoices to always fail. This will affect both the initial
	// reversed call in Start() and the paginated calls in
	// loadHistoricalInvoices(). The Start() method logs the first error
	// and continues with zero indices; then loadHistoricalInvoices() will
	// also fail and push an error to errChan.
	mock.listInvoicesErr = fmt.Errorf("rpc unavailable")

	c.Start()

	// Wait for the error from the historical load to arrive.
	select {
	case err := <-mainErrChan:
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed historical invoice")
	case <-time.After(3 * time.Second):
		t.Fatal("expected list invoices error on errChan")
	}

	// The initial load should be marked complete even after error.
	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	mock.stop()
	c.Stop()
}

// TestLndChallengerVerifyInvoiceStatusShutdown verifies that
// VerifyInvoiceStatus returns a shutdown error when the quit channel is closed.
func TestLndChallengerVerifyInvoiceStatusShutdown(t *testing.T) {
	t.Parallel()

	c, mock, _ := newFlexibleChallenger(100, true)
	c.Start()

	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	// Start a verification in a goroutine, then shut down.
	done := make(chan error, 1)
	go func() {
		done <- c.VerifyInvoiceStatus(
			lntypes.Hash{42}, lnrpc.Invoice_SETTLED,
			5*time.Second,
		)
	}()

	// Give the verifier time to start waiting.
	time.Sleep(30 * time.Millisecond)

	// Stop the challenger, which should cause the verifier to abort.
	mock.stop()
	c.Stop()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("VerifyInvoiceStatus did not return after shutdown")
	}
}

// TestLndChallengerStartWithNoExistingInvoices verifies that Start() works
// correctly when there are no existing invoices in lnd (empty response from
// the reversed ListInvoices call).
func TestLndChallengerStartWithNoExistingInvoices(t *testing.T) {
	t.Parallel()

	c, mock, _ := newFlexibleChallenger(100, true)

	// No invoices pre-populated in the mock.
	c.Start()

	// The initial load should complete quickly since there are no
	// invoices to paginate through.
	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	// Give the subscription goroutine time to call SubscribeInvoices
	// since WaitForInitialLoad only ensures the historical load is done.
	time.Sleep(50 * time.Millisecond)

	// Verify that the subscription was started at index 0.
	require.Equal(t, uint64(0), mock.getLastAddIndex())

	mock.stop()
	c.Stop()
}

// TestLndChallengerInvoiceUpdateAfterHistoricalLoad verifies that invoice
// updates received via the subscription stream after the historical load
// completes are correctly reflected in the store.
func TestLndChallengerInvoiceUpdateAfterHistoricalLoad(t *testing.T) {
	t.Parallel()

	c, mock, _ := newFlexibleChallenger(100, true)

	// Pre-populate with one invoice.
	hash := lntypes.Hash{1}
	mock.invoices = append(
		mock.invoices,
		newInvoice(hash, 1, lnrpc.Invoice_OPEN),
	)

	c.Start()

	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	// Verify the initial state.
	state, exists := c.invoiceStore.GetState(hash)
	require.True(t, exists)
	require.Equal(t, lnrpc.Invoice_OPEN, state)

	// Send an update via the subscription stream.
	mock.updateChan <- newInvoice(hash, 1, lnrpc.Invoice_SETTLED)

	// The store should eventually reflect the new state.
	err = c.VerifyInvoiceStatus(
		hash, lnrpc.Invoice_SETTLED, 2*time.Second,
	)
	require.NoError(t, err)

	mock.stop()
	c.Stop()
}

// TestLndChallengerCanceledInvoiceDeletedFromStore verifies that when a
// subscription update reports a canceled invoice, it is deleted from the
// invoice store.
func TestLndChallengerCanceledInvoiceDeletedFromStore(t *testing.T) {
	t.Parallel()

	c, mock, _ := newFlexibleChallenger(100, true)

	hash := lntypes.Hash{1}
	mock.invoices = append(
		mock.invoices,
		newInvoice(hash, 1, lnrpc.Invoice_OPEN),
	)

	c.Start()

	err := c.invoiceStore.WaitForInitialLoad(3 * time.Second)
	require.NoError(t, err)

	// Confirm the invoice is in the store.
	_, exists := c.invoiceStore.GetState(hash)
	require.True(t, exists)

	// Send a cancellation update.
	mock.updateChan <- newInvoice(hash, 1, lnrpc.Invoice_CANCELED)

	// Give the subscription reader time to process.
	time.Sleep(50 * time.Millisecond)

	// The invoice should now be deleted from the store.
	_, exists = c.invoiceStore.GetState(hash)
	require.False(t, exists, "canceled invoice should be deleted from store")

	mock.stop()
	c.Stop()
}

// TestLndChallengerDefaultBatchSize verifies that NewLndChallenger uses the
// default batch size when zero or negative is provided.
func TestLndChallengerDefaultBatchSize(t *testing.T) {
	t.Parallel()

	mockClient := &flexibleMockInvoiceClient{
		updateChan:      make(chan *lnrpc.Invoice),
		errChan:         make(chan error, 1),
		quit:            make(chan struct{}),
		subscribeCalled: make(chan struct{}),
	}

	genInvoiceReq := func(price int64) (*lnrpc.Invoice, error) {
		return newInvoice(lntypes.ZeroHash, 1, lnrpc.Invoice_OPEN),
			nil
	}

	mainErrChan := make(chan error, 5)
	c, err := NewLndChallenger(
		mockClient, 0, genInvoiceReq, context.Background,
		mainErrChan, false,
	)
	require.NoError(t, err)
	require.Equal(t, defaultListInvoicesBatchSize, c.batchSize)

	c.Stop()
	close(mockClient.quit)
}

func TestLndChallenger(t *testing.T) {
	t.Parallel()

	// First of all, test that the NewLndChallenger doesn't allow a nil
	// invoice generator function.
	errChan := make(chan error)
	_, err := NewLndChallenger(nil, 1, nil, nil, errChan, true)
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
	require.Equal(t, uint64(0), invoiceMock.getLastAddIndex())

	// Now we already have an invoice in our lnd mock. When starting the
	// challenger, we should have that invoice in the cache and a
	// subscription that only starts at our faked addIndex.
	c.Start()
	require.NoError(t, err)

	// Wait for the invoices to be loaded.
	c.invoiceStore.WaitForInitialLoad(time.Second * 3)

	// Wait for the subscription goroutine to call SubscribeInvoices so
	// we can safely read the last add index.
	select {
	case <-invoiceMock.subscribeCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("subscription not established before timeout")
	}

	// Verify the initial state using the public method, not direct access.
	require.Equal(t, uint64(99), invoiceMock.getLastAddIndex())
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
		}
	}

	// Finally test that if an error occurs in the invoice subscription the
	// challenger reports it on the main error channel to cause a shutdown.
	// The mock's error channel is buffered so we can send directly.
	expectedErr := fmt.Errorf("an expected error")
	invoiceMock.errChan <- expectedErr
	select {
	case err := <-mainErrChan:
		require.Error(t, err)

	case <-time.After(defaultTimeout):
		t.Fatalf("error not received on main chan before the timeout")
	}

	// Stop the mock client first to close its quit channel used by the
	// store, then stop the challenger. This will cause all background
	// goroutines to exit cleanly.
	invoiceMock.stop()
	c.Stop()
}
