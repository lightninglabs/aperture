package meterd

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
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
	require.False(t, authResp.RetryLater)
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
	require.False(t, authResp.RetryLater)
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
// anything: a complete JSON body without usage, an aborted stream whose report
// has no reservation to settle at, and unknown tokens.
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

	// A complete 200 JSON response without a usage object debits nothing,
	// but still reports the untouched balance of 1000 tokens (1500 sats).
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

	// An aborted stream settles at the estimate, which is zero here.
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
		refused []*pricesrpc.AuthorizeRequestResponse
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

			mu.Lock()
			defer mu.Unlock()

			if resp.Allowed {
				allowed++
			} else {
				refused = append(refused, resp)
			}
		}()
	}
	wg.Wait()

	// The bundle is still funded, so every refusal says retry.
	for _, resp := range refused {
		require.True(t, resp.RetryLater)
		require.Contains(t, resp.Reason, "in-flight")
	}

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

// TestServerModelResolution verifies that requests naming no model are
// priced as the default model, while unknown models fail closed rather than
// being quoted (and proxied) at another model's rate.
func TestServerModelResolution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestClient(t, testConfig(""))

	// An unknown model is rejected outright.
	_, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("no-such-model"),
	})
	require.ErrorContains(t, err, "unknown model")

	// A request without any parseable body is priced as the default
	// model.
	priceResp, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: "GET / HTTP/1.1\r\nHost: x\r\n\r\n",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1500, priceResp.PriceSats)

	// An authorized bundle denies a request naming a model the rate
	// source does not know, instead of letting it ride the bundle
	// upstream.
	const tokenID = "f00d"
	_, err = client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("gpt-test"),
		TokenId:         tokenID,
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	authResp, err := client.AuthorizeRequest(
		ctx, &pricesrpc.AuthorizeRequestRequest{
			Path:            "/v1/chat/completions",
			HttpRequestText: chatRequestText("no-such-model"),
			TokenId:         tokenID,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
	require.False(t, authResp.Allowed)
	require.Contains(t, authResp.Reason, "unknown request model")
}

// TestServerReservationEcho verifies that an allowed authorization carries
// the reserved estimate and that echoing it back in the usage report
// releases the exact reservation, so mismatched estimates leave no residue
// on the bundle.
func TestServerReservationEcho(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newTestClient(t, testConfig(""))

	const tokenID = "ec40"
	body := `{"model":"gpt-test","max_tokens":300,` +
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

	// The request's max_tokens plus a one-token-per-byte bound on its
	// prompt is reserved and rides back on the response.
	first := authorize()
	require.True(t, first.Allowed)
	require.EqualValues(t, 300+len(body), first.ReservedEstimate)

	second := authorize()
	require.True(t, second.Allowed)

	// Report both requests, echoing the estimates back. Each report
	// debits ceil((10*1000 + 10*2000)/1500) = 20 tokens and releases its
	// full reservation.
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
	// reservations of 381 tokens). Were the releases dropped, the 762
	// tokens of residue would leave 198 available and deny both.
	require.True(t, authorize().Allowed)
	require.True(t, authorize().Allowed)
}

// TestServerSettlesAbandonedStreamAtEstimate verifies that an incomplete,
// successful streamed response whose tail carries bytes but no usage object
// is settled at the reservation estimate rather than for free.
//
// The usage object arrives in a stream's final chunk, so a client that walks
// away mid-generation discards it, and with it the bill for every token the
// upstream already produced. The estimate is what the request itself asked
// for, so the buyer never pays past its own stated ceiling.
func TestServerSettlesAbandonedStreamAtEstimate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state.json")

	// The request below carries no max_tokens, so the reservation falls
	// back to the configured default estimate; that fallback is exactly
	// what an abandoned stream settles at, so give it a real value.
	cfg := testConfig(statePath)
	cfg.EstimatedTokens = 200
	client := newTestClient(t, cfg)

	const (
		path    = "/v1/chat/completions"
		tokenID = "abandoned-stream-token"
	)
	reqText := chatRequestText("gpt-test")

	// Book and authorize a bundle the usual way. The authorization
	// reserves the default estimate, which is what the abandoned stream
	// settles at.
	priceResp, err := client.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            path,
		HttpRequestText: reqText,
	})
	require.NoError(t, err)

	_, err = client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            path,
		HttpRequestText: reqText,
		TokenId:         tokenID,
		PriceSats:       priceResp.PriceSats,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

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

	// The tail a walked-away stream leaves behind: content chunks, no
	// usage object, no [DONE].
	abandonedTail := sseChunk(
		`{"choices":[{"delta":{"content":"hi"}}],"usage":null}`,
	) + sseChunk(
		`{"choices":[{"delta":{"content":" there"}}],"usage":null}`,
	)

	usageResp, err := client.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:          tokenID,
		Path:             path,
		ServiceName:      "llm",
		HttpStatus:       200,
		ContentType:      sseContentType,
		Complete:         false,
		ResponseTail:     []byte(abandonedTail),
		ReservedEstimate: authResp.ReservedEstimate,
	})
	require.NoError(t, err)

	// The settle debits exactly the reservation: at the test rates the
	// estimate's blended value in sats, and not zero.
	require.NotZero(t, usageResp.DebitedSats)
	require.Less(t, usageResp.RemainingSats, priceResp.PriceSats)

	t.Run("no bytes costs nothing", func(t *testing.T) {
		resp, err := client.ReportUsage(
			ctx, &pricesrpc.ReportUsageRequest{
				TokenId:          tokenID,
				Path:             path,
				ServiceName:      "llm",
				HttpStatus:       200,
				ContentType:      sseContentType,
				Complete:         false,
				ResponseTail:     nil,
				ReservedEstimate: authResp.ReservedEstimate,
			},
		)
		require.NoError(t, err)
		require.Zero(t, resp.DebitedSats)
	})

	t.Run("failed response costs nothing", func(t *testing.T) {
		resp, err := client.ReportUsage(
			ctx, &pricesrpc.ReportUsageRequest{
				TokenId:          tokenID,
				Path:             path,
				ServiceName:      "llm",
				HttpStatus:       502,
				ContentType:      sseContentType,
				Complete:         false,
				ResponseTail:     []byte(abandonedTail),
				ReservedEstimate: authResp.ReservedEstimate,
			},
		)
		require.NoError(t, err)
		require.Zero(t, resp.DebitedSats)
	})

	t.Run("complete stream with usage is unaffected", func(t *testing.T) {
		completeTail := sseChunk(
			`{"choices":[],"usage":{"prompt_tokens":10,`+
				`"completion_tokens":10,"total_tokens":20}}`,
		) + sseChunk(`[DONE]`)

		resp, err := client.ReportUsage(
			ctx, &pricesrpc.ReportUsageRequest{
				TokenId:          tokenID,
				Path:             path,
				ServiceName:      "llm",
				HttpStatus:       200,
				ContentType:      sseContentType,
				Complete:         true,
				ResponseTail:     []byte(completeTail),
				ReservedEstimate: authResp.ReservedEstimate,
			},
		)
		require.NoError(t, err)
		// 10*1000 + 10*2000 = 30000 msat = 30 sats.
		require.EqualValues(t, 30, resp.DebitedSats)
	})
}

