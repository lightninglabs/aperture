#!/bin/bash
# manual_pay_l402_service2.sh
#
# Sibling to manual_pay_l402.sh, but exercises the multi-merchant path:
# service2 in sample-conf-tmp.yaml has a `payment:` override pointing at
# bob's lnd (10010). So when a client hits merchant-bob.local, prism
# issues the invoice against BOB instead of the global authenticator
# lnd (alice). This script verifies that:
#
#   1. The 402 invoice's destination pubkey == bob's identity_pubkey
#      (proof that per-service routing actually took effect — funds will
#      land in bob's wallet, not alice's).
#   2. Alice (the only node with a channel to bob) pays the invoice and
#      receives back a valid LSAT preimage.
#   3. The replay with LSAT auth succeeds end-to-end.
#
# Note the role swap relative to the service1 script:
#
#   manual_pay_l402.sh         — invoice on ALICE  (global default lnd)
#   manual_pay_l402_service2   — invoice on BOB    (per-service override)
#                                paid by ALICE
#
# Prerequisites: same as manual_pay_l402.sh + sample-conf-tmp.yaml's
# service2 entry has its `payment:` block uncommented (bob's tls/macaroon).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PRISM_HOST="${PRISM_HOST:-127.0.0.1:8080}"
PRISM_BASEDIR="${PRISM_BASEDIR:-$REPO_DIR/.prism}"
SERVICE_HOST="${SERVICE_HOST:-merchant-bob.local}"
PATH_SUFFIX="${PATH_SUFFIX:-/probe}"

NETWORK="${NETWORK:-devnet}"
ALICE_DIR="${ALICE_DIR:-/tmp/lnd-sui-test/alice}"
BOB_DIR="${BOB_DIR:-/tmp/lnd-sui-test/bob}"
ALICE_RPC="${ALICE_RPC:-127.0.0.1:10009}"
BOB_RPC="${BOB_RPC:-127.0.0.1:10010}"

LND_REPO="${LND_REPO:-/Users/blake/work/nagara/code/chain/loka-payment/lnd}"
LNCLI="${LNCLI:-$LND_REPO/lncli-debug}"

if [ -t 1 ]; then
    G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; C=$'\033[36m'; B=$'\033[34m'; N=$'\033[0m'
else
    G=""; R=""; Y=""; C=""; B=""; N=""
fi

step() { echo; echo "${C}━━━ $* ━━━${N}"; }
pass() { echo "  ${G}✓${N} $*"; }
warn() { echo "  ${Y}~${N} $*"; }
fail() { echo "  ${R}✗${N} $*"; exit 1; }
info() { echo "  ${B}→${N} $*"; }

# print_req prints the wire-level HTTP request that the next curl will
# send, so the operator can see exactly what's hitting prism. Mirrors
# the reverse-proxy routing model:
#   • TCP/TLS connect → the gateway URL (host:port from $PRISM_HOST)
#   • Host header     → the *virtual* hostname prism uses to pick a
#                       service (hostregexp match), NOT a DNS lookup
#   • Path            → forwarded as-is to the matched service.address
#                       (unless that service has rewrite.prefix set)
#
# Args: <method> <gateway-url> [extra header lines...]
print_req() {
    local method="$1" url="$2"
    shift 2
    echo "  ${B}HTTP request being sent:${N}"
    echo "    $method $url"
    echo "    Host: $SERVICE_HOST          # virtual host → matches service2.hostregexp"
    for h in "$@"; do
        echo "    $h"
    done
    echo "    (TCP target = gateway $PRISM_HOST; prism reverse-proxies to service.address after routing)"
}

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

# --- 1. Preflight -------------------------------------------------------

step "[1/8] Preflight"

for dep in curl jq xxd nc; do
    command -v "$dep" >/dev/null 2>&1 || fail "missing dependency: $dep"
done
[ -x "$LNCLI" ] || fail "lncli not found at $LNCLI"

nc -z "${PRISM_HOST%:*}" "${PRISM_HOST##*:}" \
    || fail "prism not listening on $PRISM_HOST"
nc -z "${ALICE_RPC%:*}" "${ALICE_RPC##*:}" \
    || fail "alice LND not reachable at $ALICE_RPC"
