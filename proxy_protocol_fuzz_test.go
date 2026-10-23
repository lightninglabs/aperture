package aperture

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// proxyFuzzFrame encodes the fixed v2 layout independently of the receiver.
func proxyFuzzFrame(command, family byte, payload []byte) []byte {
	frame := []byte("\r\n\r\n\x00\r\nQUIT\n")
	frame = append(frame, command, family)
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(payload)))
	return append(frame, payload...)
}

type proxyFuzzReader struct {
	*bytes.Reader
	chunk int
}

func (r proxyFuzzReader) Read(b []byte) (int, error) {
	if len(b) > r.chunk {
		b = b[:r.chunk]
	}
	return r.Reader.Read(b)
}

// proxyFuzzConn isolates lazy parsing and connection state from network timing.
type proxyFuzzConn struct {
	*bytes.Reader
	peer     net.Addr
	deadline time.Time
	closed   bool
}

func (c *proxyFuzzConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *proxyFuzzConn) Close() error                     { c.closed = true; return nil }
func (c *proxyFuzzConn) LocalAddr() net.Addr              { return c.peer }
func (c *proxyFuzzConn) RemoteAddr() net.Addr             { return c.peer }
func (c *proxyFuzzConn) SetDeadline(time.Time) error      { return nil }
func (c *proxyFuzzConn) SetWriteDeadline(time.Time) error { return nil }
func (c *proxyFuzzConn) SetReadDeadline(deadline time.Time) error {
	c.deadline = deadline
	return nil
}

// FuzzProxyV2Wire checks arbitrary wire data, including nested lengths, against
// framing, fragmentation, and lazy-connection invariants.
func FuzzProxyV2Wire(f *testing.F) {
	f.Add([]byte{}, byte(0))
	f.Add([]byte("PROXY TCP4 192.0.2.1 192.0.2.2 1 443\r\n"), byte(63))
	for _, family := range []byte{0, 1, 0x10, 0x11, 0x12, 0x21, 0x22, 0x31, 0x32, 0xff} {
		for _, length := range []int{0, 1, 11, 12, 13, 35, 36, 216} {
			frame := proxyFuzzFrame(0x21, family, make([]byte, length))
			f.Add(frame, byte(length))
		}
	}
	for _, command := range []byte{0, 0x11, 0x20, 0x21, 0x22, 0xff} {
		f.Add(proxyFuzzFrame(command, 0xff, []byte{0, 1, 2}), byte(1))
	}
	ipv4 := proxyTestHeader("192.0.2.7", 65535)
	ipv6 := proxyTestHeader("2001:db8::7", 0)
	mappedPayload := append([]byte{}, net.ParseIP("::ffff:192.0.2.7").To16()...)
	mappedPayload = append(mappedPayload, make([]byte, 16)...)
	mappedPayload = binary.BigEndian.AppendUint16(mappedPayload, 0)
	mappedPayload = binary.BigEndian.AppendUint16(mappedPayload, 443)
	mapped := proxyFuzzFrame(0x21, 0x21, mappedPayload)
	for _, frame := range [][]byte{ipv4, ipv6, mapped} {
		for length := range frame {
			f.Add(frame[:length], byte(0))
		}
		f.Add(append(append([]byte{}, frame...), ipv4...), byte(31))
	}
	// Cover the largest payload and TLV length independently of mutation.
	large := make([]byte, 65535)
	f.Add(proxyFuzzFrame(0x21, 0x11, large), byte(63))
	f.Add(proxyFuzzFrame(0x20, 0xff, large), byte(63))
	binary.BigEndian.PutUint16(large[13:15], uint16(len(large)-15))
	f.Add(proxyFuzzFrame(0x21, 0x11, large), byte(63))
	binary.BigEndian.PutUint16(large[13:15], 65535)
	f.Add(proxyFuzzFrame(0x21, 0x11, large), byte(63))

	f.Fuzz(checkProxyV2Wire)
}

