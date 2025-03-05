package proxy

import (
	"fmt"
	"net"

	"github.com/btcsuite/btclog/v2"
	"github.com/lightningnetwork/lnd/build"
)

const Subsystem = "PRXY"

// log is a logger that is initialized with no output filters.  This
// means the package will not perform any logging by default until the caller
// requests it.
var log btclog.Logger

// The default amount of logging is none.
func init() {
	UseLogger(build.NewSubLogger(Subsystem, nil))
}

// UseLogger uses a specified Logger to output package logging info.
// This should be used in preference to SetLogWriter if the caller is also
// using btclog.
func UseLogger(logger btclog.Logger) {
	log = logger
}

// PrefixLog logs with a given static string prefix.
type PrefixLog struct {
	logger btclog.Logger
	prefix string
}

// NewRemoteIPPrefixLog returns a new prefix logger that logs the remote IP
// address.
func NewRemoteIPPrefixLog(logger btclog.Logger, remoteAddr string) (net.IP,
	*PrefixLog) {

	remoteHost, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		remoteHost = "0.0.0.0"
	}
	remoteIP := net.ParseIP(remoteHost)
	if remoteIP == nil {
		remoteIP = net.IPv4zero
	}
	return remoteIP, &PrefixLog{
		logger: logger,
		prefix: remoteIP.String(),
	}
}

// Debugf formats message according to format specifier and writes to
// log with LevelDebug.
func (s *PrefixLog) Debugf(format string, params ...interface{}) {
	s.logger.Debugf(
		fmt.Sprintf("%s %s", s.prefix, format),
		params...,
	)
}

// Infof formats message according to format specifier and writes to
// log with LevelInfo.
func (s *PrefixLog) Infof(format string, params ...interface{}) {
	s.logger.Infof(
		fmt.Sprintf("%s %s", s.prefix, format),
		params...,
	)
}

// Warnf formats message according to format specifier and writes to
// to log with LevelError.
func (s *PrefixLog) Warnf(format string, params ...interface{}) {
	s.logger.Warnf(
		fmt.Sprintf("%s %s", s.prefix, format),
		params...,
	)
}

// Errorf formats message according to format specifier and writes to
// to log with LevelError.
func (s *PrefixLog) Errorf(format string, params ...interface{}) {
	s.logger.Errorf(
		fmt.Sprintf("%s %s", s.prefix, format),
		params...,
	)
}
