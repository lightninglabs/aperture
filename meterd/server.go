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
	store *store

	grpcServer *grpc.Server

	quit chan struct{}
	wg   sync.WaitGroup
}

// NewServer creates a Server from the given configuration, loading any
// persisted bundle state.
func NewServer(cfg *Config) (*Server, error) {
	st, err := newStoreWithConfig(storeConfig{
		statePath:       cfg.StatePath,
		maxUnauthorized: cfg.MaxUnauthorizedBundles,
	})
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:   cfg,
		store: st,
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
	if err := s.store.flush(); err != nil {
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
			if err := s.store.flush(); err != nil {
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
			if n := s.store.expireStale(ttl); n > 0 {
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

// resolveModel maps a possibly empty or unknown model identifier to a
// configured model and its rates, falling back to the default model.
func (s *Server) resolveModel(model string) (string, *ModelConfig, error) {
	if model != "" {
		if rates, ok := s.cfg.Models[model]; ok {
			return model, rates, nil
		}
	}

	if s.cfg.DefaultModel != "" {
		if rates, ok := s.cfg.Models[s.cfg.DefaultModel]; ok {
			return s.cfg.DefaultModel, rates, nil
		}
	}

	return "", nil, fmt.Errorf("unknown model %q and no default model "+
		"configured", model)
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
// JSON body of the serialized HTTP request. Requests with a missing or
// unknown model are priced as the default model. When no model can be
// resolved at all, an InvalidArgument error is returned.
func (s *Server) GetPrice(_ context.Context,
	req *pricesrpc.GetPriceRequest) (*pricesrpc.GetPriceResponse, error) {

	model, rates, err := s.resolveModel(
		modelFromRequestText(req.HttpRequestText),
	)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	price := bundleQuoteSats(s.cfg.BundleTokens, rates)

	log.Debugf("Quoted %d sats for a bundle of %d tokens of model %s "+
		"(path %s)", price, s.cfg.BundleTokens, model, req.Path)

	return &pricesrpc.GetPriceResponse{PriceSats: price}, nil
}

// ChallengeMinted books a fresh token bundle for the minted challenge's
// token ID, remembering the model and the quoted price. Booking is
// idempotent per token ID.
func (s *Server) ChallengeMinted(_ context.Context,
	req *pricesrpc.ChallengeMintedRequest) (
	*pricesrpc.ChallengeMintedResponse, error) {

	model, _, err := s.resolveModel(
		modelFromRequestText(req.HttpRequestText),
	)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	booked, err := s.store.book(
		req.TokenId, model, s.cfg.BundleTokens, req.PriceSats,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to book "+
			"bundle: %v", err)
	}

	if booked {
		log.Infof("Booked bundle of %d tokens of model %s for %d "+
			"sats (token %s)", s.cfg.BundleTokens, model,
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
	// spent on a more expensive model. The booked model is authoritative.
	if booked, ok := s.store.get(req.TokenId); ok {
		requested := modelFromRequestText(req.HttpRequestText)
		resolved, _, err := s.resolveModel(requested)
		if err == nil && resolved != booked.Model {
			// Quote a bundle for the model the client actually
			// asked for, so the fresh challenge lets them buy what
			// they want rather than re-buying the cheap bundle.
			return s.denyAuthorization(
				req.TokenId, resolved,
				fmt.Sprintf("request model %q does not match "+
					"bundle model %q", resolved,
					booked.Model),
			), nil
		}
	}

	// The reservation estimate bounds how far concurrent requests can
	// overdraw a near-empty bundle before their usage is reported.
	estimate := s.requestEstimate(req.HttpRequestText)

	remaining, found, err := s.store.authorize(req.TokenId, estimate)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to update "+
			"bundle state: %v", err)
	}

	if found && remaining > 0 {
		log.Debugf("Authorized request for token %s, %d tokens "+
			"available", req.TokenId, remaining)

		return &pricesrpc.AuthorizeRequestResponse{Allowed: true}, nil
	}

	// The token is unknown or its bundle is spent. Quote the next bundle
	// at the booked model's rate when known, otherwise at the rate of the
	// model named in the request.
	reason := "unknown L402 token"
	var model string
	if found {
		reason = "token bundle exhausted"
		if b, ok := s.store.get(req.TokenId); ok {
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
	if _, rates, err := s.resolveModel(model); err == nil {
		price = bundleQuoteSats(s.cfg.BundleTokens, rates)
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

	// Release the reservation taken on authorization. The report does not
	// carry the request body, so the exact per-request estimate is not
	// recoverable here; the configured default estimate is released
	// instead. This is approximate, but the reservation is only a bound on
	// concurrent overdraw, not an accounting entry, so an imperfect release
	// merely widens or narrows that bound slightly.
	after, ok, err := s.store.debit(
		req.TokenId, debitTokens, s.cfg.EstimatedTokens,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to debit "+
			"bundle: %v", err)
	}
	if !ok {
		log.Warnf("Usage report for unknown token %s (path %s), "+
			"nothing debited", req.TokenId, req.Path)

		return &pricesrpc.ReportUsageResponse{}, nil
	}

	_, rates, err := s.resolveModel(after.Model)
	if err != nil {
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
