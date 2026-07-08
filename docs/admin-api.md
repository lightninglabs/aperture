# Admin API

Aperture includes an optional admin gRPC and REST API for managing services,
viewing transaction history, and monitoring revenue. When enabled, a web
dashboard is also served at the root path.

## Enabling

Add the `admin` section to your `aperture.yaml`:

```yaml
admin:
  enabled: true
  macaroonpath: "/path/to/admin.macaroon"  # optional, defaults to ~/.aperture/admin.macaroon
  corsorigin:                               # optional, for cross-origin browser access
    - "http://localhost:3000"
```

Requirements:
- **Database backend**: Must be `sqlite` or `postgres` (etcd does not support
  the transaction store needed by the admin API).
- **Authenticator**: An LND or LNC connection is required for invoice creation
  and payment tracking.

On first startup, aperture generates a random 32-byte root key and an admin
macaroon. The macaroon file is written to the configured `macaroonpath` (or
`~/.aperture/admin.macaroon` by default). The root key is stored alongside it
at `admin.rootkey`.

## Authentication

All admin API endpoints except `GetHealth` require a macaroon. For REST
requests, pass it as a hex-encoded header:

```bash
ADMIN_MAC=$(xxd -ps -c 1000 /path/to/admin.macaroon)
curl -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" http://localhost:8081/api/admin/info
```

For gRPC, attach the macaroon as metadata with key `macaroon`.

## REST Endpoints

The admin REST API is served under the `/api/admin/` prefix via gRPC-gateway.

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/api/admin/health` | No | Health check, returns `{"status": "ok"}` |
| GET | `/api/admin/info` | Yes | Server info (network, listen address, insecure flag, MPP config) |
| GET | `/api/admin/services` | Yes | List all configured proxy services |
| POST | `/api/admin/services` | Yes | Create a new service |
| PUT | `/api/admin/services/{name}` | Yes | Update an existing service (partial update) |
| DELETE | `/api/admin/services/{name}` | Yes | Delete a service |
| GET | `/api/admin/transactions` | Yes | List L402 transactions (paginated, filterable) |
| GET | `/api/admin/tokens` | Yes | List active L402 tokens (settled transactions) |
| DELETE | `/api/admin/tokens/{token_id}` | Yes | Revoke an L402 token |
| GET | `/api/admin/stats` | Yes | Revenue statistics with optional date range |

## Service Management

### Create a Service

```bash
curl -X POST \
  -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-api",
    "address": "127.0.0.1:8080",
    "protocol": "http",
    "host_regexp": ".*",
    "path_regexp": "^/api/v1/.*",
    "price": 100,
    "auth": "on"
  }' \
  http://localhost:8081/api/admin/services
```

**Fields**:

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `name` | Yes | — | Unique service name |
| `address` | Yes | — | Backend host:port |
| `protocol` | No | `http` | `http` or `https` |
| `host_regexp` | No | `.*` | Regex matching request Host header |
| `path_regexp` | No | — | Regex matching request URL path. **Must not match reserved paths** (`/api/admin/`, `/api/proxy/`, `/_next/`). |
| `price` | No | 0 | Price in satoshis per request |
| `auth` | No | `""` | Auth level: `on`, `off`, or `freebie N` (N free requests per IP) |
| `auth_scheme` | No | `AUTH_SCHEME_L402` | Payment auth scheme: `AUTH_SCHEME_L402` (0), `AUTH_SCHEME_MPP` (1), or `AUTH_SCHEME_L402_MPP` (2) |

### Update a Service

Partial updates — only provided fields are changed:

```bash
curl -X PUT \
  -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  -H "Content-Type: application/json" \
  -d '{"price": 250}' \
  http://localhost:8081/api/admin/services/my-api
```

### Delete a Service

```bash
curl -X DELETE \
  -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  http://localhost:8081/api/admin/services/my-api
