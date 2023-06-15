package auth

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

// CreateDummyMacHex creates a valid macaroon with dummy content for our tests.
func CreateDummyMacHex(preimage string) string {
	dummyMac, err := macaroon.New(
		[]byte("aabbccddeeff00112233445566778899"), []byte("AA=="),
		"aperture", macaroon.LatestVersion,
	)
	if err != nil {
		panic(err)
	}
	preimageCaveat := lsat.Caveat{
		Condition: lsat.PreimageKey,
		Value:     preimage,
	}
	err = lsat.AddFirstPartyCaveats(dummyMac, preimageCaveat)
	if err != nil {
		panic(err)
	}
	macBytes, err := dummyMac.MarshalBinary()
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(macBytes)
}

type MockMint struct {
}

var _ Minter = (*MockMint)(nil)

func (m *MockMint) MintLSAT(_ context.Context,
	services ...lsat.Service) (*macaroon.Macaroon, string, error) {

	return nil, "", nil
}

func (m *MockMint) VerifyLSAT(_ context.Context, p *mint.VerificationParams) error {
	return nil
}

type MockChecker struct {
	Err error
}

var _ InvoiceChecker = (*MockChecker)(nil)

func (m *MockChecker) VerifyInvoiceStatus(lntypes.Hash,
	lnrpc.Invoice_InvoiceState, time.Duration) error {

	return m.Err
}
