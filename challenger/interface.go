package challenger

import (
	"context"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
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

// InvoiceTracker is an optional capability of an InvoiceClient. A client that
// mints its invoices somewhere it cannot then enumerate them, such as a wallet
// daemon reached over an HTTP gateway, implements it and reports false.
//
// It is deliberately a separate interface rather than a method on
// InvoiceClient. The lnd-backed clients are lnrpc types we do not own and
// cannot add a method to, so the capability has to be optional, and a client
// that says nothing is taken to track its invoices exactly as before.
type InvoiceTracker interface {
	// TracksInvoices reports whether this client can list its invoices and
	// subscribe to their state changes.
	TracksInvoices() bool
}

// tracksInvoices reports whether an invoice client can be asked about invoice
// state. A client that does not declare the capability is assumed to have it,
// which is what every lnd-backed client does.
func tracksInvoices(client InvoiceClient) bool {
	tracker, ok := client.(InvoiceTracker)
	if !ok {
		return true
	}

	return tracker.TracksInvoices()
}

// Challenger is an interface that combines the mint.Challenger and the
// auth.InvoiceChecker interfaces.
type Challenger interface {
	mint.Challenger
	auth.InvoiceChecker
}
