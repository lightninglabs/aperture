package meterd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lightninglabs/aperture/pricesrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sessionTestConfig returns a config selling a single test model at four msat
// per input token and nineteen per output, which is roughly the inference
// pricing sessions exist to carry.
func sessionTestConfig(statePath string) *Config {
	cfg := testConfig(statePath)
	cfg.EstimatedTokens = 1000
	cfg.Models = map[string]*ModelConfig{
		"gpt-test": {
			InputMsatPerToken:  4,
			OutputMsatPerToken: 19,
		},
	}

	return cfg
}

// TestQuoteSessionPricesOneRequest asserts a session is quoted the cost of one
// request rather than the cost of a whole bundle. Those differ by three orders
// of magnitude on the same config, which is exactly why the session intent
// needs its own question.
func TestQuoteSessionPricesOneRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := newTestClient(t, sessionTestConfig(statePath))

	const path = "/v1/chat/completions"
	reqText := chatRequestText("gpt-test")

	// A bundle of 1000 tokens at the blended (4+19)/2 msat rate is 11500
	// msat, so 12 sats.
	bundle, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            path,
		HttpRequestText: reqText,
	})
	require.NoError(t, err)
	require.EqualValues(t, 12, bundle.PriceSats)

	// One request estimated at 1000 tokens costs the same 12 sats here,
	// because the configured estimate happens to equal the bundle size. The
	// point is that the two answers are computed from different inputs, so
	// raising the bundle size must not raise the per-request quote.
	quote, err := client.QuoteSession(ctx, &pricesrpc.QuoteSessionRequest{
		Path:            path,
		HttpRequestText: reqText,
		ServiceName:     "llm",
	})
	require.NoError(t, err)
	require.EqualValues(t, 12, quote.UnitPriceSats)

	// The deposit is left to aperture's multiplier.
	require.EqualValues(t, 0, quote.DepositSats)

	cfg := sessionTestConfig(filepath.Join(t.TempDir(), "big.json"))
	cfg.BundleTokens = 1_000_000
	bigClient := newTestClient(t, cfg)

	bigBundle, err := bigClient.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            path,
		HttpRequestText: reqText,
	})
	require.NoError(t, err)
	require.EqualValues(t, 11_500, bigBundle.PriceSats)

	bigQuote, err := bigClient.QuoteSession(
		ctx, &pricesrpc.QuoteSessionRequest{
			Path:            path,
			HttpRequestText: reqText,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
	require.EqualValues(t, 12, bigQuote.UnitPriceSats)
}

// TestQuoteSessionUsesMaxTokens asserts a request that declares its own token
// ceiling is quoted against it rather than against the configured default.
func TestQuoteSessionUsesMaxTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := newTestClient(t, sessionTestConfig(statePath))

	reqText := chatRequestTextRaw(
		`{"model":"gpt-test","max_tokens":10000}`,
	)

	quote, err := client.QuoteSession(ctx, &pricesrpc.QuoteSessionRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: reqText,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// 10000 tokens at the blended 11.5 msat rate is 115000 msat, 115 sats.
	require.EqualValues(t, 115, quote.UnitPriceSats)
}

// TestQuoteSessionRejectsUnknownModel asserts a model the seller never priced
// is refused rather than quoted at another model's rate, matching what GetPrice
// already does.
func TestQuoteSessionRejectsUnknownModel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := newTestClient(t, sessionTestConfig(statePath))

	_, err := client.QuoteSession(ctx, &pricesrpc.QuoteSessionRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("gpt-unpriced"),
		ServiceName:     "llm",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestSettleSessionCostsPerDirection asserts a completed response is costed at
// the rate of each direction rather than at the blended estimate. An
// all-completion workload costs the seller the full output rate, and settling
// it at the blend would give the buyer nineteen msat of inference for eleven
// and a half.
func TestSettleSessionCostsPerDirection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := newTestClient(t, sessionTestConfig(statePath))

	// 1000 prompt and 9000 completion tokens is 1000*4 + 9000*19 = 175000
	// msat, so 175 sats. The blended rate would have said 115.
	tail := sseChunk(
		`{"choices":[],"usage":{"prompt_tokens":1000,`+
			`"completion_tokens":9000,"total_tokens":10000}}`,
	) + sseChunk(`[DONE]`)

	resp, err := client.SettleSession(ctx, &pricesrpc.SettleSessionRequest{
		SessionId:       "session-1",
		Path:            "/v1/chat/completions",
		ServiceName:     "llm",
		HttpStatus:      200,
		ContentType:     sseContentType,
		Complete:        true,
		ResponseTail:    []byte(tail),
		EstimateSats:    115,
		HttpRequestText: chatRequestText("gpt-test"),
	})
	require.NoError(t, err)
	require.EqualValues(t, 175, resp.CostSats)
}

// TestSettleSessionFallsBackToEstimate asserts that a response the pricer
// cannot cost settles at the estimate rather than at zero. Returning zero would
// make every truncated stream, unexpected content type, or compressed body a
// free request, which is a far worse failure than charging what the buyer was
// quoted.
func TestSettleSessionFallsBackToEstimate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := newTestClient(t, sessionTestConfig(statePath))

	// A fresh message per case: a protobuf message carries a mutex and must
	// not be copied.
	base := func() *pricesrpc.SettleSessionRequest {
		return &pricesrpc.SettleSessionRequest{
			SessionId:       "session-2",
			Path:            "/v1/chat/completions",
			ServiceName:     "llm",
			HttpStatus:      200,
			ContentType:     sseContentType,
			Complete:        true,
			EstimateSats:    42,
			HttpRequestText: chatRequestText("gpt-test"),
		}
	}

	tests := []struct {
		name    string
		mutate  func(*pricesrpc.SettleSessionRequest)
		wantSat int64
	}{{
		name: "no usage object in the tail",
		mutate: func(r *pricesrpc.SettleSessionRequest) {
			r.ResponseTail = []byte(sseChunk(`[DONE]`))
		},
		wantSat: 42,
	}, {
		name: "client disconnected mid stream",
		mutate: func(r *pricesrpc.SettleSessionRequest) {
			r.Complete = false
			r.ResponseTail = []byte(sseChunk(
				`{"choices":[{"delta":{"content":"hi"}}]}`,
			))
		},
		wantSat: 42,
	}, {
		name: "a model the seller never priced",
		mutate: func(r *pricesrpc.SettleSessionRequest) {
			r.HttpRequestText = chatRequestText("gpt-unpriced")
			r.ResponseTail = []byte(sseChunk(
				`{"choices":[],"usage":{"prompt_tokens":10,` +
					`"completion_tokens":10,` +
					`"total_tokens":20}}`,
			))
		},
		wantSat: 42,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := base()
			tc.mutate(req)

			resp, err := client.SettleSession(ctx, req)
			require.NoError(t, err)
			require.EqualValues(t, tc.wantSat, resp.CostSats)
		})
	}
}

