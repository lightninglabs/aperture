#!/bin/bash
# manual_pay_l402.sh
#
# Manual verification of the full L402 payment flow through Prism.
# Uses your running prism on :8080 (per sample-conf-tmp.yaml) and drives
# the payment with *bob* (the second LND node from
# /Users/blake/work/nagara/code/chain/loka-payment/lnd/scripts/itest_sui_single_coin.sh).
# After payment, replays the request with the LSAT token and dumps
# what appears in Prism's admin API so you can see the transaction
# recorded — visible at https://127.0.0.1:8080/api/admin/transactions
# (or in the dashboard if you build prism with `make build-withdashboard`).
#
# Prerequisites:
#   1. Alice's LND running on :10009 (prism's authenticator)
#   2. Bob's LND running on :10010
#   3. An open channel Alice ↔ Bob with enough capacity (itest script opens this)
#   4. Prism running: `prism --configfile=./sample-conf-tmp.yaml`
#
# Usage:
#   ./scripts/manual_pay_l402.sh               # default: service1
#   SERVICE_HOST=foo.com PATH_SUFFIX=/bar ./scripts/manual_pay_l402.sh
#   PRISM_BASEDIR=/abs/path ./scripts/manual_pay_l402.sh

set -euo pipefail

# --- Configuration -------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PRISM_HOST="${PRISM_HOST:-127.0.0.1:8080}"
PRISM_BASEDIR="${PRISM_BASEDIR:-$REPO_DIR/.prism}"
SERVICE_HOST="${SERVICE_HOST:-service1.com}"
PATH_SUFFIX="${PATH_SUFFIX:-/probe}"

NETWORK="${NETWORK:-devnet}"
ALICE_DIR="${ALICE_DIR:-/tmp/lnd-sui-test/alice}"
BOB_DIR="${BOB_DIR:-/tmp/lnd-sui-test/bob}"
ALICE_RPC="${ALICE_RPC:-127.0.0.1:10009}"
BOB_RPC="${BOB_RPC:-127.0.0.1:10010}"

LND_REPO="${LND_REPO:-/Users/blake/work/nagara/code/chain/loka-payment/lnd}"
LNCLI="${LNCLI:-$LND_REPO/lncli-debug}"

# Colors
if [ -t 1 ]; then
    G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; C=$'\033[36m'; N=$'\033[0m'
else
    G=""; R=""; Y=""; C=""; N=""
fi

step() { echo; echo "${C}━━━ $* ━━━${N}"; }
pass() { echo "  ${G}✓${N} $*"; }
warn() { echo "  ${Y}~${N} $*"; }
fail() { echo "  ${R}✗${N} $*"; exit 1; }

# lncli wrappers. We pass --lnddir (not --network/--chain) because lncli's
# hard-coded network whitelist doesn't include "devnet"; using lnddir makes
# lncli resolve paths from the daemon's own state dir instead.
alice_cli() {
    "$LNCLI" --lnddir="$ALICE_DIR" --rpcserver="$ALICE_RPC" \
        --macaroonpath="$ALICE_DIR/data/chain/sui/$NETWORK/admin.macaroon" \
        "$@"
}
bob_cli() {
    "$LNCLI" --lnddir="$BOB_DIR" --rpcserver="$BOB_RPC" \
        --macaroonpath="$BOB_DIR/data/chain/sui/$NETWORK/admin.macaroon" \
        "$@"
}

# --- 1. Preflight --------------------------------------------------------

step "[1/7] Preflight"

for dep in curl jq xxd nc; do
    command -v "$dep" >/dev/null 2>&1 || fail "missing dependency: $dep"
done
[ -x "$LNCLI" ] || fail "lncli not found at $LNCLI (set LNCLI=...)"

nc -z "${PRISM_HOST%:*}" "${PRISM_HOST##*:}" \
    || fail "prism not listening on $PRISM_HOST — start it with: prism --configfile=./sample-conf-tmp.yaml"
nc -z "${ALICE_RPC%:*}" "${ALICE_RPC##*:}" \
    || fail "alice LND not reachable at $ALICE_RPC"
nc -z "${BOB_RPC%:*}" "${BOB_RPC##*:}" \
    || fail "bob LND not reachable at $BOB_RPC — did itest_sui_single_coin.sh finish?"
pass "prism, alice, bob all reachable"

ADMIN_MAC="$PRISM_BASEDIR/admin.macaroon"
[ -f "$ADMIN_MAC" ] \
    || fail "admin macaroon not found at $ADMIN_MAC — adjust PRISM_BASEDIR"
ADMIN_MAC_HEX=$(xxd -ps -c 10000 "$ADMIN_MAC")
pass "admin macaroon loaded (${#ADMIN_MAC_HEX} hex chars)"

# Quick channel sanity (best-effort — bob→alice route)
channels=$(bob_cli listchannels 2>/dev/null || echo '{"channels":[]}')
active=$(echo "$channels" | jq '[.channels[] | select(.active==true)] | length' 2>/dev/null || echo 0)
if [ "$active" -gt 0 ]; then
    pass "bob has $active active channel(s)"
else
    warn "bob has no active channels — payment step may fail. Run itest_sui_single_coin.sh first."
fi

curl_admin() {
    curl -sk -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" "$@"
}

# --- 2. Unauthenticated request -----------------------------------------

step "[2/7] Unauthenticated GET https://$PRISM_HOST$PATH_SUFFIX  (Host: $SERVICE_HOST)"

HDR=$(mktemp); BODY=$(mktemp)
trap 'rm -f "$HDR" "$BODY"' EXIT

code=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    "https://$PRISM_HOST$PATH_SUFFIX")

