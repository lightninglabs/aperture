package l402

import (
	"context"
	"encoding/hex"

	"gopkg.in/macaroon.v2"
)

// MacaroonCredential wraps a macaroon to implement the
// credentials.PerRPCCredentials interface.
type MacaroonCredential struct {
	*macaroon.Macaroon

	// AllowInsecure specifies if the macaroon is allowed to be sent over
	// insecure transport channels. This should only ever be set to true if
	// the insecure connection is proxied through tor and the destination
	// address is an onion service.
	AllowInsecure bool
}

// RequireTransportSecurity implements the PerRPCCredentials interface.
func (m MacaroonCredential) RequireTransportSecurity() bool {
	return !m.AllowInsecure
}

// GetRequestMetadata implements the PerRPCCredentials interface. This method
// is required in order to pass the wrapped macaroon into the gRPC context.
// With this, the macaroon will be available within the request handling scope
// of the ultimate gRPC server implementation.
func (m MacaroonCredential) GetRequestMetadata(_ context.Context,
	_ ...string) (map[string]string, error) {

	macBytes, err := m.MarshalBinary()
	if err != nil {
		return nil, err
	}

	md := make(map[string]string)
	macHex := hex.EncodeToString(macBytes)

	// Send the token under the current spec key "token" per bLIP-0026.
	// The legacy "macaroon" key is also sent for backwards compatibility
	// with servers that have not yet upgraded.
	md["token"] = macHex
	md["macaroon"] = macHex
	return md, nil
}

// NewMacaroonCredential returns a copy of the passed macaroon wrapped in a
// MacaroonCredential struct which implements PerRPCCredentials.
func NewMacaroonCredential(m *macaroon.Macaroon,
	allowInsecure bool) MacaroonCredential {

	ms := MacaroonCredential{AllowInsecure: allowInsecure}
	ms.Macaroon = m.Clone()
	return ms
}
