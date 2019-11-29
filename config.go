package kirin

import (
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/kirin/proxy"
)

var (
	kirinDataDir           = btcutil.AppDataDir("kirin", false)
	defaultConfigFilename  = "kirin.yaml"
	defaultTLSKeyFilename  = "tls.key"
	defaultTLSCertFilename = "tls.cert"
	defaultLogLevel        = "info"
	defaultLogFilename     = "kirin.log"
	defaultMaxLogFiles     = 3
	defaultMaxLogFileSize  = 10
)

type etcdConfig struct {
	Host     string `long:"host" description:"host:port of an active etcd instance"`
	User     string `long:"user" description:"user authorized to access the etcd host"`
	Password string `long:"password" description:"password of the etcd user"`
}

type authConfig struct {
	// LndHost is the hostname of the LND instance to connect to.
	LndHost string `long:"lndhost" description:"Hostname of the LND instance to connect to"`

	TLSPath string `long:"tlspath"`

	MacDir string `long:"macdir"`

	Network string `long:"network"`
}

type config struct {
	// ListenAddr is the listening address that we should use to allow Kirin
	// to listen for requests.
	ListenAddr string `long:"listenaddr" description:"The interface we should listen on for client requests"`

	// StaticRoot is the folder where the static content served by the proxy
	// is located.
	StaticRoot string `long:"staticroot" description:"The folder where the static content is located."`

	Etcd *etcdConfig `long:"etcd" description:"Configuration for the etcd instance backing the proxy."`

	Authenticator *authConfig `long:"authenticator" description:"Configuration for the authenticator."`

	// Services is a list of JSON objects in string format, which specify
	// each backend service to Kirin.
	Services []*proxy.Service `long:"service" description:"Configurations for each Kirin backend service."`

	// DebugLevel is a string defining the log level for the service either
	// for all subsystems the same or individual level by subsystem.
	DebugLevel string `long:"debuglevel" description:"Debug level for the Kirin application and its subsystems."`
}
