package macaroons

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/lightningnetwork/lnd/macaroons"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon-bakery.v2/bakery/checkers"
	"gopkg.in/macaroon.v2"
)

const (
	// CondRHash is the macaroon caveat condition for a payment hash.
	CondRHash = "r-hash"

	// CondPreimage is the macaroon caveat condition for a payment preimage.
	CondPreimage = "preimage"
)

var (
	rootKey   = "aabbccddeeff00112233445566778899"
	rootKeyId = []byte("0")
)

type rootKeyStore struct{}

// A compile time flag to ensure the rootKeyStore satisfies the
// bakery.RootKeyStore interface.
var _ bakery.RootKeyStore = (*rootKeyStore)(nil)

func (r *rootKeyStore) Get(_ context.Context, id []byte) ([]byte, error) {
	return hex.DecodeString(rootKey)
}

func (r *rootKeyStore) RootKey(_ context.Context) (rootKey, id []byte,
	err error) {

	key, err := r.Get(nil, rootKeyId)
	if err != nil {
		return nil, nil, err
	}
	return key, rootKeyId, nil
}

// Service can bake and validate macaroons.
type Service struct {
	bakery.Bakery
}

// NewService creates a new macaroon service with the given checker functions
// that should be supported when validating a macaroon.
func NewService(checks ...macaroons.Checker) (*Service, error) {
	macaroonParams := bakery.BakeryParams{
		Location:     "kirin",
		RootKeyStore: &rootKeyStore{},
		Locator:      nil,
		Key:          nil,
	}

	svc := bakery.New(macaroonParams)

	// Register all custom caveat checkers with the bakery's checker.
	checker := svc.Checker.FirstPartyCaveatChecker.(*checkers.Checker)
	for _, check := range checks {
		cond, fun := check()
		if !isRegistered(checker, cond) {
			checker.Register(cond, "std", fun)
		}
	}

	return &Service{*svc}, nil
}

// NewMacaroon bakes a new macaroon with the given allowed operations and
// optional first-party caveats.
func (s *Service) NewMacaroon(operations []bakery.Op, caveats []string) (
	[]byte, error) {

	ctx := context.Background()
	mac, err := s.Oven.NewMacaroon(
		ctx, bakery.LatestVersion, nil, operations...,
	)
	if err != nil {
		return nil, err
	}

	// Add all first party caveats before serializing the macaroon.
	for _, caveat := range caveats {
		err := mac.M().AddFirstPartyCaveat([]byte(caveat))
		if err != nil {
			return nil, err
		}
	}
	macBytes, err := mac.M().MarshalBinary()
	if err != nil {
		return nil, err
	}
	return macBytes, nil
}

// ValidateMacaroon verifies the signature chain of a macaroon and then
// checks that none of the applied restrictions are violated.
func (s *Service) ValidateMacaroon(mac *macaroon.Macaroon,
	perms []bakery.Op) error {

	// Check the method being called against the permitted operation and
	// the expiration time and IP address and return the result.
	authChecker := s.Checker.Auth(macaroon.Slice{mac})
	_, err := authChecker.Allow(context.Background(), perms...)
	return err
}

// ExtractCaveat extracts the value of a given caveat condition or returns an
// empty string if that caveat does not exist.
func ExtractCaveat(mac *macaroon.Macaroon, cond string) (string, error) {
	if mac == nil {
		return "", fmt.Errorf("macaroon cannot be nil")
	}
	for _, caveat := range mac.Caveats() {
		cavStr := string(caveat.Id)
		cavCond, cavArg, err := checkers.ParseCaveat(cavStr)
		if err != nil {
			continue
		}
		if cavCond == cond {
			return cavArg, nil
		}
	}

	return "", nil
}

// isRegistered checks to see if the required checker has already been
// registered in order to avoid a panic caused by double registration.
func isRegistered(c *checkers.Checker, name string) bool {
	if c == nil {
		return false
	}

	for _, info := range c.Info() {
		if info.Name == name &&
			info.Prefix == "" &&
			info.Namespace == "std" {
			return true
		}
	}

	return false
}
