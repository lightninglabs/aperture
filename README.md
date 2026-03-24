# L402 (Lightning HTTP 402) API Key proxy

Aperture is your portal to the Lightning-Native Web. Aperture is used in
production today by [Lightning Loop](https://lightning.engineering/loop), a
non-custodial on/off ramp for the Lightning Network.

Aperture is a HTTP 402 reverse proxy that supports proxying requests for gRPC
(HTTP/2) and REST (HTTP/1 and HTTP/2) backends using the [L402 Protocol
Standard][l402]. L402 is short for: the Lightning HTTP 402
protocol.  L402 combines HTTP 402, macaroons, and the Lightning Network to
create a new standard for authentication and paid services on the web.

L402 is a new standard protocol for authentication and paid APIs developed by
Lightning Labs. L402 API keys can serve both as authentication, as well as a
payment mechanism (one can view it as a ticket) for paid APIs. In order to
obtain a token, we require the user to pay us over Lightning in order to obtain
a preimage, which itself is a cryptographic component of the final L402 token

The implementation of the authentication token is chosen to be macaroons, as
they allow us to package attributes and capabilities along with the token. This
system allows one to automate pricing on the fly and allows for a number of
novel constructs such as automated tier upgrades. In another light, this can be
viewed as a global HTTP 402 reverse proxy at the load balancing level for web
services and APIs.

[l402]: https://github.com/lightninglabs/L402

## Installation / Setup

**lnd**

* Make sure `lnd` ports are reachable.

**aperture**

* Compilation requires go `1.19.x` or later.
* To build `aperture` in the current directory, run `make build` and then copy the
  file `./aperture` from the local directory to the server.
* To build and install `aperture` directly on the machine it will be used, run the
  `make install` command which will place the binary into your `$GOPATH/bin`
  folder.
* Make sure port `8081` is reachable from outside (or whatever port we choose,
  could also be 443 at some point)
* Make sure there is a valid `tls.cert` and `tls.key` file located in the
  `~/.aperture` directory that is valid for the domain that aperture is running on.
  Aperture doesn't support creating its own certificate through Let's Encrypt yet.
  If there is no `tls.cert` and `tls.key` found, a self-signed pair will be
  created.
* If Aperture is behind a TLS-terminating load balancer/ingress, make sure the
  load balancer's ALPN policy advertises `h2` (for example, AWS NLB
  `HTTP2Preferred` or `HTTP2Only`). Some gRPC clients fail with
  `missing selected ALPN property` if no ALPN protocol is negotiated.
  On AWS NLB, the default ALPN policy is `None`, which does not negotiate ALPN.
  If you use TCP passthrough instead of TLS termination at the load balancer,
  Aperture negotiates ALPN directly.
* Make sure all required configuration items are set in `~/.aperture/aperture.yaml`,
  compare with `sample-conf.yaml`.
* Start aperture without any command line parameters (`./aperture`), all configuration
  is done in the `~/.aperture/aperture.yaml` file.

## Rate Limiting

Aperture supports optional per-endpoint rate limiting using a token bucket
algorithm. Rate limits are configured per service and applied based on the
client's L402 token ID for authenticated requests, or IP address for
unauthenticated requests.

### Features

* **Token bucket algorithm**: Allows controlled bursting while maintaining a
  steady-state request rate.
* **Per-client isolation**: Each L402 token ID or IP address has independent
  rate limit buckets.
* **Path-based rules**: Different endpoints can have different rate limits using
  regular expressions.
* **Multiple rules**: All matching rules are evaluated; if any rule denies the
  request, it is rejected. This allows layering global and endpoint-specific
  limits.
* **Protocol-aware responses**: Returns HTTP 429 with `Retry-After` header for
  REST requests, and gRPC `ResourceExhausted` status for gRPC requests.

### Configuration

Rate limits are configured in the `ratelimits` section of each service:

```yaml
services:
  - name: "myservice"
    hostregexp: "api.example.com"
    address: "127.0.0.1:8080"
    protocol: https

    ratelimits:
      # Global rate limit for all endpoints
      - requests: 100    # Requests allowed per time window
        per: 1s          # Time window duration (1s, 1m, 1h, etc.)
        burst: 100       # Max burst capacity (defaults to 'requests')

      # Stricter limit for expensive endpoints
      - pathregexp: '^/api/v1/expensive.*$'
        requests: 5
        per: 1m
        burst: 5
```

This example configures two rate limit rules using a token bucket algorithm. Each
client gets a "bucket" of tokens that refills at the `requests/per` rate, up to the
`burst` capacity. A request consumes one token; if no tokens are available, the
request is rejected. This allows clients to make quick bursts of requests (up to
`burst`) while enforcing a steady-state rate limit over time.

1. **Global limit**: All endpoints are limited to 100 requests per second per client,
   with a burst capacity of 100.
2. **Endpoint-specific limit**: Paths matching `/api/v1/expensive.*` have a stricter
   limit of 5 requests per minute with a burst of 5. Since both rules are evaluated,
   requests to expensive endpoints must satisfy both limits.

### Configuration Options

| Option | Description | Required |
|--------|-------------|----------|
| `pathregexp` | Regular expression to match request paths. If omitted, matches all paths. | No |
| `requests` | Number of requests allowed per time window. | Yes |
| `per` | Time window duration (e.g., `1s`, `1m`, `1h`). | Yes |
| `burst` | Maximum burst size. Defaults to `requests` if not set. | No |
