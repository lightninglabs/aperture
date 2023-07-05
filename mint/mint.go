package mint

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

var (
	// ErrSecretNotFound is an error returned when we attempt to retrieve a
	// secret by its key but it is not found.
	ErrSecretNotFound = errors.New("secret not found")
)

// Challenger is an interface used to present requesters of LSATs with a
// challenge that must be satisfied before an LSAT can be validated. This
// challenge takes the form of a Lightning payment request.
type Challenger interface {
	// NewChallenge returns a new challenge in the form of a Lightning
	// payment request. The payment hash is also returned as a convenience
	// to avoid having to decode the payment request in order to retrieve
	// its payment hash.
	NewChallenge(price int64) (string, lntypes.Hash, error)

	// Stop shuts down the challenger.
	Stop()
}

// SecretStore is the store responsible for storing LSAT secrets. These secrets
// are required for proper verification of each minted LSAT.
type SecretStore interface {
	// NewSecret creates a new cryptographically random secret which is
	// keyed by the given hash.
	NewSecret(context.Context, [sha256.Size]byte) ([lsat.SecretSize]byte,
		error)

	// GetSecret returns the cryptographically random secret that
	// corresponds to the given hash. If there is no secret, then
	// ErrSecretNotFound is returned.
	GetSecret(context.Context, [sha256.Size]byte) ([lsat.SecretSize]byte,
		error)

	// RevokeSecret removes the cryptographically random secret that
	// corresponds to the given hash. This acts as a NOP if the secret does
	// not exist.
	RevokeSecret(context.Context, [sha256.Size]byte) error
}

// ServiceLimiter abstracts the source of caveats that should be applied to an
// LSAT for a particular service.
type ServiceLimiter interface {
	// ServiceCapabilities returns the capabilities caveats for each
	// service. This determines which capabilities of each service can be
	// accessed.
	ServiceCapabilities(context.Context, ...lsat.Service) ([]lsat.Caveat,
		error)

	// ServiceConstraints returns the constraints for each service. This
	// enforces additional constraints on a particular service/service
	// capability.
	ServiceConstraints(context.Context, ...lsat.Service) ([]lsat.Caveat,
		error)

	// ServiceTimeouts returns the timeout caveat for each service. This
	// will determine if and when service access can expire.
	ServiceTimeouts(context.Context, ...lsat.Service) ([]lsat.Caveat,
		error)
}

// Config packages all of the required dependencies to instantiate a new LSAT
// mint.
type Config struct {
	// Secrets is our source for LSAT secrets which will be used for
	// verification purposes.
	Secrets SecretStore

	// Challenger is our source of new challenges to present requesters of
	// an LSAT with.
	Challenger Challenger

	// ServiceLimiter provides us with how we should limit a new LSAT based
	// on its target services.
	ServiceLimiter ServiceLimiter

	// Now returns the current time.
	Now func() time.Time
}

// Mint is an entity that is able to mint and verify LSATs for a set of
// services.
type Mint struct {
	cfg Config
}

// New creates a new LSAT mint backed by its given dependencies.
func New(cfg *Config) *Mint {
	return &Mint{cfg: *cfg}
}

// MintLSAT mints a new LSAT for the target services.
func (m *Mint) MintLSAT(ctx context.Context,
	services ...lsat.Service) (*macaroon.Macaroon, string, error) {

	// Let the LSAT value as the price of the most expensive of the
	// services.
	price := maximumPrice(services)

	// We'll start by retrieving a new challenge in the form of a Lightning
	// payment request to present the requester of the LSAT with.
	paymentRequest, paymentHash, err := m.cfg.Challenger.NewChallenge(price)
	if err != nil {
		return nil, "", err
	}

	// TODO(wilmer): remove invoice if any of the operations below fail?

	// We can then proceed to mint the LSAT with a unique identifier that is
	// mapped to a unique secret.
	id, err := createUniqueIdentifier(paymentHash)
	if err != nil {
		return nil, "", err
	}
	idHash := sha256.Sum256(id)
	secret, err := m.cfg.Secrets.NewSecret(ctx, idHash)
	if err != nil {
		return nil, "", err
	}
	mac, err := macaroon.New(
		secret[:], id, "lsat", macaroon.LatestVersion,
	)
	if err != nil {
		// Attempt to revoke the secret to save space.
		_ = m.cfg.Secrets.RevokeSecret(ctx, idHash)
		return nil, "", err
	}

	// Include any restrictions that should be immediately applied to the
	// LSAT.
	var caveats []lsat.Caveat
	if len(services) > 0 {
		var err error
		caveats, err = m.caveatsForServices(ctx, services...)
		if err != nil {
			// Attempt to revoke the secret to save space.
			_ = m.cfg.Secrets.RevokeSecret(ctx, idHash)
			return nil, "", err
		}
	}
	if err := lsat.AddFirstPartyCaveats(mac, caveats...); err != nil {
		// Attempt to revoke the secret to save space.
		_ = m.cfg.Secrets.RevokeSecret(ctx, idHash)
		return nil, "", err
	}

	return mac, paymentRequest, nil
}

