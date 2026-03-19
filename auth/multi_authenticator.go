package auth

import (
	"fmt"
	"net/http"
	"sync"
)

// MultiAuthenticator wraps multiple Authenticator implementations and tries
// each in order. It supports both the Authenticator and ReceiptProvider
// interfaces, delegating receipt generation to whichever sub-authenticator
// accepted the credential.
type MultiAuthenticator struct {
	authenticators []Authenticator

	// mu protects lastAcceptedIdx which tracks which authenticator
	// accepted the most recent request. This is used to delegate
	// ReceiptHeader calls.
	mu             sync.Mutex
	lastAcceptedIdx int
}

// Compile-time interface checks.
var _ Authenticator = (*MultiAuthenticator)(nil)
var _ ReceiptProvider = (*MultiAuthenticator)(nil)

// NewMultiAuthenticator creates a new MultiAuthenticator that tries each
// authenticator in order.
func NewMultiAuthenticator(auths ...Authenticator) *MultiAuthenticator {
	return &MultiAuthenticator{
		authenticators:  auths,
		lastAcceptedIdx: -1,
	}
}

// Accept returns whether any of the wrapped authenticators accept the request.
// The first authenticator that returns true wins. Thread-safe tracking of which
// authenticator accepted allows ReceiptHeader to delegate correctly.
//
// NOTE: This is part of the Authenticator interface.
func (m *MultiAuthenticator) Accept(header *http.Header,
	serviceName string) bool {

	m.mu.Lock()
	defer m.mu.Unlock()

	for i, auth := range m.authenticators {
		if auth.Accept(header, serviceName) {
			m.lastAcceptedIdx = i
			return true
		}
	}

	m.lastAcceptedIdx = -1
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

// ReceiptHeader delegates to the sub-authenticator that accepted the most
// recent request. If the accepting authenticator implements ReceiptProvider,
// its ReceiptHeader method is called. Otherwise, nil is returned.
//
// NOTE: This is part of the ReceiptProvider interface.
func (m *MultiAuthenticator) ReceiptHeader(header *http.Header,
	serviceName string) http.Header {

	m.mu.Lock()
	idx := m.lastAcceptedIdx
	m.mu.Unlock()

	if idx < 0 || idx >= len(m.authenticators) {
		return nil
	}

	rp, ok := m.authenticators[idx].(ReceiptProvider)
	if !ok {
		return nil
	}

	return rp.ReceiptHeader(header, serviceName)
}
