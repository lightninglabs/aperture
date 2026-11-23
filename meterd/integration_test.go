package meterd

import (
	"context"
	"testing"

	"github.com/lightninglabs/aperture/pricesrpc"
	"github.com/stretchr/testify/require"
)

// TestFractionalCachedPricingFlow verifies that fractional rates and cached
// prompt tokens survive the complete bundle and session RPC flows.
func TestFractionalCachedPricingFlow(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	cfg.Models["gpt-test"] = &ModelConfig{
		InputMsatPerThousandTokens:  1000,
		OutputMsatPerThousandTokens: 2000,
		CachedInputPermille:         100,
	}
	client := newTestClient(t, cfg)
	ctx := context.Background()
	reqText := chatRequestText("gpt-test")

	price, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		HttpRequestText: reqText,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, price.PriceSats)

	_, err = client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		HttpRequestText: reqText,
		TokenId:         "cached",
		PriceSats:       price.PriceSats,
	})
	require.NoError(t, err)

	// Eighty of one hundred prompt tokens are cached. The request is worth
	// (20*1 + 80*0.1 + 200*2) msat = 428 msat, rounded up to one sat. Its
	// bundle debit is ceil(2*428 / 3) = 286 tokens, leaving 1071 msat.
	tail := `{"usage":{"prompt_tokens":100,"completion_tokens":200,` +
		`"total_tokens":300,"prompt_tokens_details":{` +
		`"cached_tokens":80}}}`
	usage, err := client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:      "cached",
		HttpStatus:   200,
		ContentType:  "application/json",
		Complete:     true,
		ResponseTail: []byte(tail),
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, usage.DebitedSats)
	require.EqualValues(t, 1, usage.RemainingSats)

	settled, err := client.SettleSession(
		ctx, &pricesrpc.SettleSessionRequest{
			HttpRequestText: reqText,
			HttpStatus:      200,
			ContentType:     "application/json",
			Complete:        true,
			ResponseTail:    []byte(tail),
		},
	)
	require.NoError(t, err)
	require.EqualValues(t, 1, settled.CostSats)
}
