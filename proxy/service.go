package proxy

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/btcsuite/btcd/btcutil/v2"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/freebie"
	"github.com/lightninglabs/aperture/pricer"
)

var (
	filePrefix       = "!file"
	filePrefixHex    = filePrefix + "+hex"
	filePrefixBase64 = filePrefix + "+base64"

	// headerEnvRefRegexp matches ${NAME} environment references in header
	// values. Only the braced form is recognized, so literal dollar signs
	// in header values keep working.
	headerEnvRefRegexp = regexp.MustCompile(
		`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`,
	)
)

const (
	// defaultServicePrice is price in satoshis to be used as the default
	// service price.
	defaultServicePrice = 1

	// maxServicePrice is the maximum price in satoshis that can be used
	// to create an invoice through lnd.
	maxServicePrice = btcutil.SatoshiPerBitcoin * 100000
)

// RewriteConfig defines what should be rewritten in a proxied client request.
type RewriteConfig struct {
	// Prefix is an absolute path that is prepended to the request path
	// before forwarding to the backend service.
	Prefix string `long:"prefix" description:"Absolute path prefix to prepend to the request path"`
}

// Service generically specifies configuration data for backend services to the
// Aperture proxy.
type Service struct {
	// Name is the name of the L402-enabled service.
	Name string `long:"name" description:"Name of the L402-enabled service"`

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

	// AuthScheme specifies which payment authentication scheme(s) to use
	// for this service. Valid values are "l402" (default), "mpp", or
	// "l402+mpp". An empty value defaults to "l402" for backwards
	// compatibility with existing deployments.
	AuthScheme string `long:"authscheme" description:"Payment auth scheme: l402, mpp, or l402+mpp"`

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
	// References of the form ${NAME} are replaced with the value of the
	// environment variable NAME at startup, so secrets like upstream API
	// keys can stay out of the config file (e.g. "Bearer ${UPSTREAM_KEY}").
	// A reference to an unset or empty variable fails startup.
	Headers map[string]string `long:"headers" description:"Header fields to always pass to the service"`

	// Timeout is an optional value that indicates in how many seconds the
	// service's caveat should time out relative to the time of creation. So
	// if a value of 100 is set, then the timeout will be 100 seconds
	// after creation of the L402.
	Timeout int64 `long:"timeout" description:"An integer value that indicates the number of seconds until the service access expires"`

	// Capabilities is the list of capabilities authorized for the service
	// at the base tier.
	Capabilities string `long:"capabilities" description:"A comma-separated list of the service capabilities authorized for the base tier"`

	// Constraints is the set of constraints that will take form of caveats.
	// They'll be enforced for a service at the base tier. The key should
	// correspond to the caveat's condition.
	Constraints map[string]string `long:"constraints" description:"The service constraints to enforce at the base tier"`

	// Price is the custom L402 value in satoshis to be used for the
	// service's endpoint.
	Price int64 `long:"price" description:"Static L402 value in satoshis to be used for this service"`

	// DynamicPrice holds the config options needed for initialising
	// the pricer if a gPRC server is to be used for price data.
	DynamicPrice pricer.Config `long:"dynamicprice" description:"Configuration for connecting to the gRPC server to use for the pricer backend"`

	// AuthWhitelistPaths is an optional list of regular expressions that
	// are matched against the path of the URL of a request. If the request
	// URL matches any of those regular expressions, the call is treated as
	// if Auth was set to "off". This allows certain RPC methods to not
	// require an L402 token. E.g. the path for a gRPC call looks like this:
	// /package_name.ServiceName/MethodName
	AuthWhitelistPaths []string `long:"authwhitelistpaths" description:"List of regular expressions for paths that don't require authentication'"`

	// AuthSkipInvoiceCreationPaths is an optional list of regular
	// expressions that are matched against the path of the URL of a
	// request. If the request URL matches any of those regular
	// expressions, the call will not try to create an invoice for the
	// request, but still try to do the l402 authentication.
	AuthSkipInvoiceCreationPaths []string `long:"authskipinvoicecreationpaths" description:"List of regular expressions for paths that will skip invoice creation'"`

	// RateLimits is an optional list of rate-limiting rules for this
	// service. Each rule specifies a path pattern and rate limit
	// parameters. All matching rules are evaluated; if any rule denies
	// the request, it is rejected.
	RateLimits []*RateLimitConfig `long:"ratelimits" description:"List of rate limiting rules for this service"`

	// Rewrite defines what should be rewritten in the client request.
	Rewrite RewriteConfig `long:"rewrite" description:"Values to rewrite in the client request"`

	// compiledHostRegexp is the compiled host regex.
	compiledHostRegexp *regexp.Regexp

	// compiledPathRegexp is the compiled path regex.
	compiledPathRegexp *regexp.Regexp

	// compiledAuthWhitelistPaths is the compiled auth whitelist paths.
	compiledAuthWhitelistPaths []*regexp.Regexp

	// compiledAuthSkipInvoiceCreationPaths is the compiled auth skip
	// invoice creation paths.
	compiledAuthSkipInvoiceCreationPaths []*regexp.Regexp

	freebieDB   freebie.DB
	pricer      pricer.Pricer
	rateLimiter *RateLimiter
}

