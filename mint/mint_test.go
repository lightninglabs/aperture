package mint

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/lightninglabs/aperture/lsat"
	"gopkg.in/macaroon.v2"
)

var (
	testService = lsat.Service{
		Name: "lightning_loop",
		Tier: lsat.BaseTier,
	}
)

// TestBasicLSAT ensures that an LSAT can only access the services it's
// authorized to.
func TestBasicLSAT(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
	})

	// Mint a basic LSAT which is only able to access the given service.
	macaroon, _, err := mint.MintLSAT(ctx, testService)
	if err != nil {
		t.Fatalf("unable to mint LSAT: %v", err)
	}

	params := VerificationParams{
		Macaroon:      macaroon,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyLSAT(ctx, &params); err != nil {
		t.Fatalf("unable to verify LSAT: %v", err)
	}

	// It should not be able to access an unknown service.
	unknownParams := params
	unknownParams.TargetService = "uknown"
	err = mint.VerifyLSAT(ctx, &unknownParams)
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatal("expected LSAT to not be authorized")
	}
}

// TestAdminLSAT ensures that an admin LSAT (one without a services caveat) is
// authorized to access any service.
func TestAdminLSAT(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
	})

	// Mint an admin LSAT by not including any services.
	macaroon, _, err := mint.MintLSAT(ctx)
	if err != nil {
		t.Fatalf("unable to mint LSAT: %v", err)
	}

	// It should be able to access any service as it doesn't have a services
	// caveat.
	params := &VerificationParams{
		Macaroon:      macaroon,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyLSAT(ctx, params); err != nil {
		t.Fatalf("unable to verify LSAT: %v", err)
	}
}

// TestRevokedLSAT ensures that we can no longer verify a revoked LSAT.
func TestRevokedLSAT(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
	})

	// Mint an LSAT and verify it.
	lsat, _, err := mint.MintLSAT(ctx)
	if err != nil {
		t.Fatalf("unable to mint LSAT: %v", err)
	}
	params := &VerificationParams{
		Macaroon:      lsat,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyLSAT(ctx, params); err != nil {
		t.Fatalf("unable to verify LSAT: %v", err)
	}

	// Proceed to revoke it. We should no longer be able to verify it after.
	idHash := sha256.Sum256(lsat.Id())
	if err := mint.cfg.Secrets.RevokeSecret(ctx, idHash); err != nil {
		t.Fatalf("unable to revoke LSAT: %v", err)
	}
	if err := mint.VerifyLSAT(ctx, params); err != ErrSecretNotFound {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

// TestTamperedLSAT ensures that an LSAT that has been tampered with by
// modifying its signature results in its verification failing.
func TestTamperedLSAT(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
	})

	// Mint a new LSAT and verify it is valid.
	mac, _, err := mint.MintLSAT(ctx, testService)
	if err != nil {
		t.Fatalf("unable to mint LSAT: %v", err)
	}
	params := VerificationParams{
		Macaroon:      mac,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyLSAT(ctx, &params); err != nil {
		t.Fatalf("unable to verify LSAT: %v", err)
	}

	// Create a tampered LSAT from the valid one.
	macBytes, err := mac.MarshalBinary()
	if err != nil {
		t.Fatalf("unable to serialize macaroon: %v", err)
	}
	macBytes[len(macBytes)-1] = 0x00
	var tampered macaroon.Macaroon
	if err := tampered.UnmarshalBinary(macBytes); err != nil {
		t.Fatalf("unable to deserialize macaroon: %v", err)
	}

	// Attempting to verify the tampered LSAT should fail.
	tamperedParams := params
	tamperedParams.Macaroon = &tampered
	err = mint.VerifyLSAT(ctx, &tamperedParams)
	if !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatal("expected tampered LSAT to be invalid")
	}
}

// TestDemotedServicesLSAT ensures that an LSAT which originally was authorized
// to access a service, but was then demoted to no longer be the case, is no
// longer authorized.
func TestDemotedServicesLSAT(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
	})

	unauthorizedService := testService
	unauthorizedService.Name = "unauthorized"

	// Mint an LSAT that is able to access two services, one of which will
	// be denied later on.
	mac, _, err := mint.MintLSAT(ctx, testService, unauthorizedService)
	if err != nil {
		t.Fatalf("unable to mint LSAT: %v", err)
	}

	// It should be able to access both services.
	authorizedParams := VerificationParams{
		Macaroon:      mac,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyLSAT(ctx, &authorizedParams); err != nil {
		t.Fatalf("unable to verify LSAT: %v", err)
	}
	unauthorizedParams := VerificationParams{
		Macaroon:      mac,
		Preimage:      testPreimage,
		TargetService: unauthorizedService.Name,
	}
	if err := mint.VerifyLSAT(ctx, &unauthorizedParams); err != nil {
		t.Fatalf("unable to verify LSAT: %v", err)
	}

	// Demote the second service by including an additional services caveat
	// that only includes the first service.
	services, err := lsat.NewServicesCaveat(testService)
	if err != nil {
		t.Fatalf("unable to create services caveat: %v", err)
	}
	err = lsat.AddFirstPartyCaveats(mac, services)
	if err != nil {
		t.Fatalf("unable to demote LSAT: %v", err)
	}

	// It should now only be able to access the first, but not the second.
	if err := mint.VerifyLSAT(ctx, &authorizedParams); err != nil {
		t.Fatalf("unable to verify LSAT: %v", err)
	}
	err = mint.VerifyLSAT(ctx, &unauthorizedParams)
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatal("expected macaroon to be invalid")
	}
}
