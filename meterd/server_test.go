package meterd

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lightninglabs/aperture/pricesrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newTestClient spins up the prices server on an in-memory connection and
// returns a client speaking to it.
func newTestClient(t *testing.T, cfg *Config) pricesrpc.PricesClient {
	t.Helper()

	server, err := NewServer(cfg)
	require.NoError(t, err)

	listener := bufconn.Listen(1024 * 1024)

	grpcServer := grpc.NewServer()
	pricesrpc.RegisterPricesServer(grpcServer, server)

	go func() {
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context,
			_ string) (net.Conn, error) {

			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
	})

	return pricesrpc.NewPricesClient(conn)
}

// testConfig returns a config selling small bundles of a single test model,
// which keeps the expected amounts easy to follow: a bundle of 1000 tokens
// at a blended rate of 1500 msat per token costs 1500 sats.
func testConfig(statePath string) *Config {
	return &Config{
		ListenAddr:   DefaultListenAddr,
		BundleTokens: 1000,
		DefaultModel: "gpt-test",
		Models: map[string]*ModelConfig{
			"gpt-test": {
				InputMsatPerToken:  1000,
				OutputMsatPerToken: 2000,
			},
		},
		StatePath: statePath,
	}
}

// TestServerRoundTrip drives the full metering flow over gRPC: quote, mint,
// authorize, draw down through usage reports until exhaustion, and the
// denial carrying the price of the next bundle.
func TestServerRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")
	client := newTestClient(t, testConfig(statePath))

	const (
		path    = "/v1/chat/completions"
		tokenID = "aabbccddee"
	)
	reqText := chatRequestText("gpt-test")

	// Step 1: quote the bundle. 1000 tokens at (1000+2000)/2 msat per
	// token is 1.5e6 msat, so 1500 sats.
	priceResp, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            path,
		HttpRequestText: reqText,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1500, priceResp.PriceSats)

	// Step 2: aperture mints the challenge, which books the bundle.
	_, err = client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            path,
		HttpRequestText: reqText,
		TokenId:         tokenID,
		PriceSats:       priceResp.PriceSats,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// Step 3: the paid token is authorized to proceed.
	authResp, err := client.AuthorizeRequest(
		ctx, &pricesrpc.AuthorizeRequestRequest{
			Path:            path,
			HttpRequestText: reqText,
			TokenId:         tokenID,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
	require.True(t, authResp.Allowed)

	// Step 4: report the usage of a streamed response. The SSE tail
	// carries content chunks with null usage and a final usage chunk of
	// 300 prompt plus 300 completion tokens. The debit is 300*1000 +
	// 300*2000 = 900000 msat, so 900 sats, leaving 400 tokens worth 600
	// sats at the blended rate.
	sseTail := sseChunk(
		`{"choices":[{"delta":{"content":"hi"}}],"usage":null}`,
	) + sseChunk(
		`{"choices":[],"usage":{"prompt_tokens":300,`+
			`"completion_tokens":300,"total_tokens":600}}`,
	) + sseChunk(`[DONE]`)

	usageResp, err := client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:      tokenID,
		Path:         path,
		ServiceName:  "llm",
		HttpStatus:   200,
		ContentType:  sseContentType,
		Complete:     true,
		ResponseTail: []byte(sseTail),
	})
	require.NoError(t, err)
	require.EqualValues(t, 900, usageResp.DebitedSats)
	require.EqualValues(t, 600, usageResp.RemainingSats)

	// A repeated booking for the same token must not top the balance
	// back up.
	_, err = client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            path,
		HttpRequestText: reqText,
		TokenId:         tokenID,
		PriceSats:       priceResp.PriceSats,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// Step 5: a second identical report over-draws the remaining 400
	// tokens, which clamps the balance at zero.
	usageResp, err = client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:      tokenID,
		Path:         path,
		ServiceName:  "llm",
		HttpStatus:   200,
		ContentType:  sseContentType,
		Complete:     true,
		ResponseTail: []byte(sseTail),
	})
	require.NoError(t, err)
	require.EqualValues(t, 900, usageResp.DebitedSats)
	require.EqualValues(t, 0, usageResp.RemainingSats)

	// Step 6: the exhausted token is denied, with the price of the next
	// bundle attached so aperture can mint a fresh challenge directly.
	authResp, err = client.AuthorizeRequest(
		ctx, &pricesrpc.AuthorizeRequestRequest{
			Path:            path,
			HttpRequestText: reqText,
			TokenId:         tokenID,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
	require.False(t, authResp.Allowed)
	require.EqualValues(t, 1500, authResp.PriceSats)
	require.Contains(t, authResp.Reason, "exhausted")

	// A completely unknown token is also denied with a quote, but for a
	// different reason.
	authResp, err = client.AuthorizeRequest(
		ctx, &pricesrpc.AuthorizeRequestRequest{
			Path:            path,
			HttpRequestText: reqText,
			TokenId:         "0123456789",
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
	require.False(t, authResp.Allowed)
	require.EqualValues(t, 1500, authResp.PriceSats)
	require.Contains(t, authResp.Reason, "unknown")
}

// TestServerJSONUsageReport verifies the draw-down of a non-streamed JSON
// response, including a front-truncated tail.
func TestServerJSONUsageReport(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestClient(t, testConfig(""))

	const tokenID = "beef"
	reqText := chatRequestText("gpt-test")

	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: reqText,
		TokenId:         tokenID,
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// The captured tail starts mid-document, as a bounded tail of a long
	// body would. The usage of 100 prompt plus 200 completion tokens is
	// worth 100*1000 + 200*2000 = 500000 msat, so 500 sats. The debit is
	// weighted by direction against the blended 1500 msat bundle rate:
	// ceil(500000/1500) = 334 tokens, leaving 666 tokens worth 999 sats.
	tail := `ng content"}}],"usage":{"prompt_tokens":100,` +
		`"completion_tokens":200,"total_tokens":300}}`

	usageResp, err := client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:      tokenID,
		Path:         "/v1/chat/completions",
		ServiceName:  "llm",
		HttpStatus:   200,
		ContentType:  "application/json",
		Complete:     true,
		ResponseTail: []byte(tail),
	})
	require.NoError(t, err)
	require.EqualValues(t, 500, usageResp.DebitedSats)
	require.EqualValues(t, 999, usageResp.RemainingSats)
}

// TestServerUsageEdgeCases verifies the usage reports that must not debit
// anything: missing usage objects, incomplete streams and unknown tokens.
func TestServerUsageEdgeCases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestClient(t, testConfig(""))

	const tokenID = "cafe"
	reqText := chatRequestText("gpt-test")

	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: reqText,
		TokenId:         tokenID,
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// A complete 200 response without a usage object debits nothing, but
	// still reports the untouched balance of 1000 tokens (1500 sats).
	usageResp, err := client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:      tokenID,
		HttpStatus:   200,
		ContentType:  "application/json",
		Complete:     true,
		ResponseTail: []byte(`{"choices":[]}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, usageResp.DebitedSats)
	require.EqualValues(t, 1500, usageResp.RemainingSats)

	// An aborted stream without a usage object debits nothing either.
	usageResp, err = client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:      tokenID,
		HttpStatus:   200,
		ContentType:  sseContentType,
		Complete:     false,
		ResponseTail: []byte(sseChunk(`{"choices":[],"usage":null}`)),
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, usageResp.DebitedSats)
	require.EqualValues(t, 1500, usageResp.RemainingSats)

	// A report for an unknown token is not an error, it simply debits
	// nothing.
	usageResp, err = client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:      "unknown",
		HttpStatus:   200,
		ContentType:  "application/json",
		Complete:     true,
		ResponseTail: []byte(`{"usage":{"total_tokens":10}}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, usageResp.DebitedSats)
	require.EqualValues(t, 0, usageResp.RemainingSats)
}

