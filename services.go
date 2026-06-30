package aperture

import (
	"context"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/proxy"
)

// staticServiceLimiter provides live-updatable restrictions for services. Its
// maps are rebuilt atomically via refresh whenever the service list changes.
type staticServiceLimiter struct {
	mu           sync.RWMutex
	capabilities map[l402.Service]l402.Caveat
	constraints  map[l402.Service][]l402.Caveat
	timeouts     map[l402.Service]int64
}

// A compile-time constraint to ensure staticServiceLimiter implements
// mint.ServiceLimiter.
var _ mint.ServiceLimiter = (*staticServiceLimiter)(nil)

// newStaticServiceLimiter instantiates a new static service limiter backed by
// the given restrictions.
func newStaticServiceLimiter(
	proxyServices []*proxy.Service) *staticServiceLimiter {

	l := &staticServiceLimiter{}
	l.refresh(proxyServices)
	return l
}

// refresh rebuilds all three service maps atomically from the given service
// list. It is safe to call concurrently with the Service* read methods.
func (l *staticServiceLimiter) refresh(proxyServices []*proxy.Service) {
	caps := make(map[l402.Service]l402.Caveat)
	cons := make(map[l402.Service][]l402.Caveat)
	tos := make(map[l402.Service]int64)

	for _, proxyService := range proxyServices {
		s := l402.Service{
			Name:  proxyService.Name,
			Tier:  l402.BaseTier,
			Price: proxyService.Price,
		}

		if proxyService.Timeout > 0 {
			tos[s] = proxyService.Timeout
		}

		caps[s] = l402.NewCapabilitiesCaveat(
			proxyService.Name, proxyService.Capabilities,
		)
		for cond, value := range proxyService.Constraints {
			caveat := l402.Caveat{Condition: cond, Value: value}
			cons[s] = append(cons[s], caveat)
		}
	}

	l.mu.Lock()
	l.capabilities = caps
	l.constraints = cons
	l.timeouts = tos
	l.mu.Unlock()
}

// ServiceCapabilities returns the capabilities caveats for each service. This
// determines which capabilities of each service can be accessed.
func (l *staticServiceLimiter) ServiceCapabilities(ctx context.Context,
	services ...l402.Service) ([]l402.Caveat, error) {

	l.mu.RLock()
	defer l.mu.RUnlock()

	res := make([]l402.Caveat, 0, len(services))
	for _, service := range services {
		capabilities, ok := l.capabilities[service]
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

	l.mu.RLock()
	defer l.mu.RUnlock()

	res := make([]l402.Caveat, 0, len(services))
	for _, service := range services {
		constraints, ok := l.constraints[service]
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

	l.mu.RLock()
	defer l.mu.RUnlock()

	res := make([]l402.Caveat, 0, len(services))
	for _, service := range services {
		numSeconds, ok := l.timeouts[service]
		if !ok {
			continue
		}
		res = append(res, l402.NewTimeoutCaveat(
			service.Name, numSeconds, time.Now,
		))
	}

	return res, nil
}
