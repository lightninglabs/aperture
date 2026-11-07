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
		// The eviction age floor matches the janitor's TTL: within it a
		// paid-but-not-yet-used bundle is immune to count eviction, and
		// beyond it the janitor reaps unauthorized bundles anyway.
		minEvictionAge := cfg.UnauthorizedBundleTTL
		if minEvictionAge <= 0 {
			minEvictionAge = DefaultUnauthorizedBundleTTL
		}

		// Zero selects the default, negative disables expiry.
		reservationTTL := cfg.ReservationTTL
		if reservationTTL == 0 {
			reservationTTL = DefaultReservationTTL
		}

		var err error
		st, err = NewJSONStore(JSONStoreConfig{
			StatePath:       cfg.StatePath,
			MaxUnauthorized: cfg.MaxUnauthorizedBundles,
			MinEvictionAge:  minEvictionAge,
			ReservationTTL:  reservationTTL,
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
// authorized request within the bundle TTL, and drops expired reservations.
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

// reservationsExpire reports whether leaked reservations are dropped.
func (s *Server) reservationsExpire() bool {
	return s.cfg.ReservationTTL >= 0
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

	requested, determinate := modelFromRequestText(req.HttpRequestText)
	if !determinate {
		return nil, status.Error(codes.InvalidArgument, "unable to "+
			"determine the request model")
	}

	model, rates, err := s.rates.ResolveModel(requested)
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

	requested, determinate := modelFromRequestText(req.HttpRequestText)
	if !determinate {
		return nil, status.Error(codes.InvalidArgument, "unable to "+
			"determine the request model")
	}

	model, _, err := s.rates.ResolveModel(requested)
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
	ceiling := s.cfg.BundleTokens
	booked, known := s.store.Get(req.TokenId)
	if known {
		requested, determinate := modelFromRequestText(
			req.HttpRequestText,
		)

		// A request whose model we could not read must not ride an
		// existing bundle: the guard below is the only thing keeping a
		// cheap bundle from paying for an expensive model, and it is
		// skipped entirely when the model is unknown. Erroring rather
		// than denying is deliberate, since a denial would mint a
		// fresh challenge and the buyer would pay for a bundle whose
		// ChallengeMinted is about to fail the same way.
		if !determinate {
			return nil, status.Error(codes.InvalidArgument,
				"unable to determine the request model")
		}

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

		ceiling = s.rates.BundleTokens(booked.Model)
	}

	// The reservation covers the prompt as well as the completion.
	estimate := s.reservationEstimate(req.HttpRequestText, ceiling)

	auth, found, err := s.store.Authorize(req.TokenId, estimate)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "unable to update "+
			"bundle state: %v", err)
	}

	if found && auth.Admitted {
		log.Debugf("Authorized request for token %s reserving %d "+
			"tokens, %d tokens available", req.TokenId, estimate,
			auth.Available)

		// The reserved estimate rides back to aperture, which echoes
		// it in the matching usage report so the exact reservation is
		// released rather than an approximate default.
		return &pricesrpc.AuthorizeRequestResponse{
			Allowed:          true,
			ReservedEstimate: estimate,
		}, nil
	}

	// The token is unknown, its bundle is spent, or in-flight
	// reservations leave too little for this request. Quote the next
	// bundle at the booked model's rate, or else the requested model's.
	reason := "unknown L402 token"
	var (
		model      string
		retryLater bool
	)
	if found {
		reason = "token bundle exhausted"
		if known {
			model = booked.Model
		}

		// A funded bundle frees up once requests in flight settle, so
		// tell the buyer to retry. Without expiry a leak would never
		// clear, so quote instead.
		if auth.Remaining > 0 && s.reservationsExpire() {
			retryLater = true
			reason = fmt.Sprintf("token bundle has %d tokens "+
				"available after in-flight reservations, "+
				"request needs %d", auth.Available, estimate)
		}
	}
	if model == "" {
		// Best effort only: this picks the model to quote the next
		// bundle at, and an unreadable one just leaves the price at
		// zero so aperture falls back to GetPrice.
		model, _ = modelFromRequestText(req.HttpRequestText)
	}

	resp := s.denyAuthorization(req.TokenId, model, reason)
	resp.RetryLater = retryLater

	return resp, nil
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

// requestEstimate returns the completion part of a request's estimate.
func (s *Server) requestEstimate(httpRequestText string) int64 {
	return s.completionEstimate(maxTokensFromRequestText(httpRequestText))
}

// completionEstimate returns max_tokens, or the configured default if unset.
func (s *Server) completionEstimate(maxTokens int64) int64 {
	if maxTokens > 0 {
		return maxTokens
	}

	return s.cfg.EstimatedTokens
}

// reservationEstimate returns the tokens to reserve for a request: its body
// size as a prompt bound (the whole ceiling if the size is unknown) plus its
// completion estimate times n, clamped to [1, ceiling].
func (s *Server) reservationEstimate(httpRequestText string,
	ceiling int64) int64 {

	if ceiling <= 0 {
		ceiling = s.cfg.BundleTokens
	}
	if ceiling <= 0 {
		ceiling = DefaultBundleTokens
	}

	bytesPerToken := s.cfg.PromptBytesPerToken
	if bytesPerToken <= 0 {
		bytesPerToken = DefaultPromptBytesPerToken
	}

	prompt := ceiling
	if size, known := requestBodySize(httpRequestText); known {
		prompt = size / bytesPerToken
		if size%bytesPerToken != 0 {
			prompt++
		}
	}

	// Each of n choices can generate up to the completion ceiling.
	// Compare by division so the product cannot overflow.
	maxTokens, choices := completionFromRequestText(httpRequestText)
	completion := min(max(s.completionEstimate(maxTokens), 0), ceiling)
	if choices > 1 {
		if completion > ceiling/choices {
			completion = ceiling
		} else {
			completion *= choices
		}
	}

	// Clamp each part before adding so the sum cannot overflow.
	estimate := min(prompt, ceiling) + completion

	return max(min(estimate, ceiling), 1)
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
				"(path %s, status %d, complete %v)",
				req.TokenId, req.Path, req.HttpStatus,
				req.Complete)
		}
	}

	// Resolve the reservation to release up front, since an abandoned
	// stream settles at exactly this estimate below. The store releases
	// only a reservation of exactly this size; every aperture release
	// echoes it, so the fallback is for other clients.
	release := req.ReservedEstimate
	if release <= 0 {
		release = s.cfg.EstimatedTokens
	}

	// An incomplete successful response that carried bytes but no usage
	// object is a streamed generation the client walked away from: the
	// usage arrives in the stream's final chunk, so abandoning the stream
	// discards the bill for every token the upstream already produced and
	// the seller already paid for. Settle those at the reservation
	// estimate rather than zero. The estimate includes the prompt bound,
	// which usually overstates the prompt. A response that produced no
	// bytes at all still costs nothing.
	settleAtEstimate := !found && !req.Complete &&
		len(req.ResponseTail) > 0 &&
		req.HttpStatus >= 200 && req.HttpStatus < 300

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
	// total count when the split is absent. An abandoned stream settles at
	// the reservation estimate instead.
	var debitTokens int64
	switch {
	case found:
		debitTokens = counts.totalTokens
		if counts.hasSplit {
			debitTokens = counts.promptTokens +
				counts.completionTokens
		}

	case settleAtEstimate:
		debitTokens = release
		log.Warnf("Incomplete %d response for token %s (path %s) "+
			"carried %d tail bytes but no usage object; settling "+
			"at the reserved estimate of %d tokens",
			req.HttpStatus, req.TokenId, req.Path,
			len(req.ResponseTail), release)
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
	// is known, and at the blended rate otherwise, which covers both a
	// splitless usage object and an abandoned stream settled at the
	// estimate. Millisatoshis round up to satoshis.
	var debitedMsat int64
	switch {
	case !found && !settleAtEstimate:

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
