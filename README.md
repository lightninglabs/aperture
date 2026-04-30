# Loka Prism L402 — Agentic Paywall Proxy

[![Website](https://img.shields.io/badge/website-lokachain.org-blue.svg)](https://lokachain.org/)
[![Twitter](https://img.shields.io/badge/twitter-@lokachain-1DA1F2.svg)](https://x.com/lokachain)
[![Status](https://img.shields.io/badge/status-Active-success.svg)](https://github.com/loka-network)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

> **The HTTP paywall layer of the Loka agentic payment stack.** A reverse
> proxy that turns any gRPC / REST backend into an L402-metered API, settled
> over Loka's Sui-adapted Lightning Network.

Loka Prism L402 is a production-ready HTTP 402 reverse proxy that mints,
verifies, and redeems [L402 (Lightning HTTP 402)][l402] tokens in front of
arbitrary backend services. Clients — humans or autonomous AI agents — pay a
Lightning invoice to obtain a macaroon-based token, which then authorizes
subsequent API calls. The proxy handles pricing, invoice generation, token
issuance, rate limiting, and revenue accounting, so your backend only has to
serve requests.

This is a fork of [lightninglabs/aperture][upstream], adapted for the **Loka
agentic payment ecosystem**: Sui-native settlement via
[`loka-p2p-lnd`](https://github.com/loka-network/loka-p2p-lnd), per-agent
wallet isolation via
[`agents-pay-service`](https://github.com/loka-network/agents-pay-service),
and an admin API / dashboard / CLI / MCP surface designed for programmatic
use by AI agents.

[l402]: https://github.com/lightninglabs/L402
[upstream]: https://github.com/lightninglabs/aperture

---

## Position in the Loka Stack

```text
┌──────────────────────────────────────────────────────────────┐
│                      AI Agents (humans too)                  │
└───────────────┬──────────────────────────┬───────────────────┘
                │ HTTP / gRPC              │ REST API
                │ + L402 / MPP token       │ (per-wallet key)
                ▼                          ▼
┌──────────────────────────┐   ┌────────────────────────────────┐
│  Loka Prism L402         │   │  Agents-Pay-Service            │
│  (this repo)             │   │  (LNbits fork, per-agent       │
│                          │   │   wallet + API key isolation)  │
│  • L402 / MPP paywalls   │   │  • BOLT11 / LNURL invoices     │
│  • Reverse proxy         │   │  • SUI / MIST denomination     │
│  • Admin API / dashboard │   │  • Extensions (orders, tpos)   │
└────────────┬─────────────┘   └────────────────┬───────────────┘
             │ invoice / settle                  │ funding source
             └──────────────┬───────────────────┘
                            ▼
            ┌──────────────────────────────────┐
            │  Loka P2P Lightning Node (LND)   │
            │  • BOLT-compliant HTLC routing   │
            │  • Sui adapter (suinotify,       │
            │    suiwallet, sui_estimator)     │
            │  • Move-enforced channel state   │
            │  • Setu backend (upcoming)       │
            └──────────────────────────────────┘
```

| Repo | Role |
|------|------|
| **[loka-prism-l402](.)** _(you are here)_ | L402 paywall proxy — the request-level metering layer in front of your APIs |
| **[loka-p2p-lnd](https://github.com/loka-network/loka-p2p-lnd)** | Lightning Network Daemon, adapted to run channels on Sui (and Setu, upcoming) |
| **[agents-pay-service](https://github.com/loka-network/agents-pay-service)** | LNbits-based per-agent wallet service — each AI agent gets an isolated API key and balance |

An AI agent calls a metered API; the agent's wallet lives in
`agents-pay-service`; Prism gates the request with an L402 challenge; the
agent's wallet pays the invoice via `loka-p2p-lnd`; Prism verifies the
preimage and forwards the request. One payment rail, three purpose-built
services.

---

## What Prism Does

**L402 (Lightning HTTP 402) paywalls.** Prism intercepts incoming HTTP/gRPC
requests, and if the client doesn't yet hold a valid token, it responds with
`402 Payment Required` plus a Lightning invoice. Once the client pays, it
presents a macaroon-based token on subsequent requests.

**Payment HTTP Auth scheme (MPP).** Alongside classic L402, Prism supports
the newer MPP scheme and prepaid sessions (deposit → top-up → close), which
remove per-request invoice overhead for agents that make many calls. Enable
via `authenticator.enablempp: true` and `enablesessions: true`.

**Reverse proxy.** Configurable per-service routing by host/path regex,
pricing (static or via a dynamic pricer gRPC), whitelist paths, and
per-endpoint token-bucket rate limits.

**Admin API + dashboard + CLI + MCP.** Manage services, inspect
transactions, revoke tokens, and query revenue stats — from a browser, a
shell, or an AI agent over MCP.

**Pluggable storage.** SQLite (default), PostgreSQL, or etcd (etcd does not
support the admin transaction store).

---

## Installation / Setup

**Prerequisites**

* Go `1.25` or later.
* A running Loka LND node (see
  [loka-p2p-lnd](https://github.com/loka-network/loka-p2p-lnd)), reachable
  over gRPC. TLS cert and admin macaroon paths go into `authenticator.tlspath`
  and `authenticator.macdir`.
* Port `8081` (or your chosen `listenaddr`) reachable from clients.
* A valid `tls.cert` / `tls.key` pair in your `basedir` (auto-generated
  self-signed if missing).

**Build**

```bash
make build                 # produces ./prism (proxy daemon) and ./prismcli (admin CLI)
make install               # installs to $GOPATH/bin
make build-withdashboard   # includes embedded Next.js dashboard
```

**Run**

```bash
# Using a custom config file:
prism --configfile=/path/to/aperture.yaml

# Or with all config in ~/.aperture/aperture.yaml (default):
prism
```

Compare your config against [`sample-conf.yaml`](sample-conf.yaml) — every
option is documented inline. Paths in `configfile`, `basedir`,
`sqlite.dbfile`, `admin.macaroonpath`, `authenticator.tlspath`, and
`authenticator.macdir` all accept `~`, `$VAR`, absolute, and CWD-relative
forms.

If Prism is behind a TLS-terminating load balancer / ingress, make sure its
ALPN policy advertises `h2` (on AWS NLB use `HTTP2Preferred` or
`HTTP2Only`) — gRPC clients will otherwise fail with
`missing selected ALPN property`.

---

## Admin API

Prism ships with an optional gRPC + REST admin API for runtime service
management, transaction history, and revenue monitoring. Enable by adding:

```yaml
admin:
  enabled: true
  macaroonpath: "~/.aperture/admin.macaroon"  # default
```

On first start Prism generates a 32-byte root key and a macaroon at the
configured path. All admin endpoints except `GetHealth` require this macaroon
(hex-encoded in `Grpc-Metadata-Macaroon` header or gRPC metadata).

| RPC | REST | Description |
|-----|------|-------------|
| `GetHealth` | `GET /api/admin/health` | Health check (no auth) |
| `GetInfo` | `GET /api/admin/info` | Server info: network, listen addr, TLS, MPP config |
| `ListServices` | `GET /api/admin/services` | List proxied backend services |
| `CreateService` | `POST /api/admin/services` | Register a new service |
| `UpdateService` | `PUT /api/admin/services/{name}` | Update service (partial) |
| `DeleteService` | `DELETE /api/admin/services/{name}` | Remove a service |
| `ListTransactions` | `GET /api/admin/transactions` | Query L402 transactions |
| `ListTokens` | `GET /api/admin/tokens` | List issued tokens |
| `RevokeToken` | `DELETE /api/admin/tokens/{token_id}` | Revoke a token |
| `GetStats` | `GET /api/admin/stats` | Revenue stats, per-service breakdown |

Services created through the admin API are persisted and survive restarts.
The proxy's routing table is updated in-place — pricing changes or backend
swaps take effect immediately with no downtime.

See [docs/admin-api.md](docs/admin-api.md) for full details.

---

## Dashboard

Built with `make build-withdashboard`, Prism embeds a Next.js web dashboard
served at the root path. It provides a visual interface for the admin API:
service management, transaction history with filtering / pagination, revenue
charts, and token administration.

The dashboard talks to the admin API through a server-side proxy that
injects the macaroon automatically. Access is restricted to loopback for
security.

See [docs/dashboard.md](docs/dashboard.md).

---

## CLI (`prismcli`)

A standalone command-line tool for the admin gRPC API. Designed to work
well for **both humans and AI agents** — tables when stdout is a TTY, JSON
when piped; semantic exit codes for scripting.

```bash
prismcli --insecure health
prismcli --insecure services list
prismcli --insecure services create --name myapi --address 127.0.0.1:8080 --price 100
prismcli --insecure services update --name myapi --price 500
prismcli --insecure stats
prismcli schema --all            # dumps full command tree as JSON
prismcli --dry-run services delete --name myapi
```

See [docs/cli.md](docs/cli.md).

### MCP Server

`prismcli` embeds an MCP (Model Context Protocol) server that exposes
every admin RPC as a typed tool over stdio JSON-RPC. Agent frameworks like
Claude Code can manage Prism directly:

```json
{
  "mcpServers": {
    "prism": {
      "command": "prismcli",
      "args": ["--insecure", "mcp", "serve"]
    }
  }
}
```

See [docs/mcp-server.md](docs/mcp-server.md).

---

## Rate Limiting

Per-endpoint token-bucket rate limiting, keyed on L402 token ID (or IP for
unauthenticated requests).

```yaml
services:
  - name: "myservice"
    hostregexp: "api.example.com"
    address: "127.0.0.1:8080"
    protocol: https

    ratelimits:
      - requests: 100            # global: 100/s per client
        per: 1s
        burst: 100
      - pathregexp: '^/api/v1/expensive.*$'   # stricter per path
        requests: 5
        per: 1m
        burst: 5
```

Multiple rules layer — a request is rejected if **any** matching rule
denies it. Responses are protocol-aware: HTTP 429 with `Retry-After` for
REST, gRPC `ResourceExhausted` for gRPC.

| Option | Description | Required |
|--------|-------------|----------|
| `pathregexp` | Regex to match request paths. Matches all paths if omitted. | No |
| `requests` | Requests allowed per time window. | Yes |
| `per` | Time window (`1s`, `1m`, `1h`, …). | Yes |
| `burst` | Max burst capacity. Defaults to `requests`. | No |

---

## Attribution

Prism is a downstream fork of [Lightning Labs' Aperture][upstream], extended
for the Loka ecosystem. Upstream routing, L402 token logic, and protocol
implementations remain compatible; Loka additions focus on the admin API,
dashboard, CLI, MCP server, rate limiting, MPP session support, and
integration with the Sui-adapted Lightning backend.

The L402 protocol itself is developed by Lightning Labs — see
[the L402 spec][l402].
