package aperture

import (
	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/build"
)

const Subsystem = "APER"

var (
	logWriter = build.NewRotatingLogWriter()

	log = build.NewSubLogger(Subsystem, logWriter.GenSubLogger)
)

func init() {
	setSubLogger(Subsystem, log, nil)
	addSubLogger(auth.Subsystem, auth.UseLogger)
	addSubLogger(lsat.Subsystem, lsat.UseLogger)
	addSubLogger(proxy.Subsystem, proxy.UseLogger)
	addSubLogger("LNDC", lndclient.UseLogger)
}

// addSubLogger is a helper method to conveniently create and register the
// logger of a sub system.
func addSubLogger(subsystem string, useLogger func(btclog.Logger)) {
	logger := build.NewSubLogger(subsystem, logWriter.GenSubLogger)
	setSubLogger(subsystem, logger, useLogger)
}

// setSubLogger is a helper method to conveniently register the logger of a sub
// system.
func setSubLogger(subsystem string, logger btclog.Logger,
	useLogger func(btclog.Logger)) {

	logWriter.RegisterSubLogger(subsystem, logger)
	if useLogger != nil {
		useLogger(logger)
	}
}
