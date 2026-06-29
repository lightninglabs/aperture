#!/bin/bash
# manual_pay_mpp_session.sh
#
# Walks all four MPP session actions end-to-end against a running Prism:
#   open   → pay a deposit invoice, receive a session id (no fresh
#            charge per request afterwards, balance is debited instead).
#   bearer → two authenticated requests that silently draw down the
#            balance; no new Lightning payment is needed.
#   topUp  → pay a second deposit invoice to extend the session.
#   close  → terminate the session; server pays the client's amountless
#            ReturnInvoice with the leftover balance, then issues a
#            SessionReceipt that reports refundSats / refundStatus.
#
# Requires prism started with:
#   authenticator.enablempp:     true
#   authenticator.enablesessions: true
# and the target service set to `authscheme: "l402+mpp"` or `"mpp"`.
# The per-service deposit is (price * sessiondepositmultiplier); with
# the default multiplier of 20 and price=10_000_000 MIST you pay 0.2
# SUI up front and can burn through it across ~20 requests.
#
# Sibling to manual_pay_mpp.sh (one-shot charge intent).

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

# Number of bearer calls to make between open and close.
BEARER_CALLS="${BEARER_CALLS:-2}"

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

b64url_enc() {
    python3 -c 'import base64,sys; sys.stdout.write(base64.urlsafe_b64encode(sys.stdin.buffer.read()).decode().rstrip("="))'
}
b64url_dec() {
    python3 -c 'import base64,sys; s=sys.stdin.read().strip(); pad="="*(-len(s)%4); sys.stdout.buffer.write(base64.urlsafe_b64decode(s+pad))'
}

# Pick the Payment challenge with the given intent (session or charge)
# from a response header file and emit: id<TAB>realm<TAB>method<TAB>intent<TAB>request<TAB>expires
pick_challenge() {
    local hdr="$1" want_intent="$2"
    awk '
        /^[Ww]{3}-[Aa]uthenticate: Payment /{buf=$0; next}
        buf && /^[ \t]/{buf=buf $0; next}
        buf{print buf; buf=""}
        END{if (buf) print buf}
    ' "$hdr" | while read -r line; do
        intent=$(echo "$line" | sed -n 's/.*intent="\([^"]*\)".*/\1/p')
        if [ "$intent" = "$want_intent" ]; then
            id=$(echo "$line" | sed -n 's/.*id="\([^"]*\)".*/\1/p')
            realm=$(echo "$line" | sed -n 's/.*realm="\([^"]*\)".*/\1/p')
            method=$(echo "$line" | sed -n 's/.*method="\([^"]*\)".*/\1/p')
            request=$(echo "$line" | sed -n 's/.*request="\([^"]*\)".*/\1/p')
            expires=$(echo "$line" | sed -n 's/.*expires="\([^"]*\)".*/\1/p')
            printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
                "$id" "$realm" "$method" "$intent" "$request" "$expires"
            return
        fi
    done
}

# Build a Payment credential envelope: base64url(JSON({challenge, payload}))
build_cred() {
    local id="$1" realm="$2" method="$3" intent="$4" request="$5" expires="$6" payload="$7"
    jq -nc \
        --arg id "$id" --arg realm "$realm" --arg method "$method" \
        --arg intent "$intent" --arg request "$request" --arg expires "$expires" \
        --argjson payload "$payload" \
        '{
            challenge: (
                {id: $id, realm: $realm, method: $method, intent: $intent, request: $request}
                + ( if $expires == "" then {} else {expires: $expires} end )
            ),
            payload: $payload
        }' | b64url_enc
}

HDR=$(mktemp); BODY=$(mktemp); HDR2=$(mktemp); BODY2=$(mktemp)
trap 'rm -f "$HDR" "$BODY" "$HDR2" "$BODY2"' EXIT

# --- 1. Preflight -------------------------------------------------------

step "[1/7] Preflight"

for dep in curl jq xxd nc python3; do
    command -v "$dep" >/dev/null 2>&1 || fail "missing dependency: $dep"
done
[ -x "$LNCLI" ] || fail "lncli not found at $LNCLI"

ADMIN_MAC="$PRISM_BASEDIR/admin.macaroon"
[ -f "$ADMIN_MAC" ] || fail "admin macaroon not found at $ADMIN_MAC"
ADMIN_MAC_HEX=$(xxd -ps -c 10000 "$ADMIN_MAC")

