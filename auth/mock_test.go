package auth_test

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

type mockMint struct {
	rejectMacaroonHex string
}

var _ auth.Minter = (*mockMint)(nil)

func (m *mockMint) MintL402(_ context.Context,
	services ...l402.Service) (*macaroon.Macaroon, string, error) {

	return nil, "", nil
}

func (m *mockMint) VerifyL402(_ context.Context, p *mint.VerificationParams) error {
	if m.rejectMacaroonHex != "" {
		macBytes, err := p.Macaroon.MarshalBinary()
		if err != nil {
			return err
		}
		if hex.EncodeToString(macBytes) == m.rejectMacaroonHex {
			return errors.New("macaroon rejected")
		}
	}

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
