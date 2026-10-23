# PROXY protocol v2

Aperture can receive [PROXY protocol v2][spec] connections from a trusted TCP
load balancer. The load balancer sends a binary preamble before the TLS
handshake or plaintext HTTP request. Aperture uses the advertised source address
and port for request logging, the IP blocklist, freebie tracking, and IP-based
rate limiting.

[spec]: https://www.haproxy.org/download/2.9/doc/proxy-protocol.txt

## Configuration

The receiver is disabled by default. To enable it on the public listener:

```yaml
listenaddr: "127.0.0.1:8081"
proxyprotocol:
  enabled: true
  trustedproxies:
    - "127.0.0.1"
  headertimeout: 5s
```

`trustedproxies` accepts individual IPv4/IPv6 addresses and CIDRs. Configure the
actual TCP source addresses of the load balancer, including any NAT between it
and Aperture. Aperture checks that transport peer before reading a preamble.
Connections from other peers are closed. The allowlist must contain at least
one entry, and `headertimeout` must be positive.

The equivalent flags are `--proxyprotocol.enabled`, repeatable
`--proxyprotocol.trustedproxy=127.0.0.1`, and
`--proxyprotocol.headertimeout=5s`.

When enabled, every connection to `listenaddr` must start with PROXY v2. Direct
HTTP/TLS and PROXY v1 connections are rejected. Configure firewall or security
group rules so only the intended senders can reach this port. Any allowed
sender can assert a client address; the protocol does not authenticate that
address. Keep the list narrow.

TLS still terminates at Aperture when `insecure` is false. HTTP/1.1, HTTP/2,
and gRPC retain their existing TLS and ALPN behavior. With `insecure: true`,
Aperture accepts plaintext HTTP/1.1 and h2c after the preamble. The Tor listener,
profiling endpoint, and Prometheus listener do not use this receiver.

## HAProxy example

For HAProxy and Aperture on the same host, use the Aperture configuration above
and forward TCP connections with:

```haproxy
frontend aperture_public
    bind :443
    mode tcp
    default_backend aperture_nodes

backend aperture_nodes
    mode tcp
    server aperture 127.0.0.1:8081 send-proxy-v2 check
```

HAProxy passes TLS through to Aperture in this example. The `send-proxy-v2`
setting also applies to its normal server health checks. If a check uses an
explicit port or address override, configure `check-send-proxy` as well. HTTP
health checks must target an endpoint appropriate for the deployment and send
the preamble before HTTP or TLS. PROXY v2 `LOCAL` health checks retain the
load balancer's actual peer address.

## Local gateways and dashboard

The admin and HashMail REST gateways connect back to Aperture's public listener.
They send a `LOCAL` preamble when the receiver is enabled. Include their actual
local TCP source address in `trustedproxies`; for a listener on loopback this
is normally `127.0.0.1` or `::1`. If the listener is bound to another interface,
include the source address used to reach that interface.

The embedded dashboard and its credential-injecting `/api/proxy/` route require
a direct loopback connection. They reject all connections received through
PROXY protocol, including `LOCAL` and headers advertising a loopback client.
When the receiver is enabled, use the authenticated admin API or `aperturecli`
through the load balancer instead. The server-side REST gateway still verifies
the caller's admin macaroon through the gRPC interceptors.

## Header handling

Aperture accepts TCP source addresses for IPv4 and IPv6. `LOCAL`, unspecified
addresses/transports, and supported wire encodings for datagram or UNIX
connections retain the actual TCP peer address. Optional TLVs are consumed
without using them as application identity or TLS authentication information.

The wire length bounds the preamble payload to 65,535 bytes. The header timeout
covers the complete preamble and is independent of HTTP request timeouts.
Malformed or incomplete headers close the connection. Reading a preamble occurs
in a per-connection server goroutine, so a stalled sender does not block the
listener from accepting other connections.

Aperture sends ordinary HTTP/gRPC to backend services. This receiver does not
emit PROXY headers on backend connections.
