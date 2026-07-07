package meterd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/pricesrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

const (
	// janitorInterval is how often the janitor scans for stale bundles.
	// It runs frequently so mint spam is reaped soon after its short TTL
	// elapses.
	janitorInterval = time.Minute

	// flushInterval is how often the coalesced state flusher writes pending
	// state to disk. Bookings only mark the store dirty, so a burst of
	// un-paid challenge mints coalesces into at most one file rewrite per
	// interval instead of one per request.
	flushInterval = 5 * time.Second

	// maxRecvMsgSize is the maximum size in bytes of a gRPC message the
	// server accepts. The pricer serializes a bounded body prefix, but the
	// default 4 MiB cap is raised so a legitimately large request head
	// cannot trip a ResourceExhausted error on the metering path.
	maxRecvMsgSize = 32 * 1024 * 1024
)

// Server implements the pricesrpc.Prices service with token bundle metering.
// A client purchases a bundle of Config.BundleTokens LLM tokens with a
// single L402 payment and draws it down across requests. Once the bundle is
// spent, AuthorizeRequest denies further requests and quotes the price of
// the next bundle, prompting aperture to mint a fresh 402 challenge.
type Server struct {
	pricesrpc.UnimplementedPricesServer

	cfg   *Config
	store Store
	rates RateSource

	grpcServer *grpc.Server

	quit chan struct{}
	wg   sync.WaitGroup
}

// serverOptions holds the pluggable dependencies of a Server that an
// embedder can override.
type serverOptions struct {
	store Store
	rates RateSource
}

// ServerOption customizes a Server beyond its config. The options exist so
// that a daemon building on meterd as a library can swap in its own storage
// and pricing while reusing the metering logic unchanged.
type ServerOption func(*serverOptions)

// WithStore makes the server track bundles in the given store instead of the
// default JSON file store.
func WithStore(store Store) ServerOption {
	return func(o *serverOptions) {
		o.store = store
	}
}

// WithRateSource makes the server price with the given rate source instead
// of the static table built from the config's models map.
func WithRateSource(rates RateSource) ServerOption {
	return func(o *serverOptions) {
		o.rates = rates
	}
}

// NewServer creates a Server from the given configuration, loading any
// persisted bundle state. Without options, bundles live in the JSON file
// store and prices come from the config's static models map.
func NewServer(cfg *Config, opts ...ServerOption) (*Server, error) {
	options := &serverOptions{}
	for _, opt := range opts {
		opt(options)
	}

	st := options.store
	if st == nil {
		var err error
		st, err = NewJSONStore(JSONStoreConfig{
			StatePath:       cfg.StatePath,
			MaxUnauthorized: cfg.MaxUnauthorizedBundles,
		})
		if err != nil {
			return nil, err
		}
	}

	rates := options.rates
	if rates == nil {
		rates = newStaticRates(cfg)
	}

	return &Server{
		cfg:   cfg,
		store: st,
		rates: rates,
		quit:  make(chan struct{}),
	}, nil
}

// Start begins listening on the configured address and serving the prices
// gRPC interface, and launches the janitor.
func (s *Server) Start() error {
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxRecvMsgSize),
	}
	if s.cfg.TLSCertPath != "" {
		creds, err := credentials.NewServerTLSFromFile(
			s.cfg.TLSCertPath, s.cfg.TLSKeyPath,
		)
		if err != nil {
			return fmt.Errorf("unable to load TLS credentials: "+
				"%w", err)
		}

		opts = append(opts, grpc.Creds(creds))
	}

	s.grpcServer = grpc.NewServer(opts...)
	pricesrpc.RegisterPricesServer(s.grpcServer, s)

	listener, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("unable to listen on %s: %w",
			s.cfg.ListenAddr, err)
	}

	log.Infof("Prices server listening on %s", listener.Addr())

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		if err := s.grpcServer.Serve(listener); err != nil {
			log.Errorf("gRPC server exited with error: %v", err)
		}
	}()

	s.wg.Add(1)
	go s.janitor()

	s.wg.Add(1)
	go s.flusher()

	return nil
}

// Stop shuts down the gRPC server, the janitor and the flusher, draining any
// pending state to disk.
func (s *Server) Stop() {
	close(s.quit)

	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}

	s.wg.Wait()

	// Drain any state still pending after the workers have stopped.
	if err := s.store.Flush(); err != nil {
		log.Errorf("Unable to flush state on shutdown: %v", err)
	}
}

