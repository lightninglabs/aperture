package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRateLimiterBasic tests basic rate limiting functionality.
func TestRateLimiterBasic(t *testing.T) {
	cfg := &RateLimitConfig{
		PathRegexp: "^/api/.*$",
		Requests:   10,
		Per:        time.Second,
		Burst:      10,
	}
	cfg.compiledPathRegexp = regexp.MustCompile(cfg.PathRegexp)

	rl := NewRateLimiter("test-service", []*RateLimitConfig{cfg})

	// First 10 requests should be allowed.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		allowed, _ := rl.Allow(req, "test-key")
		require.True(t, allowed, "request %d should be allowed", i)
	}

	// 11th request should be denied.
	req := httptest.NewRequest("GET", "/api/test", nil)
	allowed, retryAfter := rl.Allow(req, "test-key")
	require.False(t, allowed, "11th request should be denied")
	require.Greater(t, retryAfter, time.Duration(0))
}

// TestRateLimiterNoMatchingRules tests that requests pass when no rules match.
func TestRateLimiterNoMatchingRules(t *testing.T) {
	cfg := &RateLimitConfig{
		PathRegexp: "^/api/.*$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      1,
	}
	cfg.compiledPathRegexp = regexp.MustCompile(cfg.PathRegexp)

	rl := NewRateLimiter("test-service", []*RateLimitConfig{cfg})

	// Request to non-matching path should always be allowed.
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest("GET", "/other/path", nil)
		allowed, _ := rl.Allow(req, "test-key")
		require.True(t, allowed, "non-matching request should be allowed")
	}
}

// TestRateLimiterLRUEviction tests that the LRU cache evicts old entries.
func TestRateLimiterLRUEviction(t *testing.T) {
	cfg := &RateLimitConfig{
		Requests: 100,
		Per:      time.Second,
		Burst:    100,
	}

	rl := NewRateLimiter(
		"test-service", []*RateLimitConfig{cfg},
		WithMaxCacheSize(5),
	)

	// Create 10 different keys.
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		key := fmt.Sprintf("key-%d", i)
		rl.Allow(req, key)
	}

	// Cache should be at max size.
	require.Equal(t, 5, rl.Size())
}

// TestRateLimiterPathMatching tests that different path patterns have
// independent limits.
func TestRateLimiterPathMatching(t *testing.T) {
	cfgApi := &RateLimitConfig{
		PathRegexp: "^/api/.*$",
		Requests:   5,
		Per:        time.Second,
		Burst:      5,
	}
	cfgApi.compiledPathRegexp = regexp.MustCompile(cfgApi.PathRegexp)

	cfgAdmin := &RateLimitConfig{
		PathRegexp: "^/admin/.*$",
		Requests:   2,
		Per:        time.Second,
		Burst:      2,
	}
	cfgAdmin.compiledPathRegexp = regexp.MustCompile(cfgAdmin.PathRegexp)

	rl := NewRateLimiter(
		"test-service",
		[]*RateLimitConfig{cfgApi, cfgAdmin},
	)

	// API path should allow 5 requests.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/api/users", nil)
		allowed, _ := rl.Allow(req, "test-key")
		require.True(t, allowed)
	}

	// Admin path should allow 2 requests.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/admin/settings", nil)
		allowed, _ := rl.Allow(req, "test-key")
		require.True(t, allowed)
	}

	// Next admin request should be denied.
	req := httptest.NewRequest("GET", "/admin/settings", nil)
	allowed, _ := rl.Allow(req, "test-key")
	require.False(t, allowed)

	// API should still have capacity (used 5, burst is 5, but we're testing
	// a 6th).
	req = httptest.NewRequest("GET", "/api/users", nil)
	allowed, _ = rl.Allow(req, "test-key")
	require.False(t, allowed, "6th API request should be denied")
}

// TestRateLimiterMultipleRulesAllMustPass tests that all matching rules must
// pass for a request to be allowed.
func TestRateLimiterMultipleRulesAllMustPass(t *testing.T) {
	// Global rule: 100 req/sec.
	cfgGlobal := &RateLimitConfig{
		Requests: 100,
		Per:      time.Second,
		Burst:    100,
	}

	// Specific rule: 2 req/sec for /expensive.
	cfgExpensive := &RateLimitConfig{
		PathRegexp: "^/expensive$",
		Requests:   2,
		Per:        time.Second,
		Burst:      2,
	}
	cfgExpensive.compiledPathRegexp = regexp.MustCompile(cfgExpensive.PathRegexp)

	rl := NewRateLimiter(
		"test-service",
		[]*RateLimitConfig{cfgGlobal, cfgExpensive},
	)

	// Expensive should be limited by the stricter rule.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/expensive", nil)
		allowed, _ := rl.Allow(req, "test-key")
		require.True(t, allowed)
	}

	req := httptest.NewRequest("GET", "/expensive", nil)
	allowed, _ := rl.Allow(req, "test-key")
	require.False(t, allowed, "should be denied by /expensive rule")
}

