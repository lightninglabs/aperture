package proxy

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// rateLimitAllowed counts requests that passed rate limiting.
	rateLimitAllowed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "aperture",
			Subsystem: "ratelimit",
			Name:      "allowed_total",
			Help:      "Total number of requests allowed by rate limiter",
		},
		[]string{"service", "path_pattern"},
	)

	// rateLimitDenied counts requests denied by rate limiting.
	rateLimitDenied = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "aperture",
			Subsystem: "ratelimit",
			Name:      "denied_total",
			Help:      "Total number of requests denied by rate limiter",
		},
		[]string{"service", "path_pattern"},
	)

	// rateLimitRuleAllowed is the rule-specific counterpart to
	// rateLimitAllowed. Keeping this as a separate metric preserves the
	// original path-aggregated series while making duplicate path patterns
	// with different time horizons distinguishable.
	rateLimitRuleAllowed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "aperture",
			Subsystem: "ratelimit",
			Name:      "rule_allowed_total",
			Help: "Total allowed evaluations for each configured " +
				"rate limit rule",
		},
		[]string{
			"service", "path_pattern", "requests", "per", "burst",
		},
	)

	// rateLimitRuleDenied records denials for each semantically distinct rule.
	// Definition labels keep a series' meaning stable across configuration
	// reordering and distinguish duplicate path patterns with different limits.
	rateLimitRuleDenied = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "aperture",
			Subsystem: "ratelimit",
			Name:      "rule_denied_total",
			Help: "Total denied evaluations for each configured " +
				"rate limit rule",
		},
		[]string{
			"service", "path_pattern", "requests", "per", "burst",
		},
	)

	// rateLimitCacheSize tracks the current size of the rate limiter cache.
	rateLimitCacheSize = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "aperture",
			Subsystem: "ratelimit",
			Name:      "cache_size",
			Help:      "Current number of entries in the rate limiter cache",
		},
		[]string{"service"},
	)

	// rateLimitActiveCacheSize tracks the aggregate cache size of active
	// service snapshots. Unlike the legacy last-writer cache_size gauge, this
	// metric has an explicit update/removal lifecycle.
	rateLimitActiveCacheSize = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "aperture",
			Subsystem: "ratelimit",
			Name:      "active_cache_size",
			Help: "Current number of rate limiter cache entries across " +
				"active service snapshots",
		},
		[]string{"service"},
	)

	// rateLimitEvictions counts LRU cache evictions.
	rateLimitEvictions = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "aperture",
			Subsystem: "ratelimit",
			Name:      "evictions_total",
			Help:      "Total number of rate limiter cache evictions",
		},
		[]string{"service"},
	)
)

// cacheSizeMetricRegistry aggregates cache sizes for active managed limiters.
// Prometheus labels identify services rather than Proxy instances, so deleting
// or overwriting a label directly would corrupt the gauge when more than one
// Proxy in the process uses the same service name.
type cacheSizeMetricRegistry struct {
	mu sync.Mutex

	limiters map[string]map[*RateLimiter]int
	totals   map[string]int
}

var managedRateLimitCacheMetrics = &cacheSizeMetricRegistry{
	limiters: make(map[string]map[*RateLimiter]int),
	totals:   make(map[string]int),
}

// set publishes one managed limiter's size and updates its service aggregate.
func (r *cacheSizeMetricRegistry) set(limiter *RateLimiter, size int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !limiter.cacheMetricActive.Load() {
		return
	}

	serviceName := limiter.serviceName
	serviceLimiters := r.limiters[serviceName]
	if serviceLimiters == nil {
		serviceLimiters = make(map[*RateLimiter]int)
		r.limiters[serviceName] = serviceLimiters
	}

	oldSize := serviceLimiters[limiter]
	serviceLimiters[limiter] = size
	r.totals[serviceName] += size - oldSize
	rateLimitActiveCacheSize.WithLabelValues(serviceName).Set(
		float64(r.totals[serviceName]),
	)
}

// remove stops accounting for one managed limiter without disturbing other
// Proxy instances that use the same service label.
func (r *cacheSizeMetricRegistry) remove(limiter *RateLimiter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	serviceName := limiter.serviceName
	serviceLimiters := r.limiters[serviceName]
	if serviceLimiters == nil {
		return
	}

	size, ok := serviceLimiters[limiter]
	if !ok {
		return
	}

	delete(serviceLimiters, limiter)
	r.totals[serviceName] -= size
	if len(serviceLimiters) == 0 {
		delete(r.limiters, serviceName)
		delete(r.totals, serviceName)
		rateLimitActiveCacheSize.DeleteLabelValues(serviceName)
		return
	}

	rateLimitActiveCacheSize.WithLabelValues(serviceName).Set(
		float64(r.totals[serviceName]),
	)
}

// replace swaps one Proxy snapshot's limiter contributions under one registry
// lock and publishes each touched service aggregate only after the complete
// replacement has been calculated.
func (r *cacheSizeMetricRegistry) replace(oldServices,
	newServices []*Service) {

	type limiterSize struct {
		limiter *RateLimiter
		size    int
	}
	newLimiters := make([]limiterSize, 0, len(newServices))
	for _, service := range newServices {
		if service == nil || service.rateLimiter == nil ||
			!service.rateLimiter.managedCacheMetric {

			continue
		}

		newLimiters = append(newLimiters, limiterSize{
			limiter: service.rateLimiter,
			size:    service.rateLimiter.Size(),
		})
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	touched := make(map[string]struct{})
	for _, service := range oldServices {
		if service == nil || service.rateLimiter == nil ||
			!service.rateLimiter.managedCacheMetric {

			continue
		}

		limiter := service.rateLimiter
		limiter.cacheMetricActive.Store(false)
		serviceLimiters := r.limiters[limiter.serviceName]
		if serviceLimiters == nil {
			continue
		}

		size, ok := serviceLimiters[limiter]
		if !ok {
			continue
		}

		delete(serviceLimiters, limiter)
		r.totals[limiter.serviceName] -= size
		touched[limiter.serviceName] = struct{}{}
		if len(serviceLimiters) == 0 {
			delete(r.limiters, limiter.serviceName)
			delete(r.totals, limiter.serviceName)
		}
	}

	for _, entry := range newLimiters {
		limiter := entry.limiter
		limiter.cacheMetricActive.Store(true)
		serviceLimiters := r.limiters[limiter.serviceName]
		if serviceLimiters == nil {
			serviceLimiters = make(map[*RateLimiter]int)
			r.limiters[limiter.serviceName] = serviceLimiters
		}

		serviceLimiters[limiter] = entry.size
		r.totals[limiter.serviceName] += entry.size
		touched[limiter.serviceName] = struct{}{}
	}

	for serviceName := range touched {
		total, ok := r.totals[serviceName]
		if !ok {
			rateLimitActiveCacheSize.DeleteLabelValues(serviceName)
			continue
		}

		rateLimitActiveCacheSize.WithLabelValues(serviceName).Set(
			float64(total),
		)
	}
}
