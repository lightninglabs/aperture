package auth

import (
	"errors"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// labelUnknown labels a mint or verify error that can't be classified.
const labelUnknown = "unknown"

var (
	// errMalformedHeader is returned when a request carries an L402
	// credential that can't be parsed.
	errMalformedHeader = errors.New("malformed L402 header")

	// errInvoiceUnsettled is returned when a valid L402's invoice is not
	// recorded as settled.
	errInvoiceUnsettled = errors.New("invoice not settled")

	// l402MintTotal counts L402 mint attempts by result.
	l402MintTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "aperture",
			Subsystem: "l402",
			Name:      "mint_total",
			Help: "Total number of L402 mint attempts by " +
				"result",
		},
		[]string{"result"},
	)

	// l402VerifyTotal counts L402 verify attempts by reason.
	l402VerifyTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "aperture",
			Subsystem: "l402",
			Name:      "verify_total",
			Help: "Total number of L402 verify attempts by " +
				"reason",
		},
		[]string{"reason"},
	)

	// mintFailures maps the errors returned by mint.MintL402 to their
	// result label.
	mintFailures = []struct {
		err    error
		result string
	}{
		{mint.ErrChallengeFailed, "challenge_failed"},
		{mint.ErrIdentifierFailed, "identifier_failed"},
		{mint.ErrMintSecretFailed, "secret_failed"},
		{mint.ErrMacaroonFailed, "macaroon_failed"},
		{mint.ErrCaveatFailed, "caveat_failed"},
	}
)

// recordMint increments the mint counter for the outcome of a MintL402 call.
func recordMint(err error) {
	result := "ok"
	if err != nil {
		result = labelUnknown
		for _, f := range mintFailures {
			if errors.Is(err, f.err) {
				result = f.result
				break
			}
		}
	}

	l402MintTotal.WithLabelValues(result).Inc()
}

// recordVerify increments the verify counter for the outcome of a verify
// call.
func recordVerify(err error) {
	var verifyErr *mint.VerifyError
	reason := labelUnknown
	switch {
	case err == nil:
		reason = "accepted"

	case errors.Is(err, l402.ErrNoCredential):
		reason = "no_credential"

	case errors.Is(err, errMalformedHeader):
		reason = "malformed_header"

	case errors.Is(err, errInvoiceUnsettled):
		reason = "invoice_unsettled"

	case errors.As(err, &verifyErr):
		reason = string(verifyErr.Reason)
	}

	l402VerifyTotal.WithLabelValues(reason).Inc()
}