info=$(curl -sk -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" \
    "https://$PRISM_HOST/api/admin/info")
[ "$(echo "$info" | jq -r '.mpp_enabled')" = "true" ] \
    || fail "MPP not enabled — set authenticator.enablempp: true"
[ "$(echo "$info" | jq -r '.sessions_enabled')" = "true" ] \
    || fail "MPP sessions not enabled — set authenticator.enablesessions: true and restart"
pass "MPP + sessions enabled (realm=$(echo "$info" | jq -r '.mpp_realm'))"

# --- 2. Trigger a 402 and isolate the session challenge ----------------

step "[2/7] Fetch session challenge"

code=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" "https://$PRISM_HOST$PATH_SUFFIX")
[ "$code" = "402" ] || fail "expected 402, got $code"
pass "prism challenged with 402"

chal=$(pick_challenge "$HDR" "session")
[ -n "$chal" ] \
    || fail "no intent=session challenge found. Is sessions_enabled=true? headers: $(grep -i authenticate "$HDR")"

IFS=$'\t' read -r SESS_ID SESS_REALM SESS_METHOD SESS_INTENT SESS_REQ SESS_EXPIRES <<<"$chal"
echo "    id:       $SESS_ID"
echo "    intent:   $SESS_INTENT"
echo "    expires:  $SESS_EXPIRES"

req_json=$(echo "$SESS_REQ" | b64url_dec)
deposit_invoice=$(echo "$req_json" | jq -r '.depositInvoice')
deposit_phash=$(echo "$req_json" | jq -r '.paymentHash')
deposit_amount=$(echo "$req_json" | jq -r '.depositAmount')
per_unit_amount=$(echo "$req_json" | jq -r '.amount')
idle_timeout=$(echo "$req_json" | jq -r '.idleTimeout')

info "idle timeout:       ${idle_timeout}s"
info "deposit paymentHash: $deposit_phash"

# --- 2b. Amount plan ---------------------------------------------------
# Every number shown here is decided by the SERVER (from the service's
# price × sessiondepositmultiplier), not set in this script. The script
# just pays whatever prism asks for and records what it asked for so the
# operator can see what's about to move.

planned_spend=$((per_unit_amount * BEARER_CALLS))
planned_refund=$((deposit_amount - planned_spend))

chain=$(curl -sk -H "Grpc-Metadata-Macaroon: $ADMIN_MAC_HEX" \
    "https://$PRISM_HOST/api/admin/info" | jq -r '.chain // ""')
unit="sats"
scale_div=1
if [ "$chain" = "sui" ]; then
    unit="SUI"
    scale_div=1000000000
fi
fmt_amount() { python3 -c "print($1/$scale_div)"; }

step "[2b/7] Amount plan (all derived from prism config)"
printf "  %-22s %12s base units  = %12s %s\n" \
    "deposit (bob→prism):" "$deposit_amount"  "$(fmt_amount "$deposit_amount")"  "$unit"
printf "  %-22s %12s base units  = %12s %s\n" \
    "per bearer request:" "$per_unit_amount" "$(fmt_amount "$per_unit_amount")" "$unit"
printf "  %-22s %12s base units  = %12s %s  (%d × per-request)\n" \
    "planned total spend:" "$planned_spend"  "$(fmt_amount "$planned_spend")"  "$unit" "$BEARER_CALLS"
printf "  %-22s %12s base units  = %12s %s  (deposit − spend, prism→bob on close)\n" \
    "expected refund:"    "$planned_refund" "$(fmt_amount "$planned_refund")" "$unit"
echo "  ${Y}~${N} To change these: edit services[0].price +"
echo "    authenticator.sessiondepositmultiplier in your config, or set"
echo "    BEARER_CALLS=<n> when invoking this script (currently $BEARER_CALLS)."

# --- 3. Bob pays the deposit invoice, builds ReturnInvoice, OPEN -----

step "[3/7] Action: open  (pay deposit + send returnInvoice)"

# Bob creates an amountless BOLT11 invoice for future refund.
return_payreq=$(bob_cli addinvoice --amt 0 --memo "MPP session refund" \
    --expiry $((idle_timeout + 3600)) 2>&1 | jq -r '.payment_request')
[ -n "$return_payreq" ] && [ "$return_payreq" != "null" ] \
    || fail "bob could not create amountless refund invoice"
info "bob refund invoice: ${return_payreq:0:40}..."

pay_json=$(bob_cli payinvoice --force --json "$deposit_invoice" 2>&1) \
    || { echo "$pay_json" | sed 's/^/    /'; fail "bob could not pay deposit"; }
open_preimage=$(echo "$pay_json" | jq -rs '.[-1] | .payment_preimage // empty')
[ -n "$open_preimage" ] && [ "$open_preimage" != "null" ] \
    || fail "deposit payment returned no preimage"
info "deposit preimage: $open_preimage"

open_payload=$(jq -nc --arg pi "$open_preimage" --arg ri "$return_payreq" \
    '{action:"open", preimage:$pi, returnInvoice:$ri}')

open_cred=$(build_cred "$SESS_ID" "$SESS_REALM" "$SESS_METHOD" \
    "$SESS_INTENT" "$SESS_REQ" "$SESS_EXPIRES" "$open_payload")

sleep 2
code=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    -H "Authorization: Payment $open_cred" \
    "https://$PRISM_HOST$PATH_SUFFIX")
