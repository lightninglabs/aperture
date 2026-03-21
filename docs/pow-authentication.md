# L402 Proof-of-Work Authentication

## Overview

Aperture supports an alternative authentication method based on proof-of-work
(PoW) instead of Lightning payments. This allows services to gate access behind
a computational cost rather than a monetary one, which is useful for rate
limiting, spam prevention, or environments where Lightning infrastructure is
unavailable.

PoW authentication is configured per service. A single Aperture instance can
serve both Lightning-authenticated and PoW-authenticated services
simultaneously.

## How It Works

The PoW authentication flow follows the same L402 protocol structure as
Lightning authentication, but replaces the payment step with a hash
computation.

### Protocol Flow

```
Client                                    Aperture                        Backend
  |                                          |                               |
  |  1. GET /api/resource                    |                               |
  |----------------------------------------->|                               |
  |                                          |                               |
  |  2. 402 Payment Required                 |                               |
  |     WWW-Authenticate: L402               |                               |
  |       macaroon="<base64>",               |                               |
  |       pow="<difficulty>"                 |                               |
  |<-----------------------------------------|                               |
  |                                          |                               |
  |  3. Client solves PoW:                   |                               |
  |     SHA256(tokenID || nonce) has         |                               |
  |     <difficulty> leading zero bits       |                               |
  |                                          |                               |
  |  4. Client adds pow caveat to macaroon   |                               |
  |     and retries:                         |                               |
  |     Authorization: L402 <mac>:POW        |                               |
  |----------------------------------------->|                               |
  |                                          |  5. Forward authenticated     |
  |                                          |     request                   |
  |                                          |------------------------------->|
  |                                          |                               |
  |                                          |  6. Response                   |
  |                                          |<-------------------------------|
  |  7. Response                             |                               |
  |<-----------------------------------------|                               |
```

### Step-by-Step Details

1. **Client requests a protected resource.** No credentials are attached.

2. **Aperture returns a 402 with a PoW challenge.** The `WWW-Authenticate`
   header contains:
   - A freshly minted macaroon (base64-encoded) that authorizes access to
     the requested service.
   - A `pow` parameter specifying the required difficulty (number of leading
     zero bits).

3. **Client solves the PoW challenge.** The client extracts the `tokenID`
   from the macaroon's identifier and iterates nonces until it finds one
   where `SHA256(tokenID || nonce)` has the required number of leading zero
   bits. The nonce is a big-endian encoded `uint64` (8 bytes).

4. **Client retries with the solved token.** The client adds a first-party
   caveat to the macaroon encoding the solution (`pow=<difficulty>:<nonce_hex>`)
   and sends it in the `Authorization` header using the sentinel `:POW`
   instead of a preimage.

5. **Aperture verifies the token.** Aperture checks:
   - The macaroon HMAC chain is valid (it was minted by this Aperture instance).
   - The service caveat authorizes the requested service.
   - The PoW caveat contains a valid nonce for the token ID and difficulty.

6. **Request is forwarded** to the backend service on success.

## Configuration

To enable PoW authentication for a service, set `authmethod` to `"POW"` and
specify a `powdifficulty` value:

```yaml
services:
  - name: "my-pow-service"
    hostregexp: '^api\.example\.com$'
    pathregexp: '^/v1/.*$'
    address: "localhost:8082"
    protocol: https
    auth: "on"
    authmethod: "POW"
    powdifficulty: 20
```

### Configuration Fields

| Field | Type | Description |
|-------|------|-------------|
| `authmethod` | string | Set to `"POW"` to enable PoW authentication. Default is `"lightning"`. |
| `powdifficulty` | uint32 | Number of leading zero bits required in the SHA256 hash. Must be > 0 when `authmethod` is `"POW"`. |

### Choosing a Difficulty

The difficulty parameter controls how much computation is required to solve the
challenge. Each additional bit roughly doubles the expected computation time.

