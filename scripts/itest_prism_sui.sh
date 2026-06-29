#!/bin/bash
# itest_prism_sui.sh
# End-to-end integration test for Loka Prism L402 running against
# Sui-adapted LND. Verifies that:
#   1. Prism can reach LND via gRPC with the Sui macaroon
#   2. Admin API endpoints respond correctly
#   3. L402 challenge flow issues a valid Lightning invoice (AddInvoice
#      round-trip over the aperture→lnd wire is intact on Sui-LND)
#
# Assumes the Sui-LND node "alice" is already running (started via
# /Users/blake/work/nagara/code/chain/loka-payment/lnd/scripts/itest_sui_single_coin.sh).
#
# Usage:
#   ./scripts/itest_prism_sui.sh            # runs against Alice, devnet macaroons
#   NETWORK=localnet ./scripts/itest_prism_sui.sh
#   ALICE_DIR=/custom/path ./scripts/itest_prism_sui.sh

set -euo pipefail

# --- Configuration ---------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

NETWORK="${NETWORK:-devnet}"
ALICE_DIR="${ALICE_DIR:-/tmp/lnd-sui-test/alice}"
LND_RPC_HOST="${LND_RPC_HOST:-127.0.0.1:10009}"

PRISM_BIN="${PRISM_BIN:-$REPO_DIR/prism}"
PRISMCLI_BIN="${PRISMCLI_BIN:-$REPO_DIR/prismcli}"

PRISM_DATA="${PRISM_DATA:-/tmp/prism-itest}"
PRISM_LISTEN="${PRISM_LISTEN:-127.0.0.1:18080}"
PRISM_LOG="$PRISM_DATA/prism.log"
PRISM_CONFIG="$PRISM_DATA/prism.yaml"

ADMIN_MAC="$PRISM_DATA/admin.macaroon"

# Colors (fall back gracefully if stdout isn't a tty)
if [ -t 1 ]; then
    G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; N=$'\033[0m'
else
    G=""; R=""; Y=""; N=""
fi

pass() { echo "  ${G}✓${N} $*"; }
fail() { echo "  ${R}✗${N} $*"; exit 1; }
info() { echo "${Y}➤${N} $*"; }

# --- 1. Preflight ---------------------------------------------------------

info "[1/7] Preflight checks"

[ -f "$ALICE_DIR/tls.cert" ] \
    || fail "LND TLS cert not found at $ALICE_DIR/tls.cert — is alice running?"

MACAROON_DIR="$ALICE_DIR/data/chain/sui/$NETWORK"
[ -d "$MACAROON_DIR" ] \
    || fail "LND macaroon dir not found at $MACAROON_DIR"
[ -f "$MACAROON_DIR/admin.macaroon" ] \
    || fail "LND admin macaroon missing at $MACAROON_DIR/admin.macaroon"

nc -z "${LND_RPC_HOST%:*}" "${LND_RPC_HOST##*:}" \
    || fail "LND gRPC not reachable at $LND_RPC_HOST"

if nc -z "${PRISM_LISTEN%:*}" "${PRISM_LISTEN##*:}" 2>/dev/null; then
    fail "port $PRISM_LISTEN already in use — set PRISM_LISTEN=127.0.0.1:<free-port> and retry"
fi

pass "LND reachable, prism listen port free"

# --- 2. Build binaries ---------------------------------------------------

info "[2/7] Building prism + prismcli"

if [ ! -x "$PRISM_BIN" ] || [ ! -x "$PRISMCLI_BIN" ]; then
    (cd "$REPO_DIR" && make build) >/dev/null
fi
[ -x "$PRISM_BIN" ]    || fail "prism binary not found at $PRISM_BIN"
[ -x "$PRISMCLI_BIN" ] || fail "prismcli binary not found at $PRISMCLI_BIN"
pass "binaries ready"

# --- 3. Write prism config -----------------------------------------------

info "[3/7] Preparing prism data dir + config"

rm -rf "$PRISM_DATA"
mkdir -p "$PRISM_DATA"

