package freebie

import (
	"net"
	"net/http"
	"sync"

	"github.com/lightninglabs/aperture/netutil"
)

// Count is the number of free requests allowed or consumed.
type Count uint16

// memStore tracks free requests by masked IP address in memory.
type memStore struct {
	// mu protects freebieCounter and keeps each check-and-increment atomic.
	mu sync.Mutex

	// numFreebies is the per-mask allowance, fixed when the store is
	// created.
	numFreebies Count

	// freebieCounter records the number of requests consumed by each mask.
	freebieCounter map[string]Count
}

// getKey groups addresses that share the same masked IP.
func (m *memStore) getKey(ip net.IP) string {
	return netutil.MaskIP(ip).String()
}

// TakeFreebie consumes one free request if the masked IP has allowance left.
// The lock covers both the check and increment so concurrent requests cannot
// exceed the allowance.
func (m *memStore) TakeFreebie(_ *http.Request, ip net.IP) (bool, error) {
	key := m.getKey(ip)

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.freebieCounter[key] >= m.numFreebies {
		return false, nil
	}

	m.freebieCounter[key]++
	return true, nil
}

// NewMemIPMaskStore creates a new in-memory freebie store that masks IP
// addresses to keep track of free requests. IPv4 addresses are masked to /24
// and IPv6 addresses to /48. This reduces risk of abuse by users that have a
// whole range of IPs at their disposal.
func NewMemIPMaskStore(numFreebies Count) DB {
	return &memStore{
		numFreebies:    numFreebies,
		freebieCounter: make(map[string]Count),
	}
}
