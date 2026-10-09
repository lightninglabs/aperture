package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// stubMinter returns fixed results from MintL402 and VerifyL402.
type stubMinter struct {
	mintErr   error
	verifyErr error
}

func (s *stubMinter) MintL402(context.Context,
	...l402.Service) (*macaroon.Macaroon, string, error) {

	if s.mintErr != nil {
		return nil, "", s.mintErr
	}

	mac, err := macaroon.New(
		make([]byte, 32), []byte("id"), "lsat", macaroon.LatestVersion,
	)
	return mac, "invoice", err
}

func (s *stubMinter) VerifyL402(context.Context,
	*mint.VerificationParams) error {

	return s.verifyErr
}

// stubChecker returns err from VerifyInvoiceStatus.
type stubChecker struct {
	err error
}

func (s *stubChecker) VerifyInvoiceStatus(lntypes.Hash,
	lnrpc.Invoice_InvoiceState, time.Duration) error {

	return s.err
}

// counterDelta returns how much counter's label value changed while fn ran.
func counterDelta(counter *prometheus.CounterVec, label string,
	fn func()) float64 {

	c := counter.WithLabelValues(label)
	before := testutil.ToFloat64(c)
	fn()
	return testutil.ToFloat64(c) - before
}

// TestRecordMint ensures FreshChallengeHeader counts every MintL402 call
// once, under the label of the failing step.
func TestRecordMint(t *testing.T) {
	tests := []struct {
		err    error
		result string
	}{
		{nil, "ok"},
		{mint.ErrChallengeFailed, "challenge_failed"},
		{mint.ErrIdentifierFailed, "identifier_failed"},
		{mint.ErrMintSecretFailed, "secret_failed"},
		{mint.ErrMacaroonFailed, "macaroon_failed"},
		{mint.ErrCaveatFailed, "caveat_failed"},
		{errors.New("other"), "unknown"},
	}
	for _, test := range tests {
		t.Run(test.result, func(t *testing.T) {
			err := test.err
			if err != nil {
				err = fmt.Errorf("%w: cause", err)
			}
			a := NewL402Authenticator(
				&stubMinter{mintErr: err}, &stubChecker{},
			)

			d := counterDelta(l402MintTotal, test.result, func() {
				_, _ = a.FreshChallengeHeader("svc", 1)
			})
			require.Equal(t, float64(1), d)
		})
	}
}

// TestRecordVerify ensures Accept counts every request once, under the
// reason it was accepted or denied for.
func TestRecordVerify(t *testing.T) {
	mac, err := macaroon.New(
		make([]byte, 32), []byte("id"), "lsat", macaroon.LatestVersion,
	)
	require.NoError(t, err)

	validHeader := http.Header{}
	err = l402.SetHeader(&validHeader, mac, lntypes.Preimage{})
	require.NoError(t, err)

	tests := []struct {
		name       string
		reason     string
		header     http.Header
		verifyErr  error
		invoiceErr error
	}{
		{
			reason: "accepted",
			header: validHeader,
		},
		{
			name:   "no header",
			reason: "no_credential",
			header: http.Header{},
		},
		{
			name:   "another scheme",
			reason: "no_credential",
			header: http.Header{
				"Authorization": {"Payment abc"},
			},
		},
		{
			reason: "malformed_header",
			header: http.Header{
				"Authorization": {"L402 abc"},
			},
		},
		{
			reason:     "invoice_unsettled",
			header:     validHeader,
			invoiceErr: errors.New("not settled"),
		},
		{
			reason: "bad_signature",
			header: validHeader,
			verifyErr: &mint.VerifyError{
				Reason: mint.VerifyBadSignature,
				Err:    mint.ErrInvalidSignature,
			},
		},
		{
			reason:    "unknown",
			header:    validHeader,
			verifyErr: errors.New("other"),
		},
	}
	for _, test := range tests {
		name := test.reason
		if test.name != "" {
			name = test.name
		}
		t.Run(name, func(t *testing.T) {
			a := NewL402Authenticator(
				&stubMinter{verifyErr: test.verifyErr},
				&stubChecker{err: test.invoiceErr},
			)

			header := test.header.Clone()
			d := counterDelta(l402VerifyTotal, test.reason, func() {
				_ = a.Accept(&header, "svc")
			})
			require.Equal(t, float64(1), d)
		})
	}
}
