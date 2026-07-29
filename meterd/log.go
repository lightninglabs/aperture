package meterd

import (
	"os"

	"github.com/btcsuite/btclog/v2"
)

// Subsystem is the logging tag of the meterd daemon.
const Subsystem = "METR"

// log is the package level logger. It writes to stdout by default and can be
// overridden through UseLogger.
var log = btclog.NewSLogger(
	btclog.NewDefaultHandler(os.Stdout).SubSystem(Subsystem),
)

// UseLogger overrides the package level logger, for example to silence the
// daemon from tests.
func UseLogger(logger btclog.Logger) {
	log = logger
}
