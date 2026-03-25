package auth

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	// AuthSchemeL402 is the L402 (macaroon + preimage) authentication
	// scheme identifier.
	AuthSchemeL402 = "l402"

	// AuthSchemeMPP is the Payment HTTP Authentication Scheme identifier.
	AuthSchemeMPP = "mpp"

	// AuthSchemeL402MPP enables both L402 and MPP simultaneously.
	AuthSchemeL402MPP = "l402+mpp"
)

// SchemeTagged is an optional interface that authenticators can implement to
// declare which authentication scheme they handle. This is used by
// MultiAuthenticator to filter sub-authenticators based on the per-service
// auth scheme setting.
type SchemeTagged interface {
	// Scheme returns the authentication scheme identifier (e.g., "l402"
	// or "mpp") that this authenticator handles.
	Scheme() string
}

// MultiAuthenticator wraps multiple Authenticator implementations and tries
// each in order. It supports both the Authenticator and ReceiptProvider
// interfaces, delegating receipt generation to whichever sub-authenticator
// handles the credential type.
type MultiAuthenticator struct {
	authenticators []Authenticator
}

// Compile-time interface checks.
var _ Authenticator = (*MultiAuthenticator)(nil)
var _ ReceiptProvider = (*MultiAuthenticator)(nil)

// NewMultiAuthenticator creates a new MultiAuthenticator that tries each
// authenticator in order.
func NewMultiAuthenticator(auths ...Authenticator) *MultiAuthenticator {
	return &MultiAuthenticator{
		authenticators: auths,
	}
}

// Accept returns whether any of the wrapped authenticators accept the request.
// The first authenticator that returns true wins. This delegates to
// AcceptForScheme with an empty scheme, which tries all authenticators.
//
// NOTE: This is part of the Authenticator interface.
func (m *MultiAuthenticator) Accept(header *http.Header,
	serviceName string) bool {

	return m.AcceptForScheme(header, serviceName, "")
}

// AcceptForScheme returns whether any of the wrapped authenticators that match
// the given auth scheme accept the request. An empty scheme tries all
// authenticators (backwards compatible). The scheme string can be "l402",
// "mpp", or "l402+mpp".
func (m *MultiAuthenticator) AcceptForScheme(header *http.Header,
	serviceName string, scheme string) bool {

	for _, a := range m.authenticators {
		if !schemeMatches(a, scheme) {
			continue
		}

		if a.Accept(header, serviceName) {
			return true
		}
	}

	return false
}

// schemeMatches returns true if the authenticator should be used for the given
// scheme. An empty scheme matches everything. If the authenticator implements
// SchemeTagged, its declared scheme must be contained in the service's scheme
// string (e.g., "l402+mpp" contains both "l402" and "mpp").
func schemeMatches(a Authenticator, scheme string) bool {
	if scheme == "" {
		return true
	}

	tagged, ok := a.(SchemeTagged)
	if !ok {
		// Authenticators that don't declare a scheme are always
		// included (backwards compatible).
		return true
	}

	return strings.Contains(scheme, tagged.Scheme())
}

// FreshChallengeHeader returns merged challenge headers from all wrapped
// authenticators. This allows a 402 response to include challenges for
// multiple authentication schemes (e.g., both L402 and Payment), letting the
// client choose which scheme to use.
//
// NOTE: This is part of the Authenticator interface.
func (m *MultiAuthenticator) FreshChallengeHeader(serviceName string,
	servicePrice int64) (http.Header, error) {

	merged := make(http.Header)
	var numErrors int

	for _, auth := range m.authenticators {
		header, err := auth.FreshChallengeHeader(
			serviceName, servicePrice,
		)
		if err != nil {
			log.Errorf("MultiAuth: challenge generation "+
				"failed for authenticator: %v", err)
			numErrors++
			continue
		}

		// Merge all header values from this authenticator.
		for key, values := range header {
			for _, v := range values {
				merged.Add(key, v)
			}
		}
	}

	if len(merged) == 0 {
		return nil, fmt.Errorf("no authenticator produced a " +
			"challenge header")
	}

	// If some authenticators failed but at least one succeeded, log a
	// warning so operators notice the degraded multi-scheme state.
	if numErrors > 0 {
		log.Warnf("MultiAuth: %d of %d authenticators failed "+
			"to produce challenge headers — response will "+
			"contain partial scheme set", numErrors,
			len(m.authenticators))
	}

	return merged, nil
}

// ReceiptHeader tries each sub-authenticator that implements ReceiptProvider
// and returns the first non-nil receipt. This avoids the race condition of
// storing a per-request index on the shared struct; instead, each provider
// inspects the credential and only produces a receipt if it handles that
// credential type.
//
// NOTE: This is part of the ReceiptProvider interface.
func (m *MultiAuthenticator) ReceiptHeader(header *http.Header,
	serviceName string) http.Header {

	for _, auth := range m.authenticators {
		rp, ok := auth.(ReceiptProvider)
		if !ok {
			continue
		}

		receiptHdr := rp.ReceiptHeader(header, serviceName)
		if receiptHdr != nil {
			return receiptHdr
		}
	}

	return nil
}