// flusher periodically drains coalesced state to disk, so bookings do not each
// trigger a synchronous full-file rewrite.
func (s *Server) flusher() {
	defer s.wg.Done()

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.store.Flush(); err != nil {
				log.Errorf("Unable to flush state: %v", err)
			}

		case <-s.quit:
			return
		}
	}
}

// janitor periodically expires bundles that were booked but never used by an
// authorized request within the bundle TTL.
func (s *Server) janitor() {
	defer s.wg.Done()

	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	ttl := s.unauthorizedBundleTTL()
	for {
		select {
		case <-ticker.C:
			if n := s.store.ExpireStale(ttl); n > 0 {
				log.Infof("Expired %d stale bundle(s) never "+
					"used within %v", n, ttl)
			}

		case <-s.quit:
			return
		}
	}
}

// unauthorizedBundleTTL returns how long a never-authorized bundle is retained
// before the janitor reaps it, falling back to the default when unset.
func (s *Server) unauthorizedBundleTTL() time.Duration {
	if s.cfg.UnauthorizedBundleTTL > 0 {
		return s.cfg.UnauthorizedBundleTTL
	}

	return DefaultUnauthorizedBundleTTL
}

// bundleQuoteSats computes the flat price in satoshis of a full token
// bundle. The bundle is priced at the model's blended per-token rate, the
// average of the input and output rate, on the assumption that a bundle is
// drawn down by a roughly even mix of prompt and completion tokens:
//
//	price_sats = ceil(bundleTokens * (inputMsat + outputMsat) / 2 / 1000).
func bundleQuoteSats(bundleTokens int64, rates *ModelConfig) int64 {
	msatTimesTwo := bundleTokens *
		(rates.InputMsatPerToken + rates.OutputMsatPerToken)

	return (msatTimesTwo + 1999) / 2000
}

// GetPrice quotes the price of a token bundle for the model named in the
// JSON body of the serialized HTTP request. Requests naming no model are
// priced as the default model; requests naming an unknown model are
// rejected with an InvalidArgument error, since a model the seller never
// priced must not be quoted (or proxied) at another model's rate.
func (s *Server) GetPrice(_ context.Context,
	req *pricesrpc.GetPriceRequest) (*pricesrpc.GetPriceResponse, error) {

	model, rates, err := s.rates.ResolveModel(
		modelFromRequestText(req.HttpRequestText),
	)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	bundleTokens := s.rates.BundleTokens(model)
	price := bundleQuoteSats(bundleTokens, rates)

	log.Debugf("Quoted %d sats for a bundle of %d tokens of model %s "+
		"(path %s)", price, bundleTokens, model, req.Path)

	return &pricesrpc.GetPriceResponse{PriceSats: price}, nil
}

// ChallengeMinted books a fresh token bundle for the minted challenge's
// token ID, remembering the model and the quoted price. Booking is
// idempotent per token ID.
func (s *Server) ChallengeMinted(_ context.Context,
	req *pricesrpc.ChallengeMintedRequest) (
	*pricesrpc.ChallengeMintedResponse, error) {

	model, _, err := s.rates.ResolveModel(
		modelFromRequestText(req.HttpRequestText),
	)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	bundleTokens := s.rates.BundleTokens(model)
	booked, err := s.store.Book(
		req.TokenId, model, bundleTokens, req.PriceSats,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to book "+
			"bundle: %v", err)
	}

	if booked {
		log.Infof("Booked bundle of %d tokens of model %s for %d "+
			"sats (token %s)", bundleTokens, model,
			req.PriceSats, req.TokenId)
	}

	return &pricesrpc.ChallengeMintedResponse{}, nil
}

