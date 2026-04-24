#!/bin/bash
# manual_pay_mpp.sh
#
# Exercises the Payment HTTP Authentication (MPP) flow end-to-end through
# a running Prism on :8080. Sibling to manual_pay_l402.sh which
# does the L402 flow. Assumes both L402 and MPP are enabled (config has
# `authenticator.enablempp: true`) and the target service uses
# `authscheme: "l402+mpp"` (or `"mpp"`).
#
# Flow:
#   1. Unauthenticated request → 402 with *three* WWW-Authenticate headers
#      (LSAT, L402, Payment). Parse the Payment one.
#   2. Decode the `request` field (base64url JSON) to extract the BOLT11
#      invoice, payment hash, and echo parameters.
#   3. Pay the MPP invoice from bob.
#   4. Build a Payment credential: echo all challenge params + payload
#      { "preimage": "<hex>" }, base64url-encode without padding, send as
#      `Authorization: Payment <token>`.
#   5. Verify prism accepts the credential and forwards to the backend.
#
# Prereqs identical to manual_pay_l402.sh:
#   * Prism on :8080 with MPP enabled
#   * Alice LND on :10009, Bob LND on :10010, open alice↔bob channel
#   * Demo backend on :9998 (./scripts/serve_demo_backend.sh)

set -euo pipefail

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

if [ -t 1 ]; then
    G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; C=$'\033[36m'; N=$'\033[0m'
else
    G=""; R=""; Y=""; C=""; N=""
fi
step() { echo; echo "${C}━━━ $* ━━━${N}"; }
pass() { echo "  ${G}✓${N} $*"; }
warn() { echo "  ${Y}~${N} $*"; }
fail() { echo "  ${R}✗${N} $*"; exit 1; }

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

# base64url encode without padding (RFC 4648 §5), reading from stdin.
b64url_enc() {
    python3 -c '
import base64, sys
sys.stdout.write(base64.urlsafe_b64encode(sys.stdin.buffer.read()).decode().rstrip("="))
'
}
# base64url decode (accepts with or without padding).
b64url_dec() {
    python3 -c '
import base64, sys
s = sys.stdin.read().strip()
pad = "=" * (-len(s) % 4)
sys.stdout.buffer.write(base64.urlsafe_b64decode(s + pad))
'
}

# --- 1. Preflight ---------------------------------------------------------

step "[1/8] Preflight"

for dep in curl jq xxd nc python3; do
    command -v "$dep" >/dev/null 2>&1 || fail "missing dependency: $dep"
done
[ -x "$LNCLI" ] || fail "lncli not found at $LNCLI"

nc -z "${PRISM_HOST%:*}" "${PRISM_HOST##*:}" \
    || fail "prism not listening on $PRISM_HOST"
nc -z "${ALICE_RPC%:*}" "${ALICE_RPC##*:}" \
    || fail "alice lnd not reachable at $ALICE_RPC"
nc -z "${BOB_RPC%:*}" "${BOB_RPC##*:}" \
    || fail "bob lnd not reachable at $BOB_RPC"

ADMIN_MAC="$PRISM_BASEDIR/admin.macaroon"
[ -f "$ADMIN_MAC" ] || fail "admin macaroon not found at $ADMIN_MAC"
ADMIN_MAC_HEX=$(xxd -ps -c 10000 "$ADMIN_MAC")

