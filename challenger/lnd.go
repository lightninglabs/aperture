package challenger

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
)

const (
	// defaultListInvoicesBatchSize is the default number of invoices to fetch
	// in each ListInvoices call during the historical load.
	defaultListInvoicesBatchSize = 1000
)

var (
	// defaultInitialLoadTimeout is the maximum time we wait for the initial
	// batch of invoices to be loaded from lnd before allowing state checks
	// to proceed or fail.
	defaultInitialLoadTimeout = 10 * time.Second
)

// LndChallenger is a challenger that uses an lnd backend to create new LSAT
// payment challenges.
type LndChallenger struct {
	client        InvoiceClient
	clientCtx     func() context.Context
	genInvoiceReq InvoiceRequestGenerator
	batchSize     int // Added batchSize

	invoiceStore   *InvoiceStateStore
	invoicesCancel func()

	errChan chan<- error

	quit chan struct{}
	wg   sync.WaitGroup
}

// A compile time flag to ensure the LndChallenger satisfies the Challenger
// interface.
var _ Challenger = (*LndChallenger)(nil)

// NewLndChallenger creates a new challenger that uses the given connection to
// an lnd backend to create payment challenges.
func NewLndChallenger(client InvoiceClient, batchSize int,
	genInvoiceReq InvoiceRequestGenerator,
	ctxFunc func() context.Context,
	errChan chan<- error) (*LndChallenger, error) {

	// Make sure we have a valid context function. This will be called to
	// create a new context for each call to the lnd client.
	if ctxFunc == nil {
		ctxFunc = context.Background
	}

	if genInvoiceReq == nil {
		return nil, fmt.Errorf("genInvoiceReq cannot be nil")
	}

	// Use default batch size if zero or negative is provided.
	if batchSize <= 0 {
		batchSize = defaultListInvoicesBatchSize
	}

	quitChan := make(chan struct{})
	challenger := &LndChallenger{
		client:         client,
		clientCtx:      ctxFunc,
		genInvoiceReq:  genInvoiceReq,
		batchSize:      batchSize,
		invoiceStore:   NewInvoiceStateStore(quitChan),
		invoicesCancel: func() {},
		quit:           quitChan,
		errChan:        errChan,
	}

	// Start the background loading/subscription process.
	challenger.Start()

	return challenger, nil
}

// Start launches the background process to load historical invoices and
// subscribe to new invoice updates concurrently. This method returns
// immediately.
func (l *LndChallenger) Start() {
	log.Infof("Starting LND challenger background tasks...")

	// Use a short timeout context for this initial call.
	ctxIdx, cancelIdx := context.WithTimeout(l.clientCtx(), 30*time.Second)
	defer cancelIdx()

	addIndex := uint64(0)
	settleIndex := uint64(0)

	log.Debugf("Querying latest invoice indices...")
	latestInvoiceResp, err := l.client.ListInvoices(
		ctxIdx, &lnrpc.ListInvoiceRequest{
			NumMaxInvoices: 1, // Only need the latest one
			Reversed:       true,
		},
	)
	if err != nil {
		// Don't fail startup entirely, just log and proceed with 0
		// indices. The historical load will catch up.
		log.Errorf("Failed to get latest invoice indices, "+
			"subscribing from beginning (error: %v)", err)
	} else if len(latestInvoiceResp.Invoices) > 0 {
		// Indices are only meaningful if we actually got an invoice.
		latestInvoice := latestInvoiceResp.Invoices[0]
		addIndex = latestInvoice.AddIndex
		settleIndex = latestInvoice.SettleIndex

		log.Infof("Latest indices found: add=%d, settle=%d",
			addIndex, settleIndex)
	} else {
		log.Infof("No existing invoices found, subscribing " +
			"from beginning.")
	}

	cancelIdx()

	// We'll launch our first goroutine to load the historical invoices in
	// the background.
	l.wg.Add(1)
	go l.loadHistoricalInvoices()

	// We'll launch our second goroutine to subscribe to new invoices in to
	// populate the invoice store with new updates.
	l.wg.Add(1)
	go l.subscribeToInvoices(addIndex, settleIndex)

	log.Infof("LND challenger background tasks launched.")
}

