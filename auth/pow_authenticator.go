package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
)

// PoWAuthenticator is an authenticator that uses proof-of-work to authenticate
// requests instead of Lightning payments.
type PoWAuthenticator struct {
	minter       Minter
	difficulties map[string]uint32
}

// A compile time flag to ensure the PoWAuthenticator satisfies the
// Authenticator interface.
var _ Authenticator = (*PoWAuthenticator)(nil)

// NewPoWAuthenticator creates a new authenticator that authenticates requests
// based on proof-of-work. The difficulties map provides the required PoW
// difficulty (leading zero bits) for each service by name.
func NewPoWAuthenticator(minter Minter,
	difficulties map[string]uint32) *PoWAuthenticator {

	return &PoWAuthenticator{
		minter:       minter,
		difficulties: difficulties,
	}
}

// Accept returns whether or not the header successfully authenticates the user
// to a given backend service using proof-of-work.
//
// NOTE: This is part of the Authenticator interface.
func (p *PoWAuthenticator) Accept(header *http.Header,
	serviceName string) bool {

	mac, err := l402.MacaroonFromHeader(header)
	if err != nil {
		log.Debugf("Deny: %v", err)
		return false
	}

	difficulty, ok := p.difficulties[serviceName]
	if !ok {
		log.Debugf("Deny: no PoW difficulty configured for service %s",
			serviceName)
		return false
	}

	err = p.minter.VerifyL402PoW(
		context.Background(), &mint.PoWVerificationParams{
			Macaroon:      mac,
			Difficulty:    difficulty,
			TargetService: serviceName,
		},
	)
	if err != nil {
		log.Debugf("Deny: PoW L402 validation failed: %v", err)
		return false
	}

	return true
}

// FreshChallengeHeader returns a header containing a PoW challenge for the
// user to complete.
//
// NOTE: This is part of the Authenticator interface.
func (p *PoWAuthenticator) FreshChallengeHeader(serviceName string,
	servicePrice int64) (http.Header, error) {

	service := l402.Service{
		Name:  serviceName,
		Tier:  l402.BaseTier,
		Price: servicePrice,
	}
	mac, powDifficulty, err := p.minter.MintL402(
		context.Background(), service,
	)
	if err != nil {
		log.Errorf("Error minting PoW L402: %v", err)
		return nil, err
	}
	macBytes, err := mac.MarshalBinary()
	if err != nil {
		log.Errorf("Error serializing PoW L402: %v", err)
		return nil, err
	}

	header := http.Header{
		"Content-Type": []string{"application/grpc"},
	}

	str := fmt.Sprintf("macaroon=\"%s\", pow=\"%s\"",
		base64.StdEncoding.EncodeToString(macBytes), powDifficulty)

	value := l402AuthScheme + " " + str
	header.Set("WWW-Authenticate", value)
	log.Debugf("Created new PoW challenge header: [%s]", value)

	return header, nil
}
