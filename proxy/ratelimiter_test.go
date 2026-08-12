package proxy

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
		allowed, _ := rl.Allow(req, fmt.Sprintf("test-key-%d", i))
		require.True(t, allowed, "non-matching request should be allowed")
	}

	// Non-matching traffic must not allocate cache entries or keyed locks.
	require.Zero(t, rl.Size())
	rl.clientLocks.mu.Lock()
	require.Nil(t, rl.clientLocks.locks)
	rl.clientLocks.mu.Unlock()
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

// TestRateLimiterUndersizedCacheFailsClosed makes sure an admission larger than
// the explicit cache bound cannot refresh its burst by evicting its own rules.
func TestRateLimiterUndersizedCacheFailsClosed(t *testing.T) {
	first := &RateLimitConfig{
		PathRegexp: "^/same$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      1,
	}
	second := &RateLimitConfig{
		PathRegexp: "^/same$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      1,
	}
	first.compiledPathRegexp = regexp.MustCompile(first.PathRegexp)
	second.compiledPathRegexp = regexp.MustCompile(second.PathRegexp)

	rl := NewRateLimiter(
		t.Name(), []*RateLimitConfig{first, second},
		WithMaxCacheSize(1),
	)
	require.Equal(t, 1, rl.maxSize)
	req := httptest.NewRequest("GET", "/same", nil)

	allowed, retryAfter := rl.Allow(req, "test-key")
	require.False(t, allowed)
	require.Equal(t, time.Second, retryAfter)
	require.Zero(t, rl.Size())

	// The decision remains fail-closed rather than receiving a fresh burst on
	// every subsequent request.
	allowed, _ = rl.Allow(req, "test-key")
	require.False(t, allowed)
}

// TestRateLimiterCacheSizeOptions makes sure the public option remains an
// actual upper bound, including its zero-cache behavior.
func TestRateLimiterCacheSizeOptions(t *testing.T) {
	for _, size := range []int{0, -1} {
		cfg := &RateLimitConfig{
			Requests: 1,
			Per:      time.Hour,
			Burst:    1,
		}
		rl := NewRateLimiter(
			t.Name(), []*RateLimitConfig{cfg}, WithMaxCacheSize(size),
		)
		require.Zero(t, rl.maxSize)

		req := httptest.NewRequest("GET", "/limited", nil)
		allowed, _ := rl.Allow(req, "test-key")
		require.False(t, allowed)
		require.Zero(t, rl.Size())
	}

	configs := []*RateLimitConfig{{}, {}, {}}
	rl := NewRateLimiter(
		t.Name(), configs, WithMaxCacheSize(len(configs)-1),
	)
	require.Equal(t, len(configs)-1, rl.maxSize)
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

// TestRateLimiterDeniedRuleDoesNotConsumeAllowedRule makes sure a request
// denied by one rule does not consume capacity from another matching rule.
func TestRateLimiterDeniedRuleDoesNotConsumeAllowedRule(t *testing.T) {
	global := &RateLimitConfig{
		Requests: 1,
		Per:      time.Hour,
		Burst:    2,
	}
	specific := &RateLimitConfig{
		PathRegexp: "^/expensive$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      1,
	}
	specific.compiledPathRegexp = regexp.MustCompile(specific.PathRegexp)

	rl := NewRateLimiter(
		t.Name(), []*RateLimitConfig{global, specific},
	)
	expensive := httptest.NewRequest("GET", "/expensive", nil)
	other := httptest.NewRequest("GET", "/other", nil)

	allowed, _ := rl.Allow(expensive, "test-key")
	require.True(t, allowed)

	allowed, _ = rl.Allow(expensive, "test-key")
	require.False(t, allowed)

	// The rejected expensive request must not spend the second global
	// token, so an unrelated path still has capacity.
	allowed, _ = rl.Allow(other, "test-key")
	require.True(t, allowed)
}

// TestRateLimiterConcurrentMultipleRulesAtomic makes sure concurrent requests
// for one client cannot partially consume a set of matching rules.
func TestRateLimiterConcurrentMultipleRulesAtomic(t *testing.T) {
	global := &RateLimitConfig{
		Requests: 1,
		Per:      time.Hour,
		Burst:    20,
	}
	specific := &RateLimitConfig{
		PathRegexp: "^/concurrent-expensive$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      1,
	}
	specific.compiledPathRegexp = regexp.MustCompile(specific.PathRegexp)

	rl := NewRateLimiter(
		t.Name(), []*RateLimitConfig{global, specific},
	)
	var allowedCount atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			req := httptest.NewRequest(
				"GET", "/concurrent-expensive", nil,
			)
			allowed, _ := rl.Allow(req, "test-key")
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), allowedCount.Load())

	// Only the one successful request should have consumed global
	// capacity, leaving 19 tokens for paths that do not match the strict
	// rule.
	other := httptest.NewRequest("GET", "/other", nil)
	for range 19 {
		allowed, _ := rl.Allow(other, "test-key")
		require.True(t, allowed)
	}
	allowed, _ := rl.Allow(other, "test-key")
	require.False(t, allowed)
}

