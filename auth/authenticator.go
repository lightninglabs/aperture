package auth

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"

	"github.com/lightninglabs/kirin/macaroons"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon-bakery.v2/bakery/checkers"
)

var (
	authRegex  = regexp.MustCompile("LSAT (.*?):([a-f0-9]{64})")
	opWildcard = "*"
)

// LsatAuthenticator is an authenticator that uses the LSAT protocol to
// authenticate requests.
type LsatAuthenticator struct {
	challenger Challenger
	macService *macaroons.Service
}

// A compile time flag to ensure the LsatAuthenticator satisfies the
// Authenticator interface.
var _ Authenticator = (*LsatAuthenticator)(nil)

// NewLsatAuthenticator creates a new authenticator that authenticates requests
// based on LSAT tokens.
func NewLsatAuthenticator(challenger Challenger) (*LsatAuthenticator, error) {
	macService, err := macaroons.NewService()
	if err != nil {
		return nil, err
	}

	return &LsatAuthenticator{
		challenger: challenger,
		macService: macService,
	}, nil
}

// Accept returns whether or not the header successfully authenticates the user
// to a given backend service.
//
// NOTE: This is part of the Authenticator interface.
func (l *LsatAuthenticator) Accept(header *http.Header) bool {
	authHeader := header.Get("Authorization")
	log.Debugf("Trying to authorize with header value [%s].", authHeader)
	if authHeader == "" {
		return false
	}

	if !authRegex.MatchString(authHeader) {
		log.Debugf("Deny: Auth header in invalid format.")
		return false
	}

	matches := authRegex.FindStringSubmatch(authHeader)
	if len(matches) != 3 {
		log.Debugf("Deny: Auth header in invalid format.")
		return false
	}

	macBase64, preimageHex := matches[1], matches[2]
	macBytes, err := base64.StdEncoding.DecodeString(macBase64)
	if err != nil {
		log.Debugf("Deny: Base64 decode of macaroon failed: %v", err)
		return false
	}

	preimageBytes, err := hex.DecodeString(preimageHex)
	if err != nil {
		log.Debugf("Deny: Hex decode of preimage failed: %v", err)
		return false
	}

	// TODO(guggero): check preimage against payment hash caveat in the
	//  macaroon.
	if len(preimageBytes) != 32 {
		log.Debugf("Deny: Decoded preimage has invalid length.")
		return false
	}

	err = l.macService.ValidateMacaroon(macBytes, []bakery.Op{})
	if err != nil {
		log.Debugf("Deny: Macaroon validation failed: %v", err)
		return false
	}
	return true
}

// FreshChallengeHeader returns a header containing a challenge for the user to
// complete.
//
// NOTE: This is part of the Authenticator interface.
func (l *LsatAuthenticator) FreshChallengeHeader(r *http.Request) (
	http.Header, error) {

	paymentRequest, paymentHash, err := l.challenger.NewChallenge()
	if err != nil {
		log.Errorf("Error creating new challenge: %v", err)
		return nil, err
	}

	// Create a new macaroon and add the payment hash as a caveat.
	// The bakery requires at least one operation so we add an "allow all"
	// permission set for now.
	mac, err := l.macService.NewMacaroon(
		[]bakery.Op{{Entity: opWildcard, Action: opWildcard}}, []string{
			checkers.Condition(
				macaroons.CondRHash, paymentHash.String(),
			),
		},
	)
	if err != nil {
		log.Errorf("Error creating macaroon: %v", err)
		return nil, err
	}

	str := "LSAT macaroon='%s' invoice='%s'"
	str = fmt.Sprintf(
		str, base64.StdEncoding.EncodeToString(mac), paymentRequest,
	)
	header := r.Header
	header.Set("WWW-Authenticate", str)

	log.Debugf("Created new challenge header: [%s]", str)
	return header, nil
}
