package challenger

import (
	"context"
	"sync"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"google.golang.org/grpc"
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

type MockInvoiceClient struct {
	invoices   []*lnrpc.Invoice
	updateChan chan *lnrpc.Invoice
	errChan    chan error
	quit       chan struct{}

	lastAddIndex uint64
}

// ListInvoices returns a paginated list of all invoices known to lnd.
func (m *MockInvoiceClient) ListInvoices(_ context.Context,
	_ *lnrpc.ListInvoiceRequest,
	_ ...grpc.CallOption) (*lnrpc.ListInvoiceResponse, error) {

	return &lnrpc.ListInvoiceResponse{
		Invoices: m.invoices,
	}, nil
}

// SubscribeInvoices subscribes to updates on invoices.
func (m *MockInvoiceClient) SubscribeInvoices(_ context.Context,
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
func (m *MockInvoiceClient) AddInvoice(_ context.Context, in *lnrpc.Invoice,
	_ ...grpc.CallOption) (*lnrpc.AddInvoiceResponse, error) {

	m.invoices = append(m.invoices, in)

	return &lnrpc.AddInvoiceResponse{
		RHash:          in.RHash,
		PaymentRequest: in.PaymentRequest,
		AddIndex:       uint64(len(m.invoices) - 1),
	}, nil
}

func (m *MockInvoiceClient) stop() {
	close(m.quit)
}

func NewChallenger() (*LndChallenger, *MockInvoiceClient, chan error) {
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