| Difficulty | Expected Hashes | Approximate Time (single core) |
|------------|-----------------|-------------------------------|
| 8          | 256             | < 1 ms                        |
| 16         | 65,536          | ~1 ms                         |
| 20         | ~1 million      | ~10 ms                        |
| 24         | ~16 million     | ~100 ms                       |
| 28         | ~268 million    | ~1 s                          |
| 32         | ~4 billion      | ~15 s                         |

Times are rough estimates and vary by hardware. Start with a low difficulty
(16-20) and increase based on your abuse prevention needs.

### Mixed Configuration

A single Aperture instance can serve both Lightning and PoW services:

```yaml
services:
  - name: "paid-service"
    hostregexp: '^paid\.example\.com$'
    address: "localhost:8081"
    protocol: https
    auth: "on"
    # authmethod defaults to "lightning"
    price: 100

  - name: "pow-service"
    hostregexp: '^free\.example\.com$'
    address: "localhost:8082"
    protocol: https
    auth: "on"
    authmethod: "POW"
    powdifficulty: 20
```

Lightning services require an LND connection as usual. PoW services do not
interact with LND at all.

## Wire Formats

### Challenge Header (Server to Client)

```
WWW-Authenticate: L402 macaroon="<base64-encoded-macaroon>", pow="<difficulty>"
```

Example:
```
WWW-Authenticate: L402 macaroon="AgEEbHNhdA...", pow="20"
```

### Authorization Header (Client to Server)

```
Authorization: L402 <base64-encoded-macaroon-with-pow-caveat>:POW
```

The `:POW` sentinel replaces the 64-character hex preimage used in Lightning
L402 tokens. The macaroon itself contains the PoW solution as a first-party
caveat.

### PoW Caveat Format

The PoW solution is embedded in the macaroon as a first-party caveat:

```
pow=<difficulty>:<16-char-hex-nonce>
```

Example:
```
pow=20:a1b2c3d4e5f60708
```

- `difficulty`: The number of leading zero bits (decimal integer).
- `nonce`: The 8-byte solution nonce encoded as 16 hex characters (big-endian).

### gRPC Metadata

For gRPC clients, the solved macaroon is sent via the `macaroon` metadata key
(hex-encoded), identical to the Lightning L402 flow. The PoW challenge is
delivered in the `WWW-Authenticate` trailer metadata.

## Hash Function

The PoW verification uses:

```
SHA256(tokenID || nonce)
```

Where:
- `tokenID` is the 32-byte token identifier from the macaroon's binary
  identifier.
- `nonce` is the 8-byte big-endian encoding of a `uint64`.
- The result must have at least `difficulty` leading zero bits.

## Client Integration

### gRPC Client Interceptor

The `l402` package provides `NewPoWInterceptor` for gRPC clients that
automatically handles PoW challenges:

```go
import "github.com/lightninglabs/aperture/l402"

store, err := l402.NewFileStore("/path/to/token/dir")
if err != nil {
    log.Fatal(err)
}

interceptor := l402.NewPoWInterceptor(
    store,
    30*time.Second, // RPC call timeout
    false,          // allowInsecure
)

conn, err := grpc.Dial(
    "api.example.com:443",
    grpc.WithUnaryInterceptor(interceptor.UnaryInterceptor),
    grpc.WithStreamInterceptor(interceptor.StreamInterceptor),
)
```

The interceptor:
1. Checks the token store for an existing valid token.
2. If no token exists and a 402 is returned, parses the PoW challenge.
3. Solves the PoW locally (no LND connection needed).
4. Stores the solved token for reuse on subsequent requests.
5. Retries the original request with the solved token.

### Manual HTTP Client

For non-gRPC clients, the flow can be implemented manually:

```go
import "github.com/lightninglabs/aperture/l402"

// 1. Parse the WWW-Authenticate header from the 402 response.
// 2. Decode the base64 macaroon.
// 3. Extract the tokenID from the macaroon identifier.
id, err := l402.DecodeIdentifier(bytes.NewReader(mac.Id()))

// 4. Solve the PoW.
nonce, err := l402.SolvePoW(id.TokenID, difficulty)

// 5. Add the PoW caveat to the macaroon.
solvedMac := mac.Clone()
l402.AddFirstPartyCaveats(solvedMac, l402.NewCaveat(
    l402.CondPoW,
    l402.FormatPoWCaveatValue(difficulty, nonce),
))

// 6. Encode and send in the Authorization header.
macBytes, _ := solvedMac.MarshalBinary()
authValue := fmt.Sprintf("L402 %s:POW",
    base64.StdEncoding.EncodeToString(macBytes))
req.Header.Set("Authorization", authValue)
```