```

Services created or modified via the API are persisted to the database and
survive restarts. They take precedence over services defined in the config
file (matched by name).

## Transactions

### List Transactions

```bash
curl -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  "http://localhost:8081/api/admin/transactions?limit=20&offset=0&service=my-api&state=settled"
```

**Query Parameters**:

| Param | Description |
|-------|-------------|
| `limit` | Max results (1–1000, default 50) |
| `offset` | Pagination offset |
| `service` | Filter by service name |
| `state` | Filter by state: `pending` or `settled` |
| `start_date` | Start of date range (RFC 3339) |
| `end_date` | End of date range (RFC 3339) |

### Revoke a Token

```bash
TOKEN_ID=$(curl -s -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  http://localhost:8081/api/admin/tokens | jq -r '.tokens[0].token_id')

curl -X DELETE \
  -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  "http://localhost:8081/api/admin/tokens/$TOKEN_ID"
```

Revoking a token deletes the transaction record and revokes the underlying
secret, forcing the client to obtain a new L402 on their next request.

## Statistics

```bash
# All-time stats
curl -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  http://localhost:8081/api/admin/stats

# Date-filtered stats
curl -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  "http://localhost:8081/api/admin/stats?from=2026-03-01T00:00:00Z&to=2026-03-31T23:59:59Z"
```

Response:

```json
{
  "total_revenue_sats": "2450",
  "transaction_count": "17",
  "service_breakdown": [
    {"service_name": "echo-api", "total_revenue_sats": "1200"},
    {"service_name": "premium-api", "total_revenue_sats": "1250"}
  ]
}
```

## gRPC

The admin gRPC service is defined in `adminrpc/admin.proto`. Connect to the
same listen address as the REST API. The gRPC endpoint prefix is
`/adminrpc.Admin/`.

```bash
# Using grpcurl
grpcurl -plaintext \
  -H "macaroon: $(xxd -ps -c 1000 admin.macaroon)" \
  localhost:8081 adminrpc.Admin/GetInfo
```

## MPP (Payment HTTP Authentication Scheme)

When `enablempp: true` is set in the authenticator config, Aperture supports
the Payment HTTP Authentication Scheme alongside L402. The `GetInfo` endpoint
reports MPP availability:

```json
{
  "network": "regtest",
  "listen_addr": "localhost:8081",
  "insecure": true,
  "mpp_enabled": true,
  "sessions_enabled": false,
  "mpp_realm": "localhost:8081"
}
```

### Per-Service Auth Scheme

Each service can be configured with a specific auth scheme via the
`auth_scheme` enum field:

| Value | Enum Name | Description |
|-------|-----------|-------------|
| 0 | `AUTH_SCHEME_L402` | L402 only (default, backwards compatible) |
| 1 | `AUTH_SCHEME_MPP` | MPP Payment scheme only |
| 2 | `AUTH_SCHEME_L402_MPP` | Both L402 and MPP (client chooses) |

Example — create an MPP-only service:

```bash
curl -X POST \
  -H "Grpc-Metadata-Macaroon: $ADMIN_MAC" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "mpp-api",
    "address": "127.0.0.1:8080",
    "path_regexp": "^/api/mpp/.*",
    "price": 50,
    "auth": "on",
    "auth_scheme": 1
  }' \
  http://localhost:8081/api/admin/services
```

When `auth_scheme` is `AUTH_SCHEME_L402_MPP`, the 402 response includes both
`WWW-Authenticate: L402` and `WWW-Authenticate: Payment` headers, and the
response body uses RFC 9457 Problem Details JSON.

## Security

- **Macaroon auth**: All endpoints except `GetHealth` require a valid admin
  macaroon.
- **Reserved path protection**: Services cannot be created with `path_regexp`
  patterns that match `/api/admin/`, `/api/proxy/`, or `/_next/` to prevent
  hijacking internal traffic.
- **CORS**: Configurable via `admin.corsorigin` in the config file. If not
  set, cross-origin requests are blocked.
