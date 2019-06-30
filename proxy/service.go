package proxy

// Service generically specifies configuration data for backend services to the
// Kirin proxy.
type Service struct {
	// TLSCertPath is the optional path to the service's TLS certificate.
	TLSCertPath string `long:"tlscertpath" description:"Path to the service's TLS certificate"`

	// Address is the service's IP address and port.
	Address string `long:"address" description:"lnd instance rpc address"`

	// FQDN is the FQDN of the service.
	FQDN string `long:"fqdn" description:"FQDN of the service."`
}