// TestServerNoRetryLaterWithoutExpiry checks that with expiry disabled a funded
// bundle is quoted rather than told to retry.
func TestServerNoRetryLaterWithoutExpiry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cfg := testConfig("")
	cfg.BundleTokens = 1
	cfg.EstimatedTokens = 1
	cfg.ReservationTTL = -1
	client := newTestClient(t, cfg)

	const tokenID = "2020"

	_, err := client.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: chatRequestText("gpt-test"),
		TokenId:         tokenID,
		PriceSats:       2,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	authorize := func() *pricesrpc.AuthorizeRequestResponse {
		resp, err := client.AuthorizeRequest(
			ctx, &pricesrpc.AuthorizeRequestRequest{
				Path:            "/v1/chat/completions",
				HttpRequestText: chatRequestText("gpt-test"),
				TokenId:         tokenID,
				ServiceName:     "llm",
			},
		)
		require.NoError(t, err)

		return resp
	}

	require.True(t, authorize().Allowed)

	refused := authorize()
	require.False(t, refused.Allowed)
	require.False(t, refused.RetryLater)
	require.Positive(t, refused.PriceSats)
}

// plainStore hides the wrapped store's ReservationExpirer, standing in for an
// embedder's store that does not report reservation expiry.
type plainStore struct {
	Store
}

