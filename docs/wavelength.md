# Sell an API with no Lightning node

This tutorial shows how to charge for access to an API over Lightning, without
running a Lightning node. Aperture sits in front of your API and only lets paid
requests through, using the L402 protocol. Callers need no account or API key,
which suits small payments and AI agents that pay for APIs themselves.

Aperture creates its invoices from a
[Wavelength](https://github.com/lightninglabs/wavelength) wallet instead of
lnd. The Wavelength swap server provides the inbound liquidity, so the seller
needs no node, no channels and no inbound capacity. Payments land in the
seller's Wavelength wallet, as Ark coins or, for small amounts, as credit (see
step 8). Ark lets many users share on-chain transactions while each keeps
control of their own coins.

Everything runs on signet, a Bitcoin test network whose coins have no value,
against Wavelength's public signet services. The only thing you need from
outside is a few free signet coins for the buyer, from a faucet (step 7).

You will run:

| Process | Role | Address |
|---|---|---|
| `quotes.py` | the API you sell | `127.0.0.1:8090` |
| `waved` (seller) | creates the invoices | gRPC `localhost:10029`, HTTP gateway `localhost:10061` |
| `aperture` | the L402 paywall in front of the API | `localhost:8081` |
| `waved` (buyer) | pays the invoices | gRPC `localhost:10039`, HTTP gateway `localhost:10041` |

The buyer is a second Wavelength wallet so that neither side runs a Lightning
node. Any Lightning wallet that can pay a signet invoice works as well.

Each of these four processes keeps running in the foreground, so start each
one in its own terminal. Use one more terminal for all the other commands: the
`seller` and `buyer` shell functions and the variables in step 8 only exist in
the terminal where you define them.

## Prerequisites

- Go (the version in `go.mod` of both repos)
- Python 3, for the example API
- `curl`

## 1. Install Aperture and Wavelength

Aperture's Wavelength backend is not in an Aperture release yet, so build
Aperture from source:

```shell
git clone https://github.com/lightninglabs/aperture.git
(cd aperture && make install)
```

For Wavelength, download the v0.1.2 release. Its binaries include the wallet
commands (`recv`, `send`, `balance`) and the swap runtime that creates
Lightning invoices. Set `PLATFORM` to `darwin-arm64`, `darwin-amd64`,
`linux-amd64` or `linux-arm64`:

```shell
PLATFORM=darwin-arm64
NAME=wavelength-$PLATFORM-v0.1.2
curl -LO https://github.com/lightninglabs/wavelength/releases/download/v0.1.2/$NAME.tar.gz
tar -xzf $NAME.tar.gz
mv $NAME/waved $NAME/wavecli "$(go env GOPATH)/bin/"
```

Download it with `curl` rather than a browser: macOS blocks the release
binaries when they come from a browser download.

Or build Wavelength from source instead:

```shell
git clone https://github.com/lightninglabs/wavelength.git
(cd wavelength && make install-wavewalletrpc)
```

`install-wavewalletrpc` builds the same wallet commands and swap runtime. The
`make install` targets and the `mv` above all put the binaries in
`$(go env GOPATH)/bin`, which needs to be on your `PATH`.

## 2. Start the seller wallet

```shell
waved --network=signet --datadir=$HOME/.waved-seller \
  --wallet.type=lwwallet \
  --rpc.listenaddr=localhost:10029 \
  --rpc.gateway.enabled \
  --rpc.gateway.listenaddr=localhost:10061
```

With `--network=signet` and no `server.host` or `swap.serveraddress`, `waved`
uses Wavelength's public signet Ark and swap servers. `--wallet.type=lwwallet`
selects the built-in light wallet, which reads the chain from a block explorer
API instead of a Bitcoin node. `--rpc.gateway.enabled` turns on the HTTP/JSON
gateway that Aperture talks to.

In your command terminal, define a shortcut for the seller's `wavecli` and
create the wallet:

```shell
seller() {
  wavecli --network=signet --datadir=$HOME/.waved-seller \
    --rpcserver=localhost:10029 --timeout=3m "$@"
}

seller create
seller getinfo
```

