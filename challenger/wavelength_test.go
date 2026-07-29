package challenger

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/stretchr/testify/require"
)

const testPaymentHash = "161c5af497913d17bcba3edf886d15e4482300b5e4fceaeed0" +
	"23377a4b2a025e"

// recvBody is the shape the wallet daemon answers a receive request with.
func recvBody(invoice, hash string) string {
	return fmt.Sprintf(
		`{"invoice":%q,"entry":{"request":{"lightning_invoice":`+
			`{"payment_hash":%q}}}}`, invoice, hash,
	)
}

// newTestWallet spins up a stub wallet gateway and returns a client pointed at
// it, along with a pointer to the last request body it received.
func newTestWallet(t *testing.T, handler http.HandlerFunc) (
	*WavelengthInvoiceClient, *string) {

	t.Helper()

	var lastBody string
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			lastBody = string(buf)

			handler(w, r)
		},
	))
	t.Cleanup(server.Close)

	client, err := NewWavelengthInvoiceClient(server.URL, false)
	require.NoError(t, err)

	return client, &lastBody
}

// TestWavelengthAddInvoice checks the happy path: the amount is passed through
// in satoshis and both fields the challenger needs come back.
func TestWavelengthAddInvoice(t *testing.T) {
	t.Parallel()

	client, body := newTestWallet(t, func(w http.ResponseWriter,
		r *http.Request) {

		require.Equal(t, "/v1/wallet/recv", r.URL.Path)
		_, _ = w.Write([]byte(recvBody("lnbcrt115u1p4xjka5", testPaymentHash)))
	})

	resp, err := client.AddInvoice(context.Background(), &lnrpc.Invoice{
		Memo:  "L402",
		Value: 11500,
	})
	require.NoError(t, err)

	require.Equal(t, "lnbcrt115u1p4xjka5", resp.PaymentRequest)

	// The challenger commits this hash to the macaroon and later checks
	// the client's preimage against it, so it has to survive as raw bytes.
	expected, err := hex.DecodeString(testPaymentHash)
	require.NoError(t, err)
	require.Equal(t, expected, resp.RHash)

	require.Contains(t, *body, `"amt_sat":11500`)
	require.Contains(t, *body, `"memo":"L402"`)
}

// TestWavelengthAddInvoiceRejectsBadHash makes sure a hash the client cannot
// use fails the challenge rather than minting a credential that can never be
// satisfied: the preimage check would reject every token issued against it.
func TestWavelengthAddInvoiceRejectsBadHash(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		hash string
	}{
		{name: "not hex", hash: "zzzz"},
		{name: "too short", hash: "0badc0de"},
		{name: "empty", hash: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newTestWallet(t, func(w http.ResponseWriter,
				_ *http.Request) {

				_, _ = w.Write([]byte(recvBody("lnbcrt1", tc.hash)))
			})

			_, err := client.AddInvoice(
				context.Background(),
				&lnrpc.Invoice{Value: 11500},
			)
			require.Error(t, err)
		})
	}
}

// TestWavelengthAddInvoiceSurfacesWalletError checks that the wallet's own
// explanation reaches the operator instead of a bare status code.
func TestWavelengthAddInvoiceSurfacesWalletError(t *testing.T) {
	t.Parallel()

	client, _ := newTestWallet(t, func(w http.ResponseWriter,
		_ *http.Request) {

		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"wallet is not ready"}`))
	})

	_, err := client.AddInvoice(
		context.Background(), &lnrpc.Invoice{Value: 11500},
	)
	require.ErrorContains(t, err, "wallet is not ready")
}

// TestWavelengthAddInvoiceRejectsNonPositive pins down that a zero-value
// challenge is refused here rather than at the wallet, where it surfaces as a
// far less obvious error.
func TestWavelengthAddInvoiceRejectsNonPositive(t *testing.T) {
	t.Parallel()

	client, _ := newTestWallet(t, func(w http.ResponseWriter,
		_ *http.Request) {

		_, _ = w.Write([]byte(recvBody("lnbcrt1", testPaymentHash)))
	})

	for _, invoice := range []*lnrpc.Invoice{
		{Value: 0},
		{Value: -1},
	} {
		_, err := client.AddInvoice(context.Background(), invoice)
		require.ErrorContains(t, err, "must be positive")
	}
}

// TestWavelengthAddInvoiceFallsBackToMsat checks the millisatoshi path, so a
// caller that priced in msat does not silently mint a zero-value invoice.
func TestWavelengthAddInvoiceFallsBackToMsat(t *testing.T) {
	t.Parallel()

	client, body := newTestWallet(t, func(w http.ResponseWriter,
		_ *http.Request) {

		_, _ = w.Write([]byte(recvBody("lnbcrt1", testPaymentHash)))
	})

	_, err := client.AddInvoice(context.Background(), &lnrpc.Invoice{
		ValueMsat: 11_500_000,
	})
	require.NoError(t, err)
	require.Contains(t, *body, `"amt_sat":11500`)
}

// TestWavelengthRejectsStrictVerify is the important guard. Strict
// verification means refusing tokens whose invoice was never seen to settle,
// which this backend cannot observe. Accepting the flag would leave aperture
// believing it verified settlement when it did not.
func TestWavelengthRejectsStrictVerify(t *testing.T) {
	t.Parallel()

	_, err := NewWavelengthInvoiceClient("http://localhost:10061", true)
	require.ErrorContains(t, err, "strictverify is not supported")
}

// TestWavelengthRejectsBadGateway covers the misconfigurations worth catching
// at start-up rather than on the first 402 a real client is waiting on.
func TestWavelengthRejectsBadGateway(t *testing.T) {
	t.Parallel()

	for _, gateway := range []string{
		"", "localhost:10061", "ftp://localhost", "http://",
	} {
		_, err := NewWavelengthInvoiceClient(gateway, false)
		require.Error(t, err, "expected %q to be refused", gateway)
	}
}

// TestWavelengthTrackingUnsupported documents that the state-tracking half of
// the interface is unreachable rather than quietly returning empty results,
// which would read to the challenger as "no invoices exist".
func TestWavelengthTrackingUnsupported(t *testing.T) {
	t.Parallel()

	client, err := NewWavelengthInvoiceClient("http://localhost:10061", false)
	require.NoError(t, err)

	_, err = client.ListInvoices(
		context.Background(), &lnrpc.ListInvoiceRequest{},
	)
	require.Error(t, err)

	_, err = client.SubscribeInvoices(
		context.Background(), &lnrpc.InvoiceSubscription{},
	)
	require.Error(t, err)
}

// TestWavelengthGatewayPathJoin checks a trailing slash on the configured URL
// does not produce a double slash in the request path.
func TestWavelengthGatewayPathJoin(t *testing.T) {
	t.Parallel()

	client, err := NewWavelengthInvoiceClient("http://localhost:10061/", false)
	require.NoError(t, err)
	require.False(t, strings.Contains(client.recvURL, "//v1"))
	require.True(t, strings.HasSuffix(client.recvURL, "/v1/wallet/recv"))
}
