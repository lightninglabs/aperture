package aperture

import (
	"context"
	"fmt"

	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
)

// InvoiceRequestGenerator is a function type that returns a new request for the
// lnrpc.AddInvoice call.
type InvoiceRequestGenerator func(price int64) (*lnrpc.Invoice, error)

// LndChallenger is a challenger that uses an lnd backend to create new LSAT
// payment challenges.
type LndChallenger struct {
	client        lnrpc.LightningClient
	genInvoiceReq InvoiceRequestGenerator
}

// A compile time flag to ensure the LndChallenger satisfies the
// mint.Challenger interface.
var _ mint.Challenger = (*LndChallenger)(nil)

const (
	// invoiceMacaroonName is the name of the read-only macaroon belonging
	// to the target lnd node.
	invoiceMacaroonName = "invoice.macaroon"
)

// NewLndChallenger creates a new challenger that uses the given connection
// details to connect to an lnd backend to create payment challenges.
func NewLndChallenger(cfg *authConfig, genInvoiceReq InvoiceRequestGenerator) (
	*LndChallenger, error) {

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
	return &LndChallenger{
		client:        client,
		genInvoiceReq: genInvoiceReq,
	}, nil
}

// NewChallenge creates a new LSAT payment challenge, returning a payment
// request (invoice) and the corresponding payment hash.
//
// NOTE: This is part of the Challenger interface.
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