# Verify MPP is actually enabled.
info=$(curl -sk -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" \
    "https://$PRISM_HOST/api/admin/info")
mpp_on=$(echo "$info" | jq -r '.mpp_enabled // false')
[ "$mpp_on" = "true" ] \
    || fail "admin /info reports mpp_enabled=false — set authenticator.enablempp: true and restart prism"
pass "prism reachable, MPP enabled (realm=$(echo "$info" | jq -r '.mpp_realm'))"

# Verify the target service accepts MPP.
svc=$(curl -sk -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" \
    "https://$PRISM_HOST/api/admin/services" \
    | jq -c --arg h "$SERVICE_HOST" \
        '.services[] | select(.host_regexp | test($h; "x") | not | not)')
scheme=$(echo "$svc" | jq -r '.auth_scheme // empty')
case "$scheme" in
    AUTH_SCHEME_MPP|AUTH_SCHEME_L402_MPP) ;;
    *)
        warn "service for host $SERVICE_HOST has auth_scheme=$scheme"
        warn "flipping it to AUTH_SCHEME_L402_MPP so this test can proceed"
        svc_name=$(echo "$svc" | jq -r '.name')
        curl -sk -X PUT \
            -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" \
            -H "Content-Type: application/json" \
            -d '{"auth_scheme":"AUTH_SCHEME_L402_MPP"}' \
            "https://$PRISM_HOST/api/admin/services/$svc_name" \
            >/dev/null
        pass "$svc_name → AUTH_SCHEME_L402_MPP"
        ;;
esac

# --- 2. Unauthenticated request (expect 402 with Payment challenge) -----

step "[2/8] Unauthenticated GET $PATH_SUFFIX (Host: $SERVICE_HOST)"

HDR=$(mktemp); BODY=$(mktemp)
trap 'rm -f "$HDR" "$BODY"' EXIT

code=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    "https://$PRISM_HOST$PATH_SUFFIX")
[ "$code" = "402" ] || fail "expected 402, got $code. body: $(head -c 200 "$BODY")"
pass "prism challenged with 402"

# Count the WWW-Authenticate headers.
wwa_count=$(grep -ci '^www-authenticate:' "$HDR" || true)
pass "$wwa_count WWW-Authenticate challenge(s) present"

# --- 3. Parse the Payment challenge -------------------------------------

step "[3/8] Parse Payment challenge"

