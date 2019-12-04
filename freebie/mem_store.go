package freebie

import (
	"net"
	"net/http"
)

var (
	defaultIPMask = net.IPv4Mask(0xff, 0xff, 0xff, 0x00)
)

type Count uint16

type memStore struct {
	numFreebies    Count
	freebieCounter map[string]Count
}

func (m *memStore) getKey(ip net.IP) string {
	return ip.Mask(defaultIPMask).String()
}

func (m *memStore) currentCount(ip net.IP) Count {
	counter, ok := m.freebieCounter[m.getKey(ip)]
	if !ok {
		return 0
	}
	return counter
}

func (m *memStore) CanPass(r *http.Request, ip net.IP) (bool, error) {
	return m.currentCount(ip) < m.numFreebies, nil
}

func (m *memStore) TallyFreebie(r *http.Request, ip net.IP) (bool, error) {
	counter := m.currentCount(ip) + 1
	m.freebieCounter[m.getKey(ip)] = counter
	return true, nil
}

// NewMemIPMaskStore creates a new in-memory freebie store that masks the last
// byte of an IP address to keep track of free requests. The last byte of the
// address is discarded for the mapping to reduce risk of abuse by users that
// have a whole range of IPs at their disposal.
func NewMemIPMaskStore(numFreebies Count) DB {
	return &memStore{
		numFreebies:    numFreebies,
		freebieCounter: make(map[string]Count),
	}
}
