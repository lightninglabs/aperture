package aperture

import (
	"errors"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/btcutil"
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

type EtcdConfig struct {
	Host     string `long:"host" description:"host:port of an active etcd instance"`
	User     string `long:"user" description:"user authorized to access the etcd host"`
	Password string `long:"password" description:"password of the etcd user"`
}

type AuthConfig struct {
	// LndHost is the hostname of the LND instance to connect to.
	LndHost string `long:"lndhost" description:"Hostname of the LND instance to connect to"`

	TLSPath string `long:"tlspath" description:"Path to LND instance's tls certificate"`

	MacDir string `long:"macdir" description:"Directory containing LND instance's macaroons"`

	Network string `long:"network" description:"The network LND is connected to." choice:"regtest" choice:"simnet" choice:"testnet" choice:"mainnet"`

	Disable bool `long:"disable" description:"Whether to disable LND auth."`
}

func (a *AuthConfig) validate() error {
	// If we're disabled, we don't mind what these values are.
	if a.Disable {
		return nil
	}

	if a.LndHost == "" {
		return errors.New("lnd host required")
	}

	if a.TLSPath == "" {
		return errors.New("lnd tls required")
	}

	if a.MacDir == "" {
		return errors.New("lnd mac dir required")
	}

	return nil
}

type HashMailConfig struct {
	Enabled               bool          `long:"enabled"`
	MessageRate           time.Duration `long:"messagerate" description:"The average minimum time that should pass between each message."`
	MessageBurstAllowance int           `long:"messageburstallowance" description:"The burst rate we allow for messages."`
}

type TorConfig struct {
	Control     string `long:"control" description:"The host:port of the Tor instance."`
	ListenPort  uint16 `long:"listenport" description:"The port we should listen on for client requests over Tor. Note that this port should not be exposed to the outside world, it is only intended to be reached by clients through the onion service."`
	VirtualPort uint16 `long:"virtualport" description:"The port through which the onion services created can be reached at."`
	V2          bool   `long:"v2" description:"Whether we should listen for client requests through a v2 onion service."`
	V3          bool   `long:"v3" description:"Whether we should listen for client requests through a v3 onion service."`
}

type Config struct {
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

	Etcd *EtcdConfig `group:"etcd" namespace:"etcd"`

	Authenticator *AuthConfig `group:"authenticator" namespace:"authenticator"`

	Tor *TorConfig `group:"tor" namespace:"tor"`

	// Services is a list of JSON objects in string format, which specify
	// each backend service to Aperture.
	Services []*proxy.Service `long:"service" description:"Configurations for each Aperture backend service."`

	// HashMail is the configuration section for configuring the Lightning
	// Node Connect mailbox server.
	HashMail *HashMailConfig `group:"hashmail" namespace:"hashmail" description:"Configuration for the Lightning Node Connect mailbox server."`

	// Prometheus is the config for setting up an endpoint for a Prometheus
	// server to scrape metrics from.
	Prometheus *PrometheusConfig `group:"prometheus" namespace:"prometheus" description:"Configuration setting up an endpoint that a Prometheus server can scrape."`

	// DebugLevel is a string defining the log level for the service either
	// for all subsystems the same or individual level by subsystem.
	DebugLevel string `long:"debuglevel" description:"Debug level for the Aperture application and its subsystems."`

	// ConfigFile points aperture to an alternative config file.
	ConfigFile string `long:"configfile" description:"Custom path to a config file."`

	// BaseDir is a custom directory to store all aperture flies.
	BaseDir string `long:"basedir" description:"Directory to place all of aperture's files in."`
}

func (c *Config) validate() error {
	if err := c.Authenticator.validate(); err != nil {
		return err
	}

	if c.ListenAddr == "" {
		return fmt.Errorf("missing listen address for server")
	}

	return nil
}

// NewConfig initializes a new Config variable.
func NewConfig() *Config {
	return &Config{
		Etcd:          &EtcdConfig{},
		Authenticator: &AuthConfig{},
		Tor:           &TorConfig{},
		HashMail:      &HashMailConfig{},
		Prometheus:    &PrometheusConfig{},
	}
}