// TestServerUnknownModelNoDefault verifies that pricing fails with an
// InvalidArgument error when the model is unknown and no default model is
// configured.
func TestServerUnknownModelNoDefault(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cfg := testConfig("")
	cfg.DefaultModel = ""
	client := newTestClient(t, cfg)

	_, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("no-such-model"),
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// A known model still resolves without a default.
	priceResp, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("gpt-test"),
	})
	require.NoError(t, err)
	require.EqualValues(t, 1500, priceResp.PriceSats)
}

// TestServerBlendedTotalDebit verifies that a usage object carrying only a
// total count is debited at the blended rate.
func TestServerBlendedTotalDebit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestClient(t, testConfig(""))

	const tokenID = "feed"

	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("gpt-test"),
		TokenId:         tokenID,
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// Without a prompt/completion split, 100 tokens are charged at the
	// blended rate of 1500 msat per token: 150 sats debited, 900 tokens
	// worth 1350 sats remaining.
	usageResp, err := client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:      tokenID,
		HttpStatus:   200,
		ContentType:  "application/json",
		Complete:     true,
		ResponseTail: []byte(`{"usage":{"total_tokens":100}}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 150, usageResp.DebitedSats)
	require.EqualValues(t, 1350, usageResp.RemainingSats)
}

// TestServerChallengeMintedUnknownModel verifies that booking fails with an
// InvalidArgument error when no model can be resolved.
func TestServerChallengeMintedUnknownModel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cfg := testConfig("")
	cfg.DefaultModel = ""
	client := newTestClient(t, cfg)

	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("no-such-model"),
		TokenId:         "dead",
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestServerModelSubstitution verifies that a bundle booked for one model
// cannot be spent on a different model: a cheap-model bundle authorized against
// an expensive model is denied and re-quoted.
func TestServerModelSubstitution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cfg := testConfig("")
	cfg.Models["expensive"] = &ModelConfig{
		InputMsatPerToken:  10_000,
		OutputMsatPerToken: 20_000,
	}
	client := newTestClient(t, cfg)

	const tokenID = "abcd"

	// Book a bundle for the cheap default model.
	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("gpt-test"),
		TokenId:         tokenID,
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// A request for the cheap model it was booked with is authorized.
	authResp, err := client.AuthorizeRequest(
		ctx, &pricesrpc.AuthorizeRequestRequest{
			Path:            "/v1/chat/completions",
			HttpRequestText: chatRequestText("gpt-test"),
			TokenId:         tokenID,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
	require.True(t, authResp.Allowed)

	// A request for the expensive model must be denied, with the price of a
	// fresh expensive-model bundle quoted so aperture mints a new challenge.
	authResp, err = client.AuthorizeRequest(
		ctx, &pricesrpc.AuthorizeRequestRequest{
			Path:            "/v1/chat/completions",
			HttpRequestText: chatRequestText("expensive"),
			TokenId:         tokenID,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
	require.False(t, authResp.Allowed)
	require.Contains(t, authResp.Reason, "does not match")
	require.Greater(t, authResp.PriceSats, int64(1500))
}

// TestServerConcurrentOverdraw verifies that the reservation accounting bounds
// how many concurrent requests a near-empty bundle authorizes, rather than
// letting N concurrent requests all authorize and run free.
func TestServerConcurrentOverdraw(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// A bundle of exactly one token, with a per-request estimate of one, so
	// a single in-flight request reserves the whole balance.
	cfg := testConfig("")
	cfg.BundleTokens = 1
	cfg.EstimatedTokens = 1
	client := newTestClient(t, cfg)

	const tokenID = "1010"

	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("gpt-test"),
		TokenId:         tokenID,
		PriceSats:       2,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// Fire many concurrent authorizations without any intervening report,
	// so their reservations accumulate.
	const n = 50
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			resp, err := client.AuthorizeRequest(
				ctx, &pricesrpc.AuthorizeRequestRequest{
					Path: "/v1/chat/completions",
					HttpRequestText: chatRequestText(
						"gpt-test",
					),
					TokenId:     tokenID,
					ServiceName: "llm",
				},
			)
			require.NoError(t, err)

			if resp.Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Without reservations all fifty would authorize on a one-token bundle.
	// With a one-token reservation only the first sees a positive balance,
	// so the overdraw is bounded to a single request.
	require.Equal(t, 1, allowed)
}

// TestServerContentEncodingAlert verifies that a usage report with a
// non-identity Content-Encoding does not silently swallow the debit: the
// plaintext branch is exercised alongside it to confirm reports still process.
func TestServerContentEncodingAlert(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestClient(t, testConfig(""))

	const tokenID = "9999"

	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("gpt-test"),
		TokenId:         tokenID,
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	// A report declaring gzip carries bytes the observer could not have
	// decoded. It is accepted without error, but with the plaintext tail
	// here it still parses. The alert path is a log side effect; the
	// contract under test is that the RPC does not fail.
	usageResp, err := client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:         tokenID,
		HttpStatus:      200,
		ContentType:     "application/json",
		ContentEncoding: "gzip",
		Complete:        true,
		ResponseTail:    []byte(`{"usage":{"total_tokens":100}}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 150, usageResp.DebitedSats)

	// An identity encoding is treated the same as an absent one.
	usageResp, err = client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:         tokenID,
		HttpStatus:      200,
		ContentType:     "application/json",
		ContentEncoding: "identity",
		Complete:        true,
		ResponseTail:    []byte(`{"usage":{"total_tokens":100}}`),
	})
	require.NoError(t, err)
	require.EqualValues(t, 150, usageResp.DebitedSats)
}

