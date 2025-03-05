package test

import (
	"os"

	"github.com/btcsuite/btclog/v2"
)

// log is a logger that is initialized with no output filters.  This
// means the package will not perform any logging by default until the caller
// requests it.
var (
	backendLog = btclog.NewDefaultHandler(logWriter{}).SubSystem("TEST")
	logger     = btclog.NewSLogger(backendLog)
)

// logWriter implements an io.Writer that outputs to both standard output and
// the write-end pipe of an initialized log rotator.
type logWriter struct{}

func (logWriter) Write(p []byte) (n int, err error) {
	os.Stdout.Write(p)
	return len(p), nil
}
