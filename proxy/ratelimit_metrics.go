package proxy

import (
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