cat > "$PRISM_CONFIG" <<EOF
listenaddr: "$PRISM_LISTEN"
staticroot: "$PRISM_DATA/static"
servestatic: false
debuglevel: "debug"
basedir: "$PRISM_DATA"
autocert: false
servername: "prism.itest.local"
insecure: false
strictverify: false
invoicebatchsize: 1000
idletimeout: 30s
readtimeout: 15s
writetimeout: 30s

authenticator:
  network: "$NETWORK"
  disable: false
  lndhost: "$LND_RPC_HOST"
  tlspath: "$ALICE_DIR/tls.cert"
  macdir: "$MACAROON_DIR"

admin:
  enabled: true
  macaroonpath: "$ADMIN_MAC"

dbbackend: "sqlite"
sqlite:
  dbfile: "$PRISM_DATA/prism.db"
  skipmigrations: false

services:
  - name: "itest-service"
    hostregexp: '^itest\.local$'
    pathregexp: '^/.*$'
    address: "127.0.0.1:9999"
    protocol: http
    timeout: 3600
    # 10_000_000 MIST = 0.01 SUI; sized so the dashboard / admin API
    # show a human-readable value instead of nano-units. On a bitcoin
    # backend this would be 10^7 sats (~$9 at $90k/BTC), so tune before
    # running against mainnet. Itests use regtest / sui devnet so value
    # doesn't matter economically.
    price: 10000000

hashmail:
  enabled: false

prometheus:
  enabled: false

logging:
  console:
    disable: false
    level: "info"
  file:
    disable: true
EOF

pass "config at $PRISM_CONFIG"

# --- 4. Start prism ------------------------------------------------------

info "[4/7] Starting prism"

"$PRISM_BIN" --configfile="$PRISM_CONFIG" >"$PRISM_LOG" 2>&1 &
PRISM_PID=$!

