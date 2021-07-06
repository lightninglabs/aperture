package aperture

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"google.golang.org/grpc"
)

// InvoiceRequestGenerator is a function type that returns a new request for the
// lnrpc.AddInvoice call.
type InvoiceRequestGenerator func(price int64) (*lnrpc.Invoice, error)

// InvoiceClient is an interface that only implements part of a full lnd client,
// namely the part around the invoices we need for the challenger to work.
type InvoiceClient interface {
	// ListInvoices returns a paginated list of all invoices known to lnd.
	ListInvoices(ctx context.Context, in *lnrpc.ListInvoiceRequest,
		opts ...grpc.CallOption) (*lnrpc.ListInvoiceResponse, error)

	// SubscribeInvoices subscribes to updates on invoices.
	SubscribeInvoices(ctx context.Context, in *lnrpc.InvoiceSubscription,
		opts ...grpc.CallOption) (
		lnrpc.Lightning_SubscribeInvoicesClient, error)

	// AddInvoice adds a new invoice to lnd.
	AddInvoice(ctx context.Context, in *lnrpc.Invoice,
		opts ...grpc.CallOption) (*lnrpc.AddInvoiceResponse, error)
}

// LndChallenger is a challenger that uses an lnd backend to create new LSAT
// payment challenges.
type LndChallenger struct {
	client        InvoiceClient
	genInvoiceReq InvoiceRequestGenerator

	invoiceStates  map[lntypes.Hash]lnrpc.Invoice_InvoiceState
	invoicesMtx    *sync.Mutex
	invoicesCancel func()
	invoicesCond   *sync.Cond

	errChan chan<- error

	quit chan struct{}
	wg   sync.WaitGroup
}

// A compile time flag to ensure the LndChallenger satisfies the
// mint.Challenger and auth.InvoiceChecker interface.
var _ mint.Challenger = (*LndChallenger)(nil)
var _ auth.InvoiceChecker = (*LndChallenger)(nil)

const (
	// invoiceMacaroonName is the name of the invoice macaroon belonging
	// to the target lnd node.
	invoiceMacaroonName = "invoice.macaroon"
)

// NewLndChallenger creates a new challenger that uses the given connection
// details to connect to an lnd backend to create payment challenges.
func NewLndChallenger(cfg *AuthConfig, genInvoiceReq InvoiceRequestGenerator,
	errChan chan<- error) (*LndChallenger, error) {

	if genInvoiceReq == nil {
		return nil, fmt.Errorf("genInvoiceReq cannot be nil")
	}

	client, err := lndclient.NewBasicClient(
		cfg.LndHost, cfg.TLSPath, cfg.MacDir, cfg.Network,
		lndclient.MacFilename(invoiceMacaroonName),
	)
	if err != nil {
		return nil, err
	}

	invoicesMtx := &sync.Mutex{}
	return &LndChallenger{
		client:        client,
		genInvoiceReq: genInvoiceReq,
		invoiceStates: make(map[lntypes.Hash]lnrpc.Invoice_InvoiceState),
		invoicesMtx:   invoicesMtx,
		invoicesCond:  sync.NewCond(invoicesMtx),
		quit:          make(chan struct{}),
		errChan:       errChan,
	}, nil
}

// Start starts the challenger's main work which is to keep track of all
// invoices and their states. For that the backing lnd node is queried for all
// invoices on startup and the a subscription to all subsequent invoice updates
// is created.
func (l *LndChallenger) Start() error {
	// These are the default values for the subscription. In case there are
	// no invoices yet, this will instruct lnd to just send us all updates.
	// If there are existing invoices, these indices will be updated to
	// reflect the latest known invoices.
	addIndex := uint64(0)
	settleIndex := uint64(0)

	// Get a list of all existing invoices on startup and add them to our
	// cache. We need to keep track of all invoices, even quite old ones to
	// make sure tokens are valid. But to save space we only keep track of
	// an invoice's state.
	invoiceResp, err := l.client.ListInvoices(
		context.Background(), &lnrpc.ListInvoiceRequest{
			NumMaxInvoices: math.MaxUint64,
		},
	)
	if err != nil {
		return err
	}

	// Advance our indices to the latest known one so we'll only receive
	// updates for new invoices and/or newly settled invoices.
	l.invoicesMtx.Lock()
	for _, invoice := range invoiceResp.Invoices {
		if invoice.AddIndex > addIndex {
			addIndex = invoice.AddIndex
		}
		if invoice.SettleIndex > settleIndex {
			settleIndex = invoice.SettleIndex
		}
		hash, err := lntypes.MakeHash(invoice.RHash)
		if err != nil {
			l.invoicesMtx.Unlock()
			return fmt.Errorf("error parsing invoice hash: %v", err)
		}

		// Don't track the state of canceled or expired invoices.
		if invoiceIrrelevant(invoice) {
			continue
		}
		l.invoiceStates[hash] = invoice.State
	}
	l.invoicesMtx.Unlock()

	// We need to be able to cancel any subscription we make.
	ctxc, cancel := context.WithCancel(context.Background())
	l.invoicesCancel = cancel

	subscriptionResp, err := l.client.SubscribeInvoices(
		ctxc, &lnrpc.InvoiceSubscription{
			AddIndex:    addIndex,
			SettleIndex: settleIndex,
		},
	)
	if err != nil {
		cancel()
		return err
	}

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer cancel()

		l.readInvoiceStream(subscriptionResp)
	}()

	return nil
}

