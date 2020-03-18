# Lightning Service Authentication Token (LSAT) proxy

Aperture is your portal to the Lightning-Native Web. Aperture is used in
production today by [Lightning Loop](https://lightning.engineering/loop), a
non-custodial on/off ramp for the Lightning Network.

Aperture is a HTTP 402 reverse proxy that supports proxying requests for gRPC
(HTTP/2) and REST (HTTP/1 and HTTP/2) backends using the [LSAT Protocol
Standard](https://lsat.tech/). LSAT stands for: Lightning Service
Authentication Token. They combine HTTP 402, macaroons, and the Lightning
Network to create a new standard for authentication and paid servies on the
web.

LSATs are a new standard protocol for authentication and paid APIs developed by
Lightning Labs. LSATs can serve both as authentication, as well as a payment
mechanism (one can view it as a ticket) for paid APIs. In order to obtain a
token, we require the user to pay us over Lightning in order to obtain a
pre-image, which itself is a cryptographic component of the final LSAT token

The implementation of the authentication token is chosen to be macaroons, as
they allow us to package attributes and capabilities along with the token. This
system allows one to automate pricing on the fly and allows for a number of
novel constructs such as automated tier upgrades. In another light, this can be
viewed as a global HTTP 402 reverse proxy at the load balancing level for web
services and APIs.

## Installation / Setup

**lnd**

* Make sure lnd ports are reachable.

**aperture**

* Compilation requires go `1.13.x` or later.
* To build `aperture` in the current directory, run `make build` and then copy the
  file `./aperture` from the local directory to the server.
* To build and install `aperture` directly on the machine it will be used, run the
  `make install` command which will place the binary into your `$GOPATH/bin`
  folder.
* Make sure port `8081` is reachable from outside (or whatever port we choose,
  could also be 443 at some point)
* Make sure there is a valid `tls.cert` and `tls.key` file located in the
  `~/.aperture` directory that is valid for the domain that aperture is running on.
  Aperture doesn't support creating its own certificate through Let's Encrypt yet.
  If there is no `tls.cert` and `tls.key` found, a self-signed pair will be
  created.
* Make sure all required configuration items are set in `~/.aperture/aperture.yaml`,
  compare with `sample-conf.yaml`.
* Start aperture without any command line parameters (`./aperture`), all configuration
  is done in the `~/.aperture/aperture.yaml` file.

## Demo

There is a demo installation available at
[test-staging.swap.lightning.today:8081](https://test-staging.swap.lightning.today:8081).

### Use Case 1: Web GUI

If you visit the demo installation in the browser, you see a simple web GUI.
There you can request the current BOS scores for testnet. Notice that you can
only request the scores three times per IP addres. After the free requests have
been used up, you receive an LSAT token/macaroon and are challenged to pay an
invoice to authorize it.

You have two options to pay for the invoice:

1. If you have Joule installed in your browser and connected to a testnet node,
   you can click the "Pay invoice with Joule" button to pay the invoice. After
   successful payment the page should automatically refresh.
1. In case you want to pay the invoice manually, copy the payment request to
   your wallet of choice that has the feature to reveal the preimage after a
   successful payment. Copy the payment preimage in hex format, then click the
   button "Paste preimage of manual payment" and paste it in the dialog box.

### Use Case 2: cURL

First, let's request the BOS scores until we hit the freebie limit:
 
`curl -k -v https://test-staging.swap.lightning.today:8081/availability/v1/btc.json`
 
At some point, we will get an answer 402 with an authorization header:

```
www-authenticate: LSAT macaroon="...", invoice="lntb10n1..."
```

We will need both these values, the `macaroon` and the `invoice` so copy them
to a text file somewhere (without the single quotes!).
Let's pay the invoice now, choose any LN wallet that displays the preimage after
a successful payment. Copy the hex encoded preimage to the text file too once
you get it from the wallet.

Finally, you can issue the authenticated request with the following command:

```
curl -k -v \
--header "Authorization: LSAT <macaroon>:<preimage>" \
https://test-staging.swap.lightning.today:8081/availability/v1/btc.json
```