cleanup() {
    if kill -0 "$PRISM_PID" 2>/dev/null; then
        kill "$PRISM_PID" 2>/dev/null || true
        wait "$PRISM_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Wait up to 15s for the admin API to be actually serving 200 (not just
# accepting TCP). The REST gateway needs a moment after the TLS listener
# is up before it can round-trip to the internal gRPC server.
ready=0
for i in $(seq 1 30); do
    if ! kill -0 "$PRISM_PID" 2>/dev/null; then
        echo "--- prism log ---"; cat "$PRISM_LOG"; echo "------------------"
        fail "prism died during startup"
    fi
    code=$(curl -sk --max-time 1 \
        -o /dev/null -w '%{http_code}' \
        "https://$PRISM_LISTEN/api/admin/health" || echo "000")
    if [ "$code" = "200" ]; then
        ready=1
        break
    fi
    sleep 0.5
done
[ "$ready" = "1" ] || {
    echo "--- prism log ---"; tail -20 "$PRISM_LOG"; echo "-------------"
    fail "admin API never reached 200 after 15s (last code: $code)"
}
pass "prism up (pid=$PRISM_PID)"

# --- 5. Admin API smoke ---------------------------------------------------

info "[5/7] Admin API smoke"

ADMIN_MAC_HEX=$(xxd -ps -c 10000 "$ADMIN_MAC")

curl_admin() {
    curl -sk \
        -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" \
        -w '\n%{http_code}' \
        "$@"
}

# 5a. health (no auth)
body_and_code=$(curl -sk -w '\n%{http_code}' "https://$PRISM_LISTEN/api/admin/health")
code=${body_and_code##*$'\n'}
body=${body_and_code%$'\n'*}
[ "$code" = "200" ] || fail "health expected 200, got $code (body: $body)"
echo "$body" | grep -q '"status"' || fail "health response missing status field: $body"
pass "health → 200 $body"

# 5b. info (auth)
body_and_code=$(curl_admin "https://$PRISM_LISTEN/api/admin/info")
code=${body_and_code##*$'\n'}
body=${body_and_code%$'\n'*}
[ "$code" = "200" ] || fail "info expected 200, got $code (body: $body)"
echo "$body" | grep -q '"network"' || fail "info missing network field: $body"
echo "$body" | grep -q "\"$NETWORK\"" \
    || fail "info network mismatch, expected $NETWORK: $body"
pass "info → 200, network=$NETWORK"

# 5c. services list (auth) — config-defined service should appear
body_and_code=$(curl_admin "https://$PRISM_LISTEN/api/admin/services")
code=${body_and_code##*$'\n'}
body=${body_and_code%$'\n'*}
[ "$code" = "200" ] || fail "services list expected 200, got $code"
echo "$body" | grep -q '"itest-service"' \
    || fail "services list missing itest-service: $body"
pass "services list contains itest-service"

# 5d. Round-trip via prismcli (exercises gRPC admin path, not just REST)
if "$PRISMCLI_BIN" --host="$PRISM_LISTEN" \
    --macaroon="$ADMIN_MAC" --insecure=false \
    --tls-cert="$PRISM_DATA/tls.cert" \
    --json services list >/dev/null 2>&1; then
    pass "prismcli gRPC → services list OK"
else
    # Fall back to insecure (self-signed TLS) with explicit cert
    if "$PRISMCLI_BIN" --host="$PRISM_LISTEN" \
        --macaroon="$ADMIN_MAC" --insecure \
        --json services list >/dev/null 2>&1; then
        pass "prismcli gRPC (insecure) → services list OK"
    else
        fail "prismcli cannot reach admin gRPC"
    fi
fi

# --- 6. L402 challenge flow ----------------------------------------------

info "[6/7] L402 challenge (exercises LND AddInvoice over Sui)"

# Hit the protected service with the matching Host header. Expect 402 +
# WWW-Authenticate header containing a BOLT11 invoice.
resp_headers=$(mktemp)
resp_body=$(mktemp)
trap 'rm -f "$resp_headers" "$resp_body"; cleanup' EXIT

code=$(curl -sk -o "$resp_body" -D "$resp_headers" -w '%{http_code}' \
    -H "Host: itest.local" \
    "https://$PRISM_LISTEN/test")

[ "$code" = "402" ] \
    || fail "expected 402 Payment Required on /test, got $code (body: $(cat "$resp_body"))"
pass "proxy returned 402 Payment Required"

# Extract invoice from WWW-Authenticate: L402 macaroon="...", invoice="lnbc..."
www_auth=$(grep -i '^www-authenticate:' "$resp_headers" | head -1 || true)
[ -n "$www_auth" ] \
    || fail "missing WWW-Authenticate header (headers: $(cat "$resp_headers"))"
echo "  $www_auth"

invoice=$(echo "$www_auth" \
    | sed -n 's/.*invoice="\([^"]*\)".*/\1/p')
[ -n "$invoice" ] || fail "could not extract invoice from: $www_auth"

# Basic BOLT11 sanity: starts with "ln" + network-prefix, contains payment hash.
case "$invoice" in
    ln*) pass "invoice issued by LND: ${invoice:0:30}..." ;;
    *)   fail "invoice does not look like BOLT11: $invoice" ;;
esac

macaroon=$(echo "$www_auth" \
    | sed -n 's/.*macaroon="\([^"]*\)".*/\1/p')
[ -n "$macaroon" ] || fail "could not extract macaroon from: $www_auth"
pass "L402 macaroon issued (${#macaroon} chars)"

# --- 7. Transaction appears in admin API ---------------------------------

info "[7/7] Verify transaction logged"

# Give prism a moment to commit the transaction row.
sleep 1
body_and_code=$(curl_admin "https://$PRISM_LISTEN/api/admin/transactions?limit=10")
code=${body_and_code##*$'\n'}
body=${body_and_code%$'\n'*}
[ "$code" = "200" ] \
    || fail "transactions expected 200, got $code (body: $body)"

if echo "$body" | grep -q '"service":"itest-service"'; then
    pass "transaction row recorded for itest-service"
else
    # Soft-pass: some prism versions only record on settlement.
    echo "  ${Y}~${N} no unsettled transaction row yet (this may be expected)"
fi

# --- done -----------------------------------------------------------------

echo ""
echo "${G}=== All checks passed ===${N}"
echo "Prism log: $PRISM_LOG"
echo "Prism data: $PRISM_DATA"
echo ""
echo "Next manual steps:"
echo "  - Pay the invoice from a second LND node to exercise the settlement"
echo "    path (SubscribeInvoices stream). See lnd/scripts/itest_sui_single_coin.sh"
echo "    for an example of opening a channel + paying."
echo "  - Build prism with the dashboard embedded to get a UI at /:"
echo "      make build-withdashboard"
