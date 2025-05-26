package test

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/invoices"
	"github.com/lightningnetwork/lnd/lnrpc/invoicesrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/routing/route"
	"github.com/lightningnetwork/lnd/zpay32"
	"golang.org/x/net/context"
)

type mockLightningClient struct {
	lndclient.LightningClient
	lnd *LndMockServices
	wg  sync.WaitGroup
}

var _ lndclient.LightningClient = (*mockLightningClient)(nil)

// PayInvoice pays an invoice.
func (h *mockLightningClient) PayInvoice(ctx context.Context, invoice string,
	maxFee btcutil.Amount,
	outgoingChannel *uint64) chan lndclient.PaymentResult {

	done := make(chan lndclient.PaymentResult, 1)

	h.lnd.SendPaymentChannel <- PaymentChannelMessage{
		PaymentRequest: invoice,
		Done:           done,
	}

	return done
}

func (h *mockLightningClient) WaitForFinished() {
	h.wg.Wait()
}

func (h *mockLightningClient) ConfirmedWalletBalance(ctx context.Context) (
	btcutil.Amount, error) {

	return 1000000, nil
}

func (h *mockLightningClient) GetInfo(ctx context.Context) (*lndclient.Info,
	error) {

	pubKeyBytes, err := hex.DecodeString(h.lnd.NodePubkey)
	if err != nil {
		return nil, err
	}
	var pubKey [33]byte
	copy(pubKey[:], pubKeyBytes)
	return &lndclient.Info{
		BlockHeight:    600,
		IdentityPubkey: pubKey,
		Uris:           []string{h.lnd.NodePubkey + "@127.0.0.1:9735"},
	}, nil
}

func (h *mockLightningClient) EstimateFeeToP2WSH(ctx context.Context,
	amt btcutil.Amount, confTarget int32) (btcutil.Amount,
	error) {

	return 3000, nil
}

func (h *mockLightningClient) AddInvoice(ctx context.Context,
	in *invoicesrpc.AddInvoiceData) (lntypes.Hash, string, error) {

	h.lnd.lock.Lock()
	defer h.lnd.lock.Unlock()

	var hash lntypes.Hash
	switch {
	case in.Hash != nil:
		hash = *in.Hash
	case in.Preimage != nil:
		hash = (*in.Preimage).Hash()
	default:
		if _, err := rand.Read(hash[:]); err != nil {
			return lntypes.Hash{}, "", err
		}
	}

	// Create and encode the payment request as a bech32 (zpay32) string.
	creationDate := time.Now()

	payReq, err := zpay32.NewInvoice(
		h.lnd.ChainParams, hash, creationDate,
		zpay32.Description(in.Memo),
		zpay32.CLTVExpiry(in.CltvExpiry),
		zpay32.Amount(in.Value),
	)
	if err != nil {
		return lntypes.Hash{}, "", err
	}

	privKey, err := btcec.NewPrivateKey()
	if err != nil {
		return lntypes.Hash{}, "", err
	}

	payReqString, err := payReq.Encode(
		zpay32.MessageSigner{
			SignCompact: func(hash []byte) ([]byte, error) {
				// ecdsa.SignCompact returns a
				// pubkey-recoverable signature.
				sig := ecdsa.SignCompact(privKey, hash, true)

				return sig, nil
			},
		},
	)
	if err != nil {
		return lntypes.Hash{}, "", err
	}

	// Add the invoice we have created to our mock's set of invoices.
	h.lnd.Invoices[hash] = &lndclient.Invoice{
		Preimage:       nil,
		Hash:           hash,
		PaymentRequest: payReqString,
		Amount:         in.Value,
		CreationDate:   creationDate,
		State:          invoices.ContractOpen,
		IsKeysend:      false,
	}

	return hash, payReqString, nil
}

// LookupInvoice looks up an invoice in the mock's set of stored invoices.
// If it is not found, this call will fail. Note that these invoices should
// be settled using settleInvoice to have a preimage, settled state and settled
// date set.
func (h *mockLightningClient) LookupInvoice(_ context.Context,
	hash lntypes.Hash) (*lndclient.Invoice, error) {

	h.lnd.lock.Lock()
	defer h.lnd.lock.Unlock()

	inv, ok := h.lnd.Invoices[hash]
	if !ok {
		return nil, fmt.Errorf("invoice: %x not found", hash)
	}

	return inv, nil
}