// AuthorizeRequest allows a request through while the token's bundle still
// has tokens left. Unknown tokens and exhausted bundles are denied together
// with a quote for the next bundle, so aperture can mint a fresh challenge
// without a round trip through GetPrice. A request whose model does not match
// the bundle's booked model is also denied, so a cheap-model bundle cannot be
// spent on an expensive model. Business logic denials are never errors; errors
// are reserved for internal failures.
func (s *Server) AuthorizeRequest(_ context.Context,
	req *pricesrpc.AuthorizeRequestRequest) (
	*pricesrpc.AuthorizeRequestResponse, error) {

	// A request against a known bundle whose model differs from the one the
	// bundle was booked with is denied, so a cheap-model bundle cannot be
	// spent on a more expensive model. The booked model is authoritative,
	// and an unresolvable model fails closed: a model the rate source does
	// not know cannot be proxied upstream on another model's bundle.
	if booked, ok := s.store.Get(req.TokenId); ok {
		requested := modelFromRequestText(req.HttpRequestText)
		if requested != "" {
			resolved, _, err := s.rates.ResolveModel(requested)
			switch {
			case err != nil:
				return s.denyAuthorization(
					req.TokenId, requested,
					fmt.Sprintf("unknown request model "+
						"%q", requested),
				), nil

			case resolved != booked.Model:
				// Quote a bundle for the model the client
				// actually asked for, so the fresh challenge
				// lets them buy what they want rather than
				// re-buying the cheap bundle.
				return s.denyAuthorization(
					req.TokenId, resolved,
					fmt.Sprintf("request model %q does "+
						"not match bundle model %q",
						resolved, booked.Model),
				), nil
			}
		}
	}

	// The reservation estimate bounds how far concurrent requests can
	// overdraw a near-empty bundle before their usage is reported.
	estimate := s.requestEstimate(req.HttpRequestText)

	remaining, found, err := s.store.Authorize(req.TokenId, estimate)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to update "+
			"bundle state: %v", err)
	}

	if found && remaining > 0 {
		log.Debugf("Authorized request for token %s, %d tokens "+
			"available", req.TokenId, remaining)

		// The reserved estimate rides back to aperture, which echoes
		// it in the matching usage report so the exact reservation is
		// released rather than an approximate default.
		return &pricesrpc.AuthorizeRequestResponse{
			Allowed:          true,
			ReservedEstimate: estimate,
		}, nil
	}

	// The token is unknown or its bundle is spent. Quote the next bundle
	// at the booked model's rate when known, otherwise at the rate of the
	// model named in the request.
	reason := "unknown L402 token"
	var model string
	if found {
		reason = "token bundle exhausted"
		if b, ok := s.store.Get(req.TokenId); ok {
			model = b.Model
		}
	}
	if model == "" {
		model = modelFromRequestText(req.HttpRequestText)
	}

	return s.denyAuthorization(req.TokenId, model, reason), nil
}

// denyAuthorization builds a denial response quoting the price of a fresh
// bundle for the given model, so aperture can mint a new challenge without a
// round trip through GetPrice. When no rates resolve, the price is left at
// zero, which makes aperture fall back to a GetPrice call.
func (s *Server) denyAuthorization(tokenID, model,
	reason string) *pricesrpc.AuthorizeRequestResponse {

	var price int64
	if canonical, rates, err := s.rates.ResolveModel(model); err == nil {
		price = bundleQuoteSats(
			s.rates.BundleTokens(canonical), rates,
		)
	}

	log.Debugf("Denied request for token %s: %s", tokenID, reason)

	return &pricesrpc.AuthorizeRequestResponse{
		Allowed:   false,
		PriceSats: price,
		Reason:    reason,
	}
}

// requestEstimate derives the per-request token estimate to reserve on
// authorization. It uses the request body's max_tokens when present, otherwise
// the configured default estimate.
func (s *Server) requestEstimate(httpRequestText string) int64 {
	if maxTokens := maxTokensFromRequestText(httpRequestText); maxTokens > 0 {
		return maxTokens
	}

	return s.cfg.EstimatedTokens
}

