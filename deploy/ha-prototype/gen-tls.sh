#!/bin/bash
# Generate a self-signed TLS cert for the nginx LB. Browsers will
# complain (it's prism.ha.local with no real CA), so test with `curl
# -k` or import the cert into your trust store.
#
# Idempotent — re-running won't overwrite an existing pair unless you
# pass --force.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

if [ -f tls.crt ] && [ -f tls.key ] && [ "${1:-}" != "--force" ]; then
    echo "tls.crt + tls.key already exist (pass --force to regenerate)"
    exit 0
fi

openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
    -keyout tls.key -out tls.crt \
    -subj "/CN=prism.ha.local" \
    -addext "subjectAltName=DNS:prism.ha.local,DNS:localhost,IP:127.0.0.1"
chmod 644 tls.crt tls.key
echo
echo "Generated tls.crt + tls.key for CN=prism.ha.local"
echo "Add to /etc/hosts:  127.0.0.1   prism.ha.local"
