# Lightning Service Authentication Token (LSAT) proxy

Aperture is your portal to the Lightning-Native Web. Aperture is used in
production today by [Lightning Loop](https://lightning.engineering/loop), a
non-custodial on/off ramp for the Lightning Network.

Aperture is a HTTP 402 reverse proxy that supports proxying requests for gRPC
(HTTP/2) and REST (HTTP/1 and HTTP/2) backends using the [LSAT Protocol
Standard](https://lsat.tech/). LSAT stands for: Lightning Service
Authentication Token. They combine HTTP 402, macaroons, and the Lightning
Network to create a new standard for authentication and paid services on the
web.

LSATs are a new standard protocol for authentication and paid APIs developed by
Lightning Labs. LSATs can serve both as authentication, as well as a payment
mechanism (one can view it as a ticket) for paid APIs. In order to obtain a
token, we require the user to pay us over Lightning in order to obtain a
preimage, which itself is a cryptographic component of the final LSAT token

The implementation of the authentication token is chosen to be macaroons, as
they allow us to package attributes and capabilities along with the token. This
system allows one to automate pricing on the fly and allows for a number of
novel constructs such as automated tier upgrades. In another light, this can be
viewed as a global HTTP 402 reverse proxy at the load balancing level for web
services and APIs.

## Installation / Setup

**lnd**

* Make sure `lnd` ports are reachable.

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

```
