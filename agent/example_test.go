package agent_test

import (
	"context"
	"fmt"

	"github.com/lightninglabs/aperture/agent"
)

// Example_agentToAgent demonstrates how two AI agents can communicate
// through L402-protected APIs with automatic Lightning payments.
//
// Agent A (consumer) wants data from Agent B (provider).
// Agent B's API is behind an Aperture proxy requiring L402 payment.
// Agent A automatically pays the Lightning invoice and gets access.
//
// No API keys, no OAuth, no passwords — just Lightning payments.
func Example_agentToAgent() {
	// Create an agent with minimal config: just LND connection details.
	cfg := agent.DefaultConfig()
	cfg.LndHost = "localhost:10009"
	cfg.TLSPath = "/path/to/tls.cert"
	cfg.MacaroonPath = "/path/to/admin.macaroon"
	cfg.Network = "regtest"

	// Optional: set spending limits per request.
	cfg.MaxCost = 100 // max 100 sats per API call

	a, err := agent.New(cfg)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	// Start connects to LND and initializes the token cache.
	if err := a.Start(); err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer a.Stop()

	// Make a request to an L402-protected service.
	// If the service returns 402, the agent automatically:
	//   1. Parses the Lightning invoice from the challenge
	//   2. Pays the invoice via LND
	//   3. Retries with the L402 token
	ctx := context.Background()
	data, err := a.Call(ctx, "https://api.agent-b.example.com/inference")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	fmt.Printf("Got %d bytes from Agent B\n", len(data))

	// Subsequent requests reuse the cached L402 token — no re-payment.
	data2, err := a.Call(ctx, "https://api.agent-b.example.com/inference")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	fmt.Printf("Got %d bytes (cached token, no payment)\n", len(data2))
}

// Example_multiService shows an agent accessing multiple L402-protected
// services, each with independent payment and token management.
func Example_multiService() {
	cfg := agent.DefaultConfig()
	cfg.LndHost = "localhost:10009"
	cfg.TLSPath = "/path/to/tls.cert"
	cfg.MacaroonPath = "/path/to/admin.macaroon"
	cfg.Network = "regtest"
	cfg.TokenDir = "/tmp/agent-tokens" // Persist tokens across restarts

	a, err := agent.New(cfg)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	if err := a.Start(); err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer a.Stop()

	ctx := context.Background()

	// Each service gets its own L402 token and payment.
	// The agent handles all of this transparently.
	services := []string{
		"https://llm-agent.example.com/v1/chat",
		"https://data-agent.example.com/v1/query",
		"https://image-agent.example.com/v1/generate",
	}

	for _, svc := range services {
		data, err := a.Call(ctx, svc)
		if err != nil {
			fmt.Printf("Error calling %s: %v\n", svc, err)
			continue
		}
		fmt.Printf("Got %d bytes from %s\n", len(data), svc)
	}

	fmt.Printf("Tokens cached: %d\n", a.TokenStore().Count())
}
