package l402

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

// TestServicesCaveatSerialization ensures that we can properly encode/decode
// valid services from a caveat and cannot do so for invalid ones.
func TestServicesCaveatSerialization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		err   error
	}{
		{
			name:  "single service",
			value: "a:0",
			err:   nil,
		},
		{
			name:  "multiple services",
			value: "a:0,b:1,c:0",
			err:   nil,
		},
		{
			name:  "no services",
			value: "",
			err:   ErrNoServices,
		},
		{
			name:  "service missing name",
			value: ":0",
			err:   ErrInvalidService,
		},
		{
			name:  "service missing tier",
			value: "a",
			err:   ErrInvalidService,
		},
		{
			name:  "service empty tier",
			value: "a:",
			err:   ErrInvalidService,
		},
		{
			name:  "service non-numeric tier",
			value: "a:b",
			err:   ErrInvalidService,
		},
		{
			name:  "empty services",
			value: ",,",
			err:   ErrInvalidService,
		},
	}

	for _, test := range tests {
		success := t.Run(test.name, func(t *testing.T) {
			services, err := decodeServicesCaveatValue(test.value)
			if !errors.Is(err, test.err) {
				t.Fatalf("expected err \"%v\", got \"%v\"",
					test.err, err)
			}

			if test.err != nil {
				return
			}

			value, _ := encodeServicesCaveatValue(services...)
			if value != test.value {
				t.Fatalf("expected encoded services \"%v\", "+
					"got \"%v\"", test.value, value)
			}
		})
		if !success {
			return
		}
	}
}

// TestServicesFromMacaroon checks that the final services caveat is returned.
func TestServicesFromMacaroon(t *testing.T) {
	t.Parallel()

	mac, err := macaroon.New(
		[]byte("root key"), []byte("id"), "test",
		macaroon.LatestVersion,
	)
	require.NoError(t, err)

	_, err = ServicesFromMacaroon(mac)
	require.ErrorIs(t, err, ErrNoServices)

	first, err := NewServicesCaveat(
		Service{Name: "one"}, Service{Name: "two"},
	)
	require.NoError(t, err)
	last, err := NewServicesCaveat(Service{Name: "two"})
	require.NoError(t, err)
	require.NoError(t, AddFirstPartyCaveats(mac, first, last))

	services, err := ServicesFromMacaroon(mac)
	require.NoError(t, err)
	require.Equal(t, []Service{{Name: "two"}}, services)
}

// TestServicesCaveatRejectsSeparators checks that a name containing one of the
// encoding's separators is refused instead of being encoded as other services.
func TestServicesCaveatRejectsSeparators(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		// A dynamic-price resource whose path adds service "other".
		"svc/x:0,other",
		"svc/a,b",
		"svc/v1/items:batchGet",
	} {
		_, err := NewServicesCaveat(Service{Name: name})
		require.ErrorIs(t, err, ErrInvalidService, name)
	}

	caveat, err := NewServicesCaveat(Service{Name: "svc/v1/items"})
	require.NoError(t, err)
	services, err := decodeServicesCaveatValue(caveat.Value)
	require.NoError(t, err)
	require.Equal(t, []Service{{Name: "svc/v1/items"}}, services)
}
