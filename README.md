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
* Make sure all required configuration items are set in `~/.aperture/aperture.yaml`,
  compare with `sample-conf.yaml`.
* Start aperture without any command line parameters (`./aperture`), all configuration
  is done in the `~/.aperture/aperture.yaml` file.

## Per-endpoint rate limiting

Aperture supports per-endpoint rate limiting using a token bucket based on golang.org/x/time/rate.
Limits are configured per service using regular expressions that match request paths.

Key properties:
- Scope: per service, per endpoint (path regex).
- Process local: state is in-memory per Aperture process. In clustered deployments, each instance enforces its own limits.
- Evaluation: all matching rules are enforced; if any matching rule denies a request, the request is rejected.
- Protocols: applies to both REST and gRPC requests.

Behavior on limit exceed:
- HTTP/REST: returns 429 Too Many Requests and sets a Retry-After header (in seconds). Sub-second delays are rounded up to 1 second.
- gRPC: response uses HTTP/2 headers/trailers with Grpc-Status and Grpc-Message indicating the error (message: "rate limit exceeded").
- CORS headers are included consistently.

Configuration fields (under a service):
- pathregex: regular expression matched against the URL path (e.g., "/package.Service/Method").
- requests: allowed number of requests per window.
- per: size of the time window (e.g., 1s, 1m). Default: 1s.
- burst: additional burst capacity. Default: equal to requests.

Example (see sample-conf.yaml for a full example):

```yaml
services:
  - name: "service1"
    hostregexp: '^service1.com$'
    pathregexp: '^/.*$'
    address: "127.0.0.1:10009"
    protocol: https

    # Optional per-endpoint rate limits using a token bucket.
    ratelimits:
      - pathregex: '^/looprpc.SwapServer/LoopOutTerms.*$'
        requests: 5
        per: 1s
        burst: 5
      - pathregex: '^/looprpc.SwapServer/LoopOutQuote.*$'
        requests: 2
        per: 1s
        burst: 2
```

Notes:
- If multiple ratelimits match a request path, all must allow the request; the strictest rule will effectively apply.
- If requests or burst are set to 0 or negative, safe defaults are used (requests defaults to 1; burst defaults to requests).
- If per is omitted or 0, it defaults to 1s.
