package mint

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightningnetwork/lnd/lntypes"
	"gopkg.in/macaroon.v2"
)

var (
	// ErrSecretNotFound is an error returned when we attempt to retrieve a
	// secret by its key but it is not found.
	ErrSecretNotFound = errors.New("secret not found")
)

// Challenger is an interface used to present requesters of L402s with a
// challenge that must be satisfied before an L402 can be validated. This
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

// SecretStore is the store responsible for storing L402 secrets. These secrets
// are required for proper verification of each minted L402.
type SecretStore interface {
	// NewSecret creates a new cryptographically random secret which is
	// keyed by the given hash.
	NewSecret(context.Context, [sha256.Size]byte) ([l402.SecretSize]byte,
		error)

	// GetSecret returns the cryptographically random secret that
	// corresponds to the given hash. If there is no secret, then
	// ErrSecretNotFound is returned.
	GetSecret(context.Context, [sha256.Size]byte) ([l402.SecretSize]byte,
		error)

	// RevokeSecret removes the cryptographically random secret that
	// corresponds to the given hash. This acts as a NOP if the secret does
	// not exist.
	RevokeSecret(context.Context, [sha256.Size]byte) error
}

// ServiceLimiter abstracts the source of caveats that should be applied to an
// L402 for a particular service.
type ServiceLimiter interface {
	// ServiceCapabilities returns the capabilities caveats for each
	// service. This determines which capabilities of each service can be
	// accessed.
	ServiceCapabilities(context.Context, ...l402.Service) ([]l402.Caveat,
		error)

	// ServiceConstraints returns the constraints for each service. This
	// enforces additional constraints on a particular service/service
	// capability.
	ServiceConstraints(context.Context, ...l402.Service) ([]l402.Caveat,
		error)

	// ServiceTimeouts returns the timeout caveat for each service. This
	// will determine if and when service access can expire.
	ServiceTimeouts(context.Context, ...l402.Service) ([]l402.Caveat,
		error)
}

// Config packages all of the required dependencies to instantiate a new L402
// mint.
type Config struct {
	// Secrets is our source for L402 secrets which will be used for
	// verification purposes.
	Secrets SecretStore

	// Challenger is our source of new challenges to present requesters of
	// an L402 with.
	Challenger Challenger

	// ServiceLimiter provides us with how we should limit a new L402 based
	// on its target services.
	ServiceLimiter ServiceLimiter

	// Now returns the current time.
	Now func() time.Time
}

// Mint is an entity that is able to mint and verify L402s for a set of
// services.
type Mint struct {
	cfg Config
}

// New creates a new L402 mint backed by its given dependencies.
func New(cfg *Config) *Mint {
	return &Mint{cfg: *cfg}
}

// MintL402 mints a new L402 for the target services.
func (m *Mint) MintL402(ctx context.Context,
	services ...l402.Service) (*macaroon.Macaroon, string, error) {

	// Let the L402 value as the price of the most expensive of the
	// services.
	price := maximumPrice(services)

	// We'll start by retrieving a new challenge in the form of a Lightning
	// payment request to present the requester of the L402 with.
	paymentRequest, paymentHash, err := m.cfg.Challenger.NewChallenge(price)
	if err != nil {
		return nil, "", err
	}

	// TODO(wilmer): remove invoice if any of the operations below fail?

	// We can then proceed to mint the L402 with a unique identifier that is
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
	// L402.
	var caveats []l402.Caveat
	if len(services) > 0 {
		var err error
		caveats, err = m.caveatsForServices(ctx, services...)
		if err != nil {
			// Attempt to revoke the secret to save space.
			_ = m.cfg.Secrets.RevokeSecret(ctx, idHash)
			return nil, "", err
		}
	}
	if err := l402.AddFirstPartyCaveats(mac, caveats...); err != nil {
		// Attempt to revoke the secret to save space.
		_ = m.cfg.Secrets.RevokeSecret(ctx, idHash)
		return nil, "", err
	}

	return mac, paymentRequest, nil
}

// maximumPrice determines the necessary price to use for a collection
// of services.
func maximumPrice(services []l402.Service) int64 {
	var max int64

	for _, service := range services {
		if service.Price > max {
			max = service.Price
		}
	}

	return max
}

// createUniqueIdentifier creates a new L402 identifier bound to a payment hash
// and a randomly generated ID.
func createUniqueIdentifier(paymentHash lntypes.Hash) ([]byte, error) {
	tokenID, err := generateTokenID()
	if err != nil {
		return nil, err
	}

	id := &l402.Identifier{
		Version:     l402.LatestVersion,
		PaymentHash: paymentHash,
		TokenID:     tokenID,
	}

	var buf bytes.Buffer
	if err := l402.EncodeIdentifier(&buf, id); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// generateTokenID generates a new random L402 ID.
func generateTokenID() ([l402.TokenIDSize]byte, error) {
	var tokenID [l402.TokenIDSize]byte
	_, err := rand.Read(tokenID[:])
	return tokenID, err
}

// caveatsForServices returns all of the caveats that should be applied to an
// L402 for the target services.
func (m *Mint) caveatsForServices(ctx context.Context,
	services ...l402.Service) ([]l402.Caveat, error) {

	servicesCaveat, err := l402.NewServicesCaveat(services...)
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

	caveats := []l402.Caveat{servicesCaveat}
	caveats = append(caveats, capabilities...)
	caveats = append(caveats, constraints...)
	caveats = append(caveats, timeouts...)
	return caveats, nil
}

// VerificationParams holds all of the requirements to properly verify an L402.
type VerificationParams struct {
	// Macaroon is the macaroon as part of the L402 we'll attempt to verify.
	Macaroon *macaroon.Macaroon

	// Preimage is the preimage that should correspond to the L402's payment
	// hash.
	Preimage lntypes.Preimage

	// TargetService is the target service a user of an L402 is attempting
	// to access.
	TargetService string
}

// VerifyL402 attempts to verify an L402 with the given parameters.
func (m *Mint) VerifyL402(ctx context.Context,
	params *VerificationParams) error {

	// We'll first perform a quick check to determine if a valid preimage
	// was provided.
	id, err := l402.DecodeIdentifier(bytes.NewReader(params.Macaroon.Id()))
	if err != nil {
		return err
	}
	if params.Preimage.Hash() != id.PaymentHash {
		return fmt.Errorf("invalid preimage %v for %v", params.Preimage,
			id.PaymentHash)
	}

	// If there was, then we'll ensure the L402 was minted by us.
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

	// With the L402 verified, we'll now inspect its caveats to ensure the
	// target service is authorized.
	caveats := make([]l402.Caveat, 0, len(rawCaveats))
	for _, rawCaveat := range rawCaveats {
		// L402s can contain third-party caveats that we're not aware
		// of, so just skip those.
		caveat, err := l402.DecodeCaveat(rawCaveat)
		if err != nil {
			continue
		}
		caveats = append(caveats, caveat)
	}
	return l402.VerifyCaveats(
		caveats,
		l402.NewServicesSatisfier(params.TargetService),
		l402.NewTimeoutSatisfier(params.TargetService, m.cfg.Now),
	)
}