// cloneServices returns a deep copy of service configuration data. Runtime
// state is deliberately not copied: each prepared snapshot owns its compiled
// expressions, pricer, freebie store and rate limiter. This lets UpdateServices
// prepare a new snapshot without mutating configuration that may still be in
// use by an active request.
func cloneServices(services []*Service) ([]*Service, error) {
	clones := make([]*Service, len(services))
	for i, service := range services {
		if service == nil {
			return nil, fmt.Errorf("service %d: configuration must not be nil",
				i)
		}

		clone := *service
		clone.Headers = cloneStringMap(service.Headers)
		clone.Constraints = cloneStringMap(service.Constraints)
		clone.AuthWhitelistPaths = append(
			[]string(nil), service.AuthWhitelistPaths...,
		)
		clone.AuthSkipInvoiceCreationPaths = append(
			[]string(nil), service.AuthSkipInvoiceCreationPaths...,
		)

		clone.RateLimits = make(
			[]*RateLimitConfig, len(service.RateLimits),
		)
		for j, config := range service.RateLimits {
			if config == nil {
				continue
			}

			configClone := *config
			configClone.compiledPathRegexp = nil
			clone.RateLimits[j] = &configClone
		}

		clone.compiledHostRegexp = nil
		clone.compiledPathRegexp = nil
		clone.compiledAuthWhitelistPaths = nil
		clone.compiledAuthSkipInvoiceCreationPaths = nil
		clone.freebieDB = nil
		clone.pricer = nil
		clone.rateLimiter = nil

		clones[i] = &clone
	}

	return clones, nil
}

// cloneStringMap copies a string map while preserving nil maps.
func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}

	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}

	return clone
}

// copyPreparedServiceConfig restores the caller-visible preparation effects
// that UpdateServices historically applied in place. Runtime objects stay
// private to the published snapshot, while successful updates still
// materialize headers, normalize prices and make exported matching helpers work
// on the caller's configuration. This function performs no fallible work and
// must only be called after the prepared snapshot has passed all validation.
func copyPreparedServiceConfig(configured, prepared []*Service) {
	for i, preparedService := range prepared {
		configuredService := configured[i]

		copyStringMapInPlace(
			&configuredService.Headers, preparedService.Headers,
		)
		if configuredService.Price != preparedService.Price {
			configuredService.Price = preparedService.Price
		}
		if configuredService.Rewrite != preparedService.Rewrite {
			configuredService.Rewrite = preparedService.Rewrite
		}

		copyCompiledRegexp(
			&configuredService.compiledHostRegexp,
			preparedService.compiledHostRegexp,
		)
		copyCompiledRegexp(
			&configuredService.compiledPathRegexp,
			preparedService.compiledPathRegexp,
		)
		copyCompiledRegexpSlice(
			&configuredService.compiledAuthWhitelistPaths,
			preparedService.compiledAuthWhitelistPaths,
		)
		copyCompiledRegexpSlice(
			&configuredService.compiledAuthSkipInvoiceCreationPaths,
			preparedService.compiledAuthSkipInvoiceCreationPaths,
		)

		for j, preparedRule := range preparedService.RateLimits {
			copyCompiledRegexp(
				&configuredService.RateLimits[j].compiledPathRegexp,
				preparedRule.compiledPathRegexp,
			)
		}
	}
}

// copyStringMapInPlace copies source into target while retaining the caller's
// map identity. Successful service preparation historically materialized
// header directives in that map.
func copyStringMapInPlace(target *map[string]string, source map[string]string) {
	if maps.Equal(*target, source) {
		return
	}
	if *target == nil {
		*target = cloneStringMap(source)
		return
	}

	for key := range *target {
		delete(*target, key)
	}
	for key, value := range source {
		(*target)[key] = value
	}
}

