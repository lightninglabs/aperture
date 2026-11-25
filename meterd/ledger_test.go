package meterd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/pricesrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// testCredential mints an L402 macaroon for a fresh token and payment, and
// returns the token ID, the payment hash and the Authorization header value.
func testCredential(t *testing.T, seed byte) (string, lntypes.Hash, string) {
	t.Helper()

	var preimage lntypes.Preimage
	for i := range preimage {
		preimage[i] = seed
	}
	hash := preimage.Hash()

	var tokenID l402.TokenID
	for i := range tokenID {
		tokenID[i] = seed + 1
	}

	var id bytes.Buffer
	err := l402.EncodeIdentifier(&id, &l402.Identifier{
		Version:     l402.LatestVersion,
		PaymentHash: hash,
		TokenID:     tokenID,
	})
	require.NoError(t, err)

	mac, err := macaroon.New(
		[]byte("root key"), id.Bytes(), "aperture",
		macaroon.LatestVersion,
	)
	require.NoError(t, err)
	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)

	auth := fmt.Sprintf("L402 %s:%s",
		base64.StdEncoding.EncodeToString(macBytes), preimage)

	return tokenID.String(), hash, auth
}

// requestTextWithAuth is a serialized chat request carrying the given
// Authorization header.
func requestTextWithAuth(auth string) string {
	body := `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`

	return "POST /v1/chat/completions HTTP/1.1\r\n" +
		"Host: backend.example\r\n" +
		"Authorization: " + auth + "\r\n" +
		"Content-Type: application/json\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(body)) +
		"\r\n" + body
}