// ReportUsage extracts the final usage object from the captured response
// tail and debits the token's bundle by the number of consumed tokens. The
// informational satoshi amounts of the response are derived from the booked
// model's rates: the debit is rounded up and the remaining balance is
// rounded down, so rounding never favors the client.
func (s *Server) ReportUsage(_ context.Context,
	req *pricesrpc.ReportUsageRequest) (*pricesrpc.ReportUsageResponse,
	error) {

	if !req.Complete {
		log.Infof("Usage report for token %s covers an incomplete "+
			"response (path %s, status %d)", req.TokenId,
			req.Path, req.HttpStatus)
	}

	// The proxy strips the client's Accept-Encoding for metered requests so
	// the captured tail is always plaintext. A non-identity Content-Encoding
	// here means the observed tail is compressed bytes we cannot parse,
	// which would let usage go unbilled. This is not expected to happen and
	// is loudly alerted on rather than silently debiting nothing.
	if enc := req.ContentEncoding; enc != "" && enc != "identity" {
		log.Errorf("Usage report for token %s carries a non-identity "+
			"Content-Encoding %q (path %s): the captured tail may "+
			"be compressed and unparseable, usage could go "+
			"unbilled", req.TokenId, enc, req.Path)
	}

	counts, found := extractUsage(req.ContentType, req.ResponseTail)
	if !found {
		// A complete, successful response without a usage object
		// deserves a warning, since it means usage goes unbilled.
		// Incomplete or failed responses are expected to lack one.
		if req.Complete && req.HttpStatus == http.StatusOK {
			log.Warnf("No usage object found in complete 200 "+
				"response for token %s (path %s), nothing "+
				"debited", req.TokenId, req.Path)
		} else {
			log.Debugf("No usage object found for token %s "+
				"(path %s, status %d, complete %v), nothing "+
				"debited", req.TokenId, req.Path,
				req.HttpStatus, req.Complete)
		}
	}

	// Resolve the booked model's rates up front, since the debit is
	// weighted by direction when the usage split is known. A bundle whose
	// model no longer resolves falls back to the raw token count.
	var rates *ModelConfig
	if booked, ok := s.store.Get(req.TokenId); ok {
		if _, r, err := s.rates.ResolveModel(booked.Model); err == nil {
			rates = r
		}
	}

	// The number of tokens to debit is the prompt/completion sum, or the
	// total count when the split is absent.
	var debitTokens int64
	if found {
		debitTokens = counts.totalTokens
		if counts.hasSplit {
			debitTokens = counts.promptTokens +
				counts.completionTokens
		}
	}

	// The bundle was priced at the blended (input+output)/2 rate, but the
	// buyer chooses the prompt/completion mix, and an all-output workload
	// costs the seller the full output rate. When the split is known, the
	// debit is weighted by direction, charging a completion token as
	// outRate/blend bundle tokens (and a prompt token as inRate/blend), so
	// the msat value drawn from the bundle always matches the msat value
	// served. The division rounds up, never favoring the client.
	if found && counts.hasSplit && rates != nil {
		rateSum := rates.InputMsatPerToken + rates.OutputMsatPerToken
		if rateSum > 0 {
			weightedTimesTwo := 2 *
				(counts.promptTokens*rates.InputMsatPerToken +
					counts.completionTokens*
						rates.OutputMsatPerToken)
			debitTokens = (weightedTimesTwo + rateSum - 1) /
				rateSum
		}
	}

	// Release the exact reservation the authorization took when the proxy
	// echoed it back, falling back to the configured default estimate for
	// reports predating the echo. An exact release keeps mismatched
	// estimates from accumulating into phantom exhaustion on a bundle with
	// real balance left.
	release := req.ReservedEstimate
	if release <= 0 {
		release = s.cfg.EstimatedTokens
	}

	after, ok, err := s.store.Debit(req.TokenId, debitTokens, release)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to debit "+
			"bundle: %v", err)
	}
	if !ok {
		log.Warnf("Usage report for unknown token %s (path %s), "+
			"nothing debited", req.TokenId, req.Path)

		return &pricesrpc.ReportUsageResponse{}, nil
	}

	if rates == nil {
		log.Warnf("No rates for booked model %s of token %s, "+
			"reporting zero amounts", after.Model, req.TokenId)

		return &pricesrpc.ReportUsageResponse{}, nil
	}

	// The debited amount is priced exactly per direction when the split
	// is known, and at the blended rate otherwise. Millisatoshis round up
	// to satoshis.
	var debitedMsat int64
	switch {
	case !found:

	case counts.hasSplit:
		debitedMsat = counts.promptTokens*rates.InputMsatPerToken +
			counts.completionTokens*rates.OutputMsatPerToken

	default:
		blendedTimesTwo := debitTokens *
			(rates.InputMsatPerToken + rates.OutputMsatPerToken)
		debitedMsat = (blendedTimesTwo + 1) / 2
	}
	debitedSats := (debitedMsat + 999) / 1000

	// The remaining balance is valued at the blended rate and rounded
	// down.
	remainingMsat := after.RemainingTokens *
		(rates.InputMsatPerToken + rates.OutputMsatPerToken) / 2
	remainingSats := remainingMsat / 1000

	if debitTokens > 0 {
		log.Infof("Debited %d tokens (%d sats) from token %s, %d "+
			"tokens (%d sats) remaining", debitTokens,
			debitedSats, req.TokenId, after.RemainingTokens,
			remainingSats)
	}

	return &pricesrpc.ReportUsageResponse{
		DebitedSats:   debitedSats,
		RemainingSats: remainingSats,
	}, nil
}