## Token Persistence

PoW tokens are stored using the same `l402.Store` interface as Lightning
tokens. The `FileStore` implementation serializes PoW fields (`PowNonce` and
`PowDifficulty`) after the existing Lightning fields. This is backwards
compatible: older tokens without PoW fields are deserialized with zero values
for these fields.

PoW tokens are never in a "pending" state since the PoW is solved instantly
(unlike Lightning payments which may be in-flight). A stored PoW token is
immediately reusable on subsequent requests without re-solving the challenge.

## Rate Limiting

PoW-authenticated requests are rate-limited by their L402 token ID, just like
Lightning-authenticated requests. The `ExtractRateLimitKey` function in the
proxy uses `MacaroonFromHeader` which supports both the `:POW` sentinel and
the standard 64-character hex preimage format.

## Architecture

### Package Layout

```
l402/
  pow.go                 # SolvePoW, VerifyPoW, NewPoWSatisfier, FormatPoWCaveatValue
  pow_interceptor.go     # PoWClientInterceptor (gRPC client-side)
  header.go              # MacaroonFromHeader (macaroon-only extraction)
  token.go               # PowNonce, PowDifficulty fields, SolvedMacaroon()

challenger/
  pow.go                 # PoWChallenger (mint.Challenger implementation)

mint/
  mint.go                # PoWVerificationParams, VerifyL402PoW()

auth/
  pow_authenticator.go   # PoWAuthenticator (auth.Authenticator implementation)
  interface.go           # Minter.VerifyL402PoW (interface extension)

proxy/
  service.go             # AuthMethod, PowDifficulty fields, IsPoW()
  proxy.go               # Dual authenticator dispatch (lnAuthenticator/powAuthenticator)
  ratelimiter.go         # MacaroonFromHeader for both LN and PoW tokens

aperture.go              # Wiring: creates PoW challenger + authenticator when needed
```

### Server-Side Verification Flow

```
Request arrives at Proxy.ServeHTTP
    |
    v
Match service by host/path regex
    |
    v
Select authenticator: lnAuthenticator or powAuthenticator
    |                  (based on service.IsPoW())
    v
PoWAuthenticator.Accept()
    |
    +-- l402.MacaroonFromHeader()     Extract macaroon from Authorization header
    |
    +-- mint.VerifyL402PoW()
         |
         +-- DecodeIdentifier()       Get tokenID from macaroon
         +-- GetSecret()              Retrieve HMAC root key
         +-- VerifySignature()        Verify macaroon HMAC chain
         +-- VerifyCaveats()          Check services, timeout, and PoW caveats
              |
              +-- NewServicesSatisfier()   Verify target service is authorized
              +-- NewTimeoutSatisfier()    Verify token hasn't expired
              +-- NewPoWSatisfier()        Verify SHA256(tokenID||nonce) has
                                           required leading zero bits
```

## Security Considerations

- **Difficulty selection**: The difficulty should be high enough to make
  mass-generation of tokens expensive, but low enough that legitimate clients
  can solve challenges quickly. Monitor solve times and adjust as needed.

- **Token reuse**: Solved PoW tokens are reusable. The computational cost is
  paid once per token, not per request. Use timeout caveats (the `timeout`
  service config field) to force periodic re-authentication.

- **No monetary cost**: PoW tokens have no monetary value. They are suitable
  for spam prevention and rate limiting, not for paid API access. Use Lightning
  authentication for services that require payment.

- **Random payment hash**: The macaroon identifier contains a random 32-byte
  value in the payment hash field (since there is no real Lightning invoice).
  This ensures each token has a unique identifier but the hash has no
  cryptographic relationship to any payment.