// TestRateLimiterProvisionalAdmissionsAreIsolatedByClient makes sure an
// admission held open for one client does not block an unrelated client. The
// keys deliberately collide under the former 256-shard FNV lock scheme.
func TestRateLimiterProvisionalAdmissionsAreIsolatedByClient(t *testing.T) {
	cfg := &RateLimitConfig{
		Requests: 10,
		Per:      time.Hour,
		Burst:    10,
	}
	rl := NewRateLimiter(t.Name(), []*RateLimitConfig{cfg})
	req := httptest.NewRequest("GET", "/limited", nil)

	firstKey := "client-a"
	firstShard := legacyClientLockShard(firstKey)
	var collidingKey string
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("client-%d", i)
		if candidate != firstKey &&
			legacyClientLockShard(candidate) == firstShard {

			collidingKey = candidate
			break
		}
	}

	admission, allowed, _ := rl.reserve(req, firstKey)
	require.True(t, allowed)
	defer admission.Cancel()

	result := make(chan bool, 1)
	go func() {
		allowed, _ := rl.Allow(req, collidingKey)
		result <- allowed
	}()

	select {
	case allowed := <-result:
		require.True(t, allowed)

	case <-time.After(time.Second):
		t.Fatal("hash-colliding client blocked by provisional admission")
	}

	admission.Cancel()
	rl.clientLocks.mu.Lock()
	remainingLocks := len(rl.clientLocks.locks)
	rl.clientLocks.mu.Unlock()
	require.Zero(t, remainingLocks)
}

// TestRateLimiterProvisionalAdmissionsSerializeSameClient verifies that a
// provisional admission remains the exact ordering boundary for later
// requests using the same client key.
func TestRateLimiterProvisionalAdmissionsSerializeSameClient(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "cancel"
		if commit {
			name = "commit"
		}

		t.Run(name, func(t *testing.T) {
			cfg := &RateLimitConfig{
				Requests: 1,
				Per:      time.Hour,
				Burst:    1,
			}
			rl := NewRateLimiter(t.Name(), []*RateLimitConfig{cfg})
			req := httptest.NewRequest("GET", "/limited", nil)

			first, allowed, _ := rl.reserve(req, "same-client")
			require.True(t, allowed)
			defer first.Cancel()

			result := make(chan bool, 1)
			go func() {
				allowed, _ := rl.Allow(req, "same-client")
				result <- allowed
			}()

			// Wait until the second admission has registered as a waiter,
			// then prove it cannot pass the first admission out of order.
			require.Eventually(t, func() bool {
				rl.clientLocks.mu.Lock()
				defer rl.clientLocks.mu.Unlock()

				lock := rl.clientLocks.locks["same-client"]
				return lock != nil && lock.refs == 2
			}, time.Second, time.Millisecond)
			select {
			case <-result:
				t.Fatal("same-client admission completed out of order")
			default:
			}

			if commit {
				first.Commit()
			} else {
				first.Cancel()
			}

			select {
			case secondAllowed := <-result:
				require.Equal(t, !commit, secondAllowed)
			case <-time.After(time.Second):
				t.Fatal("same-client admission did not resume")
			}
			rl.clientLocks.mu.Lock()
			require.Empty(t, rl.clientLocks.locks)
			rl.clientLocks.mu.Unlock()
		})
	}
}

