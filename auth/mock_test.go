package auth_test

import (
	"context"

	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/mint"
	"github.com/lightninglabs/loop/lsat"
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
