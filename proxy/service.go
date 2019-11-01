package proxy

import "github.com/lightninglabs/kirin/auth"

// Service generically specifies configuration data for backend services to the
// Kirin proxy.
type Service struct {
	// TLSCertPath is the optional path to the service's TLS certificate.
	TLSCertPath string `long:"tlscertpath" description:"Path to the service's TLS certificate"`

	// Address is the service's IP address and port.
	Address string `long:"address" description:"service instance rpc address"`

	// Protocol is the protocol that should be used to connect to the
	// service. Currently supported is http and https.
	Protocol string `long:"protocol" description:"service instance protocol"`

	// Auth is the authentication level required for this service to be
	// accessed. Valid values are "on" for full authentication, "freebie X"
	// for X free requests per IP address before authentication is required
	// or "off" for no authentication.
	Auth auth.Level `long:"auth" description:"required authentication"`

	// HostRegexp is a regular expression that is tested against the 'Host'
	// HTTP header field to find out if this service should be used.
	HostRegexp string `long:"hostregexp" description:"Regular expression to match the host against"`

	// PathRegexp is a regular expression that is tested against the path
	// of the URL of a request to find out if this service should be used.
	PathRegexp string `long:"pathregexp" description:"Regular expression to match the path of the URL against"`
}
