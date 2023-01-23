module github.com/lightninglabs/aperture

go 1.15

require (
	github.com/btcsuite/btcd v0.23.4
	github.com/btcsuite/btcd/btcec/v2 v2.2.2
	github.com/btcsuite/btcd/btcutil v1.1.3
	github.com/btcsuite/btcd/chaincfg/chainhash v1.0.2
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f
	github.com/btcsuite/btcwallet/wtxmgr v1.5.0
	github.com/fortytw2/leaktest v1.3.0
	github.com/golang/protobuf v1.5.2
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.5.0
	github.com/jessevdk/go-flags v1.4.0
	github.com/lightninglabs/lightning-node-connect/hashmailrpc v1.0.2
	github.com/lightninglabs/lndclient v0.16.0-6
	github.com/lightningnetwork/lnd v0.15.0-beta.rc6.0.20221207163254-a0385a535b66
	github.com/lightningnetwork/lnd/cert v1.1.1
	github.com/lightningnetwork/lnd/tlv v1.1.0
	github.com/lightningnetwork/lnd/tor v1.1.0
	github.com/prometheus/client_golang v1.11.0
	github.com/stretchr/testify v1.8.0
	go.etcd.io/etcd/client/v3 v3.5.1
	go.etcd.io/etcd/server/v3 v3.5.1
	golang.org/x/crypto v0.0.0-20211215153901-e495a2d5b3d3
	golang.org/x/net v0.0.0-20211216030914-fe4d6282115f
	golang.org/x/time v0.0.0-20210220033141-f8bda1e9f3ba
	google.golang.org/grpc v1.39.0
	google.golang.org/protobuf v1.27.1
	gopkg.in/macaroon.v2 v2.1.0
	gopkg.in/yaml.v2 v2.4.0
)

// Fix etcd token renewal issue https://github.com/etcd-io/etcd/pull/13262.
replace go.etcd.io/etcd/client/v3 => github.com/lightninglabs/etcd/client/v3 v3.5.1-retry-patch
