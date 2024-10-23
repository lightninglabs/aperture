package test

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/input"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
)

type mockSigner struct {
	lnd *LndMockServices
}

func (s *mockSigner) RawClientWithMacAuth(
	ctx context.Context) (context.Context, time.Duration,
	signrpc.SignerClient) {

	return ctx, 0, nil
}

func (s *mockSigner) SignOutputRaw(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*lndclient.SignDescriptor,
	prevOutputs []*wire.TxOut) ([][]byte, error) {

	s.lnd.SignOutputRawChannel <- SignOutputRawRequest{
		Tx:              tx,
		SignDescriptors: signDescriptors,
	}

	rawSigs := [][]byte{{1, 2, 3}}

	return rawSigs, nil
}

func (s *mockSigner) ComputeInputScript(ctx context.Context, tx *wire.MsgTx,
	signDescriptors []*lndclient.SignDescriptor,
	prevOutputs []*wire.TxOut) ([]*input.Script, error) {

	return nil, fmt.Errorf("unimplemented")
}

func (s *mockSigner) SignMessage(ctx context.Context, msg []byte,
	locator keychain.KeyLocator,
	opts ...lndclient.SignMessageOption) ([]byte, error) {

	return s.lnd.Signature, nil
}

func (s *mockSigner) SignOutputRawKeyLocator(ctx context.Context,
	tx *wire.MsgTx,
	signDescriptors []*lndclient.SignDescriptor,
	prevOutputs []*wire.TxOut) ([][]byte, error) {

	return [][]byte{s.lnd.Signature}, nil
}

func (s *mockSigner) VerifyMessage(ctx context.Context, msg, sig []byte,
	pubkey [33]byte, opts ...lndclient.VerifyMessageOption) (bool, error) {

	// Make the mock somewhat functional by asserting that the message and
	// signature is what we expect from the mock parameters.
	mockAssertion := bytes.Equal(msg, []byte(s.lnd.SignatureMsg)) &&
		bytes.Equal(sig, s.lnd.Signature)

	return mockAssertion, nil
}

func (s *mockSigner) DeriveSharedKey(context.Context, *btcec.PublicKey,
	*keychain.KeyLocator) ([32]byte, error) {

	return [32]byte{4, 5, 6}, nil
}

func (s *mockSigner) MuSig2CreateSession(ctx context.Context,
	_ input.MuSig2Version, signerLoc *keychain.KeyLocator, signers [][]byte,
	opts ...lndclient.MuSig2SessionOpts) (*input.MuSig2SessionInfo, error) {

	return nil, nil
}

func (s *mockSigner) MuSig2RegisterNonces(ctx context.Context,
	sessionID [32]byte, nonces [][66]byte) (bool, error) {

	return false, nil
}

func (s *mockSigner) MuSig2Sign(ctx context.Context, sessionID [32]byte,
	message [32]byte, cleanup bool) ([]byte, error) {

	return nil, nil
}

func (s *mockSigner) MuSig2CombineSig(ctx context.Context, sessionID [32]byte,
	otherPartialSigs [][]byte) (bool, []byte, error) {

	return false, nil, nil
}

func (s *mockSigner) MuSig2Cleanup(ctx context.Context,
	sessionID [32]byte) error {

	return nil
}