// TestRateLimiterPerKeyIsolation tests that different keys have independent
// rate limits.
func TestRateLimiterPerKeyIsolation(t *testing.T) {
	cfg := &RateLimitConfig{
		Requests: 2,
		Per:      time.Second,
		Burst:    2,
	}

	rl := NewRateLimiter("test-service", []*RateLimitConfig{cfg})

	// User 1 uses their quota.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		allowed, _ := rl.Allow(req, "user-1")
		require.True(t, allowed)
	}

	// User 1 is now denied.
	req := httptest.NewRequest("GET", "/api/test", nil)
	allowed, _ := rl.Allow(req, "user-1")
	require.False(t, allowed)

	// User 2 should still have full quota.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		allowed, _ := rl.Allow(req, "user-2")
		require.True(t, allowed)
	}
}

// TestExtractRateLimitKeyIP tests IP-based key extraction for unauthenticated
// requests.
func TestExtractRateLimitKeyIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/test", nil)
	ip := net.ParseIP("192.168.1.100")

	// Unauthenticated request should use masked IP (/24 for IPv4).
	key := ExtractRateLimitKey(req, ip, false)
	require.Equal(t, "ip:192.168.1.0", key)
}

// TestExtractRateLimitKeyIPv6 tests IPv6 key extraction.
func TestExtractRateLimitKeyIPv6(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/test", nil)
	ip := net.ParseIP("2001:db8:1234:5678::1")

	// IPv6 should be masked to /48.
	key := ExtractRateLimitKey(req, ip, false)
	require.Equal(t, "ip:2001:db8:1234::", key)
}

// TestExtractRateLimitKeyUnauthenticatedIgnoresL402 tests that unauthenticated
// requests fall back to IP even if L402 header is present. This prevents DoS
// attacks where garbage L402 tokens flood the cache.
func TestExtractRateLimitKeyUnauthenticatedIgnoresL402(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/test", nil)
	// Add a garbage L402 header that would be present before authentication.
	req.Header.Set("Authorization", "L402 garbage:token")
	ip := net.ParseIP("192.168.1.100")

	// Even with L402 header present, unauthenticated=false should use
	// masked IP.
	key := ExtractRateLimitKey(req, ip, false)
	require.Equal(t, "ip:192.168.1.0", key)
}

// TestRateLimitConfigRate tests the Rate() calculation.
func TestRateLimitConfigRate(t *testing.T) {
	tests := []struct {
		name     string
		requests int
		per      time.Duration
		wantRate float64
	}{
		{
			name:     "10 per second",
			requests: 10,
			per:      time.Second,
			wantRate: 10.0,
		},
		{
			name:     "60 per minute",
			requests: 60,
			per:      time.Minute,
			wantRate: 1.0,
		},
		{
			name:     "1 per hour",
			requests: 1,
			per:      time.Hour,
			wantRate: 1.0 / 3600.0,
		},
		{
			name:     "zero per",
			requests: 10,
			per:      0,
			wantRate: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RateLimitConfig{
				Requests: tt.requests,
				Per:      tt.per,
			}
			require.InDelta(t, tt.wantRate, cfg.Rate(), 0.0001)
		})
	}
}

// TestRateLimitConfigEffectiveBurst tests the EffectiveBurst() calculation.
func TestRateLimitConfigEffectiveBurst(t *testing.T) {
	tests := []struct {
		name      string
		requests  int
		burst     int
		wantBurst int
	}{
		{
			name:      "explicit burst",
			requests:  10,
			burst:     20,
			wantBurst: 20,
		},
		{
			name:      "default to requests",
			requests:  10,
			burst:     0,
			wantBurst: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RateLimitConfig{
				Requests: tt.requests,
				Burst:    tt.burst,
			}
			require.Equal(t, tt.wantBurst, cfg.EffectiveBurst())
		})
	}
}

