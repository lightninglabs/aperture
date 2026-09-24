package l402

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/macaroon.v2"
)

const (
	// CondServices is the condition used for a services caveat.
	CondServices = "services"

	// CondCapabilitiesSuffix is the condition suffix used for a service's
	// capabilities caveat. For example, the condition of a capabilities
	// caveat for a service named `loop` would be `loop_capabilities`.
	CondCapabilitiesSuffix = "_capabilities"

	// CondTimeoutSuffix is the condition suffix used for a service's
	// timeout caveat.
	CondTimeoutSuffix = "_valid_until"
)

var (
	// ErrNoServices is an error returned when we attempt to decode the
	// services included in a caveat.
	ErrNoServices = errors.New("no services found")

	// ErrInvalidService is an error returned when we attempt to decode a
	// service with an invalid format.
	ErrInvalidService = errors.New("service must be of the form " +
		"\"name:tier\"")
)

// ServiceTier represents the different possible tiers of an L402-enabled
// service.
type ServiceTier uint8

const (
	// BaseTier is the base tier of an L402-enabled service. This tier
	// should be used for any new L402s that are not part of a service tier
	// upgrade.
	BaseTier ServiceTier = iota
)

// Service contains the details of an L402-enabled service.
type Service struct {
	// Name is the name of the L402-enabled service.
	Name string

	// Tier is the tier of the L402-enabled service.
	Tier ServiceTier

	// Price of service L402 in satoshis.
	Price int64
}

// NewServicesCaveat creates a new services caveat with the provided caveats.
func NewServicesCaveat(services ...Service) (Caveat, error) {
	value, err := encodeServicesCaveatValue(services...)
	if err != nil {
		return Caveat{}, err
	}
	return Caveat{
		Condition: CondServices,
		Value:     value,
	}, nil
}

// encodeServicesCaveatValue encodes a list of services into the expected format
// of a services caveat's value.
func encodeServicesCaveatValue(services ...Service) (string, error) {
	if len(services) == 0 {
		return "", ErrNoServices
	}

	var s strings.Builder
	for i, service := range services {
		if service.Name == "" {
			return "", errors.New("missing service name")
		}

		// The value separates services with ',' and each name from its
		// tier with ':', and nothing escapes either inside a name. A
		// dynamic-price resource name ends in the client's request
		// path, so a name containing one of those characters could
		// decode back as several services rather than itself.
		if strings.ContainsAny(service.Name, ",:") {
			return "", fmt.Errorf("%w: name %q must not contain "+
				"',' or ':'", ErrInvalidService, service.Name)
		}

		fmtStr := "%v:%v"
		if i < len(services)-1 {
			fmtStr += ","
		}

		fmt.Fprintf(&s, fmtStr, service.Name, uint8(service.Tier))
	}

	return s.String(), nil
}

// decodeServicesCaveatValue decodes a list of services from the expected format
// of a services caveat's value.
func decodeServicesCaveatValue(s string) ([]Service, error) {
	if s == "" {
		return nil, ErrNoServices
	}

	rawServices := strings.Split(s, ",")
	services := make([]Service, 0, len(rawServices))
	for _, rawService := range rawServices {
		serviceInfo := strings.Split(rawService, ":")
		if len(serviceInfo) != 2 {
			return nil, ErrInvalidService
		}

		name, tierStr := serviceInfo[0], serviceInfo[1]
		if name == "" {
			return nil, fmt.Errorf("%w: %v", ErrInvalidService,
				"empty name")
		}
		tier, err := strconv.Atoi(tierStr)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidService, err)
		}

		services = append(services, Service{
			Name: name,
			Tier: ServiceTier(tier),
		})
	}

	return services, nil
}

// ServicesFromMacaroon returns the services in the macaroon's final services
// caveat. The macaroon must still be verified before these values are trusted.
func ServicesFromMacaroon(mac *macaroon.Macaroon) ([]Service, error) {
	value, ok := HasCaveat(mac, CondServices)
	if !ok {
		return nil, ErrNoServices
	}

	return decodeServicesCaveatValue(value)
}

// NewCapabilitiesCaveat creates a new capabilities caveat for the given
// service.
func NewCapabilitiesCaveat(serviceName string, capabilities string) Caveat {
	return Caveat{
		Condition: serviceName + CondCapabilitiesSuffix,
		Value:     capabilities,
	}
}

// NewTimeoutCaveat creates a new caveat that will result in a macaroon being
// valid for numSeconds after the current time.
func NewTimeoutCaveat(serviceName string, numSeconds int64,
	now func() time.Time) Caveat {

	var (
		macaroonTimeout = time.Duration(numSeconds) * time.Second
		requestTimeout  = now().Add(macaroonTimeout)
	)

	return Caveat{
		Condition: serviceName + CondTimeoutSuffix,
		Value:     strconv.FormatInt(requestTimeout.Unix(), 10),
	}
}