// loadHistoricalInvoices fetches all past invoices relevant using pagination
// and updates the invoice store. It marks the initial load complete upon
// finishing. This runs in a goroutine.
func (l *LndChallenger) loadHistoricalInvoices() {
	defer l.wg.Done()

	log.Infof("Starting historical invoice loading "+
		"(batch size %d)...", l.batchSize)

	// Use a background context for the potentially long-running list calls.
	// Allow it to be cancelled by Stop() via the main quit channel.
	ctxList, cancelList := context.WithCancel(l.clientCtx())
	defer cancelList()

	// Goroutine to cancel the list context if quit signal is received.
	go func() {
		select {
		case <-l.quit:
			log.Warnf("Shutdown signal received, cancelling " +
				"historical invoice list context.")
			cancelList()

		case <-ctxList.Done():
		}
	}()

	startTime := time.Now()
	numInvoicesLoaded := 0
	indexOffset := uint64(0)

	for {
		// Check for shutdown signal before each batch.
		select {
		case <-l.quit:
			log.Warnf("Shutdown signal received during " +
				"historical invoice loading.")

			// Mark load complete anyway so waiters don't block
			// indefinitely.
			l.invoiceStore.MarkInitialLoadComplete()
			return
		default:
		}

		log.Debugf("Querying invoices batch starting from "+
			"index %d", indexOffset)

		req := &lnrpc.ListInvoiceRequest{
			IndexOffset:    indexOffset,
			NumMaxInvoices: uint64(l.batchSize),
		}

		invoiceResp, err := l.client.ListInvoices(ctxList, req)

		if err != nil {
			// If context was cancelled by shutdown, it's not a
			// fatal startup error.
			if strings.Contains(err.Error(), context.Canceled.Error()) {
				log.Warnf("Historical invoice loading " +
					"cancelled by shutdown.")

				l.invoiceStore.MarkInitialLoadComplete()
				return
			}
			log.Errorf("Failed to list invoices batch "+
				"(offset %d): %v", indexOffset, err)

			// Signal fatal error to the main application.
			select {
			case l.errChan <- fmt.Errorf("failed historical "+
				"invoice load batch: %w", err):
			case <-l.quit: // Don't block if shutting down
			}

			// Mark load complete on error so waiters don't block
			// indefinitely.
			l.invoiceStore.MarkInitialLoadComplete()
			return
		}

		// Process the received batch.
		invoicesInBatch := len(invoiceResp.Invoices)

		log.Debugf("Received %d invoices in batch (offset %d)",
			invoicesInBatch, indexOffset)

		fmt.Println("Loading incoies: ", spew.Sdump(invoiceResp.Invoices))

		for _, invoice := range invoiceResp.Invoices {
			// Some invoices like AMP invoices may not have a
			// payment hash populated.
			if invoice.RHash == nil {
				continue
			}

			hash, err := lntypes.MakeHash(invoice.RHash)
			if err != nil {
				log.Errorf("Error parsing invoice hash "+
					"during initial load: %v. Skipping "+
					"invoice.", err)
				continue
			}

			// Don't track the state of irrelevant invoices.
			if invoiceIrrelevant(invoice) {
				continue
			}

			l.invoiceStore.SetState(hash, invoice.State)
			numInvoicesLoaded++
		}

		// If this batch was empty or less than max, we're done with
		// history. LND documentation suggests LastIndexOffset is the
		// index of the *last* invoice returned. If no invoices
		// returned, break. If NumMaxInvoices was returned, continue
		// from LastIndexOffset. If < NumMaxInvoices returned, we are
		// also done.
		if invoicesInBatch == 0 || invoicesInBatch < l.batchSize {
			log.Debugf("Last batch processed (%d invoices), "+
				"stopping pagination.", invoicesInBatch)
			break
		}

		// Prepare for the next batch.
		indexOffset = invoiceResp.LastIndexOffset
		log.Debugf("Processed batch, %d invoices loaded so "+
			"far. Next index offset: %d",
			numInvoicesLoaded, indexOffset)
	}

	loadDuration := time.Since(startTime)

	log.Infof("Finished historical invoice loading. Loaded %d "+
		"relevant invoices in %v.", numInvoicesLoaded,
		loadDuration)

	// Mark the initial load as complete *only after* all pages are
	// processed.
	l.invoiceStore.MarkInitialLoadComplete()
}

