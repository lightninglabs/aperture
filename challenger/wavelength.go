package challenger

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc"
)

const (
	// recvPath is the wallet daemon's HTTP/JSON route for requesting an
	// inbound payment.
	recvPath = "/v1/wallet/recv"

	// defaultRecvTimeout bounds a single invoice request. Minting happens
	// inline while a client waits on its 402, so this is deliberately
	// short: a wallet that has gone away should surface as a failed
	// challenge rather than holding the request open.
	defaultRecvTimeout = 30 * time.Second
)

// WavelengthInvoiceClient mints L402 challenge invoices from a Wavelength
// wallet daemon instead of from an lnd node.
//
// The point of it is what the seller no longer has to run. An invoice from
// waved is backed by an inbound swap, so the swap server supplies the inbound
// liquidity and settles the payment into the wallet's Ark balance. The seller
// therefore needs no Lightning node, no channels, and no inbound capacity to
// get paid, which is the part of standing up an L402 seller that usually costs
// the most effort.
//
// It satisfies InvoiceClient, so it drives the existing LndChallenger rather
// than duplicating it. Only AddInvoice does any work: the other two methods
// exist to track invoice state, which this backend cannot do, and it says so
// through InvoiceTracker so the challenger knows not to ask.
type WavelengthInvoiceClient struct {
	// recvURL is the fully resolved endpoint invoices are requested from.
	recvURL string

	client *http.Client
}

// A compile-time check that we satisfy the interface the challenger needs, and
// that we declare the one capability we lack.
var _ InvoiceClient = (*WavelengthInvoiceClient)(nil)
var _ InvoiceTracker = (*WavelengthInvoiceClient)(nil)