// TestServerStartStop verifies the listen and shutdown path of the daemon.
func TestServerStartStop(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	cfg.ListenAddr = "127.0.0.1:0"

	server, err := NewServer(cfg)
	require.NoError(t, err)

	require.NoError(t, server.Start())
	server.Stop()
}

// TestServerDefaultModelFallback verifies that requests with a missing or
// unknown model are priced as the default model.
func TestServerDefaultModelFallback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestClient(t, testConfig(""))

	// An unknown model falls back to the default rates.
	priceResp, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("no-such-model"),
	})
	require.NoError(t, err)
	require.EqualValues(t, 1500, priceResp.PriceSats)

	// A request without any parseable body does too.
	priceResp, err = client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: "GET / HTTP/1.1\r\nHost: x\r\n\r\n",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1500, priceResp.PriceSats)
// TestServerReservationEcho verifies that an allowed authorization carries
// the reserved estimate and that echoing it back in the usage report
// releases the exact reservation, so mismatched estimates leave no residue
// on the bundle.
func TestServerReservationEcho(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestClient(t, testConfig(""))

	const tokenID = "ec40"
	body := `{"model":"gpt-test","max_tokens":400,` +
		`"messages":[{"role":"user","content":"hi"}]}`
	reqText := "POST /v1/chat/completions HTTP/1.1\r\n" +
		"Host: backend.example\r\n" +
		"Content-Type: application/json\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
		"\r\n" + body

	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: reqText,
		TokenId:         tokenID,
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	authorize := func() *pricesrpc.AuthorizeRequestResponse {
		resp, err := client.AuthorizeRequest(
			ctx, &pricesrpc.AuthorizeRequestRequest{
				Path:            "/v1/chat/completions",
				HttpRequestText: reqText,
				TokenId:         tokenID,
				ServiceName:     "llm",
			},
		)
		require.NoError(t, err)

		return resp
	}

	// The request's max_tokens is reserved and rides back on the
	// response.
	first := authorize()
	require.True(t, first.Allowed)
	require.EqualValues(t, 400, first.ReservedEstimate)

	second := authorize()
	require.True(t, second.Allowed)

	// Report both requests, echoing the estimates back. Each report
	// debits ceil((10*1000 + 10*2000)/1500) = 20 tokens and releases its
	// full 400-token reservation.
	tail := `{"usage":{"prompt_tokens":10,"completion_tokens":10,` +
		`"total_tokens":20}}`
	for _, est := range []int64{
		first.ReservedEstimate, second.ReservedEstimate,
	} {
		_, err = client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
			TokenId:          tokenID,
			Path:             "/v1/chat/completions",
			ServiceName:      "llm",
			HttpStatus:       200,
			ContentType:      "application/json",
			Complete:         true,
			ResponseTail:     []byte(tail),
			ReservedEstimate: est,
		})
		require.NoError(t, err)
	}

	// With exact releases, no reservation residue eats the balance: two
	// further requests still authorize (960 remaining against two fresh
	// 400-token reservations). Were the releases dropped, the 800 tokens
	// of residue would deny the second of these.
	require.True(t, authorize().Allowed)
	require.True(t, authorize().Allowed)
}