// subscribeToInvoices sets up the invoice subscription stream and starts the
// reader goroutine. This runs in a goroutine managed by Start.
func (l *LndChallenger) subscribeToInvoices(addIndex, settleIndex uint64) {
	defer l.wg.Done()

	// We need a separate context for the subscription stream, managed by
	// invoicesCancel.
	ctxSub, cancelSub := context.WithCancel(l.clientCtx())
	defer func() {
		// Only call cancelSub if l.invoicesCancel hasn't been assigned
		// yet (meaning subscription failed or shutdown happened before
		// success). If l.invoicesCancel was assigned, Stop() will
		// handle cancellation.
		if l.invoicesCancel == nil {
			cancelSub()
		}
	}()

	// Check for immediate shutdown before attempting subscription.
	select {
	case <-l.quit:
		log.Warnf("Shutdown signal received before starting " +
			"invoice subscription.")
		return
	default:
	}

	log.Infof("Attempting to subscribe to invoice updates starting "+
		"from add_index=%d, settle_index=%d", addIndex, settleIndex)

	subscriptionResp, err := l.client.SubscribeInvoices(
		ctxSub, &lnrpc.InvoiceSubscription{
			AddIndex:    addIndex,
			SettleIndex: settleIndex,
		},
	)
	if err != nil {
		// If context was cancelled by shutdown, it's not a fatal error.
		if strings.Contains(err.Error(), context.Canceled.Error()) {
			log.Warnf("Invoice subscription cancelled " +
				"during setup by shutdown.")
			return
		}

		log.Errorf("Failed to subscribe to invoices: %v", err)
		select {
		case l.errChan <- fmt.Errorf("failed invoice "+
			"subscription: %w", err):

		case <-l.quit:
		}
		return
	}

	// Store the cancel function *only after* SubscribeInvoices succeeds.
	l.invoicesCancel = cancelSub

	log.Infof("Successfully subscribed to invoice updates.")

	// Start the goroutine to read from the subscription stream. Add to
	// WaitGroup *before* launching. This WG count belongs to the
	// readInvoiceStream lifecycle, managed by this parent goroutine.
	l.wg.Add(1)
	go func() {
		// Ensure Done is called regardless of how readInvoiceStream
		// exits.
		defer l.wg.Done()

		// Ensure the subscription context is cancelled when this reader
		// goroutine exits. Calling the stored l.invoicesCancel ensures
		// Stop() can also cancel it.
		defer l.invoicesCancel()
		l.readInvoiceStream(subscriptionResp)
	}()

	log.Infof("Invoice subscription reader started.")

	// Keep this goroutine alive until quit signal to manage
	// readInvoiceStream.
	<-l.quit
	log.Infof("Invoice subscription manager shutting down.")
}

// readInvoiceStream reads the invoice update messages sent on the stream until
// the stream is aborted or the challenger is shutting down.
// This runs in a goroutine managed by subscribeToInvoices.
func (l *LndChallenger) readInvoiceStream(
	stream lnrpc.Lightning_SubscribeInvoicesClient) {

	for {
		// In case we receive the shutdown signal right after receiving
		// an update, we can exit early.
		select {
		case <-l.quit:
			return
		default:
		}

		// Wait for an update to arrive. This will block until either a
		// message receives, an error occurs or the underlying context
		// is canceled (which will also result in an error).
		invoice, err := stream.Recv()
		switch {

		case err == io.EOF:
			// The connection is shutting down, we can't continue
			// to function properly. Signal the error to the main
			// goroutine to force a shutdown/restart.
			select {
			case l.errChan <- err:
			case <-l.quit:
			default:
			}

			return

		case err != nil && strings.Contains(
			err.Error(), context.Canceled.Error(),
		):

			// The context has been canceled, we are shutting down.
			// So no need to forward the error to the main
			// goroutine.
			return

		case err != nil:
			log.Errorf("Received error from invoice subscription: "+
				"%v", err)

			// The connection is faulty, we can't continue to
			// function properly. Signal the error to the main
			// goroutine to force a shutdown/restart.
			select {
			case l.errChan <- err:
			case <-l.quit:
			default:
			}

			return

		default:
		}

		// Some invoices like AMP invoices may not have a payment hash
		// populated.
		if invoice.RHash == nil {
			continue
		}

		hash, err := lntypes.MakeHash(invoice.RHash)
		if err != nil {
			log.Errorf("Error parsing invoice hash: %v", err)
			return
		}

		if invoiceIrrelevant(invoice) {
			// Don't keep the state of canceled or expired invoices.
			l.invoiceStore.DeleteState(hash)
		} else {
			l.invoiceStore.SetState(hash, invoice.State)
		}
	}
}