`create` asks for a wallet password, then prints the recovery words once;
write them down. It can take longer than `wavecli`'s default 30 second timeout,
hence `--timeout=3m`. If you restart `waved` later, run `seller unlock` with
the same password. `getinfo` should show
`"wallet_state": "WALLET_STATE_READY"` and `"server_connected": true`. Right
after `create`, connecting can take a few seconds, and `recv` fails until it
has.

Check that the seller can receive over Lightning with an empty wallet:

```shell
seller recv --offchain --amt 5000 --memo test
```

This prints a signet invoice (`lntbs...`) even though the wallet has no funds
and no channels.

## 3. Give Aperture a receive-only credential

Aperture only needs to create invoices, so give it a macaroon that can do
nothing else:

```shell
seller bakemacaroon --save-to $HOME/.waved-seller/aperture-recv.macaroon \
  uri:/wavewalletrpc.WalletService/Recv
```

## 4. Start the API

This is the API you want to monetize, a tiny service that returns a random
quote. Save it as `quotes.py`:

```python
# A tiny paid API: returns a random quote as JSON.
import json, random
from http.server import BaseHTTPRequestHandler, HTTPServer

QUOTES = [
    "Stay humble, stack sats.",
    "Running bitcoin. - Hal Finney",
    "Don't trust, verify.",
]

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"quote": random.choice(QUOTES)}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

HTTPServer(("127.0.0.1", 8090), Handler).serve_forever()
```

```shell
python3 quotes.py
```

## 5. Start Aperture

Create a folder for Aperture's files, the database and log for this demo:

```shell
mkdir -p $HOME/.aperture-demo
```

Save this as `aperture.yaml` in that folder:

```yaml
# Replace /home/you with your home directory in all three paths below.
listenaddr: "localhost:8081"
insecure: true
basedir: "/home/you/.aperture-demo"

authenticator:
  network: "signet"
  wavelengthgateway: "http://localhost:10061"
  wavelengthmacaroonpath: "/home/you/.waved-seller/aperture-recv.macaroon"

dbbackend: "sqlite"
sqlite:
  databasefilename: "/home/you/.aperture-demo/aperture.db"

services:
  - name: "quotes"
    hostregexp: '.*'
    pathregexp: '^/quote$'
    address: "127.0.0.1:8090"
    protocol: http
    price: 100
```

`listenaddr` is where Aperture itself listens, so callers use port 8081. Under
`services`, `address` is where your API runs (`quotes.py` on port 8090):
Aperture forwards paid requests matching `pathregexp` there, and `price` is in
sats.

Replace `/home/you` with your home directory (`echo $HOME`, for example
`/Users/you` on macOS); the paths must be absolute. `insecure: true` serves
plain HTTP on localhost; drop it and configure TLS for anything public.

In a new terminal, start Aperture:

```shell
aperture --configfile=$HOME/.aperture-demo/aperture.yaml
```

The log shows `minting invoices from http://localhost:10061`.

## 6. Ask for the API without paying

```shell
curl -i http://localhost:8081/quote
```

Aperture answers `402 Payment Required` with an L402 challenge, once under the
older LSAT name and once as L402:

```
HTTP/1.1 402 Payment Required
Www-Authenticate: LSAT macaroon="AgEEbHNhdA...", invoice="lntbs1u1p4..."
Www-Authenticate: L402 macaroon="AgEEbHNhdA...", invoice="lntbs1u1p4..."
```

The macaroon is the token you will use once you have paid, and the invoice
for 100 sats was created by the seller's Wavelength wallet.

## 7. Fund the buyer

In a new terminal, start a second wallet for the buyer on other ports:

```shell
waved --network=signet --datadir=$HOME/.waved-buyer \
  --wallet.type=lwwallet \
  --rpc.listenaddr=localhost:10039 \
  --rpc.gateway.listenaddr=localhost:10041
```

Back in your command terminal:

```shell
buyer() {
  wavecli --network=signet --datadir=$HOME/.waved-buyer \
    --rpcserver=localhost:10039 --timeout=3m "$@"
}

buyer create
buyer recv --onchain
```