# The header is multi-line (curl folds long headers). Collect *all* lines
# starting with "www-authenticate: Payment " plus their continuations,
# then keep only the one with intent="charge" — when MPP sessions are
# enabled, prism returns both a charge-intent AND a session-intent
# challenge; picking the first one naively lands on the wrong scheme.
pay_line=$(awk '
    /^[Ww]{3}-[Aa]uthenticate: Payment /{if (buf) print buf; buf=$0; next}
    buf && /^[ \t]/{buf=buf $0; next}
    buf{print buf; buf=""}
    END{if (buf) print buf}
' "$HDR" | grep 'intent="charge"' | head -1)

[ -n "$pay_line" ] || fail "no Payment WWW-Authenticate challenge with intent=charge found — did the target service opt out of MPP, or is only session intent active?"

extract_auth_param() {
    # extract_auth_param <line> <key> → value (without surrounding quotes)
    echo "$1" | sed -n "s/.*$2=\"\\([^\"]*\\)\".*/\\1/p"
}

chal_id=$(extract_auth_param "$pay_line" "id")
chal_realm=$(extract_auth_param "$pay_line" "realm")
chal_method=$(extract_auth_param "$pay_line" "method")
chal_intent=$(extract_auth_param "$pay_line" "intent")
chal_request=$(extract_auth_param "$pay_line" "request")
chal_expires=$(extract_auth_param "$pay_line" "expires")

for v in "$chal_id" "$chal_realm" "$chal_method" "$chal_intent" "$chal_request"; do
    [ -n "$v" ] || fail "failed to parse challenge fields from: $pay_line"
done

echo "    id:      $chal_id"
echo "    realm:   $chal_realm"
echo "    method:  $chal_method"
echo "    intent:  $chal_intent"
echo "    expires: $chal_expires"

# Decode the request field (base64url JSON).
req_json=$(echo "$chal_request" | b64url_dec)
echo "$req_json" | jq . | sed 's/^/    /'

invoice=$(echo "$req_json" | jq -r '.methodDetails.invoice')
phash=$(echo "$req_json" | jq -r '.methodDetails.paymentHash')
amount=$(echo "$req_json" | jq -r '.amount')
pass "MPP invoice: ${invoice:0:40}..."
pass "payment_hash: $phash"
pass "amount: $amount base units"

# --- 4. Pay invoice with bob --------------------------------------------

step "[4/8] Pay with bob"

pay_json=$(bob_cli payinvoice --force --json "$invoice" 2>&1) || {
    echo "$pay_json" | sed 's/^/    /'
    fail "bob payinvoice failed"
}
final=$(echo "$pay_json" | jq -s '.[-1]' 2>/dev/null) || final="$pay_json"
preimage=$(echo "$final" | jq -r '.payment_preimage // empty')
status=$(echo "$final" | jq -r '.status // empty')

[ -n "$preimage" ] && [ "$preimage" != "null" ] \
    || { echo "$final" | sed 's/^/    /'; fail "no preimage returned"; }

echo "    status:   $status"
echo "    preimage: $preimage"
pass "invoice settled on lightning"

# --- 5. Build the Payment credential ------------------------------------

step "[5/8] Build Authorization: Payment credential"

# Echo back *all* challenge params exactly, plus payload with the preimage.
# This is what mpp.ParseCredential expects.
cred_json=$(jq -nc \
    --arg id "$chal_id" \
    --arg realm "$chal_realm" \
    --arg method "$chal_method" \
    --arg intent "$chal_intent" \
    --arg request "$chal_request" \
    --arg expires "$chal_expires" \
    --arg preimage "$preimage" \
    '{
        challenge: (
            {id: $id, realm: $realm, method: $method, intent: $intent, request: $request}
            + ( if $expires == "" then {} else {expires: $expires} end )
        ),
        payload: {preimage: $preimage}
    }')

echo "    credential JSON (pretty, request truncated):"
echo "$cred_json" | jq '{
    challenge: (.challenge | {id, realm, method, intent, expires, request: ((.request // "")[:32] + "...")}),
    payload
}' | sed 's/^/      /'

cred_b64=$(printf '%s' "$cred_json" | b64url_enc)
auth_header="Payment $cred_b64"
pass "Authorization header built (${#cred_b64} b64url chars)"

# --- 6. Replay with Payment credential ----------------------------------

step "[6/8] Replay request with Payment credential"

sleep 2   # let prism's SubscribeInvoices see settlement

code2=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    -H "Authorization: $auth_header" \
    "https://$PRISM_HOST$PATH_SUFFIX")

echo "  HTTP $code2"
case "$code2" in
    401|402) echo "  body: $(head -c 300 "$BODY")"; fail "auth rejected" ;;
    200|201|204) pass "MPP auth accepted — backend reached" ;;
    *) pass "MPP auth accepted (backend returned $code2 — expected for dummy backends)" ;;
esac

# --- 7. Check for Payment-Receipt header --------------------------------

step "[7/8] Look for Payment-Receipt"

receipt=$(grep -i '^payment-receipt:' "$HDR" | head -1 | tr -d '\r' || true)
if [ -n "$receipt" ]; then
    pass "Payment-Receipt header present"
    # Format: "payment-receipt: <b64url json>" — no scheme prefix.
    rct_b64=$(echo "$receipt" \
        | sed -n 's/^[Pp]ayment-[Rr]eceipt: *\([A-Za-z0-9_-]*\).*/\1/p')
    if [ -n "$rct_b64" ]; then
        echo "    decoded receipt:"
        echo "$rct_b64" | b64url_dec | jq . | sed 's/^/      /' \
            || warn "could not decode receipt (invalid base64 or JSON)"
    else
        warn "could not extract base64 from receipt line"
    fi
else
    warn "no Payment-Receipt header (server may not issue one for charge intent)"
fi

# --- 8. Admin API ---------------------------------------------------------

step "[8/8] admin transactions (filter by service)"

svc_name=$(echo "$svc" | jq -r '.name')
curl -sk -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" \
    "https://$PRISM_HOST/api/admin/transactions?limit=3&service=$svc_name" \
    | jq '.transactions // . | .[:3]' | sed 's/^/    /'

echo
echo "${G}━━━ MPP flow complete ━━━${N}"
echo "Try the L402 flow on the same service:"
echo "  ./scripts/manual_pay_l402.sh"
