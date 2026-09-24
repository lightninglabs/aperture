package proxy

import (
	"fmt"
	"math/rand/v2"
	"net/http/httptest"
	"regexp"
	"runtime"
	"testing"
	"time"
)

// newBenchRule returns a rate limit rule for the given path pattern.
func newBenchRule(pathRegexp string, requests int, per time.Duration,
	burst int) *RateLimitConfig {

	cfg := &RateLimitConfig{
		PathRegexp: pathRegexp,
		Requests:   requests,
		Per:        per,
		Burst:      burst,
	}
	cfg.compiledPathRegexp = regexp.MustCompile(pathRegexp)

	return cfg
}

// BenchmarkRateLimiterAllow measures a request matching two rules, either
// allowed by both or denied by one, in which case all reservations are
// cancelled.
func BenchmarkRateLimiterAllow(b *testing.B) {
	const unlimited = 1_000_000_000

	cases := []struct {
		name      string
		expensive *RateLimitConfig
	}{
		{
			name: "allowed",
			expensive: newBenchRule(
				"^/expensive$", unlimited, time.Second,
				unlimited,
			),
		},
		{
			// A single token, used up by the warm-up request.
			name: "denied",
			expensive: newBenchRule(
				"^/expensive$", 1, time.Hour, 1,
			),
		},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			shared := newBenchRule(
				".*", unlimited, time.Second, unlimited,
			)
			rl := NewRateLimiter(
				"bench-service",
				[]*RateLimitConfig{shared, c.expensive},
			)
			req := httptest.NewRequest("GET", "/expensive", nil)

			// Warm up the cache.
			rl.Allow(req, "bench-key")

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rl.Allow(req, "bench-key")
			}
		})
	}
}

// BenchmarkRateLimiterAllowParallel measures many concurrent goroutines
// sending requests on behalf of a pool of clients. Every client uses one cache
// entry per rule, so pools above 5000 clients no longer fit into the default
// cache and force evictions.
func BenchmarkRateLimiterAllowParallel(b *testing.B) {
	// goroutines is the approximate number of concurrent goroutines
	// calling Allow, similar to many open client connections.
	const goroutines = 1024

	for _, clients := range []int{1_000, 5_000, 10_000, 20_000} {
		b.Run(fmt.Sprintf("clients-%d", clients), func(b *testing.B) {
			keys := make([]string, clients)
			for i := range keys {
				keys[i] = fmt.Sprintf("ip:%d.%d.%d.0",
					10+i/65536, i/256%256, i%256)
			}

			shared := newBenchRule(".*", 500, 10*time.Second, 200)
			expensive := newBenchRule(
				"^/expensive$", 20, 10*time.Second, 40,
			)
			rl := NewRateLimiter(
				"bench-service",
				[]*RateLimitConfig{shared, expensive},
			)
			expensiveReq := httptest.NewRequest(
				"GET", "/expensive", nil,
			)
			otherReq := httptest.NewRequest("GET", "/other", nil)

			parallelism := goroutines / runtime.GOMAXPROCS(0)
			b.SetParallelism(max(parallelism, 1))
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				// Pick a random client and path for every
				// request. One in four requests goes to
				// /expensive.
				for pb.Next() {
					req := otherReq
					if rand.IntN(4) == 0 {
						req = expensiveReq
					}
					rl.Allow(req, keys[rand.IntN(clients)])
				}
			})
		})
	}
}
