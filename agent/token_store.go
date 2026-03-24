package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwire"
	"gopkg.in/macaroon.v2"
)

// Credential represents a paid L402 credential that an agent can use to
// authenticate with a service. This is the minimum information needed:
// a macaroon (from the server) and a preimage (proof of payment).
type Credential struct {
	// Macaroon is the base macaroon issued by the server.
	Macaroon *macaroon.Macaroon

	// Preimage is the proof of Lightning payment.
	Preimage lntypes.Preimage

	// PaymentHash identifies the payment.
	PaymentHash lntypes.Hash

	// AmountPaid is the total amount paid in millisatoshis.
	AmountPaid lnwire.MilliSatoshi

	// RoutingFeePaid is the routing fee paid in millisatoshis.
	RoutingFeePaid lnwire.MilliSatoshi

	// CreatedAt is when this credential was acquired.
	CreatedAt time.Time
}

// AuthHeader returns the L402 Authorization header value for this credential.
func (c *Credential) AuthHeader() (string, error) {
	macBytes, err := c.Macaroon.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("marshal macaroon: %w", err)
	}

	macB64 := base64.StdEncoding.EncodeToString(macBytes)
	return fmt.Sprintf("L402 %s:%s", macB64, c.Preimage), nil
}

// ApplyToRequest sets the Authorization header on an HTTP request.
func (c *Credential) ApplyToRequest(req *http.Request) error {
	authValue, err := c.AuthHeader()
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", authValue)
	return nil
}

// TokenStore provides thread-safe in-memory caching of L402 credentials,
// keyed by the target service URL. This allows an agent to reuse paid tokens
// across multiple requests to the same service without re-paying.
type TokenStore struct {
	mu     sync.RWMutex
	tokens map[string]*Credential
}

// NewTokenStore creates a new in-memory token store.
func NewTokenStore(storeDir string) (*TokenStore, error) {
	return &TokenStore{
		tokens: make(map[string]*Credential),
	}, nil
}

// Get retrieves a cached credential for the given service URL. Returns nil
// if no credential exists.
func (s *TokenStore) Get(serviceURL string) *Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cred, ok := s.tokens[tokenKey(serviceURL)]
	if !ok {
		return nil
	}

	return cred
}

// Put stores a paid credential for a service URL.
func (s *TokenStore) Put(serviceURL string, cred *Credential) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokens[tokenKey(serviceURL)] = cred
}

// Delete removes a cached credential for the given service URL.
func (s *TokenStore) Delete(serviceURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.tokens, tokenKey(serviceURL))
}

// Count returns the number of credentials currently cached.
func (s *TokenStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.tokens)
}

// tokenKey creates a deterministic key from a service URL by hashing it.
func tokenKey(serviceURL string) string {
	h := sha256.Sum256([]byte(serviceURL))
	return hex.EncodeToString(h[:16])
}
