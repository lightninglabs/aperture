---
name: aperture
description: Manage Aperture L402 reverse proxy via aperturecli. Use when creating/updating/deleting services, checking transactions, managing tokens, viewing stats, or changing pricing on an Aperture instance.
---

# Aperture CLI Skill

Manage an Aperture L402 reverse proxy using `aperturecli`, the admin CLI and
MCP server.

## Quick Reference

| Action | Command |
|--------|---------|
| Server info | `aperturecli --insecure info` |
| Health check | `aperturecli --insecure health` |
| List services | `aperturecli --insecure services list` |
| Create service | `aperturecli --insecure services create --name <name> --address <addr> --price <sats>` |
| Update price | `aperturecli --insecure services update --name <name> --price <sats>` |
| Delete service | `aperturecli --insecure services delete --name <name>` |
| List transactions | `aperturecli --insecure transactions list` |
| List tokens | `aperturecli --insecure tokens list` |
| Revoke token | `aperturecli --insecure tokens revoke --token-id <id>` |
| Revenue stats | `aperturecli --insecure stats` |
| CLI schema | `aperturecli schema --all` |
| Start MCP server | `aperturecli --insecure mcp serve` |

## Connection Flags

All commands accept these connection flags:

```bash
--host localhost:8081          # gRPC endpoint (default)
--macaroon ~/.aperture/admin.macaroon  # Auth credential (default)
--tls-cert /path/to/tls.cert  # TLS certificate
--insecure                     # Skip TLS (dev mode)
--timeout 30s                  # RPC timeout (default)
```

## Dynamic Pricing

To change a service's price without affecting other fields:

```bash
aperturecli --insecure services update --name myapi --price 500
```

Only flags that are explicitly passed are updated. This enables targeted
changes to pricing, address, protocol, auth, or routing patterns.

## Dry-Run Mode

Preview mutating operations without executing them:

```bash
aperturecli --insecure --dry-run services create \
  --name test --address 127.0.0.1:8080 --price 100
```

Outputs the request JSON and exits with code 10 (no mutation).

## Output Modes

- **Agent mode** (default when piped): JSON output, structured errors on stderr.
- **Human mode** (default in TTY): Tables and formatted text.
- Override: `--json` or `--human`.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Invalid arguments |
| 3 | Connection error / timeout |
| 4 | Auth failure |
| 5 | Not found |
| 10 | Dry-run passed |

## MCP Server

Start the MCP server for agent framework integration:

```bash
aperturecli --insecure mcp serve
```

This exposes all admin RPCs as typed MCP tools over stdio JSON-RPC:
`get_info`, `get_health`, `list_services`, `create_service`,
`update_service`, `delete_service`, `list_transactions`, `list_tokens`,
`revoke_token`, `get_stats`.

### Claude Code MCP Config

```json
{
  "mcpServers": {
    "aperture": {
      "command": "aperturecli",
      "args": ["--insecure", "mcp", "serve"]
    }
  }
}
```

## Service Creation Example

```bash
# Create a service gating an API behind Lightning payments:
aperturecli --insecure services create \
  --name weather-api \
  --address 10.0.0.5:8080 \
  --protocol http \
  --host-regexp '^weather\.example\.com$' \
  --path-regexp '^/api/.*$' \
  --price 50 \
  --auth on

# Verify it was created:
aperturecli --insecure services list

# Check revenue:
aperturecli --insecure stats --from 2026-01-01T00:00:00Z
```

## Filtering Transactions

```bash
# Filter by service and state:
aperturecli --insecure transactions list \
  --service weather-api \
  --state settled \
  --limit 100

# Filter by date range:
aperturecli --insecure transactions list \
  --from 2026-03-01T00:00:00Z \
  --to 2026-03-31T23:59:59Z
```
