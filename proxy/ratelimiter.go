package proxy

import (
	"regexp"
	"sync"
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
	// protects the l402Limiters map.
	sync.Mutex

	// re is the regular expression used to match the path of the URL.
	re *regexp.Regexp

	// global limiter is used when no per-L402 key can be derived.
	limiter *rate.Limiter

	// limiter per L402 key.
	limit rate.Limit

	// burst is the burst size allowed in addition to steady rate.
	burst int

	// l402Limiters is a map of per-L402 key limiters.
	l402Limiters map[string]*rate.Limiter
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
	limit := rate.Every(per / time.Duration(requests))
	lim := rate.NewLimiter(limit, burst)
	r.compiled = &compiledRateLimit{
		re:           re,
		limiter:      lim,
		limit:        limit,
		burst:        burst,
		l402Limiters: make(map[string]*rate.Limiter),
	}

	return nil
}

// allowFor returns true if the rate limit permits an event now for the given
// key. If the key is empty, the global limiter is used.
func (c *compiledRateLimit) allowFor(key string) bool {
	if key == "" {
		return c.limiter.Allow()
	}
	l := c.getOrCreate(key)

	return l.Allow()
}

// reserveDelay reserves a token on the limiter for the given key and returns
// the suggested delay. Callers can use the delay to set Retry-After without
// consuming tokens.
func (c *compiledRateLimit) reserveDelay(key string) (time.Duration, bool) {
	var l *rate.Limiter
	if key == "" {
		l = c.limiter
	} else {
		l = c.getOrCreate(key)
	}

	res := l.Reserve()
	if !res.OK() {
		return 0, false
	}

	delay := res.Delay()
	res.CancelAt(time.Now())

	return delay, true
}

func (c *compiledRateLimit) getOrCreate(key string) *rate.Limiter {
	c.Lock()
	defer c.Unlock()

	if l, ok := c.l402Limiters[key]; ok {
		return l
	}

	l := rate.NewLimiter(c.limit, c.burst)
	c.l402Limiters[key] = l

	return l
}