// maximumPrice determines the necessary price to use for a collection
// of services.
func maximumPrice(services []lsat.Service) int64 {
	var max int64

	for _, service := range services {
		if service.Price > max {
			max = service.Price
		}
	}

	return max
}

// createUniqueIdentifier creates a new LSAT identifier bound to a payment hash
// and a randomly generated ID.
func createUniqueIdentifier(paymentHash lntypes.Hash) ([]byte, error) {
	tokenID, err := generateTokenID()
	if err != nil {
		return nil, err
	}

	id := &lsat.Identifier{
		Version:     lsat.LatestVersion,
		PaymentHash: paymentHash,
		TokenID:     tokenID,
	}

	var buf bytes.Buffer
	if err := lsat.EncodeIdentifier(&buf, id); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// generateTokenID generates a new random LSAT ID.
func generateTokenID() ([lsat.TokenIDSize]byte, error) {
	var tokenID [lsat.TokenIDSize]byte
	_, err := rand.Read(tokenID[:])
	return tokenID, err
}

// caveatsForServices returns all of the caveats that should be applied to an
// LSAT for the target services.
func (m *Mint) caveatsForServices(ctx context.Context,
	services ...lsat.Service) ([]lsat.Caveat, error) {

	servicesCaveat, err := lsat.NewServicesCaveat(services...)
	if err != nil {
		return nil, err
	}
	capabilities, err := m.cfg.ServiceLimiter.ServiceCapabilities(
		ctx, services...,
	)
	if err != nil {
		return nil, err
	}
	constraints, err := m.cfg.ServiceLimiter.ServiceConstraints(
		ctx, services...,
	)
	if err != nil {
		return nil, err
	}
	timeouts, err := m.cfg.ServiceLimiter.ServiceTimeouts(ctx, services...)
	if err != nil {
		return nil, err
	}

	caveats := []lsat.Caveat{servicesCaveat}
	caveats = append(caveats, capabilities...)
	caveats = append(caveats, constraints...)
	caveats = append(caveats, timeouts...)
	return caveats, nil
}

// VerificationParams holds all of the requirements to properly verify an LSAT.
type VerificationParams struct {
	// Macaroon is the macaroon as part of the LSAT we'll attempt to verify.
	Macaroon *macaroon.Macaroon

	// Preimage is the preimage that should correspond to the LSAT's payment
	// hash.
	Preimage lntypes.Preimage

	// TargetService is the target service a user of an LSAT is attempting
	// to access.
	TargetService string
}

// VerifyLSAT attempts to verify an LSAT with the given parameters.
func (m *Mint) VerifyLSAT(ctx context.Context,
	params *VerificationParams) error {

	// We'll first perform a quick check to determine if a valid preimage
	// was provided.
	id, err := lsat.DecodeIdentifier(bytes.NewReader(params.Macaroon.Id()))
	if err != nil {
		return err
	}
	if params.Preimage.Hash() != id.PaymentHash {
		return fmt.Errorf("invalid preimage %v for %v", params.Preimage,
			id.PaymentHash)
	}

	// If there was, then we'll ensure the LSAT was minted by us.
	secret, err := m.cfg.Secrets.GetSecret(
		ctx, sha256.Sum256(params.Macaroon.Id()),
	)
	if err != nil {
		return err
	}
	rawCaveats, err := params.Macaroon.VerifySignature(secret[:], nil)
	if err != nil {
		return err
	}

	// With the LSAT verified, we'll now inspect its caveats to ensure the
	// target service is authorized.
	caveats := make([]lsat.Caveat, 0, len(rawCaveats))
	for _, rawCaveat := range rawCaveats {
		// LSATs can contain third-party caveats that we're not aware
		// of, so just skip those.
		caveat, err := lsat.DecodeCaveat(rawCaveat)
		if err != nil {
			continue
		}
		caveats = append(caveats, caveat)
	}
	return lsat.VerifyCaveats(
		caveats,
		lsat.NewServicesSatisfier(params.TargetService),
		lsat.NewTimeoutSatisfier(params.TargetService, m.cfg.Now),
	)
}
