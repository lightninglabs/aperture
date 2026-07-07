package proxy

import (
	"bytes"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/netutil"
	"github.com/lightninglabs/neutrino/cache/lru"
	"golang.org/x/time/rate"
)

const (
	// DefaultMaxCacheSize is the default maximum number of rate limiter
	// entries to keep in the LRU cache.
	DefaultMaxCacheSize = 10_000
)

// limiterKey is a composite key for the rate limiter cache. Using a struct
// instead of a concatenated string saves memory because the pathPattern field
// can reference the same underlying string across multiple keys.
type limiterKey struct {
	// clientKey identifies the client (e.g., "ip:1.2.3.4" or "token:abc").
	clientKey string
	// pathPattern is the rate limit rule's PathRegexp (pointer to config's
	// string, not a copy).
	pathPattern string
}

// limiterEntry holds a rate.Limiter. Implements cache.Value interface.
type limiterEntry struct {
	limiter *rate.Limiter
}

// Size implements cache.Value. Returns 1 so the LRU cache counts entries
// rather than bytes.
func (e *limiterEntry) Size() (uint64, error) {
	return 1, nil
}

// RateLimiter manages per-key rate limiters with LRU eviction.
type RateLimiter struct {
	// cacheMu protects the LRU cache which is not concurrency-safe.
	cacheMu sync.Mutex

	// configs is the list of rate limit configurations for this limiter.
	configs []*RateLimitConfig

	// cache is the LRU cache of rate limiter entries.
	cache *lru.Cache[limiterKey, *limiterEntry]

	// maxSize is the maximum number of entries in the cache.
	maxSize int

	// serviceName is used for metrics labels.
	serviceName string
}

// RateLimiterOption is a functional option for configuring a RateLimiter.
type RateLimiterOption func(*RateLimiter)

// WithMaxCacheSize sets the maximum cache size.
func WithMaxCacheSize(size int) RateLimiterOption {
	return func(rl *RateLimiter) {
		rl.maxSize = size
	}
}

// NewRateLimiter creates a new RateLimiter with the given configurations.
func NewRateLimiter(serviceName string, configs []*RateLimitConfig,
	opts ...RateLimiterOption) *RateLimiter {

	rl := &RateLimiter{
		configs:     configs,
		maxSize:     DefaultMaxCacheSize,
		serviceName: serviceName,
	}

	for _, opt := range opts {
		opt(rl)
	}

	// Initialize the LRU cache with the configured max size.
	rl.cache = lru.NewCache[limiterKey, *limiterEntry](uint64(rl.maxSize))

	return rl
}

// Allow checks if a request should be allowed based on all matching rate
// limits. Returns (allowed, retryAfter) where retryAfter is the suggested
// duration to wait if denied.
func (rl *RateLimiter) Allow(r *http.Request, key string) (bool,
	time.Duration) {

	path := r.URL.Path

	// Collect all matching configs and their reservations. We need to check
	// all rules before consuming any tokens, so that if any rule denies we
	// can cancel all reservations.
	type ruleReservation struct {
		cfg         *RateLimitConfig
		reservation *rate.Reservation
	}
	reservations := make([]ruleReservation, 0, len(rl.configs))

	for _, cfg := range rl.configs {
		if !cfg.Matches(path) {
			continue
		}

		// Create composite key: client key + path pattern for
		// independent limiting per rule. Using a struct instead of
		// string concatenation saves memory since pathPattern
		// references the config's string.
		cacheKey := limiterKey{
			clientKey:   key,
			pathPattern: cfg.PathRegexp,
		}

		limiter := rl.getOrCreateLimiter(cacheKey, cfg)
		reservation := limiter.Reserve()

		reservations = append(reservations, ruleReservation{
			cfg:         cfg,
			reservation: reservation,
		})
	}

	// If no rules matched, allow the request.
	if len(reservations) == 0 {
		return true, 0
	}

	// Check if all reservations can proceed immediately. If any rule
	// denies, we must cancel ALL reservations to avoid consuming tokens
	// unfairly.
	var maxWait time.Duration
	allAllowed := true

	for _, rr := range reservations {
		if !rr.reservation.OK() {
			// Rate is zero or infinity.
			allAllowed = false
			maxWait = time.Second

			break
		}

		delay := rr.reservation.Delay()
		if delay > 0 {
			allAllowed = false
			if delay > maxWait {
				maxWait = delay
			}
		}
	}

	// If any rule denied, cancel all reservations and return denied.
	if !allAllowed {
		for _, rr := range reservations {
			rr.reservation.Cancel()
			rateLimitDenied.WithLabelValues(
				rl.serviceName, rr.cfg.PathRegexp,
			).Inc()
		}

		return false, maxWait
	}

	// All rules allowed - tokens are consumed, record metrics.
	for _, rr := range reservations {
		rateLimitAllowed.WithLabelValues(
			rl.serviceName, rr.cfg.PathRegexp,
		).Inc()
	}

	return true, 0
}

// getOrCreateLimiter retrieves an existing limiter or creates a new one.
func (rl *RateLimiter) getOrCreateLimiter(key limiterKey,
	cfg *RateLimitConfig) *rate.Limiter {

	rl.cacheMu.Lock()
	defer rl.cacheMu.Unlock()

	// Try to get existing entry from cache (also updates LRU order).
	if entry, err := rl.cache.Get(key); err == nil {
		return entry.limiter
	}

	// Create a new limiter.
	limiter := rate.NewLimiter(
		rate.Limit(cfg.Rate()), cfg.EffectiveBurst(),
	)

	entry := &limiterEntry{
		limiter: limiter,
	}

	// Put handles eviction automatically when cache is full.
	evicted, _ := rl.cache.Put(key, entry)
	if evicted {
		rateLimitEvictions.WithLabelValues(rl.serviceName).Inc()
	}

	rateLimitCacheSize.WithLabelValues(rl.serviceName).Set(
		float64(rl.cache.Len()),
	)

	return limiter
}

// Size returns the current number of entries in the cache.
func (rl *RateLimiter) Size() int {
	rl.cacheMu.Lock()
	defer rl.cacheMu.Unlock()

	return rl.cache.Len()
}

// ExtractRateLimitKey extracts the rate-limiting key from a request.
// For authenticated requests, it uses the L402 token ID. For unauthenticated
// requests, it falls back to the client IP address.
//
// IMPORTANT: The authenticated parameter should only be true if the L402 token
// has been validated by the authenticator. Using unvalidated L402 tokens as
// keys is a DoS vector since attackers can flood the cache with garbage tokens.
func ExtractRateLimitKey(r *http.Request, remoteIP net.IP,
	authenticated bool) string {

	// Only use L402 token ID if the request has been authenticated.
	// This prevents DoS attacks where garbage L402 tokens flood the cache.
	if authenticated {
		mac, _, _, err := l402.FromHeader(&r.Header)
		if err == nil && mac != nil {
			identifier, err := l402.DecodeIdentifier(
				bytes.NewBuffer(mac.Id()),
			)
			if err == nil {
				return "token:" + identifier.TokenID.String()
			}
		}
	}

	// Fall back to IP address for unauthenticated requests.
	// Mask the IP to group clients from the same network segment.
	return "ip:" + netutil.MaskIP(remoteIP).String()
}
