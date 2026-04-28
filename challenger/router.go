package challenger

import (
	"fmt"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
)

// RouterChallenger dispatches challenge creation and invoice verification
// across multiple underlying Challengers, one per merchant-service. Used
// for multi-tenant deployments where each service's invoices must land
// in the merchant's own lnd wallet, so the gateway operator never takes
// custody.
//
// Services that don't have a per-service lnd configured fall through to
// the default (global) challenger. VerifyInvoiceStatus routes by payment
// hash — the router tracks hash→subChallenger mapping as invoices are
// minted, then dispatches lookups accordingly. Unknown hashes fall back
// to the default challenger as well (covers the case where prism is
// restarted and in-memory state is lost but the invoice still exists on
// the original lnd).
type RouterChallenger struct {
	// defaultChallenger is the gateway operator's global lnd; used for
	// services without per-service configuration, and as a fallback
	// when VerifyInvoiceStatus is called with an unknown hash.
	defaultChallenger Challenger

	// perService holds a challenger per configured service name.
	perService map[string]Challenger

	// hashRoute records which sub-challenger created which invoice so
	// VerifyInvoiceStatus can route lookups to the right lnd. The
	// router mutex protects concurrent writes from the different
	// mint/verify paths.
	routeMu   sync.RWMutex
	hashRoute map[lntypes.Hash]Challenger
}

// Compile-time interface checks: RouterChallenger satisfies both the
// plain Challenger contract and the ServiceAwareChallenger extension.
var _ Challenger = (*RouterChallenger)(nil)
var _ mint.ServiceAwareChallenger = (*RouterChallenger)(nil)

// NewRouterChallenger builds a router that dispatches challenge and
// verify calls across multiple Challengers. `perService` maps service
// names to their dedicated challenger; services not present in the map
// use `defaultChallenger`.
func NewRouterChallenger(defaultChallenger Challenger,
	perService map[string]Challenger) *RouterChallenger {

	if perService == nil {
		perService = make(map[string]Challenger)
	}
	return &RouterChallenger{
		defaultChallenger: defaultChallenger,
		perService:        perService,
		hashRoute:         make(map[lntypes.Hash]Challenger),
	}
}

// NewChallenge is the legacy entry point; with no service context, it
// delegates to the default challenger. Kept so the router still
// satisfies the plain mint.Challenger interface.
func (r *RouterChallenger) NewChallenge(price int64) (
	string, lntypes.Hash, error) {

	return r.newChallengeVia(r.defaultChallenger, price)
}

// NewChallengeForService routes the invoice creation to the service's
// own lnd when configured, otherwise uses the default.
func (r *RouterChallenger) NewChallengeForService(serviceName string,
	price int64) (string, lntypes.Hash, error) {

	chal := r.defaultChallenger
	if serviceName != "" {
		if sub, ok := r.perService[serviceName]; ok {
			chal = sub
		}
	}
	return r.newChallengeVia(chal, price)
}

// newChallengeVia issues the invoice against `c` and records the hash →
// challenger mapping so later VerifyInvoiceStatus calls route to the
// same lnd.
func (r *RouterChallenger) newChallengeVia(c Challenger, price int64) (
	string, lntypes.Hash, error) {

	payReq, hash, err := c.NewChallenge(price)
	if err != nil {
		return "", lntypes.Hash{}, err
	}

	r.routeMu.Lock()
	r.hashRoute[hash] = c
	r.routeMu.Unlock()

	return payReq, hash, nil
}

// VerifyInvoiceStatus routes the lookup to whichever sub-challenger
// originally created the invoice. If the hash is unknown (e.g. after a
// gateway restart), falls back to the default challenger, which will
// either find the invoice (single-lnd deployments) or return a standard
// "invoice not found" error (multi-merchant deployments where the
// invoice is on a different lnd).
func (r *RouterChallenger) VerifyInvoiceStatus(hash lntypes.Hash,
	state lnrpc.Invoice_InvoiceState, timeout time.Duration) error {

	r.routeMu.RLock()
	sub, ok := r.hashRoute[hash]
	r.routeMu.RUnlock()

	if !ok {
		// After restart the in-memory route table is empty. Try each
		// sub-challenger in turn so that invoices minted by a
		// previous process still verify correctly.
		for _, c := range r.perService {
			if err := c.VerifyInvoiceStatus(hash, state, timeout); err == nil {
				// Remember the route for future lookups.
				r.routeMu.Lock()
				r.hashRoute[hash] = c
				r.routeMu.Unlock()
				return nil
			}
		}
		// Last resort: the default.
		return r.defaultChallenger.VerifyInvoiceStatus(
			hash, state, timeout,
		)
	}
	return sub.VerifyInvoiceStatus(hash, state, timeout)
}

// Stop shuts down every wrapped challenger. Errors are swallowed; each
// sub-challenger logs its own stop issues.
func (r *RouterChallenger) Stop() {
	if r.defaultChallenger != nil {
		r.defaultChallenger.Stop()
	}
	for _, c := range r.perService {
		c.Stop()
	}
}

// ensureDistinctMerchants is a helper used by aperture startup to
// sanity-check that no two services share the same (lndhost, macpath)
// pair by accident — which would silently pool their funds on the same
// lnd and defeat the whole point of the per-service routing. Returns a
// descriptive error on collision, nil otherwise.
func EnsureDistinctMerchants(configs map[string]*MerchantKey) error {
	seen := make(map[MerchantKey]string)
	for name, key := range configs {
		if key == nil {
			continue
		}
		if prev, ok := seen[*key]; ok {
			return fmt.Errorf("services %q and %q share the same "+
				"lnd endpoint (%s) and macaroon — invoices "+
				"for both services would pool on one lnd, "+
				"undermining per-merchant isolation",
				prev, name, key.LndHost)
		}
		seen[*key] = name
	}
	return nil
}

// MerchantKey identifies a distinct (lnd, macaroon) pair used for
// collision detection above.
type MerchantKey struct {
	LndHost string
	MacPath string
}