// TestSettleSessionCostsUnsplitUsage asserts a usage object carrying only a
// total is costed at the blended rate, which is the same assumption the
// estimate was made under.
func TestSettleSessionCostsUnsplitUsage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := newTestClient(t, sessionTestConfig(statePath))

	tail := sseChunk(
		`{"choices":[],"usage":{"total_tokens":2000}}`,
	) + sseChunk(`[DONE]`)

	resp, err := client.SettleSession(ctx, &pricesrpc.SettleSessionRequest{
		SessionId:       "session-3",
		Path:            "/v1/chat/completions",
		ServiceName:     "llm",
		HttpStatus:      200,
		ContentType:     sseContentType,
		Complete:        true,
		ResponseTail:    []byte(tail),
		EstimateSats:    12,
		HttpRequestText: chatRequestText("gpt-test"),
	})
	require.NoError(t, err)

	// 2000 tokens at the blended 11.5 msat rate is 23000 msat, 23 sats.
	require.EqualValues(t, 23, resp.CostSats)
}

// TestSettleSessionRoundsTowardsTheSeller asserts a sub-satoshi cost never
// settles to zero. Inference is priced in millisatoshis, so a short request
// costs a fraction of a satoshi, and rounding it down would make small requests
// free.
func TestSettleSessionRoundsTowardsTheSeller(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := newTestClient(t, sessionTestConfig(statePath))

	// One prompt token and one completion token is 23 msat, well under a
	// satoshi.
	tail := sseChunk(
		`{"choices":[],"usage":{"prompt_tokens":1,` +
			`"completion_tokens":1,"total_tokens":2}}`,
	)

	resp, err := client.SettleSession(ctx, &pricesrpc.SettleSessionRequest{
		SessionId:       "session-4",
		Path:            "/v1/chat/completions",
		ServiceName:     "llm",
		HttpStatus:      200,
		ContentType:     sseContentType,
		Complete:        true,
		ResponseTail:    []byte(tail),
		EstimateSats:    12,
		HttpRequestText: chatRequestText("gpt-test"),
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.CostSats)
}