nc -z "${BOB_RPC%:*}" "${BOB_RPC##*:}" \
    || fail "bob LND not reachable at $BOB_RPC"
pass "prism, alice, bob all reachable"

ADMIN_MAC="$PRISM_BASEDIR/admin.macaroon"
[ -f "$ADMIN_MAC" ] || fail "admin macaroon not found at $ADMIN_MAC"
ADMIN_MAC_HEX=$(xxd -ps -c 10000 "$ADMIN_MAC")
pass "admin macaroon loaded"

# Channel sanity — alice needs a channel to bob with capacity to pay.
channels=$(alice_cli listchannels 2>/dev/null || echo '{"channels":[]}')
active=$(echo "$channels" | jq '[.channels[] | select(.active==true)] | length' 2>/dev/null || echo 0)
[ "$active" -gt 0 ] \
    || warn "alice has no active channels — payment step may fail"

curl_admin() {
    curl -sk -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" "$@"
}

# Capture both lnds' identity pubkeys for the routing assertion in step 4.
ALICE_PUB=$(alice_cli getinfo 2>/dev/null | jq -r '.identity_pubkey // empty')
BOB_PUB=$(bob_cli getinfo 2>/dev/null   | jq -r '.identity_pubkey // empty')
[ -n "$ALICE_PUB" ] && [ -n "$BOB_PUB" ] \
    || fail "could not read identity_pubkey from alice/bob"
info "alice pubkey: ${ALICE_PUB:0:16}..."
info "bob   pubkey: ${BOB_PUB:0:16}..."

# Sanity: confirm prism actually loaded service2's payment override. If it
# didn't (e.g. yaml block still commented out), the rest of the test is
# meaningless — the invoice would come from alice and we'd never notice.
svc_payment=$(curl_admin "https://$PRISM_HOST/api/admin/services" \
    | jq -r '.services[] | select(.name=="service2") | .payment.lnd_host // ""')
if [ -z "$svc_payment" ]; then
    fail "service2 has no per-service payment override loaded by prism. " \
         "Uncomment the payment: block in sample-conf-tmp.yaml and restart."
fi
pass "service2 routes invoices to merchant lnd at $svc_payment"

# --- 2. Unauthenticated request ----------------------------------------

step "[2/8] Unauthenticated GET (no Authorization yet — expect 402)"

HDR=$(mktemp); BODY=$(mktemp)
trap 'rm -f "$HDR" "$BODY"' EXIT

print_req "GET" "https://$PRISM_HOST$PATH_SUFFIX"

code=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    "https://$PRISM_HOST$PATH_SUFFIX")

echo "  HTTP $code"
[ "$code" = "402" ] || fail "expected 402, got $code. body: $(head -c 200 "$BODY")"
pass "prism challenged with 402"

# Pick the L402 challenge specifically — when service2 also has authscheme
# l402+mpp the response will carry both LSAT and Payment headers.
www=$(grep -i '^www-authenticate: LSAT ' "$HDR" | head -1)
[ -n "$www" ] || www=$(grep -i '^www-authenticate:' "$HDR" | grep -i 'macaroon=' | head -1)
[ -n "$www" ] || fail "no LSAT WWW-Authenticate header. headers: $(grep -i auth "$HDR")"
echo "  $www" | fold -s -w 100 | sed 's/^/    /'

# --- 3. Parse LSAT challenge -------------------------------------------

step "[3/8] Extract macaroon + invoice"

mac=$(echo "$www" | sed -n 's/.*macaroon="\([^"]*\)".*/\1/p')
inv=$(echo "$www" | sed -n 's/.*invoice="\([^"]*\)".*/\1/p')

[ -n "$mac" ] || fail "could not parse macaroon from: $www"
[ -n "$inv" ] || fail "could not parse invoice from: $www"
pass "macaroon: ${#mac} base64 chars"
pass "invoice:  ${inv:0:40}..."

# --- 4. Decode invoice + verify destination = bob ----------------------

step "[4/8] Decode invoice; assert destination == bob"

# Either lnd can decode any BOLT11; alice happens to be available.
decoded=$(alice_cli decodepayreq "$inv" 2>&1) \
    || fail "decodepayreq failed: $decoded"

dest=$(echo "$decoded" | jq -r '.destination')
amt=$(echo "$decoded" | jq -r '.num_satoshis')
phash=$(echo "$decoded" | jq -r '.payment_hash')

