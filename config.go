package kirin

import (
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/kirin/proxy"
)

var (
	kirinDataDir           = btcutil.AppDataDir("kirin", false)
	defaultConfigFilename  = "kirin.yaml"
	defaultTLSKeyFilename  = "tls.key"
	defaultTLSCertFilename = "tls.crt"
)

type config struct {
	// ListenAddr is the listening address that we should use to allow Kirin
	// to listen for requests.
	ListenAddr string `long:"listenaddr" description:"The interface we should listen on for client requests"`

	// Services is a list of JSON objects in string format, which specify
	// each backend service to Kirin.
	Services []*proxy.Service `long:"service" description:"JSON configurations for each Kirin backend service."`
}
