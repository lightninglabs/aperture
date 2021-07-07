package proxy

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"

	"github.com/btcsuite/btcutil"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/freebie"
	"github.com/lightninglabs/aperture/pricer"
)

var (
	filePrefix       = "!file"
	filePrefixHex    = filePrefix + "+hex"
	filePrefixBase64 = filePrefix + "+base64"
)

const (
	// defaultServicePrice is price in satoshis to be used as the default
	// service price.
	defaultServicePrice = 1

	// maxServicePrice is the maximum price in satoshis that can be used
	// to create an invoice through lnd.
	maxServicePrice = btcutil.SatoshiPerBitcoin * 100000
)

// Service generically specifies configuration data for backend services to the
// Aperture proxy.
type Service struct {
	// Name is the name of the LSAT-enabled service.
	Name string `long:"name" description:"Name of the LSAT-enabled service"`

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

	// Headers is a map of strings that defines header name and values that
	// should always be passed to the backend service, overwriting any
	// headers with the same name that might have been set by the client
	// request.
	// If the value of a header field starts with the prefix "!file+hex:",
	// the rest of the value is treated as a path to a file and the content
	// of that file is sent to the backend with each call (hex encoded).
	// If the value starts with the prefix "!file+base64:", the content of
	// the file is sent encoded as base64.
	Headers map[string]string `long:"headers" description:"Header fields to always pass to the service"`

	// Capabilities is the list of capabilities authorized for the service
	// at the base tier.
	Capabilities string `long:"capabilities" description:"A comma-separated list of the service capabilities authorized for the base tier"`

	// Constraints is the set of constraints that will take form of caveats.
	// They'll be enforced for a service at the base tier. The key should
	// correspond to the caveat's condition.
	Constraints map[string]string `long:"constraints" description:"The service constraints to enforce at the base tier"`

	// Price is the custom LSAT value in satoshis to be used for the
	// service's endpoint.
	Price int64 `long:"price" description:"Static LSAT value in satoshis to be used for this service"`

	// DynamicPrice holds the config options needed for initialising
	// the pricer if a gPRC server is to be used for price data.
	DynamicPrice pricer.Config `long:"dynamicprice" description:"Configuration for connecting to the gRPC server to use for the pricer backend"`

	// AuthWhitelistPaths is an optional list of regular expressions that
	// are matched against the path of the URL of a request. If the request
	// URL matches any of those regular expressions, the call is treated as
	// if Auth was set to "off". This allows certain RPC methods to not
	// require an LSAT token. E.g. the path for a gRPC call looks like this:
	// /package_name.ServiceName/MethodName
	AuthWhitelistPaths []string `long:"authwhitelistpaths" description:"List of regular expressions for paths that don't require authentication'"`

	freebieDb freebie.DB
	pricer    pricer.Pricer
}

// ResourceName returns the string to be used to identify which resource a
// macaroon has access to. If DynamicPrice Enabled option is set to true then
// the service has further restrictions per resource and so the name will
// include both the service name and the specific resource name. Otherwise
// authorisation is only restricted by service name.
func (s *Service) ResourceName(resourcePath string) string {
	if s.DynamicPrice.Enabled {
		return fmt.Sprintf("%s%s", s.Name, resourcePath)
	}

	return s.Name
}

// AuthRequired determines the auth level required for a given request.
func (s *Service) AuthRequired(r *http.Request) auth.Level {
	// Does the request match any whitelist entry?
	for _, pathRegexp := range s.AuthWhitelistPaths {
		pathRegexp := regexp.MustCompile(pathRegexp)
		if pathRegexp.MatchString(r.URL.Path) {
			log.Tracef("Req path [%s] matches whitelist entry "+
				"[%s].", r.URL.Path, pathRegexp)
			return auth.LevelOff
		}
	}

	// By default we always return the service level auth setting.
	return s.Auth
}

// prepareServices prepares the backend service configurations to be used by the
// proxy.
func prepareServices(services []*Service) error {
	for _, service := range services {
		// Each freebie enabled service gets its own store.
		if service.Auth.IsFreebie() {
			service.freebieDb = freebie.NewMemIPMaskStore(
				service.Auth.FreebieCount(),
			)
		}

		// Replace placeholders/directives in the header fields with the
		// actual desired values.
		for key, value := range service.Headers {
			if !strings.HasPrefix(value, filePrefix) {
				continue
			}

			parts := strings.Split(value, ":")
			if len(parts) != 2 {
				return fmt.Errorf("invalid header config, " +
					"must be '!file+hex:path'")
			}
			prefix, fileName := parts[0], parts[1]
			bytes, err := ioutil.ReadFile(fileName)
			if err != nil {
				return err
			}

			// There are two supported formats to encode the file
			// content in: hex and base64.
			switch {
			case prefix == filePrefixHex:
				newValue := hex.EncodeToString(bytes)
				service.Headers[key] = newValue

			case prefix == filePrefixBase64:
				newValue := base64.StdEncoding.EncodeToString(
					bytes,
				)
				service.Headers[key] = newValue

			default:
				return fmt.Errorf("unsupported file prefix "+
					"format %s", value)
			}
		}

		// Make sure all whitelist regular expression entries actually
		// compile so we run into an eventual panic during startup and
		// not only when the request happens.
		for _, entry := range service.AuthWhitelistPaths {
			_, err := regexp.Compile(entry)
			if err != nil {
				return fmt.Errorf("error validating auth "+
					"whitelist: %v", err)
			}
		}

		// If dynamic prices are enabled then use the provided
		// DynamicPrice options to initialise a gRPC backed
		// pricer client.
		if service.DynamicPrice.Enabled {
			priceClient, err := pricer.NewGRPCPricer(
				&service.DynamicPrice,
			)
			if err != nil {
				return fmt.Errorf("error initializing "+
					"pricer: %v", err)
			}

			service.pricer = priceClient
			continue
		}

		// Check that the price for the service is not negative and not
		// more than the maximum amount allowed by lnd. If no price, or
		// a price of zero satoshis, is set the then default price of 1
		// satoshi is to be used.
		switch {
		case service.Price == 0:
			log.Debugf("Using default LSAT price of %v satoshis for "+
				"service %s.", defaultServicePrice, service.Name)
			service.Price = defaultServicePrice
		case service.Price < 0:
			return fmt.Errorf("negative price set for "+
				"service %s", service.Name)
		case service.Price > maxServicePrice:
			return fmt.Errorf("maximum price exceeded for "+
				"service %s", service.Name)
		}

		// Initialise a default pricer where all resources in a server
		// are given the same price.
		service.pricer = pricer.NewDefaultPricer(service.Price)
	}
	return nil
}