`recv --onchain` prints a boarding address (`tb1p...`). Send it signet coins
from a signet faucet, for example https://signetfaucet.com or
https://alt.signetfaucet.com. Some faucets batch many payouts into one large
transaction and replace it until it confirms, so the first confirmation can
take a while.

Once the coins have confirmed, the wallet moves them into Ark by itself in the
operator's next round. When that round is confirmed, `balance` shows them under
`confirmed_sat`:

```shell
buyer balance
```

## 8. Pay and call the API

Ask for the API and keep the macaroon and invoice from the challenge:

```shell
CHALLENGE=$(curl -s -i http://localhost:8081/quote | grep -i '^www-authenticate: L402')
MACAROON=$(echo "$CHALLENGE" | sed -E 's/.*macaroon="([^"]+)".*/\1/')
INVOICE=$(echo "$CHALLENGE" | sed -E 's/.*invoice="([^"]+)".*/\1/')
```

Pay the invoice from the buyer and keep the preimage that `send` prints once
the payment completes:

```shell
buyer send "$INVOICE" --yes | tee send.json
PREIMAGE=$(sed -n 's/.*"preimage": "\([0-9a-f]*\)".*/\1/p' send.json)
```

```
"status": "COMPLETE",
"amount_sat": -100,
"fee_sat": 0,
"settlement": "CREDIT",
"preimage": "fdea60c9..."
```

Call the API again with the L402 credential, `<macaroon>:<preimage>`:

```shell
curl -H "Authorization: L402 $MACAROON:$PREIMAGE" http://localhost:8081/quote
```

```
{"quote": "Don't trust, verify."}
```

The same credential keeps working for later calls. To make access expire, set
`timeout` (in seconds) on the service in `aperture.yaml`.

Check the seller's side:

```shell
seller balance
```

The 100 sats show up as `credit_available_sat`. The signet operator's smallest
Ark coin is 1,000 sats, so smaller payments are not turned into a coin: the
Wavelength swap server keeps them as credit, a balance in its own ledger, until
they are spent or add up to enough for a coin. Unlike Ark coins, credit is held
by the server, so you trust it with those sats; see
[Credit System](https://github.com/lightninglabs/wavelength/blob/main/docs/credit_system.md).
Larger payments arrive as Ark coins. A buyer paying from any other Lightning
wallet works the same way from Aperture's point of view.

## How it works

When a request has no valid L402, Aperture mints a new token: it asks the
seller wallet for an invoice (`POST /v1/wallet/recv` on the gateway), binds
the macaroon to that invoice's payment hash and returns both in the
`WWW-Authenticate` header. Paying the invoice reveals its preimage. Aperture
accepts the request when the preimage hashes to the payment hash in the
macaroon and the macaroon's signature and caveats check out.

## Running on mainnet

The setup is the same on mainnet, with a few differences:

- Wavelength is under active development, and `waved` refuses to run on
  mainnet unless you pass `--allow-mainnet`. Read the
  [Wavelength README](https://github.com/lightninglabs/wavelength#project-status)
  before using it with real funds.
- Use `--network=mainnet` for both wallets and `network: "mainnet"` in
  Aperture's `authenticator` section. With no `server.host` or
  `swap.serveraddress`, `waved` then uses its built-in mainnet Ark and swap
  servers.
- Drop `insecure: true` and serve Aperture over TLS.

## Limitations

- `strictverify` is not supported with the Wavelength backend. Aperture
  refuses to start if it is set. Like the lnd backend's default, Aperture
  trusts that a valid preimage means the invoice was paid.
- The seller's balance lives in Ark, and payments below the operator's
  minimum coin size are held as credit by the swap server. See the Wavelength
  docs for how to move funds on-chain.
- `aperture.yaml` uses `databasefilename` under `sqlite`. The `dbfile` key
  shown in `sample-conf.yaml` is not read from the config file, and Aperture
  then falls back to `~/.aperture/aperture.db` even when `basedir` is set.