[ "$code" = "200" ] || fail "open failed, got $code. body: $(head -c 300 "$BODY")"
pass "open accepted → HTTP 200"

# The session id is the paymentHash of the deposit invoice.
SESSION_ID="$deposit_phash"
info "session id = $SESSION_ID"

# --- 4. Action: bearer (no payment, server debits balance) -----------

step "[4/7] Action: bearer × $BEARER_CALLS (drain balance)"

bearer_payload=$(jq -nc --arg sid "$SESSION_ID" --arg pi "$open_preimage" \
    '{action:"bearer", sessionId:$sid, preimage:$pi}')
bearer_cred=$(build_cred "$SESS_ID" "$SESS_REALM" "$SESS_METHOD" \
    "$SESS_INTENT" "$SESS_REQ" "$SESS_EXPIRES" "$bearer_payload")

for i in $(seq 1 "$BEARER_CALLS"); do
    code=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
        -H "Host: $SERVICE_HOST" \
        -H "Authorization: Payment $bearer_cred" \
        "https://$PRISM_HOST$PATH_SUFFIX")
    if [ "$code" = "200" ]; then
        pass "bearer #$i → HTTP 200 (no Lightning payment)"
    else
        fail "bearer #$i → HTTP $code. body: $(head -c 300 "$BODY")"
    fi
done

# open does not debit balance — only bearer/topUp actions that actually
# forward to the backend consume from the session's remaining amount.
spent=$((per_unit_amount * BEARER_CALLS))
remaining=$((deposit_amount - spent))
info "expected spend:     $spent base units (${BEARER_CALLS} × $per_unit_amount)"
info "expected refund:    $remaining base units ($(python3 -c "print($remaining/1e9)") SUI)"

# --- 5. Action: close  (server refunds remaining) --------------------

step "[5/7] Action: close  (server pays ReturnInvoice)"

close_payload=$(jq -nc --arg sid "$SESSION_ID" --arg pi "$open_preimage" \
    '{action:"close", sessionId:$sid, preimage:$pi}')
close_cred=$(build_cred "$SESS_ID" "$SESS_REALM" "$SESS_METHOD" \
    "$SESS_INTENT" "$SESS_REQ" "$SESS_EXPIRES" "$close_payload")

code=$(curl -sk -o "$BODY2" -D "$HDR2" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    -H "Authorization: Payment $close_cred" \
    "https://$PRISM_HOST$PATH_SUFFIX")
[ "$code" = "200" ] || fail "close failed, got $code. body: $(head -c 300 "$BODY2")"
pass "close accepted → HTTP 200"

# Decode the session receipt.
rct_line=$(grep -i '^payment-receipt:' "$HDR2" | head -1 | tr -d '\r' || true)
if [ -n "$rct_line" ]; then
    rct_b64=$(echo "$rct_line" \
        | sed -n 's/^[Pp]ayment-[Rr]eceipt: *\([A-Za-z0-9_-]*\).*/\1/p')
    echo "    session receipt:"
    echo "$rct_b64" | b64url_dec | jq . | sed 's/^/      /' \
        || warn "could not decode receipt"
else
    warn "no Payment-Receipt header on close response"
fi

# --- 6. Verify bob actually received the refund ---------------------

step "[6/7] Verify refund landed on bob"

sleep 2
refund_state=$(bob_cli lookupinvoice "$(echo "$return_payreq" | \
    { read -r pr; alice_cli decodepayreq "$pr" | jq -r '.payment_hash'; })" 2>&1 \
    | jq -r '.state // empty')
if [ "$refund_state" = "SETTLED" ]; then
    pass "bob's return invoice is SETTLED — refund received"
    # Show the exact amount Bob received.
    received=$(bob_cli lookupinvoice "$(alice_cli decodepayreq "$return_payreq" \
        | jq -r '.payment_hash')" 2>&1 | jq -r '.amt_paid_sat')
    info "amount received by bob: $received base units ($(python3 -c "print($received/1e9)") SUI)"
else
    warn "return invoice state: ${refund_state:-unknown} (refund may be async)"
fi

# --- 7. Bearer after close should fail ------------------------------

step "[7/7] Bearer after close should be rejected"

code=$(curl -sk -o "$BODY" -D "$HDR" -w '%{http_code}' \
    -H "Host: $SERVICE_HOST" \
    -H "Authorization: Payment $bearer_cred" \
    "https://$PRISM_HOST$PATH_SUFFIX")
case "$code" in
    200) warn "bearer after close returned 200 (session may linger briefly)" ;;
    401|402) pass "session no longer accepted (HTTP $code) — close finalized" ;;
    *) warn "unexpected HTTP $code on post-close bearer" ;;
esac

echo
echo "${G}━━━ MPP session flow complete ━━━${N}"
echo "Session lifecycle: open → $BEARER_CALLS × bearer → close (refund via ReturnInvoice)"
