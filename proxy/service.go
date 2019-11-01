package proxy

// Service generically specifies configuration data for backend services to the
// Kirin proxy.
type Service struct {
	// TLSCertPath is the optional path to the service's TLS certificate.
	TLSCertPath string `long:"tlscertpath" description:"Path to the service's TLS certificate"`

	// Address is the service's IP address and port.
	Address string `long:"address" description:"lnd instance rpc address"`

	// HostRegexp is a regular expression that is tested against the 'Host'
	// HTTP header field to find out if this service should be used.
	HostRegexp string `long:"hostregexp" description:"Regular expression to match the host against"`

	// PathRegexp is a regular expression that is tested against the path
	// of the URL of a request to find out if this service should be used.
	PathRegexp string `long:"pathregexp" description:"Regular expression to match the path of the URL against"`
}
