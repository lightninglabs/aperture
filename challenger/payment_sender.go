package challenger

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc"
)

// PaymentClient is the subset of the lnrpc.LightningClient interface needed
// for sending payments.
type PaymentClient interface {
	// SendPaymentSync sends a payment synchronously and returns the
	// result.
	SendPaymentSync(ctx context.Context, in *lnrpc.SendRequest,
		opts ...grpc.CallOption) (*lnrpc.SendResponse, error)
}

// LndPaymentSender sends Lightning payments via LND's SendPaymentSync RPC.
// This is used by the MPP session authenticator to refund unspent session
// balances to the client's return invoice.
type LndPaymentSender struct {
	client PaymentClient
}

// NewLndPaymentSender creates a new LndPaymentSender.
func NewLndPaymentSender(client PaymentClient) *LndPaymentSender {
	return &LndPaymentSender{client: client}
}

// SendPayment sends a payment to the given invoice with the specified amount
// in satoshis. Returns the payment preimage hex on success.
func (s *LndPaymentSender) SendPayment(ctx context.Context, invoice string,
	amtSats int64) (string, error) {

	resp, err := s.client.SendPaymentSync(ctx, &lnrpc.SendRequest{
		PaymentRequest: invoice,
		Amt:            amtSats,
	})
	if err != nil {
		return "", fmt.Errorf("failed to send payment: %w", err)
	}

	if resp.PaymentError != "" {
		return "", fmt.Errorf("payment error: %s",
			resp.PaymentError)
	}

	return hex.EncodeToString(resp.PaymentPreimage), nil
}