func checkProxyV2Wire(t *testing.T, data []byte, fragment byte) {
	peer := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
	bulk := bytes.NewReader(data)
	addr, err := readProxyV2(bulk, peer)
	chunked := bytes.NewReader(data)
	other, otherErr := readProxyV2(proxyFuzzReader{chunked, int(fragment%64) + 1}, peer)
	if (err == nil) != (otherErr == nil) || bulk.Len() != chunked.Len() {
		t.Fatal("fragmentation changed acceptance or consumed bytes")
	}
	if err == nil {
		if !bytes.Equal(data[:12], []byte("\r\n\r\n\x00\r\nQUIT\n")) ||
			(data[12] != 0x20 && data[12] != 0x21) {

			t.Fatal("invalid signature, version, or command was accepted")
		}
		if data[12] == 0x21 && (data[13]>>4 > 3 || data[13]&0xf > 2) {
			t.Fatal("invalid family or transport was accepted")
		}
		tcp, ok := addr.(*net.TCPAddr)
		if !ok || tcp.IP == nil || tcp.Port < 0 || tcp.Port > 65535 {
			t.Fatal("successful parse returned invalid client metadata")
		}
		if addr.String() != other.String() {
			t.Fatal("fragmentation changed client metadata")
		}
		consumed := 16 + int(binary.BigEndian.Uint16(data[14:16]))
		if len(data)-bulk.Len() != consumed {
			t.Fatal("successful parse consumed application bytes or left header bytes")
		}
		if data[12] == 0x20 || data[13] != 0x11 && data[13] != 0x21 {
			if addr != peer {
				t.Fatal("LOCAL or unsupported transport replaced the actual peer")
			}
		}
	} else if addr != nil || other != nil {
		t.Fatal("failed parse returned usable client metadata")
	}
	if len(data) >= 16 && len(data)-bulk.Len() > 16+int(binary.BigEndian.Uint16(data[14:16])) {
		t.Fatal("parser read beyond the advertised frame")
	}

	checkProxyV2LazyConn(t, data, peer, addr, err, bulk.Len())
}

func checkProxyV2LazyConn(t *testing.T, data []byte, peer, addr net.Addr,
	parseErr error, remaining int) {

	transport := &proxyFuzzConn{Reader: bytes.NewReader(data), peer: peer}
	conn := &proxyProtocolConn{Conn: transport, peer: peer, timeout: time.Second}
	ctx := proxyProtocolContext(context.Background(), conn)
	if transport.Len() != len(data) || ctx.Value(proxyProtocolPeerKey{}) != peer {
		t.Fatal("connection context parsed data or lost the actual peer")
	}
	remote := conn.RemoteAddr()
	position := transport.Len()
	if conn.RemoteAddr().String() != remote.String() || transport.Len() != position {
		t.Fatal("RemoteAddr parsed the preamble more than once")
	}
	var next [1]byte
	n, readErr := conn.Read(next[:])
	if parseErr != nil {
		if n != 0 || readErr == nil || !transport.closed || remote != peer {
			t.Fatal("failed preamble leaked application data, identity, or a connection")
		}
	} else {
		if remote.String() != addr.String() || transport.closed || !transport.deadline.IsZero() {
			t.Fatal("valid preamble changed identity, closed the connection, or retained its deadline")
		}
		if remaining > 0 && (n != 1 || readErr != nil || next[0] != data[len(data)-remaining]) {
			t.Fatal("lazy read did not begin at the application boundary")
		}
	}
}

// proxyV2FrameProperty generates valid frames with known source identity rather
// than requiring arbitrary-byte mutation to discover valid address/TLV layouts.
func proxyV2FrameProperty(t *rapid.T) {
	family := rapid.SampledFrom([]byte{0x11, 0x21, 0x12, 0x22, 0x31, 0x32}).Draw(t, "family")
	addressLen := 4
	if family>>4 == 2 {
		addressLen = 16
	}
	if family>>4 == 3 {
		addressLen = 108
	}
	source := rapid.SliceOfN(rapid.Byte(), addressLen, addressLen).Draw(t, "source")
	destination := rapid.SliceOfN(rapid.Byte(), addressLen, addressLen).Draw(t, "destination")
	sourcePort := rapid.Uint16().Draw(t, "source port")
	destinationPort := rapid.Uint16().Draw(t, "destination port")
	payload := append(append([]byte{}, source...), destination...)
	if addressLen != 108 {
		payload = binary.BigEndian.AppendUint16(payload, sourcePort)
		payload = binary.BigEndian.AppendUint16(payload, destinationPort)
	}
	lastTLV := -1
	for count := rapid.IntRange(0, 8).Draw(t, "TLV count"); count > 0; count-- {
		lastTLV = len(payload)
		value := rapid.SliceOfN(rapid.Byte(), 0, 512).Draw(t, "TLV value")
		payload = append(payload, rapid.Byte().Draw(t, "TLV type"))
		payload = binary.BigEndian.AppendUint16(payload, uint16(len(value)))
		payload = append(payload, value...)
	}
	frame := proxyFuzzFrame(0x21, family, payload)
	peer := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
	var want net.Addr = peer
	if family == 0x11 || family == 0x21 {
		want = &net.TCPAddr{IP: net.IP(source), Port: int(sourcePort)}
	}
	suffix := rapid.SliceOfN(rapid.Byte(), 0, 128).Draw(t, "application bytes")
	reader := bytes.NewReader(append(append([]byte{}, frame...), suffix...))
	got, err := readProxyV2(proxyFuzzReader{reader, rapid.IntRange(1, 64).Draw(t, "fragment")}, peer)
	if err != nil || got.String() != want.String() {
		t.Fatalf("valid frame: got %v, %v; want %v", got, err, want)
	}
	remaining, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(remaining, suffix) {
		t.Fatal("valid frame changed application bytes")
	}
	cut := rapid.IntRange(0, len(frame)-1).Draw(t, "truncation")
	if _, err := readProxyV2(bytes.NewReader(frame[:cut]), peer); err == nil {
		t.Fatal("truncated declared frame was accepted")
	}
	if lastTLV >= 0 {
		bad := append([]byte{}, frame...)
		// Increase the final value length by one without extending the frame.
		binary.BigEndian.PutUint16(bad[16+lastTLV+1:], uint16(len(payload)-lastTLV-2))
		if _, err := readProxyV2(bytes.NewReader(bad), peer); err == nil {
			t.Fatal("TLV extending beyond the preamble was accepted")
		}
	}

	// LOCAL treats every payload byte and family as opaque, even when the
	// same bytes cannot represent valid addresses or TLVs.
	local := proxyFuzzFrame(0x20, rapid.Byte().Draw(t, "LOCAL family"), payload)
	got, err = readProxyV2(bytes.NewReader(local), peer)
	if err != nil || got != peer {
		t.Fatal("LOCAL did not preserve the actual peer")
	}
}

