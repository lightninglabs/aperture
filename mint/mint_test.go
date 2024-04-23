package mint

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/l402"
	"github.com/stretchr/testify/require"
	"gopkg.in/macaroon.v2"
)

var (
	testService = l402.Service{
		Name: "lightning_loop",
		Tier: l402.BaseTier,
	}
)

// TestBasicL402 ensures that an L402 can only access the services it's
// authorized to.
func TestBasicL402(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
		Now:            time.Now,
	})

	// Mint a basic L402 which is only able to access the given service.
	macaroon, _, err := mint.MintL402(ctx, testService)
	if err != nil {
		t.Fatalf("unable to mint L402: %v", err)
	}

	params := VerificationParams{
		Macaroon:      macaroon,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyL402(ctx, &params); err != nil {
		t.Fatalf("unable to verify L402: %v", err)
	}

	// It should not be able to access an unknown service.
	unknownParams := params
	unknownParams.TargetService = "unknown"
	err = mint.VerifyL402(ctx, &unknownParams)
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatal("expected L402 to not be authorized")
	}
}

// TestAdminL402 ensures that an admin L402 (one without a services caveat) is
// authorized to access any service.
func TestAdminL402(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
		Now:            time.Now,
	})

	// Mint an admin L402 by not including any services.
	macaroon, _, err := mint.MintL402(ctx)
	if err != nil {
		t.Fatalf("unable to mint L402: %v", err)
	}

	// It should be able to access any service as it doesn't have a services
	// caveat.
	params := &VerificationParams{
		Macaroon:      macaroon,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyL402(ctx, params); err != nil {
		t.Fatalf("unable to verify L402: %v", err)
	}
}

// TestRevokedL402 ensures that we can no longer verify a revoked L402.
func TestRevokedL402(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
		Now:            time.Now,
	})

	// Mint an L402 and verify it.
	l402, _, err := mint.MintL402(ctx)
	if err != nil {
		t.Fatalf("unable to mint L402: %v", err)
	}
	params := &VerificationParams{
		Macaroon:      l402,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyL402(ctx, params); err != nil {
		t.Fatalf("unable to verify L402: %v", err)
	}

	// Proceed to revoke it. We should no longer be able to verify it after.
	idHash := sha256.Sum256(l402.Id())
	if err := mint.cfg.Secrets.RevokeSecret(ctx, idHash); err != nil {
		t.Fatalf("unable to revoke L402: %v", err)
	}
	if err := mint.VerifyL402(ctx, params); err != ErrSecretNotFound {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

// TestTamperedL402 ensures that an L402 that has been tampered with by
// modifying its signature results in its verification failing.
func TestTamperedL402(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
		Now:            time.Now,
	})

	// Mint a new L402 and verify it is valid.
	mac, _, err := mint.MintL402(ctx, testService)
	if err != nil {
		t.Fatalf("unable to mint L402: %v", err)
	}
	params := VerificationParams{
		Macaroon:      mac,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyL402(ctx, &params); err != nil {
		t.Fatalf("unable to verify L402: %v", err)
	}

	// Create a tampered L402 from the valid one.
	macBytes, err := mac.MarshalBinary()
	if err != nil {
		t.Fatalf("unable to serialize macaroon: %v", err)
	}
	macBytes[len(macBytes)-1] = 0x00
	var tampered macaroon.Macaroon
	if err := tampered.UnmarshalBinary(macBytes); err != nil {
		t.Fatalf("unable to deserialize macaroon: %v", err)
	}

	// Attempting to verify the tampered L402 should fail.
	tamperedParams := params
	tamperedParams.Macaroon = &tampered
	err = mint.VerifyL402(ctx, &tamperedParams)
	if !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatal("expected tampered L402 to be invalid")
	}
}

// TestDemotedServicesL402 ensures that an L402 which originally was authorized
// to access a service, but was then demoted to no longer be the case, is no
// longer authorized.
func TestDemotedServicesL402(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
		Now:            time.Now,
	})

	unauthorizedService := testService
	unauthorizedService.Name = "unauthorized"

	// Mint an L402 that is able to access two services, one of which will
	// be denied later on.
	mac, _, err := mint.MintL402(ctx, testService, unauthorizedService)
	if err != nil {
		t.Fatalf("unable to mint L402: %v", err)
	}

	// It should be able to access both services.
	authorizedParams := VerificationParams{
		Macaroon:      mac,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}
	if err := mint.VerifyL402(ctx, &authorizedParams); err != nil {
		t.Fatalf("unable to verify L402: %v", err)
	}
	unauthorizedParams := VerificationParams{
		Macaroon:      mac,
		Preimage:      testPreimage,
		TargetService: unauthorizedService.Name,
	}
	if err := mint.VerifyL402(ctx, &unauthorizedParams); err != nil {
		t.Fatalf("unable to verify L402: %v", err)
	}

	// Demote the second service by including an additional services caveat
	// that only includes the first service.
	services, err := l402.NewServicesCaveat(testService)
	if err != nil {
		t.Fatalf("unable to create services caveat: %v", err)
	}
	err = l402.AddFirstPartyCaveats(mac, services)
	if err != nil {
		t.Fatalf("unable to demote L402: %v", err)
	}

	// It should now only be able to access the first, but not the second.
	if err := mint.VerifyL402(ctx, &authorizedParams); err != nil {
		t.Fatalf("unable to verify L402: %v", err)
	}
	err = mint.VerifyL402(ctx, &unauthorizedParams)
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatal("expected macaroon to be invalid")
	}
}

// TestExpiredServicesL402 asserts the behavior of the Timeout caveat.
func TestExpiredServicesL402(t *testing.T) {
	t.Parallel()

	initialTime := int64(1000)
	mockTime := newMockTime(initialTime)

	ctx := context.Background()
	mint := New(&Config{
		Secrets:        newMockSecretStore(),
		Challenger:     newMockChallenger(),
		ServiceLimiter: newMockServiceLimiter(),
		Now:            mockTime.now,
	})

	// Mint a new l402 for accessing a test service.
	mac, _, err := mint.MintL402(ctx, testService)
	require.NoError(t, err)

	authorizedParams := VerificationParams{
		Macaroon:      mac,
		Preimage:      testPreimage,
		TargetService: testService.Name,
	}

	// It should be able to access the service if no timeout caveat added.
	require.NoError(t, mint.VerifyL402(ctx, &authorizedParams))

	// Add a timeout caveat that expires in the future.
	timeout := l402.NewTimeoutCaveat(testService.Name, 1000, mockTime.now)
	require.NoError(t, l402.AddFirstPartyCaveats(mac, timeout))

	// Make sure that the L402 is still valid after timeout is added since
	// the timeout has not yet been reached.
	require.NoError(t, mint.VerifyL402(ctx, &authorizedParams))

	// Force time to pass such that the L402 should no longer be valid.
	mockTime.setTime(initialTime + 1001)

	// Assert that the L402 is no longer valid due to the timeout being
	// reached.
	err = mint.VerifyL402(ctx, &authorizedParams)
	require.Contains(t, err.Error(), "not authorized")
}

type mockTime struct {
	time time.Time
}

func newMockTime(initialTime int64) *mockTime {
	return &mockTime{
		time: time.Unix(initialTime, 0),
	}
}

func (mt *mockTime) now() time.Time {
	return mt.time
}

func (mt *mockTime) setTime(timestamp int64) {
	mt.time = time.Unix(timestamp, 0)
}
