package proxy

import (
	"fmt"
	"time"
)

const (
	// challengeLimiterName labels the challenge limiter in the rate limit
	// metrics.
	challengeLimiterName = "challenge"

	// DefaultChallengeRequests is the default number of challenges one
	// client network may be issued per DefaultChallengePer. It is sized
	// against a seller whose invoice backend caps the number of unpaid
	// invoices it holds, such as a Wavelength wallet with 32 pending
	// receive intents. One challenge can mint up to three invoices (L402,
	// MPP charge, MPP session), so ten challenges hold at most thirty.
	DefaultChallengeRequests = 10

	// DefaultChallengePer is the default window for challenge issuance. It
	// matches the lifetime of an unpaid invoice, so a client network
	// cannot hold more than its burst of unpaid invoices at a steady state.
	DefaultChallengePer = time.Hour
)

// ChallengeRateLimitConfig bounds how many payment challenges the proxy issues
// to one client network. Every challenge mints invoices at the seller's
// backend before anyone has paid, so without a bound an unauthenticated client
// decides how many unpaid invoices the seller holds.
type ChallengeRateLimitConfig struct {
	// Requests is the number of challenges allowed per Per. Zero disables
	// the limit.
	Requests int `long:"requests" description:"Number of 402 challenges allowed per client network per time window. 0 disables the limit."`

	// Per is the time window for Requests.
	Per time.Duration `long:"per" description:"Time window for the challenge limit (e.g., 1m, 1h)."`

	// Burst is the number of challenges a client network may be issued at
	// once. It defaults to Requests.
	Burst int `long:"burst" description:"Maximum burst of challenges per client network (defaults to requests)."`
}

// DefaultChallengeRateLimitConfig returns the default challenge limit.
func DefaultChallengeRateLimitConfig() *ChallengeRateLimitConfig {
	return &ChallengeRateLimitConfig{
		Requests: DefaultChallengeRequests,
		Per:      DefaultChallengePer,
	}
}

// SetChallengeRateLimit installs a per client network limit on challenge
// issuance. Clients are grouped like the service rate limits, by the same
// masked source address, and the limit applies before any invoice is minted.
// A nil config, or one with zero Requests, removes the limit. It must be
// called before the proxy serves requests.
func (p *Proxy) SetChallengeRateLimit(cfg *ChallengeRateLimitConfig) error {
	if cfg == nil || cfg.Requests == 0 {
		p.challengeLimiter = nil

		return nil
	}

	if cfg.Requests < 0 || cfg.Per <= 0 || cfg.Burst < 0 {
		return fmt.Errorf("challenge rate limit needs positive " +
			"requests and per, and a non-negative burst")
	}

	p.challengeLimiter = NewRateLimiter(
		challengeLimiterName, []*RateLimitConfig{{
			Requests: cfg.Requests,
			Per:      cfg.Per,
			Burst:    cfg.Burst,
		}},
	)

	return nil
}
