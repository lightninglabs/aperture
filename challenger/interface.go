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

// Challenger is an interface that combines the mint.Challenger and the
// auth.InvoiceChecker interfaces.
type Challenger interface {
	mint.Challenger
	auth.InvoiceChecker
}

// InvoiceReconciler is implemented by challengers that can replay the
// state of every known invoice on demand. Used at startup to catch up
// on events that fired while prism was offline, and by an optional
// periodic ticker to recover from any gaps in the live SubscribeInvoices
// stream (e.g. brief lnd disconnects).
//
// Implementations must be safe to call concurrently with the live
// subscription. Settle/expire callbacks downstream are expected to be
// idempotent so duplicate notifications don't cause spurious DB writes.
type InvoiceReconciler interface {
	Reconcile() error
}
