package kirin

import (
	"github.com/btcsuite/btclog"
	"github.com/lightninglabs/kirin/auth"
	"github.com/lightninglabs/kirin/proxy"
	"github.com/lightningnetwork/lnd/build"
)

var (
	logWriter = build.NewRotatingLogWriter()

	log = build.NewSubLogger("MAIN", logWriter.GenSubLogger)
)

func init() {
	setSubLogger("MAIN", log, nil)
	addSubLogger("AUTH", auth.UseLogger)
	addSubLogger("PRXY", proxy.UseLogger)
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