func TestProxyProtocolFrameProperties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, proxyV2FrameProperty)
}

func FuzzProxyV2Frames(f *testing.F) {
	for _, value := range []byte{0, 1, 127, 255} {
		f.Add(bytes.Repeat([]byte{value}, 4096))
	}
	f.Fuzz(rapid.MakeFuzz(proxyV2FrameProperty))
}

// FuzzProxyProtocolNetworks covers config parsing and admission against a
// separate net/netip prefix-membership oracle, including mapped IPv4 addresses.
func FuzzProxyProtocolNetworks(f *testing.F) {
	for _, input := range []string{
		"", "localhost", "127.0.0.1", "0.0.0.0/0", "192.0.2.7/32",
		"192.0.2.7/24", "192.0.2.7/024", "::1", "::/0", "2001:db8::1/128",
		"::ffff:192.0.2.1", "::ffff:192.0.2.0/120", "fe80::1%lo0",
		"192.0.2.1/33", "::1/129", "127.0.0.1\x00", " 127.0.0.1",
	} {
		f.Add(input, []byte{192, 0, 2, 1})
		f.Add(input, []byte(net.ParseIP("::ffff:192.0.2.1").To16()))
	}
	f.Fuzz(func(t *testing.T, input string, rawPeer []byte) {
		cfg := &ProxyProtocolConfig{
			Enabled: true, TrustedProxies: []string{input}, HeaderTimeout: time.Second,
		}
		wantPrefix, validConfig := proxyFuzzInputPrefix(input)
		networks, err := proxyProtocolNetworks(cfg)
		if (err == nil) != validConfig {
			t.Fatalf("config acceptance differs from input prefix: %q, %v", input, err)
		}
		if err != nil {
			if len(networks) != 0 {
				t.Fatal("invalid allowlist produced usable networks")
			}
			return
		}
		if len(networks) != 1 {
			t.Fatal("one trusted address did not produce one network")
		}
		prefix, err := netip.ParsePrefix(networks[0].String())
		if err != nil {
			t.Fatalf("accepted network is not a valid prefix: %v", err)
		}
		if prefix != wantPrefix {
			t.Fatalf("allowlist changed input prefix: %q: got %v, want %v", input, prefix, wantPrefix)
		}
		candidate, valid := netip.AddrFromSlice(rawPeer)
		want := valid && prefix.Contains(candidate.Unmap())
		got := trustedProxy(&net.TCPAddr{IP: net.IP(rawPeer)}, networks)
		if got != want {
			t.Fatalf("admission mismatch: prefix %v, peer %x: got %v, want %v", prefix, rawPeer, got, want)
		}
		if trustedProxy(&net.UnixAddr{Name: input, Net: "unix"}, networks) {
			t.Fatal("non-TCP peer admitted by IP allowlist")
		}
	})
}

// proxyFuzzInputPrefix derives the intended subnet from the input, independently
// of the receiver's net.IPNet. net.ParseCIDR accepts zero-padded prefix lengths,
// while netip.ParsePrefix requires canonical decimal lengths.
func proxyFuzzInputPrefix(input string) (netip.Prefix, bool) {
	if ip, err := netip.ParseAddr(input); err == nil && ip.Zone() == "" {
		ip = ip.Unmap()
		return netip.PrefixFrom(ip, ip.BitLen()), true
	}
	address, bits, found := strings.Cut(input, "/")
	if !found || bits == "" {
		return netip.Prefix{}, false
	}
	bits = strings.TrimLeft(bits, "0")
	if bits == "" {
		bits = "0"
	}
	prefix, err := netip.ParsePrefix(address + "/" + bits)
	if err != nil {
		return netip.Prefix{}, false
	}
	prefix = prefix.Masked()
	if prefix.Addr().Is4In6() {
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
	}
	return prefix, true
}