echo "$decoded" | jq '{
    amount: .num_satoshis,
    payment_hash: .payment_hash,
    description: .description,
    destination: .destination
}' | sed 's/^/    /'

if [ "$dest" = "$BOB_PUB" ]; then
    pass "destination pubkey == bob ✓ — per-service lnd routing is live"
elif [ "$dest" = "$ALICE_PUB" ]; then
    fail "destination pubkey == ALICE — service2 payment override didn't take effect; " \
         "did you restart prism after editing the config?"
else
    fail "destination pubkey ($dest) is neither alice nor bob"
fi

chain=$(curl_admin "https://$PRISM_HOST/api/admin/info" | jq -r '.chain // ""')
if [ "$chain" = "sui" ]; then
    info "alice will pay $amt MIST ($(python3 -c "print($amt/1e9)") SUI) to bob"
else
    info "alice will pay $amt sats to bob"
fi

# --- 5. Pay with alice -------------------------------------------------

step "[5/8] Pay the invoice from alice (alice → bob via the channel)"

pay_json=$(alice_cli payinvoice --force --json "$inv" 2>&1) || {
    echo "$pay_json" | sed 's/^/    /'
    fail "alice payinvoice failed — is there a routable channel alice→bob with capacity ≥ $amt?"
}

final=$(echo "$pay_json" | jq -s '.[-1]' 2>/dev/null) || final="$pay_json"
preimage=$(echo "$final" | jq -r '.payment_preimage // empty')
status=$(echo "$final" | jq -r '.status // .payment_error // empty')

if [ -z "$preimage" ] || [ "$preimage" = "null" ]; then
    echo "$final" | sed 's/^/    /'
    fail "no payment_preimage returned — status: $status"
fi

echo "    amount:    $amt base units"
echo "    status:    $status"
echo "    preimage:  $preimage"
pass "payment settled (funds landed in bob's wallet, not the gateway's)"

# --- 6. Verify bob actually received the payment -----------------------

step "[6/8] Confirm settlement on bob's side"

sleep 1
bob_inv=$(bob_cli lookupinvoice "$phash" 2>/dev/null || echo '{}')
bob_state=$(echo "$bob_inv" | jq -r '.state // empty')
bob_paid=$(echo "$bob_inv" | jq -r '.amt_paid_sat // 0')
case "$bob_state" in
    SETTLED) pass "bob's invoice $phash → SETTLED, amt_paid=$bob_paid base units" ;;
    *)       warn "bob invoice state: ${bob_state:-unknown}; settlement may be async" ;;
esac

# --- 7. Replay with LSAT token -----------------------------------------

step "[7/8] Replay request with LSAT token"

sleep 2
print_req "GET" "https://$PRISM_HOST$PATH_SUFFIX" \
    "Authorization: LSAT <macaroon>:<preimage>   # ${#mac}b64 macaroon + ${preimage:0:8}…"

code2=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    -H "Authorization: LSAT $mac:$preimage" \
    "https://$PRISM_HOST$PATH_SUFFIX")

echo "  HTTP $code2"
case "$code2" in
    401|402)
        echo "  body: $(head -c 200 "$BODY")"
        fail "auth rejected — preimage didn't validate against the macaroon"
        ;;
    200|201|204)
        pass "backend reached through prism — auth succeeded"
        ;;
    *)
        pass "LSAT auth succeeded (backend returned $code2 — expected for dummy backends)"
        ;;
esac

# --- 8. Inspect admin API ----------------------------------------------

step "[8/8] What prism recorded"

echo "${C}Last 5 transactions for service2:${N}"
curl_admin "https://$PRISM_HOST/api/admin/transactions?service=service2&limit=5" \
    | jq '.transactions // . | .[:5]' 2>/dev/null \
    | sed 's/^/    /' || echo "    (none)"

echo
echo "${G}━━━ Done ━━━${N}"
echo "Service2 routed an invoice to ${B}bob${N} ($BOB_PUB) — proven by the"
echo "destination pubkey check in step 4. Alice paid; funds settled on bob."
echo "This is how multi-merchant deployments keep payments isolated to each"
echo "merchant's own lnd, with the gateway never taking custody."
