# MCP Server

`aperturecli` includes an embedded MCP (Model Context Protocol) server that
exposes all admin API operations as typed tools over stdio JSON-RPC. This
enables direct integration with AI agent frameworks like Claude Code.

## Starting the Server

```bash
aperturecli --insecure mcp serve
```

The server uses the same connection flags as the CLI (`--host`, `--macaroon`,
`--tls-cert`, `--insecure`). It communicates over stdin/stdout using the MCP
JSON-RPC protocol.

## Claude Code Integration

Add to your Claude Code MCP configuration (`.claude/settings.json` or
project-level):

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

For production with TLS:

```json
{
  "mcpServers": {
    "aperture": {
      "command": "aperturecli",
      "args": [
        "--host", "aperture.example.com:8081",
        "--macaroon", "/path/to/admin.macaroon",
        "--tls-cert", "/path/to/tls.cert",
        "mcp", "serve"
      ]
    }
  }
}
```

## Available Tools

| Tool | Description |
|------|-------------|
| `get_info` | Get server information (network, listen address, TLS status) |
| `get_health` | Check server health |
| `list_services` | List all configured backend services |
| `create_service` | Create a new backend service with pricing and auth |
| `update_service` | Update an existing service (e.g. change price, address) |
| `delete_service` | Delete a backend service by name |
| `list_transactions` | List L402 transactions with optional filters |
| `list_tokens` | List all issued L402 tokens |
| `revoke_token` | Revoke an L402 token by ID |
| `get_stats` | Get revenue statistics with date range and per-service breakdown |

## Tool Parameters

Tool input schemas are automatically inferred from Go struct tags. Required
fields are marked in the JSON Schema. Example for `create_service`:

```json
{
  "name": "myapi",
  "address": "127.0.0.1:8080",
  "protocol": "http",
  "price": 100,
  "auth": "on"
}
```

The `update_service` tool accepts an optional `price` field (pointer type),
so omitting it leaves the current price unchanged — enabling targeted updates
like dynamic pricing changes.

## Example Agent Interaction

An agent can manage Aperture services programmatically:

```
Agent: "List all services"
→ calls list_services tool
← returns JSON with service configs

Agent: "Update the pricing for myapi to 500 sats"
→ calls update_service with {"name": "myapi", "price": 500}
← returns updated service config

Agent: "Show revenue stats for March"
→ calls get_stats with {"from": "2026-03-01T00:00:00Z", "to": "2026-03-31T23:59:59Z"}
← returns total revenue, transaction count, per-service breakdown
```