// NewWavelengthInvoiceClient creates an invoice client against the wallet
// daemon's HTTP/JSON gateway, given its base URL (for example
// http://localhost:10061).
//
// strictVerify is rejected rather than ignored. Strict verification means the
// challenger watches every invoice's state and refuses a token whose invoice
// it has not seen settle, which it does by listing and subscribing to
// invoices. This backend can do neither, so accepting the flag would leave
// aperture believing it was verifying settlement when it was not: the failure
// would be silent and would only show up as tokens honoured for invoices that
// were never paid.
func NewWavelengthInvoiceClient(gateway string,
	strictVerify bool) (*WavelengthInvoiceClient, error) {

	if strictVerify {
		return nil, fmt.Errorf("strictverify is not supported with " +
			"the wavelength authenticator: the wallet daemon " +
			"exposes no invoice subscription to verify " +
			"settlement against")
	}

	if gateway == "" {
		return nil, fmt.Errorf("wavelength gateway URL is required")
	}

	parsed, err := url.Parse(gateway)
	if err != nil {
		return nil, fmt.Errorf("invalid wavelength gateway URL %q: %w",
			gateway, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("wavelength gateway URL %q must be "+
			"http or https", gateway)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("wavelength gateway URL %q has no host",
			gateway)
	}

	return &WavelengthInvoiceClient{
		recvURL: strings.TrimRight(parsed.String(), "/") + recvPath,
		client:  &http.Client{Timeout: defaultRecvTimeout},
	}, nil
}

// recvRequest asks the wallet for an inbound payment of a given size.
type recvRequest struct {
	AmountSat int64  `json:"amt_sat"`
	Memo      string `json:"memo,omitempty"`
}

// recvResponse is the wallet's reply. The payment hash is nested under the
// ledger entry that tracks the receive, and is read from there rather than
// decoded out of the payment request, so the two can never disagree about
// which hash this challenge commits to.
type recvResponse struct {
	Invoice string `json:"invoice"`
	Entry   struct {
		Request struct {
			LightningInvoice struct {
				PaymentHash string `json:"payment_hash"`
			} `json:"lightning_invoice"`
		} `json:"request"`
	} `json:"entry"`
}

// gatewayError is the error shape the gateway returns for a failed call.
type gatewayError struct {
	Message string `json:"message"`
}

// AddInvoice mints a challenge invoice, translating lnd's request and response
// shapes onto the wallet's receive call.
func (w *WavelengthInvoiceClient) AddInvoice(ctx context.Context,
	in *lnrpc.Invoice, _ ...grpc.CallOption) (*lnrpc.AddInvoiceResponse,
	error) {

	// The mint prices challenges in satoshis, but tolerate a millisatoshi
	// amount rather than silently minting a zero-value invoice, which the
	// wallet would reject with a far less obvious error.
	//
	// The conversion rounds up. Rounding down would hand the buyer an
	// invoice cheaper than the price the mint just quoted, and it would do
	// so worst where the price is smallest: 1500 msat would mint 1 sat,
	// and any price below a full satoshi would round to nothing and fail
	// the challenge outright. Overflow on the addition lands negative and
	// is caught by the check below, so it fails closed.
	amountSat := in.Value
	if amountSat == 0 && in.ValueMsat > 0 {
		amountSat = (in.ValueMsat + 999) / 1000
	}
	if amountSat <= 0 {
		return nil, fmt.Errorf("invoice amount must be positive, got "+
			"%d sat", amountSat)
	}

	body, err := json.Marshal(&recvRequest{
		AmountSat: amountSat,
		Memo:      in.Memo,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal recv request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, w.recvURL, bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("build recv request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call wallet recv: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		var gwErr gatewayError
		_ = json.NewDecoder(resp.Body).Decode(&gwErr)

		return nil, fmt.Errorf("wallet recv failed (%d): %s",
			resp.StatusCode, gwErr.Message)
	}

	var parsed recvResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode recv response: %w", err)
	}

	if parsed.Invoice == "" {
		return nil, fmt.Errorf("wallet returned an empty invoice")
	}

	// The hash is what the macaroon commits to and what the client's
	// preimage is checked against, so a malformed one has to fail the
	// challenge here. Letting it through would mint a credential that can
	// never be satisfied.
	hash, err := hex.DecodeString(
		parsed.Entry.Request.LightningInvoice.PaymentHash,
	)
	if err != nil {
		return nil, fmt.Errorf("decode payment hash: %w", err)
	}
	if len(hash) != 32 {
		return nil, fmt.Errorf("payment hash must be 32 bytes, got %d",
			len(hash))
	}

	return &lnrpc.AddInvoiceResponse{
		RHash:          hash,
		PaymentRequest: parsed.Invoice,
	}, nil
}

// TracksInvoices reports that this backend cannot be asked about invoice
// state. The wallet daemon's gateway exposes minting and nothing else, so
// there is no way to enumerate the invoices we have handed out or to learn
// when one of them settles.
//
// NOTE: This is part of the InvoiceTracker interface.
func (w *WavelengthInvoiceClient) TracksInvoices() bool {
	return false
}

// ListInvoices is not supported on this backend. The challenger asks for the
// capability through InvoiceTracker before it calls this, so reaching it means
// something bypassed that check, and an error is the right answer.
func (w *WavelengthInvoiceClient) ListInvoices(_ context.Context,
	_ *lnrpc.ListInvoiceRequest, _ ...grpc.CallOption) (
	*lnrpc.ListInvoiceResponse, error) {

	return nil, fmt.Errorf("listing invoices is not supported by the " +
		"wavelength authenticator")
}

// SubscribeInvoices is not supported on this backend, for the same reason as
// ListInvoices.
func (w *WavelengthInvoiceClient) SubscribeInvoices(_ context.Context,
	_ *lnrpc.InvoiceSubscription, _ ...grpc.CallOption) (
	lnrpc.Lightning_SubscribeInvoicesClient, error) {

	return nil, fmt.Errorf("subscribing to invoices is not supported by " +
		"the wavelength authenticator")
}