// TestRateLimitConfigMatches tests the Matches() method.
func TestRateLimitConfigMatches(t *testing.T) {
	tests := []struct {
		name       string
		pathRegexp string
		path       string
		want       bool
	}{
		{
			name:       "no pattern matches all",
			pathRegexp: "",
			path:       "/anything",
			want:       true,
		},
		{
			name:       "pattern matches",
			pathRegexp: "^/api/.*$",
			path:       "/api/users",
			want:       true,
		},
		{
			name:       "pattern does not match",
			pathRegexp: "^/api/.*$",
			path:       "/admin/users",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &RateLimitConfig{
				PathRegexp: tt.pathRegexp,
			}
			if tt.pathRegexp != "" {
				cfg.compiledPathRegexp = regexp.MustCompile(
					tt.pathRegexp,
				)
			}
			require.Equal(t, tt.want, cfg.Matches(tt.path))
		})
	}
}

// TestSendRateLimitResponseHTTP tests HTTP rate limit response.
func TestSendRateLimitResponseHTTP(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/test", nil)

	sendRateLimitResponse(w, req, 5*time.Second)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "5", w.Header().Get("Retry-After"))
	require.Contains(t, w.Body.String(), "rate limit exceeded")
}

// TestSendRateLimitResponseHTTPSubSecond tests that sub-second delays are
// rounded up to 1 second.
func TestSendRateLimitResponseHTTPSubSecond(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/test", nil)

	sendRateLimitResponse(w, req, 500*time.Millisecond)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "1", w.Header().Get("Retry-After"))
}

// TestSendRateLimitResponseHTTPRoundUp tests that fractional seconds are
// rounded up, not down. This ensures clients don't retry before the limit
// actually resets.
func TestSendRateLimitResponseHTTPRoundUp(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/test", nil)

	// 1.1 seconds should round up to 2 seconds, not down to 1.
	sendRateLimitResponse(w, req, 1100*time.Millisecond)

	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "2", w.Header().Get("Retry-After"))
}

// TestSendRateLimitResponseGRPC tests gRPC rate limit response.
func TestSendRateLimitResponseGRPC(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/grpc.Service/Method", nil)
	req.Header.Set("Content-Type", "application/grpc")

	sendRateLimitResponse(w, req, 5*time.Second)

	require.Equal(t, http.StatusOK, w.Code) // gRPC always returns 200.
	require.Equal(t, "5", w.Header().Get("Retry-After"))
	require.Equal(t, "8", w.Header().Get("Grpc-Status")) // ResourceExhausted.
	require.Equal(t, "rate limit exceeded", w.Header().Get("Grpc-Message"))
}

// TestRateLimiterTokenRefill tests that tokens refill over time.
func TestRateLimiterTokenRefill(t *testing.T) {
	cfg := &RateLimitConfig{
		Requests: 10,
		Per:      100 * time.Millisecond, // Fast refill for testing.
		Burst:    1,
	}

	rl := NewRateLimiter("test-service", []*RateLimitConfig{cfg})

	// Use the one available token.
	req := httptest.NewRequest("GET", "/api/test", nil)
	allowed, _ := rl.Allow(req, "test-key")
	require.True(t, allowed)

	// Immediate second request should be denied.
	allowed, _ = rl.Allow(req, "test-key")
	require.False(t, allowed)

	// Wait for refill.
	time.Sleep(15 * time.Millisecond)

	// Should have a token now.
	allowed, _ = rl.Allow(req, "test-key")
	require.True(t, allowed)
}

// newMultiRuleLimiter returns a limiter where a catch-all rule is shared by
// every request and a stricter rule additionally applies to /expensive. Both
// rules refill so slowly that no tokens are restored during a test, making
// token counts exact.
func newMultiRuleLimiter(sharedBurst, expensiveBurst int) (*RateLimiter,
	*RateLimitConfig, *RateLimitConfig) {

	shared := &RateLimitConfig{
		PathRegexp: ".*",
		Requests:   1,
		Per:        time.Hour,
		Burst:      sharedBurst,
	}
	shared.compiledPathRegexp = regexp.MustCompile(shared.PathRegexp)

	expensive := &RateLimitConfig{
		PathRegexp: "^/expensive$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      expensiveBurst,
	}
	expensive.compiledPathRegexp = regexp.MustCompile(expensive.PathRegexp)

	rl := NewRateLimiter(
		"test-service", []*RateLimitConfig{shared, expensive},
	)

	return rl, shared, expensive
}

// testClientKey is the client key used by tokensLeft and allowN.
const testClientKey = "test-key"

