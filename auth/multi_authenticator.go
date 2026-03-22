package auth

import (
	"fmt"
	"net/http"
)

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
// The first authenticator that returns true wins.
//
// NOTE: This is part of the Authenticator interface.
func (m *MultiAuthenticator) Accept(header *http.Header,
	serviceName string) bool {

	for _, auth := range m.authenticators {
		if auth.Accept(header, serviceName) {
			return true
		}
	}

	return false
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

	for _, auth := range m.authenticators {
		header, err := auth.FreshChallengeHeader(
			serviceName, servicePrice,
		)
		if err != nil {
			log.Errorf("Error getting challenge header from "+
				"authenticator: %v", err)
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