// readInvoiceStream reads the invoice update messages sent on the stream until
// the stream is aborted or the challenger is shutting down.
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

		hash, err := lntypes.MakeHash(invoice.RHash)
		if err != nil {
			log.Errorf("Error parsing invoice hash: %v", err)
			return
		}

		l.invoicesMtx.Lock()
		if invoiceIrrelevant(invoice) {
			// Don't keep the state of canceled or expired invoices.
			delete(l.invoiceStates, hash)
		} else {
			l.invoiceStates[hash] = invoice.State
		}

		// Before releasing the lock, notify our conditions that listen
		// for updates on the invoice state.
		l.invoicesCond.Broadcast()
		l.invoicesMtx.Unlock()
	}
}

// Stop shuts down the challenger.
func (l *LndChallenger) Stop() {
	l.invoicesCancel()
	close(l.quit)
	l.wg.Wait()
}

// NewChallenge creates a new LSAT payment challenge, returning a payment
// request (invoice) and the corresponding payment hash.
//
// NOTE: This is part of the mint.Challenger interface.
func (l *LndChallenger) NewChallenge(price int64) (string, lntypes.Hash, error) {
	// Obtain a new invoice from lnd first. We need to know the payment hash
	// so we can add it as a caveat to the macaroon.
	invoice, err := l.genInvoiceReq(price)
	if err != nil {
		log.Errorf("Error generating invoice request: %v", err)
		return "", lntypes.ZeroHash, err
	}
	ctx := context.Background()
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

// VerifyInvoiceStatus checks that an invoice identified by a payment
// hash has the desired status. To make sure we don't fail while the
// invoice update is still on its way, we try several times until either
// the desired status is set or the given timeout is reached.
//
// NOTE: This is part of the auth.InvoiceChecker interface.
func (l *LndChallenger) VerifyInvoiceStatus(hash lntypes.Hash,
	state lnrpc.Invoice_InvoiceState, timeout time.Duration) error {

	// Prevent the challenger to be shut down while we're still waiting for
	// status updates.
	l.wg.Add(1)
	defer l.wg.Done()

	var (
		condWg         sync.WaitGroup
		doneChan       = make(chan struct{})
		timeoutReached bool
		hasInvoice     bool
		invoiceState   lnrpc.Invoice_InvoiceState
	)

	// First of all, spawn a goroutine that will signal us on timeout.
	// Otherwise if a client subscribes to an update on an invoice that
	// never arrives, and there is no other activity, it would block
	// forever in the condition.
	condWg.Add(1)
	go func() {
		defer condWg.Done()

		select {
		case <-doneChan:
		case <-time.After(timeout):
		case <-l.quit:
		}

		l.invoicesCond.L.Lock()
		timeoutReached = true
		l.invoicesCond.Broadcast()
		l.invoicesCond.L.Unlock()
	}()

	// Now create the main goroutine that blocks until an update is received
	// on the condition.
	condWg.Add(1)
	go func() {
		defer condWg.Done()
		l.invoicesCond.L.Lock()

		// Block here until our condition is met or the allowed time is
		// up. The Wait() will return whenever a signal is broadcast.
		invoiceState, hasInvoice = l.invoiceStates[hash]
		for !(hasInvoice && invoiceState == state) && !timeoutReached {
			l.invoicesCond.Wait()

			// The Wait() above has re-acquired the lock so we can
			// safely access the states map.
			invoiceState, hasInvoice = l.invoiceStates[hash]
		}

		// We're now done.
		l.invoicesCond.L.Unlock()
		close(doneChan)
	}()

	// Wait until we're either done or timed out.
	condWg.Wait()

	// Interpret the result so we can return a more descriptive error than
	// just "failed".
	switch {
	case !hasInvoice:
		return fmt.Errorf("no active or settled invoice found for "+
			"hash=%v", hash)

	case invoiceState != state:
		return fmt.Errorf("invoice status not correct before timeout, "+
			"hash=%v, status=%v", hash, invoiceState)

	default:
		return nil
	}
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
