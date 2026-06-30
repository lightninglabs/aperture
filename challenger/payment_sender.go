package challenger

import (
	"context"
	"fmt"
	"io"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"google.golang.org/grpc"
)

// PaymentClient is the subset of the lnrpc.LightningClient interface needed
// for sending payments.
type PaymentClient interface {
	// SendPaymentV2 sends a payment and returns a stream of payment
	// updates.
	SendPaymentV2(ctx context.Context, in *routerrpc.SendPaymentRequest,
		opts ...grpc.CallOption) (routerrpc.Router_SendPaymentV2Client,
		error)
}

// LndPaymentSender sends Lightning payments via LND's SendPaymentV2 RPC.
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

	stream, err := s.client.SendPaymentV2(ctx, &routerrpc.SendPaymentRequest{
		PaymentRequest: invoice,
		Amt:            amtSats,
	})
	if err != nil {
		return "", fmt.Errorf("failed to send payment: %w", err)
	}

	for {
		payment, err := stream.Recv()
		if err == io.EOF {
			return "", fmt.Errorf("payment stream ended without result")
		}
		if err != nil {
			return "", fmt.Errorf("failed to receive payment "+
				"update: %w", err)
		}

		switch payment.Status {
		case lnrpc.Payment_SUCCEEDED:
			return payment.PaymentPreimage, nil

		case lnrpc.Payment_FAILED:
			return "", fmt.Errorf("payment failed: %s",
				payment.FailureReason)
		}
	}
}
