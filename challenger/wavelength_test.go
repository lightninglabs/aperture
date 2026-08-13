package challenger

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/stretchr/testify/require"
	macaroon "gopkg.in/macaroon.v2"
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
			// Read to EOF rather than issuing one Read into a
			// ContentLength-sized buffer. A single Read may return
			// a short prefix, which would make the assertions on
			// this body fail intermittently, and a chunked request
			// reports a ContentLength of -1, which would panic the
			// handler goroutine on the make.
			buf, _ := io.ReadAll(r.Body)
			lastBody = string(buf)

			handler(w, r)
		},
	))
	t.Cleanup(server.Close)

	client, err := NewWavelengthInvoiceClient(server.URL, false, "")
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

	_, err := NewWavelengthInvoiceClient("http://localhost:10061", true, "")
	require.ErrorContains(t, err, "strictverify is not supported")
}

// TestWavelengthRejectsBadGateway covers the misconfigurations worth catching
// at start-up rather than on the first 402 a real client is waiting on.
func TestWavelengthRejectsBadGateway(t *testing.T) {
	t.Parallel()

	for _, gateway := range []string{
		"", "localhost:10061", "ftp://localhost", "http://",
	} {
		_, err := NewWavelengthInvoiceClient(gateway, false, "")
		require.Error(t, err, "expected %q to be refused", gateway)
	}
}

// TestWavelengthTrackingUnsupported documents that the state-tracking half of
// the interface is unreachable rather than quietly returning empty results,
// which would read to the challenger as "no invoices exist".
func TestWavelengthTrackingUnsupported(t *testing.T) {
	t.Parallel()

	client, err := NewWavelengthInvoiceClient("http://localhost:10061", false, "")
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

	client, err := NewWavelengthInvoiceClient("http://localhost:10061/", false, "")
	require.NoError(t, err)
	require.False(t, strings.Contains(client.recvURL, "//v1"))
	require.True(t, strings.HasSuffix(client.recvURL, "/v1/wallet/recv"))
}

// TestWavelengthAddInvoiceRoundsUp checks the millisatoshi conversion never
// mints an invoice for less than was asked.
//
// The mint quotes a price and this client turns it into the whole number of
// satoshis the wallet is asked to receive. Rounding down would sell the
// service below the quoted price, and it would misbehave worst where it
// matters most: a sub-satoshi price would round to nothing and fail the
// challenge outright, which reads as a broken proxy rather than as a price the
// wallet cannot express.
func TestWavelengthAddInvoiceRoundsUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		valueMsat int64
		wantSat   int64
	}{{
		name:      "exact satoshi is unchanged",
		valueMsat: 2000,
		wantSat:   2,
	}, {
		name:      "partial satoshi rounds up",
		valueMsat: 1500,
		wantSat:   2,
	}, {
		name:      "one msat over rounds up",
		valueMsat: 1001,
		wantSat:   2,
	}, {
		name:      "sub satoshi price still bills a satoshi",
		valueMsat: 1,
		wantSat:   1,
	}, {
		name:      "just under a satoshi rounds up",
		valueMsat: 999,
		wantSat:   1,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, body := newTestWallet(t, func(w http.ResponseWriter,
				_ *http.Request) {

				_, _ = w.Write([]byte(recvBody(
					"lnbcrt1", testPaymentHash,
				)))
			})

			_, err := client.AddInvoice(
				context.Background(),
				&lnrpc.Invoice{ValueMsat: tc.valueMsat},
			)
			require.NoError(t, err)

			require.Contains(t, *body, fmt.Sprintf(
				`"amt_sat":%d`, tc.wantSat,
			))
		})
	}
}

// TestWavelengthAddInvoicePrefersValue checks that an amount given in satoshis
// is used as given, rather than being recomputed from the msat field.
func TestWavelengthAddInvoicePrefersValue(t *testing.T) {
	t.Parallel()

	client, body := newTestWallet(t, func(w http.ResponseWriter,
		_ *http.Request) {

		_, _ = w.Write([]byte(recvBody("lnbcrt1", testPaymentHash)))
	})

	_, err := client.AddInvoice(context.Background(), &lnrpc.Invoice{
		Value:     7,
		ValueMsat: 1500,
	})
	require.NoError(t, err)

	require.Contains(t, *body, `"amt_sat":7`)
}

// TestWavelengthSendsMacaroon checks that a configured macaroon reaches the
// gateway on the header it forwards into gRPC metadata.
//
// Without it the daemon answers 401, AddInvoice fails, and the client waiting
// for a 402 gets a 500 instead. That is the default state of a wallet daemon:
// it serves unauthenticated only when started with rpc.no-macaroons, which it
// refuses outright on mainnet.
func TestWavelengthSendsMacaroon(t *testing.T) {
	t.Parallel()

	path, serialized := writeTestMacaroon(t)

	var got string
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("macaroon")
			_, _ = w.Write([]byte(recvBody(
				"lnbcrt1", testPaymentHash,
			)))
		},
	))
	t.Cleanup(server.Close)

	client, err := NewWavelengthInvoiceClient(server.URL, false, path)
	require.NoError(t, err)

	_, err = client.AddInvoice(
		context.Background(), &lnrpc.Invoice{Value: 11500},
	)
	require.NoError(t, err)

	// Hex encoded, which is the form the gateway decodes.
	require.Equal(t, hex.EncodeToString(serialized), got)
}

// TestWavelengthOmitsMacaroonWhenUnset checks that leaving the option empty
// sends no credential at all, rather than an empty one the daemon would have
// to interpret.
func TestWavelengthOmitsMacaroonWhenUnset(t *testing.T) {
	t.Parallel()

	var present bool
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, present = r.Header["Macaroon"]
			_, _ = w.Write([]byte(recvBody(
				"lnbcrt1", testPaymentHash,
			)))
		},
	))
	t.Cleanup(server.Close)

	client, err := NewWavelengthInvoiceClient(server.URL, false, "")
	require.NoError(t, err)

	_, err = client.AddInvoice(
		context.Background(), &lnrpc.Invoice{Value: 11500},
	)
	require.NoError(t, err)

	require.False(t, present)
}

// TestWavelengthRejectsBadMacaroon covers the two ways the option is got
// wrong. Both fail at construction, which is start-up, rather than on the
// first request a paying client is waiting on.
func TestWavelengthRejectsBadMacaroon(t *testing.T) {
	t.Parallel()

	_, err := NewWavelengthInvoiceClient(
		"http://localhost:10061", false,
		filepath.Join(t.TempDir(), "absent.macaroon"),
	)
	require.ErrorContains(t, err, "read wavelength macaroon")

	// A file that exists but is not a macaroon. Hex encoding would have
	// accepted it happily and the daemon would have rejected it later as
	// an authentication failure naming nothing useful.
	notMac := filepath.Join(t.TempDir(), "tls.cert")
	require.NoError(t, os.WriteFile(notMac, []byte("not a macaroon"), 0o600))

	_, err = NewWavelengthInvoiceClient(
		"http://localhost:10061", false, notMac,
	)
	require.ErrorContains(t, err, "is not a macaroon")
}

// writeTestMacaroon writes a serialized macaroon to a temp file, returning its
// path and the bytes on disk.
func writeTestMacaroon(t *testing.T) (string, []byte) {
	t.Helper()

	mac, err := macaroon.New(
		[]byte("root-key"), []byte("wavelength"), "waved",
		macaroon.LatestVersion,
	)
	require.NoError(t, err)

	serialized, err := mac.MarshalBinary()
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "waved.macaroon")
	require.NoError(t, os.WriteFile(path, serialized, 0o600))

	return path, serialized
}
