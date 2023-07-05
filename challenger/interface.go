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
