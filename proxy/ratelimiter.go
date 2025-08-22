package proxy

import (
	"regexp"
	"time"

	"golang.org/x/time/rate"
)

// RateLimit defines a per-endpoint rate limit using a token bucket.
// Requests allowed per time window with optional burst.
// Example YAML:
//
//	ratelimits:
//	  - pathregex: '^/looprpc.SwapServer/LoopOutQuote.*$'
//	    requests: 5
//	    per: 1s
//	    burst: 5
//
// If burst is 0, it defaults to requests.
// If per is 0, it defaults to 1s.
// Note: All limits are in-memory and per-process.
type RateLimit struct {
	PathRegexp string        `long:"pathregex" description:"Regular expression to match the path of the URL against for rate limiting" yaml:"pathregex"`
	Requests   int           `long:"requests" description:"Number of requests allowed per time window" yaml:"requests"`
	Per        time.Duration `long:"per" description:"Size of the time window (e.g., 1s, 1m)" yaml:"per"`
	Burst      int           `long:"burst" description:"Burst size allowed in addition to steady rate" yaml:"burst"`

	// compiled is internal state prepared at startup.
	compiled *compiledRateLimit
}

type compiledRateLimit struct {
	re      *regexp.Regexp
	limiter *rate.Limiter
}

// compile prepares the regular expression and the limiter.
func (r *RateLimit) compile() error {
	per := r.Per
	if per == 0 {
		per = time.Second
	}
	requests := r.Requests
	if requests <= 0 {
		requests = 1
	}
	burst := r.Burst
	if burst <= 0 {
		burst = requests
	}

	re, err := regexp.Compile(r.PathRegexp)
	if err != nil {
		return err
	}

	// rate.Every(per/requests) creates an average rate of requests
	// per 'per'.
	lim := rate.NewLimiter(rate.Every(per/time.Duration(requests)), burst)
	r.compiled = &compiledRateLimit{re: re, limiter: lim}

	return nil
}

// allow returns true if the rate limit permits an event now.
func (c *compiledRateLimit) allow() bool {
	return c.limiter.Allow()
}
