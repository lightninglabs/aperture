package aperture

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"
)

var proxyV2Signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

// proxyProtocolNetworks validates the receiver configuration once at startup.
func proxyProtocolNetworks(cfg *ProxyProtocolConfig) ([]*net.IPNet, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	if cfg.HeaderTimeout <= 0 {
		return nil, fmt.Errorf("proxyprotocol.headertimeout must be positive")
	}
	if len(cfg.TrustedProxies) == 0 {
		return nil, fmt.Errorf("proxyprotocol requires at least one trustedproxy")
	}

	networks := make([]*net.IPNet, 0, len(cfg.TrustedProxies))
	for _, addr := range cfg.TrustedProxies {
		if ip := net.ParseIP(addr); ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			networks = append(networks, &net.IPNet{
				IP: ip, Mask: net.CIDRMask(bits, bits),
			})
			continue
		}
		_, network, err := net.ParseCIDR(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: %w", addr, err)
		}
		networks = append(networks, network)
	}
	return networks, nil
}

func trustedProxy(addr net.Addr, networks []*net.IPNet) bool {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	for _, network := range networks {
		if network.Contains(tcp.IP) {
			return true
		}
	}
	return false
}

type proxyProtocolListener struct {
	net.Listener
	networks []*net.IPNet
	timeout  time.Duration
}

// Accept checks the transport peer without reading from the connection. Header
// reads happen in the HTTP server's per-connection goroutine, so one stalled
// sender cannot block the accept loop.
func (l *proxyProtocolListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !trustedProxy(conn.RemoteAddr(), l.networks) {
			_ = conn.Close()
			continue
		}
		return &proxyProtocolConn{
			Conn: conn, peer: conn.RemoteAddr(), timeout: l.timeout,
		}, nil
	}
}

type proxyProtocolConn struct {
	net.Conn
	peer    net.Addr
	timeout time.Duration
	once    sync.Once
	remote  net.Addr
	err     error
}

func (c *proxyProtocolConn) readHeader() {
	c.remote = c.peer
	c.err = c.SetReadDeadline(time.Now().Add(c.timeout))
	if c.err != nil {
		return
	}
	c.remote, c.err = readProxyV2(c.Conn, c.peer)
	if c.err == nil {
		c.err = c.SetReadDeadline(time.Time{})
	}
	if c.err != nil {
		_ = c.Close()
	}
}

func (c *proxyProtocolConn) RemoteAddr() net.Addr {
	c.once.Do(c.readHeader)
	if c.remote == nil {
		return c.peer
	}
	return c.remote
}

func (c *proxyProtocolConn) Read(b []byte) (int, error) {
	c.once.Do(c.readHeader)
	if c.err != nil {
		return 0, c.err
	}
	return c.Conn.Read(b)
}

// readProxyV2 reads exactly one preamble, leaving application bytes untouched.
// The uint16 wire length limits the allocation to 65535 bytes.
func readProxyV2(r io.Reader, peer net.Addr) (net.Addr, error) {
	var header [16]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	if !bytes.Equal(header[:12], proxyV2Signature) || header[12]>>4 != 2 {
		return nil, fmt.Errorf("invalid PROXY v2 signature or version")
	}
	command := header[12] & 0xf
	if command > 1 {
		return nil, fmt.Errorf("invalid PROXY v2 command %d", command)
	}
	payload := make([]byte, int(binary.BigEndian.Uint16(header[14:])))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}

	// LOCAL ignores the family and the complete opaque payload. UNSPEC also
	// retains the transport endpoints instead of inventing a client address.
	family, protocol := header[13]>>4, header[13]&0xf
	if command == 0 {
		return peer, nil
	}
	if family > 3 || protocol > 2 {
		return nil, fmt.Errorf("invalid PROXY v2 family or transport")
	}
	if family == 0 || protocol == 0 {
		return peer, nil
	}

	addressLen := [...]int{0, 12, 36, 216}[family]
	if len(payload) < addressLen {
		return nil, fmt.Errorf("short PROXY v2 address block")
	}
	for tlvs := payload[addressLen:]; len(tlvs) > 0; {
		if len(tlvs) < 3 {
			return nil, fmt.Errorf("short PROXY v2 TLV header")
		}
		size := 3 + int(binary.BigEndian.Uint16(tlvs[1:3]))
		if size > len(tlvs) {
			return nil, fmt.Errorf("short PROXY v2 TLV value")
		}
		tlvs = tlvs[size:]
	}

	// Valid datagram and UNIX headers are consumed as UNSPEC, as required
	// for receivers that do not implement those address families/transports.
	if protocol != 1 || family == 3 {
		return peer, nil
	}
	ipLen := 4
	if family == 2 {
		ipLen = 16
	}
	return &net.TCPAddr{
		IP:   net.IP(payload[:ipLen]),
		Port: int(binary.BigEndian.Uint16(payload[2*ipLen:])),
	}, nil
}

type proxyProtocolPeerKey struct{}

// proxyProtocolContext records the actual TCP peer before any header reads.
// ConnContext runs on the accept loop and must never parse a preamble.
func proxyProtocolContext(ctx context.Context, conn net.Conn) context.Context {
	if tlsConn, ok := conn.(*tls.Conn); ok {
		conn = tlsConn.NetConn()
	}
	if proxyConn, ok := conn.(*proxyProtocolConn); ok {
		return context.WithValue(ctx, proxyProtocolPeerKey{}, proxyConn.peer)
	}
	return ctx
}

func isProxyProtocolRequest(r *http.Request) bool {
	return r.Context().Value(proxyProtocolPeerKey{}) != nil
}

func serveProxyProtocol(server *http.Server, cfg *Config) error {
	networks, err := proxyProtocolNetworks(cfg.ProxyProtocol)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	listener = &proxyProtocolListener{
		Listener: listener, networks: networks,
		timeout: cfg.ProxyProtocol.HeaderTimeout,
	}
	server.ConnContext = proxyProtocolContext
	if cfg.Insecure {
		return server.Serve(listener)
	}
	return server.ServeTLS(listener, "", "")
}

// proxyProtocolDialOption lets the REST gateways dial the same public listener.
// These are local, authenticated gRPC calls, so they send a LOCAL preamble.
func proxyProtocolDialOption(cfg *Config) grpc.DialOption {
	if cfg.ProxyProtocol == nil || !cfg.ProxyProtocol.Enabled {
		return grpc.EmptyDialOption{}
	}
	return grpc.WithContextDialer(func(ctx context.Context,
		addr string) (net.Conn, error) {

		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		if err := conn.SetWriteDeadline(time.Now().Add(
			cfg.ProxyProtocol.HeaderTimeout,
		)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		header := append([]byte{}, proxyV2Signature...)
		header = append(header, 0x20, 0, 0, 0)
		if _, err := conn.Write(header); err != nil {
			_ = conn.Close()
			return nil, err
		}
		if err := conn.SetWriteDeadline(time.Time{}); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	})
}