// copyCompiledRegexp avoids rewriting an unchanged caller-owned service during
// unrelated updates. That keeps copy-back idempotent for configurations shared
// with the admin service holder.
func copyCompiledRegexp(target **regexp.Regexp, source *regexp.Regexp) {
	if regexpsEqual(*target, source) {
		return
	}

	*target = source
}

// copyCompiledRegexpSlice copies compiled patterns only when their expressions
// changed.
func copyCompiledRegexpSlice(target *[]*regexp.Regexp,
	source []*regexp.Regexp) {

	if len(*target) == len(source) {
		equal := true
		for i := range source {
			if !regexpsEqual((*target)[i], source[i]) {
				equal = false
				break
			}
		}
		if equal {
			return
		}
	}

	*target = append([]*regexp.Regexp(nil), source...)
}

// regexpsEqual reports whether two compiled expressions have equivalent source
// patterns.
func regexpsEqual(left, right *regexp.Regexp) bool {
	if left == nil || right == nil {
		return left == right
	}

	return left.String() == right.String()
}

// prepareRewrite validates and normalizes the rewrite configuration.
func (s *Service) prepareRewrite() error {
	if s.Rewrite.Prefix == "" {
		return nil
	}

	u, err := url.Parse(s.Rewrite.Prefix)
	if err != nil {
		return fmt.Errorf("invalid prefix format: %v", err)
	}
	if u.Host != "" || u.Scheme != "" || u.Path == "" || u.Path[0] != '/' ||
		u.RawQuery != "" || u.Fragment != "" {

		return fmt.Errorf("invalid prefix format: expected absolute "+
			"path, got %q", u)
	}

	// Store the prefix as-is since it's already validated as an absolute
	// path. The rewrite function will use it directly without redundant
	// re-encoding via EscapedPath().
	s.Rewrite.Prefix = u.Path

	return nil
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
	for _, pathRegexp := range s.compiledAuthWhitelistPaths {
		if pathRegexp.MatchString(r.URL.Path) {
			log.Tracef("Req path [%s] matches whitelist entry "+
				"[%s].", r.URL.Path, pathRegexp)
			return auth.LevelOff
		}
	}

	// By default we always return the service level auth setting.
	return s.Auth
}

// SkipInvoiceCreation determines if an invoice should be created for a
// given request.
func (s *Service) SkipInvoiceCreation(r *http.Request) bool {
	for _, pathRegexp := range s.compiledAuthSkipInvoiceCreationPaths {
		if pathRegexp.MatchString(r.URL.Path) {
			log.Tracef("Req path [%s] matches skip  entry "+
				"[%s].", r.URL.Path, pathRegexp)
			return true
		}
	}

	return false
}

