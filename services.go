package aperture

import (
	"context"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/proxy"
)

// staticServiceLimiter provides static restrictions for services.
//
// TODO(wilmer): use etcd instead.
type staticServiceLimiter struct {
	capabilities map[lsat.Service]lsat.Caveat
	constraints  map[lsat.Service][]lsat.Caveat
}

// A compile-time constraint to ensure staticServiceLimiter implements
// mint.ServiceLimiter.
var _ mint.ServiceLimiter = (*staticServiceLimiter)(nil)

// newStaticServiceLimiter instantiates a new static service limiter backed by
// the given restrictions.
func newStaticServiceLimiter(proxyServices []*proxy.Service) *staticServiceLimiter {
	capabilities := make(map[lsat.Service]lsat.Caveat)
	constraints := make(map[lsat.Service][]lsat.Caveat)

	for _, proxyService := range proxyServices {
		s := lsat.Service{
			Name:  proxyService.Name,
			Tier:  lsat.BaseTier,
			Price: proxyService.Price,
		}
		capabilities[s] = lsat.NewCapabilitiesCaveat(
			proxyService.Name, proxyService.Capabilities,
		)
		for cond, value := range proxyService.Constraints {
			caveat := lsat.Caveat{Condition: cond, Value: value}
			constraints[s] = append(constraints[s], caveat)
		}
	}

	return &staticServiceLimiter{
		capabilities: capabilities,
		constraints:  constraints,
	}
}

// ServiceCapabilities returns the capabilities caveats for each service. This
// determines which capabilities of each service can be accessed.
func (l *staticServiceLimiter) ServiceCapabilities(ctx context.Context,
	services ...lsat.Service) ([]lsat.Caveat, error) {

	res := make([]lsat.Caveat, 0, len(services))
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
	services ...lsat.Service) ([]lsat.Caveat, error) {

	res := make([]lsat.Caveat, 0, len(services))
	for _, service := range services {
		constraints, ok := l.constraints[service]
		if !ok {
			continue
		}
		res = append(res, constraints...)
	}

	return res, nil
}
