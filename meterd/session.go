package meterd

import (
	"context"
	"net/http"

	"github.com/lightninglabs/aperture/pricesrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// QuoteSession prices one request drawn against a prepaid MPP session balance.
//
// Where GetPrice quotes a whole token bundle, this quotes the single request in
// hand, at the same blended per-token rate the bundle is priced at. The
// estimate is the request's max_tokens when it declares one and the configured
// default otherwise, which is the same estimate the L402 path reserves against
// a bundle. A session and a bundle therefore charge the same rate for the same
// work; what differs is only who holds the balance.
//
// Nothing is booked or reserved here. The session's balance lives in aperture,
// which deducts this estimate itself and refunds whatever is left when the
// session closes, so a quote the buyer never uses costs the pricer nothing to
// have given.
func (s *Server) QuoteSession(_ context.Context,
	req *pricesrpc.QuoteSessionRequest) (*pricesrpc.QuoteSessionResponse,
	error) {

	requested, determinate := modelFromRequestText(req.HttpRequestText)
	if !determinate {
		return nil, status.Error(codes.InvalidArgument, "unable to "+
			"determine the request model")
	}

	model, rates, err := s.rates.ResolveModel(requested)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	estimate := s.requestEstimate(req.HttpRequestText)
	unitPrice := bundleQuoteSats(estimate, rates)

	log.Debugf("Quoted %d sats for one session request of %d estimated "+
		"tokens of model %s (path %s)", unitPrice, estimate, model,
		req.Path)

	// The deposit is left to aperture's configured multiplier. How many
	// requests a session should hold before it needs a top-up is an
	// operator policy, not a pricing question, and the operator already
	// expresses it with sessiondepositmultiplier.
	return &pricesrpc.QuoteSessionResponse{
		UnitPriceSats: unitPrice,
	}, nil
}

// SettleSession costs a response that was served against a session balance and
// returns what the request should actually have cost.
//
// The cost is derived from the same trailing usage object ReportUsage reads,
// and priced exactly per direction when the prompt and completion split is
// known: an all-completion workload costs the full output rate rather than the
// blended one the estimate assumed. Millisatoshis round up to satoshis, so
// rounding never favours the client.
//
// A response that carries no usage object settles at the estimate rather than
// at zero. Returning zero would make every unparseable response free, which
// turns a truncated stream or an unexpected content type into an unmetered
// request. Leaving the estimate in place charges what the challenge told the
// buyer a request would cost, which is the honest answer when nothing better is
// known.
func (s *Server) SettleSession(_ context.Context,
	req *pricesrpc.SettleSessionRequest) (*pricesrpc.SettleSessionResponse,
	error) {

	if !req.Complete {
		log.Infof("Session %s settlement covers an incomplete response "+
			"(path %s, status %d)", req.SessionId, req.Path,
			req.HttpStatus)
	}

	// Aperture strips the client's Accept-Encoding on a session bearer
	// request so the captured tail is plaintext. A non-identity encoding
	// here means the tail is compressed bytes that cannot be costed, which
	// would silently settle every request at its estimate.
	if enc := req.ContentEncoding; enc != "" && enc != "identity" {
		log.Errorf("Session %s settlement carries a non-identity "+
			"Content-Encoding %q (path %s): the captured tail may "+
			"be compressed and unparseable, usage could go "+
			"unpriced", req.SessionId, enc, req.Path)
	}

	counts, found := extractUsage(req.ContentType, req.ResponseTail)
	if !found {
		if req.Complete && req.HttpStatus == http.StatusOK {
			log.Warnf("No usage object found in complete 200 "+
				"response for session %s (path %s), settling "+
				"at the %d sat estimate", req.SessionId,
				req.Path, req.EstimateSats)
		} else {
			log.Debugf("No usage object found for session %s "+
				"(path %s, status %d, complete %v), settling "+
				"at the %d sat estimate", req.SessionId,
				req.Path, req.HttpStatus, req.Complete,
				req.EstimateSats)
		}

		return &pricesrpc.SettleSessionResponse{
			CostSats: req.EstimateSats,
		}, nil
	}

	// A session books no model the way a bundle does, so the rates come
	// from the model named in the echoed request. Without them there is no
	// way to price the tokens, and the estimate stands: guessing at another
	// model's rates would be worse than charging what the buyer was quoted.
	requested, determinate := modelFromRequestText(req.HttpRequestText)
	if !determinate {
		log.Warnf("Unable to determine the model of the session %s "+
			"request, settling at the %d sat estimate",
			req.SessionId, req.EstimateSats)

		return &pricesrpc.SettleSessionResponse{
			CostSats: req.EstimateSats,
		}, nil
	}

	_, rates, err := s.rates.ResolveModel(requested)
	if err != nil {
		log.Warnf("No rates to price session %s against, settling at "+
			"the %d sat estimate: %v", req.SessionId,
			req.EstimateSats, err)

		return &pricesrpc.SettleSessionResponse{
			CostSats: req.EstimateSats,
		}, nil
	}

	costSats := usageCostSats(&counts, rates)

	log.Infof("Session %s request cost %d sats against a %d sat estimate "+
		"(path %s)", req.SessionId, costSats, req.EstimateSats,
		req.Path)

	return &pricesrpc.SettleSessionResponse{CostSats: costSats}, nil
}

// usageCostSats prices a usage count at a model's rates, in satoshis rounded
// up. When the prompt and completion split is known each direction is charged
// its own rate; otherwise the total is charged at the blended rate, which is
// the same assumption a bundle is priced under.
func usageCostSats(counts *usageCounts, rates *ModelConfig) int64 {
	var costMsat int64
	if counts.hasSplit {
		costMsat = counts.promptTokens*rates.InputMsatPerToken +
			counts.completionTokens*rates.OutputMsatPerToken
	} else {
		blendedTimesTwo := counts.totalTokens *
			(rates.InputMsatPerToken + rates.OutputMsatPerToken)
		costMsat = (blendedTimesTwo + 1) / 2
	}

	return (costMsat + 999) / 1000
}
