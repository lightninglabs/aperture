package netutil

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClientIP checks which address is attributed to a request for each mix
// of peer and X-Forwarded-For header.
func TestClientIP(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{
		"127.0.0.1", "10.0.0.0/8", "fd00::/8",
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		peer string
		xff  []string
		want string
	}{
		{
			name: "no header",
			peer: "203.0.113.9:1000",
			want: "203.0.113.9",
		},
		{
			name: "untrusted peer ignores forged header",
			peer: "203.0.113.9:1000",
			xff:  []string{"198.51.100.1"},
			want: "203.0.113.9",
		},
		{
			name: "trusted peer takes the appended client",
			peer: "127.0.0.1:1000",
			xff:  []string{"1.1.1.1, 2.2.2.2, 198.51.100.7"},
			want: "198.51.100.7",
		},
		{
			name: "trusted peer single entry",
			peer: "127.0.0.1:1000",
			xff:  []string{"198.51.100.7"},
			want: "198.51.100.7",
		},
		{
			name: "trusted hops are skipped",
			peer: "127.0.0.1:1000",
			xff:  []string{"9.9.9.9, 198.51.100.7, 10.1.2.3, 10.4.5.6"},
			want: "198.51.100.7",
		},
		{
			name: "multiple header lines are one list",
			peer: "127.0.0.1:1000",
			xff:  []string{"9.9.9.9", "198.51.100.7, 10.1.2.3"},
			want: "198.51.100.7",
		},
		{
			name: "whitespace is trimmed",
			peer: "127.0.0.1:1000",
			xff:  []string{"  9.9.9.9 ,\t198.51.100.7  "},
			want: "198.51.100.7",
		},
		{
			name: "trusted peer without header",
			peer: "127.0.0.1:1000",
			want: "127.0.0.1",
		},
		{
			name: "garbage entries fall back to the peer",
			peer: "127.0.0.1:1000",
			xff:  []string{"unknown, , not-an-ip, 1.2.3"},
			want: "127.0.0.1",
		},
		{
			name: "only trusted entries fall back to the peer",
			peer: "127.0.0.1:1000",
			xff:  []string{"10.1.1.1, 127.0.0.1"},
			want: "127.0.0.1",
		},
		{
			name: "garbage is skipped for a valid entry to its left",
			peer: "127.0.0.1:1000",
			xff:  []string{"198.51.100.7, garbage"},
			want: "198.51.100.7",
		},
		{
			name: "ipv6 client",
			peer: "127.0.0.1:1000",
			xff:  []string{"1.1.1.1, 2001:db8::7"},
			want: "2001:db8::7",
		},
		{
			name: "ipv6 trusted peer and hop",
			peer: "[fd00::1]:1000",
			xff:  []string{"2001:db8::7, fd00::2"},
			want: "2001:db8::7",
		},
		{
			name: "ipv6 untrusted peer ignores header",
			peer: "[2001:db8::5]:1000",
			xff:  []string{"1.1.1.1"},
			want: "2001:db8::5",
		},
		{
			name: "bad peer address",
			peer: "garbage",
			xff:  []string{"1.1.1.1"},
			want: "0.0.0.0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = test.peer
			for _, line := range test.xff {
				r.Header.Add("X-Forwarded-For", line)
			}

			got := ClientIP(r, trusted)
			require.True(t, net.ParseIP(test.want).Equal(got),
				"got %v, want %v", got, test.want)
		})
	}
}

// TestClientIPNoTrustedProxies checks that with nothing trusted, which is the
// default, the header is never read, even from a loopback peer.
func TestClientIPNoTrustedProxies(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:1000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")

	require.True(t, net.ParseIP("127.0.0.1").Equal(ClientIP(r, nil)))
}

// TestParseTrustedProxies checks CIDR and bare address parsing, including
// that a bare address matches only itself.
func TestParseTrustedProxies(t *testing.T) {
	nets, err := ParseTrustedProxies([]string{
		" 127.0.0.1 ", "::1", "10.0.0.0/8", "2001:db8::/32",
	})
	require.NoError(t, err)
	require.Len(t, nets, 4)

	require.True(t, containsIP(nets, net.ParseIP("127.0.0.1")))
	require.False(t, containsIP(nets, net.ParseIP("127.0.0.2")))
	require.True(t, containsIP(nets, net.ParseIP("::1")))
	require.True(t, containsIP(nets, net.ParseIP("10.255.0.1")))
	require.True(t, containsIP(nets, net.ParseIP("2001:db8:1::1")))
	require.False(t, containsIP(nets, net.ParseIP("11.0.0.1")))

	for _, bad := range []string{
		"", "nope", "10.0.0.0/33", "10.0.0.0/", "1.2.3", "::1/129",
		"1.2.3.4:80",
	} {
		_, err := ParseTrustedProxies([]string{"127.0.0.1", bad})
		require.Error(t, err, bad)
	}

	nets, err = ParseTrustedProxies(nil)
	require.NoError(t, err)
	require.Empty(t, nets)
}