// expiringStore is an embedder's store that reports it drops reservations.
type expiringStore struct {
	Store
}

// ReservationsExpire reports that reservations are dropped.
func (expiringStore) ReservationsExpire() bool {
	return true
}

// TestServerRetryLaterFollowsStore checks that the retry signal comes from the
// store rather than the config: a custom store keeps the 402 quote unless it
// reports that its reservations expire, whatever reservationttl says.
func TestServerRetryLaterFollowsStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wrap       func(Store) Store
		retryLater bool
	}{{
		name:       "store without expiry",
		wrap:       func(s Store) Store { return plainStore{s} },
		retryLater: false,
	}, {
		name:       "store with expiry",
		wrap:       func(s Store) Store { return expiringStore{s} },
		retryLater: true,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()

			inner, err := NewJSONStore(JSONStoreConfig{})
			require.NoError(t, err)

			// The config leaves reservationttl at its default,
			// which must not decide the answer for a custom store.
			cfg := testConfig("")
			cfg.BundleTokens = 1
			cfg.EstimatedTokens = 1
			server, err := NewServer(cfg, WithStore(tc.wrap(inner)))
			require.NoError(t, err)

			const tokenID = "3030"

			_, err = server.ChallengeMinted(
				ctx, &pricesrpc.ChallengeMintedRequest{
					Path:            "/v1/chat/completions",
					HttpRequestText: chatRequestText("gpt-test"),
					TokenId:         tokenID,
					PriceSats:       2,
					ServiceName:     "llm",
				},
			)
			require.NoError(t, err)

			req := &pricesrpc.AuthorizeRequestRequest{
				Path:            "/v1/chat/completions",
				HttpRequestText: chatRequestText("gpt-test"),
				TokenId:         tokenID,
				ServiceName:     "llm",
			}
			resp, err := server.AuthorizeRequest(ctx, req)
			require.NoError(t, err)
			require.True(t, resp.Allowed)

			// The first request's reservation holds the whole
			// balance, so the second is refused.
			resp, err = server.AuthorizeRequest(ctx, req)
			require.NoError(t, err)
			require.False(t, resp.Allowed)
			require.Equal(t, tc.retryLater, resp.RetryLater)
			require.Positive(t, resp.PriceSats)
		})
	}
}