// tokensLeft returns the number of tokens currently available in the bucket
// of the given rule for testClientKey.
func tokensLeft(rl *RateLimiter, cfg *RateLimitConfig) float64 {
	rl.cacheMu.Lock()
	defer rl.cacheMu.Unlock()

	limiter := rl.getOrCreateLimiter(limiterKey{
		clientKey:   testClientKey,
		pathPattern: cfg.PathRegexp,
	}, cfg)

	return limiter.Tokens()
}

// allowN sends n requests for the given path and returns how many were
// allowed.
func allowN(rl *RateLimiter, path string, n int) int {
	allowed := 0
	for i := 0; i < n; i++ {
		req := httptest.NewRequest("GET", path, nil)
		if ok, _ := rl.Allow(req, testClientKey); ok {
			allowed++
		}
	}

	return allowed
}

// TestRateLimiterDeniedRequestRefundsOtherRules tests that a request denied by
// one matching rule does not consume tokens from the other matching rules.
func TestRateLimiterDeniedRequestRefundsOtherRules(t *testing.T) {
	t.Run("denied by specific rule", func(t *testing.T) {
		rl, shared, expensive := newMultiRuleLimiter(10, 2)

		// Only the first two requests fit into the expensive bucket.
		// The denied ones must not drain the shared bucket.
		require.Equal(t, 2, allowN(rl, "/expensive", 20))
		require.InDelta(t, 8, tokensLeft(rl, shared), 0.01)
		require.InDelta(
			t, 0, tokensLeft(rl, expensive), 0.01,
		)

		// The remaining shared capacity is still usable by other
		// requests of the same client.
		require.Equal(t, 8, allowN(rl, "/other", 20))
	})

	t.Run("denied by shared rule", func(t *testing.T) {
		rl, shared, expensive := newMultiRuleLimiter(2, 10)

		// Exhaust the shared bucket with requests to other paths.
		require.Equal(t, 2, allowN(rl, "/other", 2))

		// Expensive requests are now denied by the shared rule and must
		// not drain the expensive bucket.
		require.Zero(t, allowN(rl, "/expensive", 20))
		require.InDelta(t, 0, tokensLeft(rl, shared), 0.01)
		require.InDelta(
			t, 10, tokensLeft(rl, expensive), 0.01,
		)
	})
}

// TestRateLimiterDeniedRequestRetryAfter tests that a denied request reports
// the longest wait time of all matching rules.
func TestRateLimiterDeniedRequestRetryAfter(t *testing.T) {
	tests := []struct {
		name           string
		sharedPer      time.Duration
		sharedBurst    int
		expensivePer   time.Duration
		wantRetryAfter time.Duration
	}{
		{
			name:           "only expensive rule denies",
			sharedPer:      time.Hour,
			sharedBurst:    10,
			expensivePer:   2 * time.Hour,
			wantRetryAfter: 2 * time.Hour,
		},
		{
			name:           "both deny, expensive waits longer",
			sharedPer:      time.Hour,
			sharedBurst:    1,
			expensivePer:   2 * time.Hour,
			wantRetryAfter: 2 * time.Hour,
		},
		{
			name:           "both deny, shared waits longer",
			sharedPer:      3 * time.Hour,
			sharedBurst:    1,
			expensivePer:   2 * time.Hour,
			wantRetryAfter: 3 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shared := &RateLimitConfig{
				PathRegexp: ".*",
				Requests:   1,
				Per:        tt.sharedPer,
				Burst:      tt.sharedBurst,
			}
			shared.compiledPathRegexp = regexp.MustCompile(
				shared.PathRegexp,
			)

			expensive := &RateLimitConfig{
				PathRegexp: "^/expensive$",
				Requests:   1,
				Per:        tt.expensivePer,
				Burst:      1,
			}
			expensive.compiledPathRegexp = regexp.MustCompile(
				expensive.PathRegexp,
			)

			rl := NewRateLimiter(
				"test-service",
				[]*RateLimitConfig{shared, expensive},
			)

			// Use up the only expensive token.
			require.Equal(
				t, 1, allowN(rl, "/expensive", 1),
			)

			req := httptest.NewRequest("GET", "/expensive", nil)
			allowed, retryAfter := rl.Allow(req, testClientKey)
			require.False(t, allowed)
			require.InDelta(
				t, tt.wantRetryAfter.Seconds(),
				retryAfter.Seconds(), 1,
			)
		})
	}
}
