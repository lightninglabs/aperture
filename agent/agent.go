// Package agent provides AI agent integration for Aperture's L402 payment
// protocol. It enables fully autonomous machine-to-machine payments where AI
// agents can access L402-protected APIs without human intervention.
//
// The core idea: an AI agent only needs a Lightning wallet connection to
// authenticate with any L402-protected service. No API keys, no OAuth, no
// passwords — just pay the invoice and present the receipt (preimage).
//
// # Minimum Information for Authenticity
//
// The L402 protocol requires only two pieces of information:
//   - A macaroon (issued by the server as part of the 402 challenge)
//   - A preimage (proof of Lightning payment)
//
// Together these form the L402 token: "L402 <macaroon>:<preimage>"
//
// This is the minimal authentication credential — it simultaneously proves
// payment AND grants access. No registration, no identity verification,
// no shared secrets beyond the Lightning payment itself.
//
// # Agent-to-Agent Payment Flow
//
//  1. Agent A wants to call Agent B's API (protected by Aperture)
//  2. Agent A sends HTTP request → gets 402 + Lightning invoice
//  3. Agent A pays the invoice via its LND node → gets preimage
//  4. Agent A retries request with "Authorization: L402 <mac>:<preimage>"
//  5. Aperture verifies payment and proxies to Agent B's backend
//
// This entire flow is automatic and requires zero human interaction.
//
// # Usage
//
//	agent, err := agent.New(agent.DefaultConfig())
//	// ... configure LND connection ...
//	err = agent.Start()
//
//	// Make authenticated requests to any L402 service
//	resp, err := agent.Client().Get(ctx, "https://api.example.com/data")
//
//	// The agent automatically handles:
//	// - 402 challenges
//	// - Invoice parsing
//	// - Lightning payments
//	// - Token caching and reuse
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/lightninglabs/lndclient"
)

// Agent represents an AI agent that can autonomously pay for and access
// L402-protected services. It wraps an LND connection and an L402-aware
// HTTP client.
type Agent struct {
	cfg    *Config
	client *Client
	lnd    *lndclient.GrpcLndServices
	store  *TokenStore

	started bool
	stopped bool
	mu      sync.Mutex
}

// New creates a new Agent with the given configuration.
func New(cfg *Config) (*Agent, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Generate a random agent ID if none provided.
	if cfg.AgentID == "" {
		idBytes := make([]byte, 8)
		if _, err := rand.Read(idBytes); err != nil {
			return nil, fmt.Errorf("generate agent ID: %w", err)
		}
		cfg.AgentID = "agent-" + hex.EncodeToString(idBytes)
	}

	return &Agent{cfg: cfg}, nil
}

// Start initializes the agent's LND connection, token store, and HTTP client.
// After Start returns, the agent is ready to make authenticated requests.
func (a *Agent) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.started {
		return fmt.Errorf("agent %s already started", a.cfg.AgentID)
	}

	if err := a.cfg.Validate(); err != nil {
		return err
	}

	log.Infof("Starting agent %s on network %s",
		a.cfg.AgentID, a.cfg.Network)

	// Initialize the token store.
	var err error
	a.store, err = NewTokenStore(a.cfg.TokenDir)
	if err != nil {
		return fmt.Errorf("create token store: %w", err)
	}

	// Connect to LND.
	a.lnd, err = lndclient.NewLndServices(&lndclient.LndServicesConfig{
		LndAddress:  a.cfg.LndHost,
		Network:     lndclient.Network(a.cfg.Network),
		TLSPath:     a.cfg.TLSPath,
		MacaroonDir: a.cfg.MacaroonPath,
	})
	if err != nil {
		return fmt.Errorf("connect to LND: %w", err)
	}

	// Create the L402-aware HTTP client.
	a.client = NewClient(
		a.lnd, a.store, a.cfg.MaxCost, a.cfg.MaxRoutingFee,
		a.cfg.PaymentTimeout, a.cfg.RequestTimeout,
		a.cfg.AllowInsecure,
	)

	a.started = true
	log.Infof("Agent %s started successfully", a.cfg.AgentID)

	return nil
}

// Stop shuts down the agent and releases all resources.
func (a *Agent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.started || a.stopped {
		return nil
	}

	log.Infof("Stopping agent %s", a.cfg.AgentID)
	a.lnd.Close()
	a.stopped = true

	return nil
}

// Client returns the L402-aware HTTP client. Use this to make requests to
// L402-protected services. The client automatically handles 402 challenges,
// pays invoices, and caches tokens.
func (a *Agent) Client() *Client {
	return a.client
}

// TokenStore returns the agent's token cache, which can be inspected for
// debugging or accounting purposes.
func (a *Agent) TokenStore() *TokenStore {
	return a.store
}

// ID returns the agent's unique identifier.
func (a *Agent) ID() string {
	return a.cfg.AgentID
}

// Call is a high-level convenience method that makes a GET request to an
// L402-protected URL and returns the response body. This is the simplest
// way for one agent to call another agent's API.
func (a *Agent) Call(ctx context.Context, url string) ([]byte, error) {
	resp, err := a.client.Get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected status %d from %s",
			resp.StatusCode, url)
	}

	return readBody(resp)
}

// readBody reads and returns the full response body, with a reasonable limit
// to prevent memory exhaustion.
func readBody(resp *http.Response) ([]byte, error) {
	const maxBody = 10 * 1024 * 1024 // 10MB
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}