// Stop shuts down the challenger.
func (l *LndChallenger) Stop() {
	log.Infof("Stopping LND challenger...")
	// Signal all goroutines to exit.
	close(l.quit)

	// Cancel the subscription context if it exists and was set.
	// invoicesCancel is initialized to a no-op, so safe to call always.
	l.invoicesCancel()

	// Wait for all background goroutines (loadHistorical, subscribeToInvoices,
	// and readInvoiceStream) to finish.
	l.wg.Wait()
	log.Infof("LND challenger stopped.")
}

// NewChallenge creates a new LSAT payment challenge, returning a payment
// request (invoice) and the corresponding payment hash.
//
// NOTE: This is part of the mint.Challenger interface.
func (l *LndChallenger) NewChallenge(price int64) (string, lntypes.Hash,
	error) {

	// Obtain a new invoice from lnd first. We need to know the payment hash
	// so we can add it as a caveat to the macaroon.
	invoice, err := l.genInvoiceReq(price)
	if err != nil {
		log.Errorf("Error generating invoice request: %v", err)
		return "", lntypes.ZeroHash, err
	}

	ctx := l.clientCtx()
	response, err := l.client.AddInvoice(ctx, invoice)
	if err != nil {
		log.Errorf("Error adding invoice: %v", err)
		return "", lntypes.ZeroHash, err
	}

	paymentHash, err := lntypes.MakeHash(response.RHash)
	if err != nil {
		log.Errorf("Error parsing payment hash: %v", err)
		return "", lntypes.ZeroHash, err
	}

	return response.PaymentRequest, paymentHash, nil
}

// VerifyInvoiceStatus checks that an invoice identified by a payment hash has
// the desired status. It waits until the desired status is reached or the
// given timeout occurs. It also handles waiting for the initial invoice load
// if necessary.
//
// NOTE: This is part of the auth.InvoiceChecker interface.
func (l *LndChallenger) VerifyInvoiceStatus(hash lntypes.Hash,
	state lnrpc.Invoice_InvoiceState, timeout time.Duration) error {

	// Prevent the challenger from being shut down while we're still waiting
	// for status updates. Add to WG *before* calling wait.
	l.wg.Add(1)
	defer l.wg.Done()

	// Check for immediate shutdown signal before potentially blocking.
	select {
	case <-l.quit:
		return fmt.Errorf("challenger shutting down")
	default:
	}

	// Delegate the waiting logic to the invoice store.
	// We use a default timeout for the initial load wait, and the provided
	// timeout for the specific state wait.
	err := l.invoiceStore.WaitForState(
		hash, state, defaultInitialLoadTimeout, timeout,
	)
	if err != nil {
		// Add context to the error message.
		return fmt.Errorf("error verifying invoice status for hash %v "+
			"(target state %v): %w", hash, state, err)
	}

	return nil
}

// invoiceIrrelevant returns true if an invoice is nil, canceled or non-settled
// and expired.
func invoiceIrrelevant(invoice *lnrpc.Invoice) bool {
	if invoice == nil || invoice.State == lnrpc.Invoice_CANCELED {
		return true
	}

	creation := time.Unix(invoice.CreationDate, 0)
	expiration := creation.Add(time.Duration(invoice.Expiry) * time.Second)
	expired := time.Now().After(expiration)

	notSettled := invoice.State == lnrpc.Invoice_OPEN ||
		invoice.State == lnrpc.Invoice_ACCEPTED

	return expired && notSettled
}
