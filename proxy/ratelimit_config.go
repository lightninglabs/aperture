package proxy

import (
	"regexp"
	"time"
)

// RateLimitConfig defines a rate limiting rule for a specific path pattern.
type RateLimitConfig struct {
	// PathRegexp is a regular expression that matches request paths
	// to which this rate limit applies. If empty, matches all paths.
	PathRegexp string `long:"pathregexp" description:"Regular expression to match the path of the URL against for rate limiting"`

	// Requests is the number of requests allowed per time window (Per).
	Requests int `long:"requests" description:"Number of requests allowed per time window"`

	// Per is the required time window duration (e.g., 1s, 1m, 1h).
	Per time.Duration `long:"per" description:"Time window for rate limiting (e.g., 1s, 1m, 1h)"`

	// Burst is the maximum number of requests that can be made in a burst,
	// exceeding the steady-state rate. Defaults to Requests if not set.
	Burst int `long:"burst" description:"Maximum burst size (defaults to Requests if not set)"`

	// compiledPathRegexp is the compiled version of PathRegexp.
	compiledPathRegexp *regexp.Regexp
}

// Rate returns the rate.Limit value (requests per second) for this
// configuration.
func (r *RateLimitConfig) Rate() float64 {
	if r.Per == 0 {
		return 0
	}

	return float64(r.Requests) / r.Per.Seconds()
}

// EffectiveBurst returns the burst value, defaulting to Requests if Burst
// is 0.
func (r *RateLimitConfig) EffectiveBurst() int {
	if r.Burst == 0 {
		return r.Requests
	}

	return r.Burst
}

// Matches returns true if the given path matches this rate limit's path
// pattern.
func (r *RateLimitConfig) Matches(path string) bool {
	if r.compiledPathRegexp == nil {
		return true // No pattern means match all
	}

	return r.compiledPathRegexp.MatchString(path)
}
