#!/bin/bash
# serve_demo_backend.sh
#
# Starts a tiny Python HTTP server on :9998 with a few fixture files, used
# as a dummy backend for the L402 payment flow demo (see
# manual_pay_through_prism.sh). Point a Prism service at 127.0.0.1:9998
# with protocol=http and exercise the full 402 → pay → 200 round-trip.
#
# Example service1 stanza in sample-conf-tmp.yaml:
#
#   services:
#     - name: "service1"
#       hostregexp: '^service1.com$'
#       pathregexp: '^/.*$'
#       address: "127.0.0.1:9998"
#       protocol: http
#       price: 0
#
# Usage:
#   ./scripts/serve_demo_backend.sh           # foreground, Ctrl-C to stop
#   PORT=9998 ./scripts/serve_demo_backend.sh # custom port
#   ./scripts/serve_demo_backend.sh &         # background
#
# Default fixture content served:
#   GET /            → index.html (HTML page)
#   GET /data.json   → JSON payload
#   GET /probe       → probe.txt (matches manual_pay_through_prism.sh default)
#   GET /<anything>  → 404 (standard http.server behavior)

set -euo pipefail

PORT="${PORT:-9998}"
BIND="${BIND:-127.0.0.1}"
SERVE_DIR="${SERVE_DIR:-/tmp/prism-backend}"

# Prepare fixture files only if the serve dir doesn't already have them.
mkdir -p "$SERVE_DIR"

if [ ! -f "$SERVE_DIR/index.html" ]; then
    cat > "$SERVE_DIR/index.html" <<'HTML'
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Prism L402 Demo Backend</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 720px;
         margin: 4em auto; padding: 0 1em; color: #222; }
  h1 { border-bottom: 2px solid #6c5ce7; padding-bottom: .3em; }
  code { background: #f4f4f4; padding: .15em .4em; border-radius: 4px; }
  .box { background: #e8f5e9; padding: 1em 1.2em; border-left: 4px solid #2e7d32;
         border-radius: 4px; margin: 1.2em 0; }
</style>
</head>
<body>
  <h1>Hello from the Prism backend</h1>
  <div class="box">
    <strong>If you are seeing this page</strong>, your Lightning payment
    cleared and Prism validated the L402 token before forwarding the
    request to this backend (<code>127.0.0.1:9998</code>).
  </div>
  <p>Try other endpoints:</p>
  <ul>
    <li><a href="/data.json"><code>GET /data.json</code></a> — JSON payload</li>
  </ul>
  <p>Swap this backend for your real service by changing
  <code>services[0].address</code> in <code>sample-conf-tmp.yaml</code>
  and restarting Prism.</p>
</body>
</html>
HTML
fi

if [ ! -f "$SERVE_DIR/data.json" ]; then
    cat > "$SERVE_DIR/data.json" <<'JSON'
{
  "status": "paid",
  "message": "If you received this response, the L402 token was validated.",
  "demo": true,
  "backend": "python http.server",
  "note": "Pair this with sample-conf-tmp.yaml service1 (address=127.0.0.1:9998)."
}
JSON
fi

# /probe is the default path used by scripts/manual_pay_through_prism.sh,
# so we ship a fixture file with no extension so GET /probe returns 200.
if [ ! -f "$SERVE_DIR/probe" ]; then
    cat > "$SERVE_DIR/probe" <<'TEXT'
ok — L402 token validated; backend reached via Prism.
TEXT
fi

if ! command -v python3 >/dev/null 2>&1; then
    echo "error: python3 not found on PATH" >&2
    exit 1
fi

echo "Serving $SERVE_DIR on http://$BIND:$PORT"
echo "Fixtures:"
echo "  GET /            → index.html"
echo "  GET /data.json   → data.json"
echo "  GET /probe       → probe"
echo
echo "Ctrl-C to stop. Pair with:"
echo "  ./prism --configfile=./sample-conf-tmp.yaml    # Prism on :8080"
echo "  ./scripts/manual_pay_through_prism.sh          # drives a paid request"

cd "$SERVE_DIR"
exec python3 -m http.server "$PORT" --bind "$BIND"