echo "  HTTP $code"
if [ "$code" = "200" ]; then
    warn "Got 200 immediately. This service is probably whitelisted, price=0, or"
    warn "the Host header doesn't match any protected service. Inspect your config."
    echo "  body: $(head -c 200 "$BODY")"
    echo ""
    echo "Hint: the sample config's service1 has price:0. To force a 402, edit"
    echo "sample-conf-tmp.yaml and set a non-zero price, then restart prism."
    exit 0
fi
[ "$code" = "402" ] || fail "expected 402, got $code. body: $(head -c 200 "$BODY")"
pass "prism challenged with 402"

www=$(grep -i '^www-authenticate:' "$HDR" | head -1)
[ -n "$www" ] || fail "no WWW-Authenticate header. Headers: $(cat "$HDR")"
echo "  $www" | fold -s -w 100 | sed 's/^/    /'

# --- 3. Parse LSAT challenge --------------------------------------------

step "[3/7] Extract macaroon + invoice"

mac=$(echo "$www" | sed -n 's/.*macaroon="\([^"]*\)".*/\1/p')
inv=$(echo "$www" | sed -n 's/.*invoice="\([^"]*\)".*/\1/p')

[ -n "$mac" ] || fail "could not parse macaroon from: $www"
[ -n "$inv" ] || fail "could not parse invoice from: $www"
pass "macaroon: ${#mac} base64 chars"
pass "invoice:  ${inv:0:40}..."

# --- 4. Decode invoice --------------------------------------------------

step "[4/7] Decode invoice (via alice)"

decoded=$(alice_cli decodepayreq "$inv" 2>&1) \
    || fail "decodepayreq failed: $decoded"
echo "$decoded" | jq '{
    amount_sats: .num_satoshis,
    payment_hash: .payment_hash,
    description: .description,
    expiry_sec: .expiry,
    destination: .destination
}' | sed 's/^/    /'

amt=$(echo "$decoded" | jq -r '.num_satoshis')
phash=$(echo "$decoded" | jq -r '.payment_hash')

# Render the cost in the chain's natural unit so the operator sees what
# they're about to spend. prism's admin GetInfo tells us which chain lnd
# is running on.
chain=$(curl -sk -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" \
    "https://$PRISM_HOST/api/admin/info" | jq -r '.chain // ""')
if [ "$chain" = "sui" ]; then
    echo "    → bob will pay $amt MIST ($(python3 -c "print($amt/1e9)") SUI)"
else
    echo "    → bob will pay $amt sats"
fi

# --- 5. Pay with bob ----------------------------------------------------

step "[5/7] Pay the invoice from bob"

pay_json=$(bob_cli payinvoice --force --json "$inv" 2>&1) || {
    echo "$pay_json" | sed 's/^/    /'
    fail "bob payinvoice failed — is there a routable channel alice←bob with capacity ≥ $amt sats?"
}

# lncli sometimes returns multiple JSON objects (streaming status updates).
# Take the last one (the terminal status).
final=$(echo "$pay_json" | jq -s '.[-1]' 2>/dev/null) \
    || final="$pay_json"

preimage=$(echo "$final" | jq -r '.payment_preimage // empty')
status=$(echo "$final" | jq -r '.status // .payment_error // empty')

if [ -z "$preimage" ] || [ "$preimage" = "null" ]; then
    echo "$final" | sed 's/^/    /'
    fail "no payment_preimage returned — status: $status"
fi

echo "    amount:    $amt sats"
echo "    status:    $status"
echo "    preimage:  $preimage"
pass "payment settled"

# --- 6. Replay with LSAT token ------------------------------------------

step "[6/7] Replay request with LSAT token"

# Give prism a moment to process the SubscribeInvoices settlement event.
sleep 2

code2=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    -H "Authorization: LSAT $mac:$preimage" \
    "https://$PRISM_HOST$PATH_SUFFIX")

echo "  HTTP $code2"
case "$code2" in
    401|402)
        echo "  body: $(head -c 200 "$BODY")"
        fail "auth rejected — did the preimage match the invoice's payment hash?"
        ;;
    200|201|204)
        pass "backend reached through prism — auth succeeded"
        ;;
    *)
        # Any non-auth-error status means LSAT validation passed; the
        # backend just responded oddly (dummy endpoints like Alice's gRPC
        # return 415 to HTTP requests, down services return 502/503, etc.)
        pass "LSAT auth succeeded (backend returned $code2 — expected for dummy backends)"
        ;;
esac

# --- 7. Inspect admin API -----------------------------------------------

step "[7/7] What prism recorded"

echo "${C}Transactions (last 5):${N}"
curl_admin "https://$PRISM_HOST/api/admin/transactions?limit=5" \
    | jq '.transactions // . | .[:5]' 2>/dev/null \
    | sed 's/^/    /' || echo "    (none or endpoint unavailable)"

echo
echo "${C}Tokens (last 5):${N}"
curl_admin "https://$PRISM_HOST/api/admin/tokens?limit=5" \
    | jq '.tokens // . | .[:5]' 2>/dev/null \
    | sed 's/^/    /' || echo "    (none)"

echo
echo "${C}Revenue stats:${N}"
curl_admin "https://$PRISM_HOST/api/admin/stats" \
    | jq . 2>/dev/null \
    | sed 's/^/    /' || echo "    (unavailable)"

echo
echo "${G}━━━ Done ━━━${N}"
echo "Inspect the admin API anytime:"
echo "  curl -sk -H \"Grpc-Metadata-Macaroon: \$(xxd -ps -c 10000 $ADMIN_MAC)\" \\"
echo "    https://$PRISM_HOST/api/admin/transactions | jq ."
echo
echo "For a web UI, rebuild with the dashboard embedded:"
echo "  (cd dashboard && npm ci) && make build-withdashboard"
echo "Then https://$PRISM_HOST/ will serve the Next.js dashboard."