// prepareServices prepares the backend service configurations to be used by the
// proxy.
func prepareServices(services []*Service) error {
	for _, service := range services {
		// Each freebie enabled service gets its own store.
		if service.Auth.IsFreebie() {
			service.freebieDB = freebie.NewMemIPMaskStore(
				service.Auth.FreebieCount(),
			)
		}

		// Replace placeholders/directives in the header fields with the
		// actual desired values. Environment references are expanded
		// first, so a file directive's path may itself carry one.
		for key, value := range service.Headers {
			value, err := expandHeaderEnv(value)
			if err != nil {
				return err
			}
			service.Headers[key] = value

			if !strings.HasPrefix(value, filePrefix) {
				continue
			}

			parts := strings.Split(value, ":")
			if len(parts) != 2 {
				return fmt.Errorf("invalid header config, " +
					"must be '!file+hex:path'")
			}
			prefix, fileName := parts[0], parts[1]
			bytes, err := os.ReadFile(fileName)
			if err != nil {
				return err
			}

			// There are two supported formats to encode the file
			// content in: hex and base64.
			switch prefix {
			case filePrefixHex:
				newValue := hex.EncodeToString(bytes)
				service.Headers[key] = newValue

			case filePrefixBase64:
				newValue := base64.StdEncoding.EncodeToString(
					bytes,
				)
				service.Headers[key] = newValue

			default:
				return fmt.Errorf("unsupported file prefix "+
					"format %s", value)
			}
		}

		err := service.prepareRewrite()
		if err != nil {
			return err
		}

		// Compile the host regex.
		compiledHostRegexp, err := regexp.Compile(service.HostRegexp)
		if err != nil {
			return fmt.Errorf("error compiling host regex: %w", err)
		}
		service.compiledHostRegexp = compiledHostRegexp

		// Compile the path regex. Assign once after successful compilation so
		// a failed re-prepare leaves the prior compiled value intact.
		var compiledPathRegexp *regexp.Regexp
		if service.PathRegexp != "" {
			compiledPathRegexp, err = regexp.Compile(
				service.PathRegexp,
			)
			if err != nil {
				return fmt.Errorf("error compiling path "+
					"regex: %w", err)
			}
		}
		service.compiledPathRegexp = compiledPathRegexp

		service.compiledAuthWhitelistPaths = make(
			[]*regexp.Regexp, 0, len(service.AuthWhitelistPaths),
		)

		// Make sure all whitelist regular expression entries actually
		// compile so we run into an eventual panic during startup and
		// not only when the request happens.
		for _, entry := range service.AuthWhitelistPaths {
			regExp, err := regexp.Compile(entry)
			if err != nil {
				return fmt.Errorf("error validating auth "+
					"whitelist: %w", err)
			}
			service.compiledAuthWhitelistPaths = append(
				service.compiledAuthWhitelistPaths, regExp,
			)
		}

		service.compiledAuthSkipInvoiceCreationPaths = make(
			[]*regexp.Regexp, 0, len(
				service.AuthSkipInvoiceCreationPaths,
			),
		)

		// Make sure all skip invoice creation regular expression
		// entries actually compile so we run into an eventual panic
		// during startup and not only when the request happens.
		for _, entry := range service.AuthSkipInvoiceCreationPaths {
			regExp, err := regexp.Compile(entry)
			if err != nil {
				return fmt.Errorf("error validating skip "+
					"invoice creation whitelist: %w", err)
			}
			service.compiledAuthSkipInvoiceCreationPaths = append(
				service.compiledAuthSkipInvoiceCreationPaths,
				regExp,
			)
		}

		// Validate and compile rate limit configurations.
		if len(service.RateLimits) > 0 {
			for i, rl := range service.RateLimits {
				if rl == nil {
					return fmt.Errorf("service %s rate "+
						"limit %d: configuration must not "+
						"be nil", service.Name, i)
				}

				// Validate required fields.
				if rl.Requests <= 0 {
					return fmt.Errorf("service %s rate "+
						"limit %d: requests must be "+
						"positive", service.Name, i)
				}
				if rl.Per <= 0 {
					return fmt.Errorf("service %s rate "+
						"limit %d: per duration must "+
						"be positive", service.Name, i)
				}
				if rl.Burst < 0 {
					return fmt.Errorf("service %s rate "+
						"limit %d: burst must not be "+
						"negative", service.Name, i)
				}

				// Compile the path regex, assigning only after successful
				// compilation. An empty expression intentionally stores nil
				// and matches all paths.
				var compiled *regexp.Regexp
				if rl.PathRegexp != "" {
					compiled, err = regexp.Compile(
						rl.PathRegexp,
					)
					if err != nil {
						return fmt.Errorf("service %s "+
							"rate limit %d: error "+
							"compiling path regex: "+
							"%w", service.Name, i,
							err)
					}
				}
				rl.compiledPathRegexp = compiled
			}

			// Create the rate limiter for this service.
			service.rateLimiter = NewRateLimiter(
				service.Name, service.RateLimits,
			)

			log.Infof("Initialized rate limiter for service %s "+
				"with %d rules", service.Name,
				len(service.RateLimits))
		} else {
			// A Service can be reused in UpdateServices. Explicitly clear
			// a previously initialized limiter when its rules are removed.
			service.rateLimiter = nil
		}

		// Validate the dynamic pricer configuration, which also catches
		// a metered-but-disabled misconfiguration that would silently
		// turn metering off.
		if err := service.DynamicPrice.Validate(); err != nil {
			return fmt.Errorf("service %s dynamic price config: %w",
				service.Name, err)
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
			log.Debugf("Using default L402 price of %v satoshis for "+
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

// expandHeaderEnv replaces ${NAME} environment references in a header value
// with the variable's value. A reference to an unset or empty variable is an
// error rather than an empty substitution, so a missing secret fails startup
// instead of silently sending a malformed header upstream.
func expandHeaderEnv(value string) (string, error) {
	var expandErr error
	expanded := headerEnvRefRegexp.ReplaceAllStringFunc(
		value, func(ref string) string {
			// Strip the ${ and } delimiters around the name.
			name := ref[2 : len(ref)-1]

			envValue, ok := os.LookupEnv(name)
			if !ok || envValue == "" {
				expandErr = fmt.Errorf("environment variable "+
					"%s referenced in a service header is "+
					"not set", name)
			}

			return envValue
		},
	)
	if expandErr != nil {
		return "", expandErr
	}

	return expanded, nil
}
