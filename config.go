package aperture

import (
	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture/proxy"
)

var (
	apertureDataDir        = btcutil.AppDataDir("aperture", false)
	defaultConfigFilename  = "aperture.yaml"
	defaultTLSKeyFilename  = "tls.key"
	defaultTLSCertFilename = "tls.cert"
	defaultLogLevel        = "info"
	defaultLogFilename     = "aperture.log"
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

type torConfig struct {
	Control     string `long:"control" description:"The host:port of the Tor instance."`
	ListenPort  uint16 `long:"listenport" description:"The port we should listen on for client requests over Tor. Note that this port should not be exposed to the outside world, it is only intended to be reached by clients through the onion service."`
	VirtualPort uint16 `long:"virtualport" description:"The port through which the onion services created can be reached at."`
	V2          bool   `long:"v2" description:"Whether we should listen for client requests through a v2 onion service."`
	V3          bool   `long:"v3" description:"Whether we should listen for client requests through a v3 onion service."`
}

type config struct {
	// ListenAddr is the listening address that we should use to allow Aperture
	// to listen for requests.
	ListenAddr string `long:"listenaddr" description:"The interface we should listen on for client requests."`

	// ServerName can be set to a fully qualifying domain name that should
	// be used while creating a certificate through Let's Encrypt.
	ServerName string `long:"servername" description:"Server name (FQDN) to use for the TLS certificate."`

	// AutoCert can be set to true if aperture should try to create a valid
	// certificate through Let's Encrypt using ServerName.
	AutoCert bool `long:"autocert" description:"Automatically create a Let's Encrypt cert using ServerName."`

	// Insecure can be set to disable TLS on incoming connections.
	Insecure bool `long:"insecure" description:"Listen on an insecure connection, disabling TLS for incoming connections."`

	// StaticRoot is the folder where the static content served by the proxy
	// is located.
	StaticRoot string `long:"staticroot" description:"The folder where the static content is located."`

	// ServeStatic defines if static content should be served from the
	// directory defined by StaticRoot.
	ServeStatic bool `long:"servestatic" description:"Flag to enable or disable static content serving."`

	Etcd *etcdConfig `long:"etcd" description:"Configuration for the etcd instance backing the proxy."`

	Authenticator *authConfig `long:"authenticator" description:"Configuration for the authenticator."`

	Tor *torConfig `long:"tor" description:"Configuration for the Tor instance backing the proxy."`

	// Services is a list of JSON objects in string format, which specify
	// each backend service to Aperture.
	Services []*proxy.Service `long:"service" description:"Configurations for each Aperture backend service."`

	// DebugLevel is a string defining the log level for the service either
	// for all subsystems the same or individual level by subsystem.
	DebugLevel string `long:"debuglevel" description:"Debug level for the Aperture application and its subsystems."`
}
