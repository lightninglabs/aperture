package agent

import (
	"errors"
	"time"

	"github.com/btcsuite/btcd/btcutil"
)

const (
	// DefaultMaxCostSats is the default maximum amount in satoshis that an
	// agent will pay for a single L402 token automatically.
	DefaultMaxCostSats = btcutil.Amount(1000)

	// DefaultMaxRoutingFeeSats is the default maximum routing fee in
	// satoshis that an agent will pay to acquire an L402 token.
	DefaultMaxRoutingFeeSats = btcutil.Amount(10)

	// DefaultPaymentTimeout is the default maximum time we allow an
	// invoice payment to take before giving up.
	DefaultPaymentTimeout = 60 * time.Second

	// DefaultRequestTimeout is the default timeout for HTTP requests
	// made by an agent.
	DefaultRequestTimeout = 30 * time.Second
)

// Config holds the configuration for an AI agent that can autonomously handle
// L402 payments. This is intentionally minimal — an agent only needs Lightning
// payment capability and a target to call.
type Config struct {
	// AgentID is a unique identifier for this agent. Used for logging and
	// token storage namespacing. If empty, a random ID is generated.
	AgentID string `long:"agentid" description:"Unique identifier for this agent instance."`

	// Network is the Bitcoin network to operate on (mainnet, testnet,
	// regtest, simnet, signet).
	Network string `long:"network" description:"The Bitcoin network to use." choice:"regtest" choice:"simnet" choice:"testnet" choice:"mainnet" choice:"signet"`

	// LndHost is the hostname:port of the LND instance this agent uses
	// for paying invoices.
	LndHost string `long:"lndhost" description:"Hostname of the LND instance to connect to."`

	// TLSPath is the path to the LND node's TLS certificate.
	TLSPath string `long:"tlspath" description:"Path to LND instance's TLS certificate."`

	// MacaroonPath is the path to the LND admin macaroon.
	MacaroonPath string `long:"macaroonpath" description:"Path to the LND macaroon file."`

	// MaxCost is the maximum amount in satoshis an agent will auto-pay
	// for a single L402 token. Prevents runaway spending.
	MaxCost btcutil.Amount `long:"maxcost" description:"Maximum satoshis to pay per L402 token."`

	// MaxRoutingFee is the maximum routing fee in satoshis the agent
	// will pay per payment.
	MaxRoutingFee btcutil.Amount `long:"maxroutingfee" description:"Maximum routing fee satoshis per payment."`

	// PaymentTimeout is how long to wait for an invoice payment to
	// complete.
	PaymentTimeout time.Duration `long:"paymenttimeout" description:"Maximum time to wait for a payment."`

	// RequestTimeout is the timeout for individual HTTP requests.
	RequestTimeout time.Duration `long:"requesttimeout" description:"Timeout for HTTP requests."`

	// AllowInsecure allows connecting to services over plain HTTP.
	// Should only be used for testing or Tor onion services.
	AllowInsecure bool `long:"allowinsecure" description:"Allow connections without TLS."`

	// TokenDir is the directory where L402 tokens are cached. If empty,
	// tokens are stored in memory only (lost on restart).
	TokenDir string `long:"tokendir" description:"Directory for persistent token storage."`
}

// DefaultConfig returns a Config with sensible defaults for an AI agent.
func DefaultConfig() *Config {
	return &Config{
		Network:        "mainnet",
		MaxCost:        DefaultMaxCostSats,
		MaxRoutingFee:  DefaultMaxRoutingFeeSats,
		PaymentTimeout: DefaultPaymentTimeout,
		RequestTimeout: DefaultRequestTimeout,
	}
}

// Validate checks that the configuration has all required fields.
func (c *Config) Validate() error {
	if c.LndHost == "" {
		return errors.New("agent: lnd host is required")
	}

	if c.TLSPath == "" {
		return errors.New("agent: lnd TLS path is required")
	}

	if c.MacaroonPath == "" {
		return errors.New("agent: lnd macaroon path is required")
	}

	if c.Network == "" {
		return errors.New("agent: network is required")
	}

	if c.MaxCost <= 0 {
		return errors.New("agent: max cost must be positive")
	}

	return nil
}
