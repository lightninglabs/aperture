module github.com/lightninglabs/aperture

go 1.15

require (
	github.com/btcsuite/btcd v0.21.0-beta.0.20201208033208-6bd4c64a54fa
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcutil v1.0.2
	github.com/btcsuite/btcwallet/wtxmgr v1.2.0
	github.com/fortytw2/leaktest v1.3.0
	github.com/golang/protobuf v1.4.3
	github.com/jonboulle/clockwork v0.2.0 // indirect
	github.com/json-iterator/go v1.1.10 // indirect
	github.com/lightninglabs/lndclient v0.12.0-4
	github.com/lightningnetwork/lnd v0.12.1-beta
	github.com/lightningnetwork/lnd/cert v1.0.3
	github.com/stretchr/testify v1.5.1
	github.com/tmc/grpc-websocket-proxy v0.0.0-20200122045848-3419fae592fc // indirect
	go.etcd.io/etcd v3.4.14+incompatible
	go.uber.org/zap v1.15.0 // indirect
	golang.org/x/crypto v0.0.0-20200709230013-948cd5f35899
	golang.org/x/net v0.0.0-20200520004742-59133d7f0dd7
	google.golang.org/grpc v1.29.1
	gopkg.in/macaroon.v2 v2.1.0
	gopkg.in/yaml.v2 v2.2.8
	sigs.k8s.io/yaml v1.2.0 // indirect
)

// Fix incompatibility of etcd go.mod package.
// See https://github.com/etcd-io/etcd/issues/11154
replace go.etcd.io/etcd => go.etcd.io/etcd v0.5.0-alpha.5.0.20201125193152-8a03d2e9614b
