package aperture

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/proxy"
)

// staticServiceLimiter provides static restrictions for services.
//
// TODO(wilmer): use etcd instead.
type staticServiceLimiter struct {
	capabilities map[limiterKey]l402.Caveat
	constraints  map[limiterKey][]l402.Caveat
	timeouts     map[limiterKey]int64
}

// limiterKey identifies the restrictions configured for a service tier. The
// price a token sells for is deliberately not part of it: a dynamic-price
// service quotes a different price per resource, and a static one only gets
// its default price after this limiter is built, yet the same restrictions
// apply to every token a service issues.
type limiterKey struct {
	name string
	tier l402.ServiceTier
}

// limiterKeyFor returns the key of the configured service that issues tokens
// under the given service. A dynamic-price service names each resource after
// itself followed by the request path, and service names cannot contain a
// slash, so the name up to the first slash is the configured service.
func limiterKeyFor(service l402.Service) limiterKey {
	name, _, _ := strings.Cut(service.Name, "/")

	return limiterKey{name: name, tier: service.Tier}
}

// A compile-time constraint to ensure staticServiceLimiter implements
// mint.ServiceLimiter.
var _ mint.ServiceLimiter = (*staticServiceLimiter)(nil)

// newStaticServiceLimiter instantiates a new static service limiter backed by
// the given restrictions. Tokens are named after their service, so services
// that share a name share their tokens, and must agree on the restrictions
// those tokens carry.
func newStaticServiceLimiter(
	proxyServices []*proxy.Service) (*staticServiceLimiter, error) {

	capabilities := make(map[limiterKey]l402.Caveat)
	constraints := make(map[limiterKey][]l402.Caveat)
	timeouts := make(map[limiterKey]int64)
	first := make(map[limiterKey]*proxy.Service)

	for _, proxyService := range proxyServices {
		s := limiterKey{name: proxyService.Name, tier: l402.BaseTier}

		if prev, ok := first[s]; ok {
			if !sameRestrictions(prev, proxyService) {
				return nil, fmt.Errorf("services named %q share "+
					"tokens but set different timeout, "+
					"capabilities or constraints",
					proxyService.Name)
			}

			// Its restrictions are already recorded.
			continue
		}
		first[s] = proxyService

		if proxyService.Timeout > 0 {
			timeouts[s] = proxyService.Timeout
		}

		capabilities[s] = l402.NewCapabilitiesCaveat(
			proxyService.Name, proxyService.Capabilities,
		)
		for cond, value := range proxyService.Constraints {
			caveat := l402.Caveat{Condition: cond, Value: value}
			constraints[s] = append(constraints[s], caveat)
		}
	}

	return &staticServiceLimiter{
		capabilities: capabilities,
		constraints:  constraints,
		timeouts:     timeouts,
	}, nil
}

// sameRestrictions reports whether two services put the same restrictions on
// the tokens they issue, compared the way they take effect: only a positive
// timeout adds a caveat, and capabilities are checked as a set.
func sameRestrictions(a, b *proxy.Service) bool {
	return max(a.Timeout, 0) == max(b.Timeout, 0) &&
		maps.Equal(
			capabilitySet(a.Capabilities),
			capabilitySet(b.Capabilities),
		) &&
		maps.Equal(a.Constraints, b.Constraints)
}

// capabilitySet splits a capabilities value the way the capabilities
// satisfier does: on commas, without trimming.
func capabilitySet(capabilities string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, capability := range strings.Split(capabilities, ",") {
		set[capability] = struct{}{}
	}

	return set
}

// ServiceCapabilities returns the capabilities caveats for each service. This
// determines which capabilities of each service can be accessed.
func (l *staticServiceLimiter) ServiceCapabilities(ctx context.Context,
	services ...l402.Service) ([]l402.Caveat, error) {

	res := make([]l402.Caveat, 0, len(services))
	for _, service := range services {
		// The caveat was built under the configured service name,
		// which is the name backends check capabilities under.
		capabilities, ok := l.capabilities[limiterKeyFor(service)]
		if !ok {
			continue
		}
		res = append(res, capabilities)
	}

	return res, nil
}

// ServiceConstraints returns the constraints for each service. This enforces
// additional constraints on a particular service/service capability.
func (l *staticServiceLimiter) ServiceConstraints(ctx context.Context,
	services ...l402.Service) ([]l402.Caveat, error) {

	res := make([]l402.Caveat, 0, len(services))
	for _, service := range services {
		constraints, ok := l.constraints[limiterKeyFor(service)]
		if !ok {
			continue
		}
		res = append(res, constraints...)
	}

	return res, nil
}

// ServiceTimeouts returns the timeout caveat for each service. This enforces
// an expiration time for service access if enabled.
func (l *staticServiceLimiter) ServiceTimeouts(ctx context.Context,
	services ...l402.Service) ([]l402.Caveat, error) {

	res := make([]l402.Caveat, 0, len(services))
	for _, service := range services {
		numSeconds, ok := l.timeouts[limiterKeyFor(service)]
		if !ok {
			continue
		}

		// The verifier checks expiry under the name the token is
		// presented for, which for a dynamic-price resource is the
		// resource name, so the caveat is written under the token's
		// own name. A caveat splits its condition from its value at
		// the first '=', so under a name containing one the timeout
		// could not be read back and the token would never expire.
		if strings.Contains(service.Name, "=") {
			return nil, fmt.Errorf("cannot limit %q in time: its "+
				"name contains '='", service.Name)
		}
		res = append(res, l402.NewTimeoutCaveat(
			service.Name, numSeconds, time.Now,
		))
	}

	return res, nil
}
