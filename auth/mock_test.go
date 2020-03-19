package auth_test

import (
	"context"

	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
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