// ListTransactions returns all known transactions of the backing lnd node.
func (h *mockLightningClient) ListTransactions(context.Context, int32, int32,
	...lndclient.ListTransactionsOption) ([]lndclient.Transaction, error) {

	h.lnd.lock.Lock()
	txs := h.lnd.Transactions
	h.lnd.lock.Unlock()

	return txs, nil
}

// ListChannels retrieves all channels of the backing lnd node.
func (h *mockLightningClient) ListChannels(context.Context, bool, bool,
	...lndclient.ListChannelsOption) ([]lndclient.ChannelInfo, error) {

	return h.lnd.Channels, nil
}

// ClosedChannels returns a list of our closed channels.
func (h *mockLightningClient) ClosedChannels(
	_ context.Context) ([]lndclient.ClosedChannel, error) {

	return h.lnd.ClosedChannels, nil
}

// PendingChannels returns a list of lnd's pending channels.
func (h *mockLightningClient) PendingChannels(_ context.Context) (
	*lndclient.PendingChannels, error) {

	return nil, nil
}

// ForwardingHistory returns the mock's set of forwarding events.
func (h *mockLightningClient) ForwardingHistory(_ context.Context,
	_ lndclient.ForwardingHistoryRequest) (*lndclient.ForwardingHistoryResponse,
	error) {

	return &lndclient.ForwardingHistoryResponse{
		LastIndexOffset: 0,
		Events:          h.lnd.ForwardingEvents,
	}, nil
}

// ListInvoices returns our mock's invoices.
func (h *mockLightningClient) ListInvoices(_ context.Context,
	_ lndclient.ListInvoicesRequest) (*lndclient.ListInvoicesResponse,
	error) {

	invoices := make([]lndclient.Invoice, 0, len(h.lnd.Invoices))
	for _, invoice := range h.lnd.Invoices {
		invoices = append(invoices, *invoice)
	}

	return &lndclient.ListInvoicesResponse{
		Invoices: invoices,
	}, nil
}

// ListPayments makes a paginated call to our list payments endpoint.
func (h *mockLightningClient) ListPayments(_ context.Context,
	_ lndclient.ListPaymentsRequest) (*lndclient.ListPaymentsResponse,
	error) {

	return &lndclient.ListPaymentsResponse{
		Payments: h.lnd.Payments,
	}, nil
}

// ChannelBackup retrieves the backup for a particular channel. The
// backup is returned as an encrypted chanbackup.Single payload.
func (h *mockLightningClient) ChannelBackup(
	context.Context, wire.OutPoint) ([]byte, error) {

	return nil, nil
}

// ChannelBackups retrieves backups for all existing pending open and
// open channels. The backups are returned as an encrypted
// chanbackup.Multi payload.
func (h *mockLightningClient) ChannelBackups(
	ctx context.Context) ([]byte, error) {

	return nil, nil
}

// DecodePaymentRequest decodes a payment request.
func (h *mockLightningClient) DecodePaymentRequest(_ context.Context,
	_ string) (*lndclient.PaymentRequest, error) {

	return nil, nil
}

// OpenChannel opens a channel to the peer provided with the amounts
// specified.
func (h *mockLightningClient) OpenChannel(_ context.Context, _ route.Vertex,
	_ btcutil.Amount, _ btcutil.Amount, _ bool,
	_ ...lndclient.OpenChannelOption) (*wire.OutPoint, error) {

	return nil, nil
}

// CloseChannel closes the channel provided.
func (h *mockLightningClient) CloseChannel(_ context.Context, _ *wire.OutPoint,
	_ bool, _ int32, _ btcutil.Address,
	_ ...lndclient.CloseChannelOption) (chan lndclient.CloseChannelUpdate,
	chan error, error) {

	return nil, nil, nil
}

// Connect attempts to connect to a peer at the host specified.
func (h *mockLightningClient) Connect(_ context.Context, _ route.Vertex,
	_ string, _ bool) error {

	return nil
}
