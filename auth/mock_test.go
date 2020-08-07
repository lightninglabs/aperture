package auth_test

import (
	"context"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

type mockMint struct {
}

var _ auth.Minter = (*mockMint)(nil)

func (m *mockMint) MintLSAT(_ context.Context,
	services ...lsat.Service) (*macaroon.Macaroon, string, error) {

	return nil, "", nil
}

func (m *mockMint) VerifyLSAT(_ context.Context, p *mint.VerificationParams) error {
	return nil
}

type mockChecker struct {
	err error
}

var _ auth.InvoiceChecker = (*mockChecker)(nil)

func (m *mockChecker) VerifyInvoiceStatus(lntypes.Hash,
	lnrpc.Invoice_InvoiceState, time.Duration) error {

	return m.err
}