// bookTestBundle books a bundle for the given token through ChallengeMinted.
func bookTestBundle(t *testing.T, client pricesrpc.PricesClient,
	tokenID string) {

	t.Helper()

	_, err := client.ChallengeMinted(
		t.Context(), &pricesrpc.ChallengeMintedRequest{
			Path:            "/v1/chat/completions",
			HttpRequestText: chatRequestText("gpt-test"),
			TokenId:         tokenID,
			PriceSats:       1,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
}

// authorizeText runs one AuthorizeRequest for the given token and serialized
// request.
func authorizeText(t *testing.T, client pricesrpc.PricesClient, tokenID,
	reqText string) *pricesrpc.AuthorizeRequestResponse {

	t.Helper()

	resp, err := client.AuthorizeRequest(
		t.Context(), &pricesrpc.AuthorizeRequestRequest{
			Path:            "/v1/chat/completions",
			HttpRequestText: reqText,
			TokenId:         tokenID,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)

	return resp
}

// largePromptBody returns a chat body with a prompt of promptBytes bytes.
func largePromptBody(promptBytes, maxTokens int) string {
	return fmt.Sprintf(`{"model":"gpt-test","max_tokens":%d,`+
		`"messages":[{"role":"user","content":%q}]}`, maxTokens,
		strings.Repeat("a", promptBytes))
}

// TestServerPromptReservationBoundsOverdraw checks that many large-prompt,
// tiny-max_tokens requests cannot all be admitted against one bundle.
func TestServerPromptReservationBoundsOverdraw(t *testing.T) {
	t.Parallel()

	const (
		bundleTokens = 100_000
		promptBytes  = 15_000
		n            = 200
		tokenID      = "0verd2a"
	)

	cfg := testConfig("")
	cfg.BundleTokens = bundleTokens
	client := newTestClient(t, cfg)
	bookTestBundle(t, client, tokenID)

	reqText := chatRequestTextRaw(largePromptBody(promptBytes, 1))

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		allowed  int
		reserved int64
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			resp, err := client.AuthorizeRequest(
				t.Context(), &pricesrpc.AuthorizeRequestRequest{
					Path:            "/v1/chat/completions",
					HttpRequestText: reqText,
					TokenId:         tokenID,
					ServiceName:     "llm",
				},
			)
			if err != nil || !resp.Allowed {
				return
			}

			mu.Lock()
			allowed++
			reserved += resp.ReservedEstimate
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Six requests fit, not two hundred.
	require.GreaterOrEqual(t, allowed, 1)
	require.LessOrEqual(t, reserved, int64(bundleTokens))
	require.LessOrEqual(t, allowed, bundleTokens/promptBytes)
}

// TestServerTruncatedBodyReservation checks that a truncated body reserves its
// declared size, or the whole bundle when none is declared.
func TestServerTruncatedBodyReservation(t *testing.T) {
	t.Parallel()

	const (
		bundleTokens = 1_000_000
		declared     = 200_000
		capBytes     = 64 * 1024
	)

	cfg := testConfig("")
	cfg.BundleTokens = bundleTokens
	cfg.EstimatedTokens = 10
	client := newTestClient(t, cfg)

	// The body as the backend sees it, and the prefix aperture forwards.
	body := largePromptBody(declared, 1)
	prefix := body[:capBytes]

	t.Run("declared length", func(t *testing.T) {
		t.Parallel()

		const tokenID = "7c01"
		bookTestBundle(t, client, tokenID)

		reqText := "POST /v1/chat/completions HTTP/1.1\r\n" +
			"Host: backend.example\r\n" +
			"Content-Type: application/json\r\n" +
			fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
			"\r\n" + prefix

		// The prefix cuts the JSON, so max_tokens is unreadable and
		// the completion part is the configured default. The prompt
		// part is the declared length, not the prefix, which a parser
		// that ignored Content-Length could not produce: it would see
		// an unparseable prefix and reserve the whole bundle.
		resp := authorizeText(t, client, tokenID, reqText)
		require.True(t, resp.Allowed)
		require.EqualValues(
			t, int64(len(body))+cfg.EstimatedTokens,
			resp.ReservedEstimate,
		)
		require.EqualValues(
			t, int64((len(body)+3)/4)+cfg.EstimatedTokens,
			resp.SettleEstimate,
		)
	})

	t.Run("unknown length", func(t *testing.T) {
		t.Parallel()

		const tokenID = "7c02"
		bookTestBundle(t, client, tokenID)

		reqText := "POST /v1/chat/completions HTTP/1.1\r\n" +
			"Host: backend.example\r\n" +
			"Content-Type: application/json\r\n" +
			"Transfer-Encoding: chunked\r\n" +
			"\r\n" + prefix

		resp := authorizeText(t, client, tokenID, reqText)
		require.True(t, resp.Allowed)
		require.EqualValues(t, bundleTokens, resp.ReservedEstimate)

		// The bytes past the prefix are unseen, so the settle figure
		// cannot be smaller than the reservation either.
		require.EqualValues(t, bundleTokens, resp.SettleEstimate)

		// With the whole bundle reserved, nothing else fits while
		// that request is in flight.
		other := authorizeText(
			t, client, tokenID, chatRequestText("gpt-test"),
		)
		require.False(t, other.Allowed)
	})
}

// TestServerAdmissionNeedsCoverage checks the admission rule end to end:
// covered, lone, busy and exhausted.
func TestServerAdmissionNeedsCoverage(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	cfg.BundleTokens = 1000
	client := newTestClient(t, cfg)

	const tokenID = "adm1"
	bookTestBundle(t, client, tokenID)

	// Roughly 600 tokens of reservation each.
	big := chatRequestTextRaw(largePromptBody(500, 50))

	first := authorizeText(t, client, tokenID, big)
	require.True(t, first.Allowed)
	require.Greater(t, first.ReservedEstimate, int64(500))

	// About 400 left can't cover a second one; the buyer should retry.
	second := authorizeText(t, client, tokenID, big)
	require.False(t, second.Allowed)
	require.True(t, second.RetryLater)
	require.Contains(t, second.Reason, "in-flight")

	// The first request reports 990 tokens of usage, leaving 10.
	tail := `{"usage":{"total_tokens":990}}`
	_, err := client.ReportUsage(t.Context(), &pricesrpc.ReportUsageRequest{
		TokenId:          tokenID,
		HttpStatus:       200,
		ContentType:      "application/json",
		Complete:         true,
		ResponseTail:     []byte(tail),
		ReservedEstimate: first.ReservedEstimate,
	})
	require.NoError(t, err)

	// Ten tokens can't cover it, but a lone request is admitted.
	lone := authorizeText(t, client, tokenID, big)
	require.True(t, lone.Allowed)

	// Its settlement clamps the balance at zero.
	_, err = client.ReportUsage(t.Context(), &pricesrpc.ReportUsageRequest{
		TokenId:          tokenID,
		HttpStatus:       200,
		ContentType:      "application/json",
		Complete:         true,
		ResponseTail:     []byte(`{"usage":{"total_tokens":600}}`),
		ReservedEstimate: lone.ReservedEstimate,
	})
	require.NoError(t, err)

	exhausted := authorizeText(t, client, tokenID, big)
	require.False(t, exhausted.Allowed)
	require.False(t, exhausted.RetryLater)
	require.EqualValues(t, 1500, exhausted.PriceSats)
	require.Contains(t, exhausted.Reason, "exhausted")
}

// TestServerReservationCountsChoices checks that n choices reserve max_tokens
// each.
func TestServerReservationCountsChoices(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	client := newTestClient(t, cfg)

	const tokenID = "c401"
	bookTestBundle(t, client, tokenID)

	body := `{"model":"gpt-test","max_tokens":100,"n":4,` +
		`"messages":[{"role":"user","content":"hi"}]}`

	resp := authorizeText(t, client, tokenID, chatRequestTextRaw(body))
	require.True(t, resp.Allowed)
	require.EqualValues(t, len(body)+400, resp.ReservedEstimate)
}

// TestReservationEstimateChoicesOverflow checks that a huge max_tokens times n
// cannot overflow into a tiny reservation.
func TestReservationEstimateChoicesOverflow(t *testing.T) {
	t.Parallel()

	const ceiling = int64(4_000_000_000)

	cfg := testConfig("")
	s := &Server{cfg: cfg}

	body := fmt.Sprintf(`{"model":"gpt-test","max_tokens":%d,"n":%d,`+
		`"messages":[]}`, ceiling, ceiling)

	got, _ := s.reservationEstimate(chatRequestTextRaw(body), ceiling)
	require.Equal(t, ceiling, got)
}

// TestServerAbandonedStreamSettlesRealistically checks that a stream the
// client walks away from is debited the settle estimate, a realistic prompt
// figure plus the completion ceiling, rather than the one token per byte
// reservation that gates admission. A report without the echoed settle
// estimate keeps settling at the reservation, and an echoed value larger
// than the reservation is clamped to it.
func TestServerAbandonedStreamSettlesRealistically(t *testing.T) {
	t.Parallel()

	const (
		bundleTokens = 500_000
		promptBytes  = 150_000
		estimated    = 10_000
		prefixBytes  = 64 * 1024
	)

	// Aperture forwards only a 64 KiB prefix of the body with its full
	// Content-Length, so the cut JSON hides max_tokens and the completion
	// part is the configured default.
	body := largePromptBody(promptBytes, 1)
	full := chatRequestTextRaw(body)
	reqText := full[:len(full)-len(body)+prefixBytes]
	reserve := int64(len(body) + estimated)
	settle := int64((len(body)+3)/4 + estimated)

	abandonedTail := []byte(sseChunk(
		`{"choices":[{"delta":{"content":"hi"}}],"usage":null}`,
	))

	tests := []struct {
		name       string
		echo       func(resp *pricesrpc.AuthorizeRequestResponse) int64
		wantDebits int64
	}{{
		name: "echoed settle estimate",
		echo: func(r *pricesrpc.AuthorizeRequestResponse) int64 {
			return r.SettleEstimate
		},
		wantDebits: settle,
	}, {
		name: "report predating the echo",
		echo: func(*pricesrpc.AuthorizeRequestResponse) int64 {
			return 0
		},
		wantDebits: reserve,
	}, {
		name: "settle estimate past the reservation",
		echo: func(r *pricesrpc.AuthorizeRequestResponse) int64 {
			return r.ReservedEstimate + 1
		},
		wantDebits: reserve,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			cfg := testConfig("")
			cfg.BundleTokens = bundleTokens
			cfg.EstimatedTokens = estimated
			server, err := NewServer(cfg)
			require.NoError(t, err)

			const tokenID = "5e77"
			_, err = server.ChallengeMinted(
				ctx, &pricesrpc.ChallengeMintedRequest{
					Path:            "/v1/chat/completions",
					HttpRequestText: reqText,
					TokenId:         tokenID,
					PriceSats:       1,
					ServiceName:     "llm",
				},
			)
			require.NoError(t, err)

			auth, err := server.AuthorizeRequest(
				ctx, &pricesrpc.AuthorizeRequestRequest{
					Path:            "/v1/chat/completions",
					HttpRequestText: reqText,
					TokenId:         tokenID,
					ServiceName:     "llm",
				},
			)
			require.NoError(t, err)
			require.True(t, auth.Allowed)
			require.Equal(t, reserve, auth.ReservedEstimate)
			require.Equal(t, settle, auth.SettleEstimate)

			_, err = server.ReportUsage(
				ctx, &pricesrpc.ReportUsageRequest{
					TokenId:          tokenID,
					Path:             "/v1/chat/completions",
					ServiceName:      "llm",
					HttpStatus:       200,
					ContentType:      sseContentType,
					Complete:         false,
					ResponseTail:     abandonedTail,
					ReservedEstimate: auth.ReservedEstimate,
					SettleEstimate:   tc.echo(auth),
				},
			)
			require.NoError(t, err)

			// The debit is the expected figure and the reservation
			// is released in full.
			b, ok := server.store.Get(tokenID)
			require.True(t, ok)
			require.Equal(
				t, bundleTokens-tc.wantDebits,
				b.RemainingTokens,
			)
			require.Zero(t, b.Reserved)
		})
	}
}

// TestServerSequentialRequestBeforeReport pins down why aperture reports usage
// before a response ends. A buyer's next request that arrives while the
// previous request's report is still pending finds that reservation held, so
// in the second half of a bundle it is told to retry later even though
// nothing else is in flight. Once the report lands the same request is
// admitted.
func TestServerSequentialRequestBeforeReport(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	cfg := testConfig("")
	cfg.BundleTokens = 1000
	client := newTestClient(t, cfg)

	const tokenID = "5e9e"
	bookTestBundle(t, client, tokenID)

	reqText := chatRequestTextRaw(
		`{"model":"gpt-test","max_tokens":200,` +
			`"messages":[{"role":"user","content":"hi"}]}`,
	)
	report := func(reserved int64, totalTokens int) {
		t.Helper()

		_, err := client.ReportUsage(
			ctx, &pricesrpc.ReportUsageRequest{
				TokenId:     tokenID,
				Path:        "/v1/chat/completions",
				ServiceName: "llm",
				HttpStatus:  200,
				ContentType: "application/json",
				Complete:    true,
				ResponseTail: []byte(fmt.Sprintf(
					`{"usage":{"total_tokens":%d}}`,
					totalTokens,
				)),
				ReservedEstimate: reserved,
			},
		)
		require.NoError(t, err)
	}

	// Draw the bundle into its second half.
	first := authorizeText(t, client, tokenID, reqText)
	require.True(t, first.Allowed)
	report(first.ReservedEstimate, 600)

	// The previous request has finished but its report has not landed.
	pending := authorizeText(t, client, tokenID, reqText)
	require.True(t, pending.Allowed)
	require.Less(t, int64(400), 2*pending.ReservedEstimate)

	next := authorizeText(t, client, tokenID, reqText)
	require.False(t, next.Allowed)
	require.True(t, next.RetryLater)

	// With the report in, the same request goes through.
	report(pending.ReservedEstimate, 10)
	next = authorizeText(t, client, tokenID, reqText)
	require.True(t, next.Allowed)
}

// TestServerSettlesCompleteResponseWithoutUsage checks which complete
// responses without usage settle at the estimate.
func TestServerSettlesCompleteResponseWithoutUsage(t *testing.T) {
	t.Parallel()

	cfg := testConfig("")
	cfg.EstimatedTokens = 200
	client := newTestClient(t, cfg)

	testCases := []struct {
		name        string
		tokenID     string
		contentType string
		tail        string
		debited     int64
		remaining   int64
	}{{
		name:        "stream without include_usage",
		tokenID:     "c0de01",
		contentType: sseContentType,
		tail: sseChunk(
			`{"choices":[{"delta":{"content":"hi"}}]}`,
		) + sseChunk(
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		) + sseChunk(`[DONE]`),

		// The settle estimate is the 200 token completion default
		// plus a prompt token per four bytes of the 64 byte body, 216
		// tokens. At the blended 1500 msat per token that is 324 sats,
		// leaving 784 tokens worth 1176 sats.
		debited:   324,
		remaining: 1176,
	}, {
		name:        "model listing without usage",
		tokenID:     "c0de02",
		contentType: "application/json",
		tail: `{"object":"list","data":[{"id":"gpt-test",` +
			`"object":"model"}]}`,
		debited:   0,
		remaining: 1500,
	}, {
		name:        "stream that failed after a 200",
		tokenID:     "c0de03",
		contentType: sseContentType,
		tail: sseChunk(
			`{"error":{"message":"overloaded"}}`,
		) + sseChunk(`[DONE]`),
		debited:   0,
		remaining: 1500,
	}, {
		name:        "stream with a mixed case content type",
		tokenID:     "c0de04",
		contentType: "Text/Event-Stream; charset=utf-8",
		tail: sseChunk(
			`{"choices":[{"delta":{"content":"hi"}}]}`,
		) + sseChunk(`[DONE]`),
		debited:   324,
		remaining: 1176,
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			reqText := chatRequestText("gpt-test")

			_, err := client.ChallengeMinted(
				ctx, &pricesrpc.ChallengeMintedRequest{
					Path:            "/v1/chat/completions",
					HttpRequestText: reqText,
					TokenId:         tc.tokenID,
					PriceSats:       1500,
					ServiceName:     "llm",
				},
			)
			require.NoError(t, err)

			auth, err := client.AuthorizeRequest(
				ctx, &pricesrpc.AuthorizeRequestRequest{
					Path:            "/v1/chat/completions",
					HttpRequestText: reqText,
					TokenId:         tc.tokenID,
					ServiceName:     "llm",
				},
			)
			require.NoError(t, err)
			require.True(t, auth.Allowed)
			require.EqualValues(t, 264, auth.ReservedEstimate)
			require.EqualValues(t, 216, auth.SettleEstimate)

			resp, err := client.ReportUsage(
				ctx, &pricesrpc.ReportUsageRequest{
					TokenId:          tc.tokenID,
					Path:             "/v1/chat/completions",
					ServiceName:      "llm",
					HttpStatus:       200,
					ContentType:      tc.contentType,
					Complete:         true,
					ResponseTail:     []byte(tc.tail),
					ReservedEstimate: auth.ReservedEstimate,
					SettleEstimate:   auth.SettleEstimate,
				},
			)
			require.NoError(t, err)

			require.EqualValues(t, tc.debited, resp.DebitedSats)
			require.EqualValues(
				t, tc.remaining, resp.RemainingSats,
			)
		})
	}
}