// TestPaymentHashFromRequestText checks which credentials yield a payment
// hash for a token.
func TestPaymentHashFromRequestText(t *testing.T) {
	t.Parallel()

	tokenID, hash, auth := testCredential(t, 7)
	otherToken, _, _ := testCredential(t, 9)

	// A credential whose preimage does not match the identifier's hash.
	badPreimage := auth[:len(auth)-64] +
		hex.EncodeToString(bytes.Repeat([]byte{1}, 32))

	// The legacy LSAT scheme carries the same credential.
	lsat := "LSAT " + strings.TrimPrefix(auth, "L402 ")

	// A macaroon cut short, and one in the v1 text format.
	macB64, preimageHex, _ := strings.Cut(strings.TrimPrefix(auth, "L402 "), ":")
	macBytes, err := base64.StdEncoding.DecodeString(macB64)
	require.NoError(t, err)
	truncated := "L402 " + base64.StdEncoding.EncodeToString(macBytes[:20]) +
		":" + preimageHex
	v1 := append([]byte{1}, macBytes[1:]...)
	oldFormat := "L402 " + base64.StdEncoding.EncodeToString(v1) + ":" +
		preimageHex

	// An identifier of an unknown version, otherwise well formed.
	idBytes := append([]byte{0, 1}, hash[:]...)
	tokenBytes, err := hex.DecodeString(tokenID)
	require.NoError(t, err)
	idBytes = append(idBytes, tokenBytes...)
	futureMac, err := macaroon.New(
		[]byte("root key"), idBytes, "aperture", macaroon.LatestVersion,
	)
	require.NoError(t, err)
	futureBytes, err := futureMac.MarshalBinary()
	require.NoError(t, err)
	future := "L402 " + base64.StdEncoding.EncodeToString(futureBytes) +
		":" + preimageHex

	mppHash := sha256.Sum256([]byte("mpp preimage"))
	mppToken := hex.EncodeToString(mppHash[:])

	tests := []struct {
		name    string
		text    string
		tokenID string
		want    lntypes.Hash
		ok      bool
	}{{
		name:    "l402 credential",
		text:    requestTextWithAuth(auth),
		tokenID: tokenID,
		want:    hash,
		ok:      true,
	}, {
		name:    "lsat credential",
		text:    requestTextWithAuth(lsat),
		tokenID: tokenID,
		want:    hash,
		ok:      true,
	}, {
		name:    "truncated macaroon",
		text:    requestTextWithAuth(truncated),
		tokenID: tokenID,
	}, {
		name:    "macaroon not in the v2 format",
		text:    requestTextWithAuth(oldFormat),
		tokenID: tokenID,
	}, {
		name:    "identifier of an unknown version",
		text:    requestTextWithAuth(future),
		tokenID: tokenID,
	}, {
		name:    "l402 credential of another token",
		text:    requestTextWithAuth(auth),
		tokenID: otherToken,
	}, {
		name:    "preimage does not match",
		text:    requestTextWithAuth(badPreimage),
		tokenID: tokenID,
	}, {
		name:    "mpp charge credential",
		text:    requestTextWithAuth("Payment eyJjaGFsbGVuZ2UiOnt9fQ"),
		tokenID: mppToken,
		want:    mppHash,
		ok:      true,
	}, {
		name:    "mpp credential with a token that is not a hash",
		text:    requestTextWithAuth("Payment eyJjaGFsbGVuZ2UiOnt9fQ"),
		tokenID: "beef",
	}, {
		name:    "no credential",
		text:    chatRequestText("gpt-test"),
		tokenID: tokenID,
	}, {
		name:    "unparseable request",
		text:    "not a request",
		tokenID: tokenID,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := paymentHashFromRequestText(tc.text, tc.tokenID)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

// ledgerStore is a store that keeps a usage ledger in memory.
type ledgerStore struct {
	Store

	mu       sync.Mutex
	payments map[string]lntypes.Hash
	records  []UsageRecord
	failPay  bool
}

// RecordPayment remembers the token's payment hash.
func (l *ledgerStore) RecordPayment(tokenID string, hash lntypes.Hash) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.failPay {
		return fmt.Errorf("ledger unavailable")
	}
	l.payments[tokenID] = hash

	return nil
}

// DebitUsage debits through the wrapped store and remembers the record.
func (l *ledgerStore) DebitUsage(tokenID string, releaseEstimate int64,
	usage UsageRecord) (Bundle, bool, error) {

	l.mu.Lock()
	l.records = append(l.records, usage)
	l.mu.Unlock()

	return l.Store.Debit(tokenID, usage.Tokens, releaseEstimate)
}

// newLedgerServer returns a server metering against a ledger store.
func newLedgerServer(t *testing.T, cfg *Config) (*Server, *ledgerStore) {
	t.Helper()

	inner, err := NewJSONStore(JSONStoreConfig{})
	require.NoError(t, err)

	ledger := &ledgerStore{
		Store:    inner,
		payments: make(map[string]lntypes.Hash),
	}
	server, err := NewServer(cfg, WithStore(ledger))
	require.NoError(t, err)

	return server, ledger
}

// recomputeDebit derives a weighted debit from a usage record alone, the way
// a buyer holding a receipt would: the usage's msat value doubled, divided by
// the summed rates, rounded up.
func recomputeDebit(r UsageRecord) int64 {
	fresh := r.PromptTokens - r.CachedTokens
	value := new(big.Int).Mul(
		big.NewInt(fresh*1000), big.NewInt(r.InputMsatPerThousandTokens),
	)
	value.Add(value, new(big.Int).Mul(
		big.NewInt(r.CachedTokens*r.CachedInputPermille),
		big.NewInt(r.InputMsatPerThousandTokens),
	))
	value.Add(value, new(big.Int).Mul(
		big.NewInt(r.CompletionTokens*1000),
		big.NewInt(r.OutputMsatPerThousandTokens),
	))
	value.Mul(value, big.NewInt(2))

	den := big.NewInt(
		(r.InputMsatPerThousandTokens + r.OutputMsatPerThousandTokens) *
			1000,
	)
	q, m := new(big.Int).QuoRem(value, den, new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}

	return q.Int64()
}

// TestServerUsageLedger drives a ledger store through authorization and the
// three kinds of usage report, and checks that every record explains its
// debit.
func TestServerUsageLedger(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfg := testConfig("")
	cfg.BundleTokens = 100000
	cfg.Models["gpt-test"].CachedInputPermille = 100
	server, ledger := newLedgerServer(t, cfg)

	tokenID, hash, auth := testCredential(t, 3)
	reqText := requestTextWithAuth(auth)

	_, err := server.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: reqText,
		TokenId:         tokenID,
		PriceSats:       150000,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	report := func(contentType, tail string, complete bool,
		authResp *pricesrpc.AuthorizeRequestResponse) {

		t.Helper()

		_, err := server.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
			TokenId:          tokenID,
			Path:             "/v1/chat/completions",
			ServiceName:      "llm",
			HttpStatus:       200,
			ContentType:      contentType,
			Complete:         complete,
			ResponseTail:     []byte(tail),
			ReservedEstimate: authResp.ReservedEstimate,
			SettleEstimate:   authResp.SettleEstimate,
		})
		require.NoError(t, err)
	}
	authorize := func() *pricesrpc.AuthorizeRequestResponse {
		t.Helper()

		resp, err := server.AuthorizeRequest(
			ctx, &pricesrpc.AuthorizeRequestRequest{
				Path:            "/v1/chat/completions",
				HttpRequestText: reqText,
				TokenId:         tokenID,
				ServiceName:     "llm",
			},
		)
		require.NoError(t, err)
		require.True(t, resp.Allowed)

		return resp
	}

	// An admitted request binds the token to its payment.
	resp := authorize()
	require.Equal(t, hash, ledger.payments[tokenID])

	// A split with cached prompt tokens is weighted, and the record
	// carries the rates that recompute it.
	report("application/json", `{"usage":{"prompt_tokens":1000,`+
		`"completion_tokens":300,"prompt_tokens_details":`+
		`{"cached_tokens":800}}}`, true, resp)

	// A total without a split debits the plain count.
	report("application/json", `{"usage":{"total_tokens":42}}`, true,
		authorize())

	// A stream the client abandoned settles at the estimate.
	report("text/event-stream", "data: {\"choices\":[]}\n\n", false,
		authorize())

	require.Len(t, ledger.records, 3)

	split := ledger.records[0]
	require.Equal(t, UsageSplit, split.Source)
	require.EqualValues(t, 1000, split.PromptTokens)
	require.EqualValues(t, 800, split.CachedTokens)
	require.EqualValues(t, 300, split.CompletionTokens)
	require.True(t, split.Weighted)
	require.EqualValues(t, 1000000, split.InputMsatPerThousandTokens)
	require.EqualValues(t, 2000000, split.OutputMsatPerThousandTokens)
	require.EqualValues(t, 100, split.CachedInputPermille)
	require.Equal(t, recomputeDebit(split), split.Tokens)

	// 200 fresh prompt tokens at 1000 msat, 800 cached at 100 msat and
	// 300 completion tokens at 2000 msat are worth 880000 msat, which is
	// ceil(880000/1500) = 587 bundle tokens at the blended rate.
	require.EqualValues(t, 587, split.Tokens)

	total := ledger.records[1]
	require.Equal(t, UsageTotal, total.Source)
	require.EqualValues(t, 42, total.TotalTokens)
	require.EqualValues(t, 42, total.Tokens)
	require.False(t, total.Weighted)

	estimate := ledger.records[2]
	require.Equal(t, UsageEstimate, estimate.Source)
	require.Positive(t, estimate.Tokens)
	require.False(t, estimate.Weighted)

	// The ledger's debits are the store's debits.
	bundle, ok := ledger.Get(tokenID)
	require.True(t, ok)
	require.EqualValues(t,
		100000-split.Tokens-total.Tokens-estimate.Tokens,
		bundle.RemainingTokens,
	)
}

