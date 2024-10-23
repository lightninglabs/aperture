package aperture

import (
	"github.com/btcsuite/btclog/v2"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/challenger"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/signal"
)

const Subsystem = "APER"

var (
	logWriter = build.NewRotatingLogWriter()
	log       = build.NewSubLogger(Subsystem, nil)
)

// SetupLoggers initializes all package-global logger variables.
func SetupLoggers(root *build.SubLoggerManager, intercept signal.Interceptor) {
	genLogger := genSubLogger(root, intercept)

	log = build.NewSubLogger(Subsystem, genLogger)

	lnd.SetSubLogger(root, Subsystem, log)
	lnd.AddSubLogger(root, auth.Subsystem, intercept, auth.UseLogger)
	lnd.AddSubLogger(root, l402.Subsystem, intercept, l402.UseLogger)
	lnd.AddSubLogger(root, proxy.Subsystem, intercept, proxy.UseLogger)
	lnd.AddSubLogger(root, "LNDC", intercept, lndclient.UseLogger)
	lnd.AddSubLogger(
		root, challenger.Subsystem, intercept, challenger.UseLogger,
	)
}

// genSubLogger creates a logger for a subsystem. We provide an instance of
// a signal.Interceptor to be able to shutdown in the case of a critical error.
func genSubLogger(root *build.SubLoggerManager,
	interceptor signal.Interceptor) func(string) btclog.Logger {

	// Create a shutdown function which will request shutdown from our
	// interceptor if it is listening.
	shutdown := func() {
		if !interceptor.Listening() {
			return
		}

		interceptor.RequestShutdown()
	}

	// Return a function which will create a sublogger from our root
	// logger without shutdown fn.
	return func(tag string) btclog.Logger {
		return root.GenSubLogger(tag, shutdown)
	}
}
