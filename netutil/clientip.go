package netutil

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ParseTrustedProxies parses a list of CIDR blocks and single IP addresses
// into the networks of the reverse proxies whose forwarding headers can be
// trusted. A bare address is a network of exactly one host. An empty list
// returns no networks, which means no proxy is trusted.
func ParseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)

		if strings.Contains(entry, "/") {
			_, ipNet, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy "+
					"CIDR %q: %w", entry, err)
			}
			nets = append(nets, ipNet)

			continue
		}

		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: "+
				"not an IP address or CIDR", entry)
		}

		bits := 8 * net.IPv6len
		if ip4 := ip.To4(); ip4 != nil {
			ip, bits = ip4, 8*net.IPv4len
		}
		nets = append(nets, &net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(bits, bits),
		})
	}

	return nets, nil
}

// ClientIP resolves the address of the client that sent a request. The TCP
// peer address is the answer unless the peer is one of the trusted proxies, in
// which case the X-Forwarded-For header is consulted.
//
// A reverse proxy appends the address it saw to the end of the header, after
// whatever value the client supplied. Only the entries to the right of the
// client-supplied part were written by infrastructure we trust, so the header
// is read from right to left. Entries naming a trusted proxy are hops of our
// own infrastructure and are skipped, and the first remaining entry that
// parses as an IP address is the client. Entries to its left can be forged
// and are never read.
//
// A header from a peer that is not trusted is ignored, since anyone can send
// one. When no usable entry exists the peer address is returned. An
// unparseable peer address resolves to 0.0.0.0.
func ClientIP(r *http.Request, trusted []*net.IPNet) net.IP {
	peer := peerIP(r.RemoteAddr)
	if !containsIP(trusted, peer) {
		return peer
	}

	// The header may be split over several lines, which is the same as
	// one comma separated list in line order.
	var entries []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(line, ",")...)
	}

	for i := len(entries) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(entries[i]))
		if ip == nil || containsIP(trusted, ip) {
			continue
		}

		return ip
	}

	return peer
}

// peerIP returns the host of a "host:port" remote address, or 0.0.0.0 if it
// is not one.
func peerIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return net.IPv4zero
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return net.IPv4zero
	}

	return ip
}

// containsIP reports whether any of the networks contains the address.
func containsIP(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}

	return false
}