// TestServerUsageLedgerMatchesPlainStore checks that a ledger store debits
// exactly what a plain store debits for the same reports.
func TestServerUsageLedgerMatchesPlainStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tails := []string{
		`{"usage":{"prompt_tokens":17,"completion_tokens":5}}`,
		`{"usage":{"prompt_tokens":1,"completion_tokens":0}}`,
		`{"usage":{"prompt_tokens":999,"completion_tokens":1,` +
			`"prompt_tokens_details":{"cached_tokens":998}}}`,
		`{"usage":{"total_tokens":7}}`,
	}

	remaining := func(store Store) int64 {
		cfg := testConfig("")
		cfg.BundleTokens = 100000
		cfg.Models["gpt-test"].CachedInputPermille = 333
		server, err := NewServer(cfg, WithStore(store))
		require.NoError(t, err)

		_, err = server.ChallengeMinted(
			ctx, &pricesrpc.ChallengeMintedRequest{
				Path:            "/v1/chat/completions",
				HttpRequestText: chatRequestText("gpt-test"),
				TokenId:         "ab",
				PriceSats:       1,
				ServiceName:     "llm",
			},
		)
		require.NoError(t, err)

		for _, tail := range tails {
			resp, err := server.AuthorizeRequest(
				ctx, &pricesrpc.AuthorizeRequestRequest{
					Path:            "/v1/chat/completions",
					HttpRequestText: chatRequestText("gpt-test"),
					TokenId:         "ab",
					ServiceName:     "llm",
				},
			)
			require.NoError(t, err)
			require.True(t, resp.Allowed)

			_, err = server.ReportUsage(
				ctx, &pricesrpc.ReportUsageRequest{
					TokenId:          "ab",
					HttpStatus:       200,
					ContentType:      "application/json",
					Complete:         true,
					ResponseTail:     []byte(tail),
					ReservedEstimate: resp.ReservedEstimate,
					SettleEstimate:   resp.SettleEstimate,
				},
			)
			require.NoError(t, err)
		}

		b, ok := store.Get("ab")
		require.True(t, ok)

		return b.RemainingTokens
	}

	plain, err := NewJSONStore(JSONStoreConfig{})
	require.NoError(t, err)
	inner, err := NewJSONStore(JSONStoreConfig{})
	require.NoError(t, err)
	ledger := &ledgerStore{
		Store:    inner,
		payments: make(map[string]lntypes.Hash),
	}

	require.Equal(t, remaining(plain), remaining(ledger))
	require.Len(t, ledger.records, len(tails))
	for _, r := range ledger.records[:3] {
		require.Equal(t, recomputeDebit(r), r.Tokens)
	}
}

// TestServerUsageLedgerPaymentFailure checks that a ledger that cannot record
// a payment does not deny the request.
func TestServerUsageLedgerPaymentFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server, ledger := newLedgerServer(t, testConfig(""))
	ledger.failPay = true

	tokenID, _, auth := testCredential(t, 5)
	reqText := requestTextWithAuth(auth)

	_, err := server.ChallengeMinted(ctx, &pricesrpc.ChallengeMintedRequest{
		Path:            "/v1/chat/completions",
		HttpRequestText: reqText,
		TokenId:         tokenID,
		PriceSats:       1500,
		ServiceName:     "llm",
	})
	require.NoError(t, err)

	resp, err := server.AuthorizeRequest(
		ctx, &pricesrpc.AuthorizeRequestRequest{
			Path:            "/v1/chat/completions",
			HttpRequestText: reqText,
			TokenId:         tokenID,
			ServiceName:     "llm",
		},
	)
	require.NoError(t, err)
	require.True(t, resp.Allowed)
	require.Empty(t, ledger.payments)
}
