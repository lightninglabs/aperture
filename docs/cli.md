# prismcli

`prismcli` is a standalone command-line interface for the Loka Prism L402
admin gRPC API. It provides full CRUD management of backend services,
transaction queries, token management, revenue statistics, and an embedded
MCP server for AI agent integration.

## Installation

```bash
# From source (includes version injection):
make install

# Or directly:
go install github.com/lightninglabs/aperture/cmd/prismcli@latest
```

## Quick Start

```bash
# Check server health (insecure mode for local dev):
prismcli --insecure health

# List all services:
prismcli --insecure services list

# Create a new service with pricing:
prismcli --insecure services create \
  --name myapi \
  --address 127.0.0.1:8080 \
  --protocol http \
  --price 100 \
  --auth on

# Dynamically change pricing:
prismcli --insecure services update --name myapi --price 500

# Preview a change without executing:
prismcli --insecure --dry-run services delete --name myapi
```

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `localhost:8081` | Prism admin gRPC host:port |
| `--macaroon` | `~/.aperture/admin.macaroon` | Path to admin macaroon file |
| `--tls-cert` | | Path to TLS certificate for server verification |
| `--insecure` | `false` | Skip TLS (plaintext gRPC) |
| `--json` | `false` | Force JSON output |
| `--human` | `false` | Force human-readable output |
| `--dry-run` | `false` | Preview mutating commands without executing |
| `--timeout` | `30s` | RPC call timeout |

## Output Modes

`prismcli` is agent-friendly by default:

- **When stdout is a TTY** (interactive terminal): human-readable tables.
- **When stdout is piped** (agent/script mode): JSON output.
- Override with `--json` or `--human` (mutually exclusive).

Errors are emitted as structured JSON on stderr when in non-TTY mode:

```json
{"error":true,"code":"connection_error","message":"...","exit_code":3}
```

## Exit Codes

| Code | Kind | Meaning |
|------|------|---------|
| 0 | success | Command completed successfully |
| 1 | general_error | Unclassified error |
| 2 | invalid_args | Invalid arguments or validation failure |
| 3 | connection_error | gRPC connection failure or timeout |
| 4 | auth_failure | Macaroon authentication failure |
| 5 | not_found | Requested resource not found |
| 10 | dry_run_passed | Dry-run completed (no action taken) |

## Commands

### `info`
Display server information (network, listen address, TLS status).

### `health`
Health check endpoint. Useful for monitoring and readiness probes.

### `services list`
List all configured backend services with name, address, protocol, price, and
auth level.

### `services create`
Create a new backend service. Required flags: `--name`, `--address`.

### `services update`
Update one or more fields of an existing service. Only flags that are
explicitly provided are changed, enabling targeted updates:

```bash
# Change only the price:
prismcli services update --name myapi --price 500

# Change address and protocol:
prismcli services update --name myapi --address 10.0.0.5:8080 --protocol https
```

### `services delete`
Delete a backend service by name.

### `transactions list`
Query L402 transactions with optional filters:

```bash
prismcli transactions list \
  --service myapi \
  --state settled \
  --from 2026-01-01T00:00:00Z \
  --to 2026-03-25T00:00:00Z \
  --limit 50
```

### `tokens list`
List issued L402 tokens with pagination (`--limit`, `--offset`).

### `tokens revoke`
Revoke a token by ID: `prismcli tokens revoke --token-id <id>`.

### `stats`
Revenue statistics with optional date range and per-service breakdown:

```bash
prismcli stats --from 2026-01-01T00:00:00Z --to 2026-03-25T00:00:00Z
```

### `schema`
Machine-readable CLI introspection for agent discovery:

```bash
# List top-level commands:
prismcli schema

# Full schema tree:
prismcli schema --all

# Schema for a specific command:
prismcli schema services
```

### `version`
Print the build version.

## Dry-Run Mode

All mutating commands support `--dry-run`, which serializes the gRPC request
as JSON without calling the server:

```bash
$ prismcli --dry-run services create --name test --address 127.0.0.1:8080 --price 100
{
  "dry_run": true,
  "rpc": "CreateService",
  "request": { "name": "test", "address": "127.0.0.1:8080", ... }
}
```

The process exits with code 10 (`dry_run_passed`) and does not emit an error.