// TestRateLimiterAdmissionDropsReferences makes sure finalization releases the
// exact-client lock and stops retaining reservations even if a caller keeps the
// admission object alive.
func TestRateLimiterAdmissionDropsReferences(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := "cancel"
		if commit {
			name = "commit"
		}

		t.Run(name, func(t *testing.T) {
			cfg := &RateLimitConfig{
				Requests: 1,
				Per:      time.Hour,
				Burst:    1,
			}
			rl := NewRateLimiter(t.Name(), []*RateLimitConfig{cfg})
			req := httptest.NewRequest("GET", "/limited", nil)

			admission, allowed, _ := rl.reserve(req, "test-key")
			require.True(t, allowed)
			require.NotEmpty(t, admission.reservations)
			require.NotNil(t, admission.clientLocks)
			require.NotNil(t, admission.clientLock)
			require.Equal(t, "test-key", admission.clientKey)

			if commit {
				admission.Commit()
			} else {
				admission.Cancel()
			}

			require.Nil(t, admission.reservations)
			require.Nil(t, admission.clientLocks)
			require.Nil(t, admission.clientLock)
			require.Empty(t, admission.clientKey)
			require.Empty(t, admission.serviceName)
			require.True(t, admission.now.IsZero())

			// Finalization remains idempotent in either order.
			admission.Commit()
			admission.Cancel()

			rl.clientLocks.mu.Lock()
			require.Empty(t, rl.clientLocks.locks)
			rl.clientLocks.mu.Unlock()
		})
	}
}

// TestRateLimiterAdmissionConcurrentFinalization races Commit and Cancel to
// verify that exactly one terminal action takes effect and the client lock is
// released once.
func TestRateLimiterAdmissionConcurrentFinalization(t *testing.T) {
	cfg := &RateLimitConfig{
		Requests: 1,
		Per:      time.Hour,
		Burst:    1,
	}
	rl := NewRateLimiter(t.Name(), []*RateLimitConfig{cfg})
	req := httptest.NewRequest("GET", "/limited", nil)
	labels := map[string]string{
		"service":      t.Name(),
		"path_pattern": "",
	}
	allowedBefore := prometheusCounterValue(
		t, "aperture_ratelimit_allowed_total", labels,
	)

	admission, allowed, _ := rl.reserve(req, "same-client")
	require.True(t, allowed)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(commit bool) {
			defer wg.Done()
			<-start

			if commit {
				admission.Commit()
				return
			}

			admission.Cancel()
		}(i%2 == 0)
	}
	close(start)
	wg.Wait()

	allowedAfter := prometheusCounterValue(
		t, "aperture_ratelimit_allowed_total", labels,
	)
	commitWon := allowedAfter == allowedBefore+1
	require.True(t, commitWon || allowedAfter == allowedBefore)
	require.Nil(t, admission.reservations)
	require.Nil(t, admission.clientLocks)
	require.Nil(t, admission.clientLock)

	nextAllowed, _ := rl.Allow(req, "same-client")
	require.Equal(t, !commitWon, nextAllowed)
	rl.clientLocks.mu.Lock()
	require.Empty(t, rl.clientLocks.locks)
	rl.clientLocks.mu.Unlock()
}

// TestClientLockSetHighChurn exercises pooled lock reuse while many keys and
// waiters contend. At most one goroutine may hold a given key at a time, and no
// historical key may remain after the final release.
func TestClientLockSetHighChurn(t *testing.T) {
	const (
		keyCount = 8
		workers  = 32
		loops    = 500
	)

	var locks clientLockSet
	active := make([]atomic.Int32, keyCount)
	var violated atomic.Bool
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()

			for iteration := 0; iteration < loops; iteration++ {
				keyIndex := (worker + iteration) % keyCount
				key := fmt.Sprintf("key-%d", keyIndex)
				lock := locks.lock(key)
				if active[keyIndex].Add(1) != 1 {
					violated.Store(true)
				}
				runtime.Gosched()
				active[keyIndex].Add(-1)
				locks.unlock(key, lock)
			}
		}(worker)
	}
	wg.Wait()

	require.False(t, violated.Load())
	locks.mu.Lock()
	require.Empty(t, locks.locks)
	locks.mu.Unlock()
}

// legacyClientLockShard computes the shard used by the former lock scheme.
func legacyClientLockShard(key string) uint32 {
	const (
		offset32 = uint32(2166136261)
		prime32  = uint32(16777619)
	)

	hash := offset32
	for idx := 0; idx < len(key); idx++ {
		hash ^= uint32(key[idx])
		hash *= prime32
	}

	return hash % 256
}

// TestRateLimiterDuplicatePatternsIndependent makes sure rules with identical
// path patterns retain their own rates and bursts.
func TestRateLimiterDuplicatePatternsIndependent(t *testing.T) {
	lenient := &RateLimitConfig{
		PathRegexp: "^/same$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      10,
	}
	strict := &RateLimitConfig{
		PathRegexp: "^/same$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      1,
	}
	lenient.compiledPathRegexp = regexp.MustCompile(lenient.PathRegexp)
	strict.compiledPathRegexp = regexp.MustCompile(strict.PathRegexp)

	rl := NewRateLimiter(
		t.Name(), []*RateLimitConfig{lenient, strict},
	)
	req := httptest.NewRequest("GET", "/same", nil)
	strictLabels := map[string]string{
		"service":      t.Name(),
		"path_pattern": strict.PathRegexp,
		"requests":     "1",
		"per":          time.Hour.String(),
		"burst":        "1",
	}
	strictDeniedBefore := prometheusCounterValue(
		t, "aperture_ratelimit_rule_denied_total", strictLabels,
	)

	allowed, _ := rl.Allow(req, "test-key")
	require.True(t, allowed)
	allowed, _ = rl.Allow(req, "test-key")
	require.False(t, allowed)
	require.Equal(t, 2, rl.Size())

	require.Equal(
		t, strictDeniedBefore+1, prometheusCounterValue(
			t, "aperture_ratelimit_rule_denied_total", strictLabels,
		),
	)
}

// TestRateLimiterDeniedMetrics makes sure the legacy path metric retains its
// original all-matching-rules semantics, while the rule-specific metric only
// attributes the denial to the rule that actually rejected the request.
func TestRateLimiterDeniedMetrics(t *testing.T) {
	global := &RateLimitConfig{
		Requests: 1,
		Per:      time.Hour,
		Burst:    2,
	}
	specific := &RateLimitConfig{
		PathRegexp: "^/metric-expensive$",
		Requests:   1,
		Per:        time.Hour,
		Burst:      1,
	}
	specific.compiledPathRegexp = regexp.MustCompile(specific.PathRegexp)

	rl := NewRateLimiter(
		t.Name(), []*RateLimitConfig{global, specific},
	)
	globalLabels := map[string]string{
		"service":      t.Name(),
		"path_pattern": "",
	}
	specificLabels := map[string]string{
		"service":      t.Name(),
		"path_pattern": specific.PathRegexp,
	}
	globalBefore := prometheusCounterValue(
		t, "aperture_ratelimit_denied_total", globalLabels,
	)
	specificBefore := prometheusCounterValue(
		t, "aperture_ratelimit_denied_total", specificLabels,
	)
	ruleGlobalLabels := map[string]string{
		"service":      t.Name(),
		"path_pattern": "",
		"requests":     "1",
		"per":          time.Hour.String(),
		"burst":        "2",
	}
	ruleSpecificLabels := map[string]string{
		"service":      t.Name(),
		"path_pattern": specific.PathRegexp,
		"requests":     "1",
		"per":          time.Hour.String(),
		"burst":        "1",
	}
	ruleGlobalBefore := prometheusCounterValue(
		t, "aperture_ratelimit_rule_denied_total", ruleGlobalLabels,
	)
	ruleSpecificBefore := prometheusCounterValue(
		t, "aperture_ratelimit_rule_denied_total", ruleSpecificLabels,
	)
	req := httptest.NewRequest("GET", "/metric-expensive", nil)

	allowed, _ := rl.Allow(req, "test-key")
	require.True(t, allowed)
	allowed, _ = rl.Allow(req, "test-key")
	require.False(t, allowed)

	require.Equal(
		t, globalBefore+1, prometheusCounterValue(
			t, "aperture_ratelimit_denied_total", globalLabels,
		),
	)
	require.Equal(
		t, specificBefore+1, prometheusCounterValue(
			t, "aperture_ratelimit_denied_total", specificLabels,
		),
	)
	require.Equal(
		t, ruleGlobalBefore, prometheusCounterValue(
			t, "aperture_ratelimit_rule_denied_total",
			ruleGlobalLabels,
		),
	)
	require.Equal(
		t, ruleSpecificBefore+1, prometheusCounterValue(
			t, "aperture_ratelimit_rule_denied_total",
			ruleSpecificLabels,
		),
	)
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

// prometheusCounterValue returns the value of a counter with an exact label
// set, or zero if the labeled metric has not been collected yet.
func prometheusCounterValue(t *testing.T, name string,
	wantLabels map[string]string) float64 {

	t.Helper()

	metricFamilies, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	for _, family := range metricFamilies {
		if family.GetName() != name {
			continue
		}

		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if !mapsEqual(labels, wantLabels) {
				continue
			}

			return metric.GetCounter().GetValue()
		}
	}

	return 0
}

func mapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}

	for key, value := range left {
		if right[key] != value {
			return false
		}
	}

	return true
}
