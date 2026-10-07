package aperture

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/aperturedb"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/pricer"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

// TestReusableChargePolicyForServices pins the shape of the name the policy
// is asked about: the authenticator passes the resource name, which for a
// dynamically priced service is the service name with the request path
// appended. Matching on equality alone silently re-enables single-use
// charges on every metered service, which is exactly the failure a live run
// caught.
func TestReusableChargePolicyForServices(t *testing.T) {
	t.Parallel()

	policy := reusableChargePolicyForServices(map[string]struct{}{
		"inference": {},
	})

	// The resource name of a dynamically priced service carries the path.
	require.True(t, policy("inference/v1/chat/completions"))

	// A service without dynamic pricing is asked about by bare name.
	require.True(t, policy("inference"))

	// Prefix matching must not bleed across the path separator.
	require.False(t, policy("inference2/v1/chat/completions"))
	require.False(t, policy("inference2"))
	require.False(t, policy("other"))
}

// TestMergeServicesFromDBPreservesConfig verifies that persisted admin API
// changes override only the fields represented in the database.
func TestMergeServicesFromDBPreservesConfig(t *testing.T) {
	db := aperturedb.NewTestDB(t)
	_, _, _, _, serviceStore := initSQLStores(db.BaseDB)

	configOnlyFirst := &proxy.Service{Name: "config-only-first"}
	configAndDB := &proxy.Service{
		Name:        "config-and-db",
		TLSCertPath: "backend.cert",
		Address:     "config:8080",
		Protocol:    "https",
		Auth:        auth.Level("on"),
		AuthScheme:  "l402",
		HostRegexp:  "^config.example$",
		PathRegexp:  "^/config$",
		Headers: map[string]string{
			"Authorization": "config-secret",
		},
		Timeout:      60,
		Capabilities: "read,write",
		Constraints:  map[string]string{"region": "us"},
		Price:        100,
		DynamicPrice: pricer.Config{
			Enabled:        true,
			GRPCAddress:    "pricer:10000",
			Insecure:       true,
			Metered:        true,
			UsageTailBytes: 4096,
		},
		AuthWhitelistPaths: []string{"^/public$"},
		AuthSkipInvoiceCreationPaths: []string{
			"^/health$",
		},
		RateLimits: []*proxy.RateLimitConfig{
			{
				PathRegexp: "^/limited$",
				Requests:   10,
				Per:        time.Minute,
				Burst:      2,
			},
		},
		Rewrite: proxy.RewriteConfig{Prefix: "/v1"},
	}
	configOnlyLast := &proxy.Service{Name: "config-only-last"}

	err := serviceStore.UpsertService(
		t.Context(), aperturedb.ServiceParams{
			Name:       configAndDB.Name,
			Address:    "database:8081",
			Protocol:   "http",
			HostRegexp: "^database.example$",
			PathRegexp: "^/database$",
			Auth:       "freebie 2",
			AuthScheme: "mpp",
			Price:      250,
		},
	)
	require.NoError(t, err)

	err = serviceStore.UpsertService(
		t.Context(), aperturedb.ServiceParams{
			Name:       "database-only",
			Address:    "database:8082",
			Protocol:   "https",
			HostRegexp: "^new.example$",
			PathRegexp: "^/new$",
			Auth:       "on",
			AuthScheme: "l402+mpp",
			Price:      500,
		},
	)
	require.NoError(t, err)

	// Created last but first by name: the admin API appends services in
	// creation order, and a restart must keep that order.
	err = serviceStore.UpsertService(
		t.Context(), aperturedb.ServiceParams{
			Name:     "added-later",
			Address:  "database:8083",
			Protocol: "http",
			Auth:     "off",
		},
	)
	require.NoError(t, err)

	expectedMerged := *configAndDB
	expectedMerged.Address = "database:8081"
	expectedMerged.Protocol = "http"
	expectedMerged.HostRegexp = "^database.example$"
	expectedMerged.PathRegexp = "^/database$"
	expectedMerged.Auth = auth.Level("freebie 2")
	expectedMerged.AuthScheme = "mpp"
	expectedMerged.Price = 250

	expectedDBOnly := &proxy.Service{
		Name:       "database-only",
		Address:    "database:8082",
		Protocol:   "https",
		HostRegexp: "^new.example$",
		PathRegexp: "^/new$",
		Auth:       auth.Level("on"),
		AuthScheme: "l402+mpp",
		Price:      500,
	}
	expectedAddedLater := &proxy.Service{
		Name:     "added-later",
		Address:  "database:8083",
		Protocol: "http",
		Auth:     auth.Level("off"),
	}

	merged := mergeServicesFromDB(
		[]*proxy.Service{
			configOnlyFirst, configAndDB, configOnlyLast,
		},
		serviceStore,
	)
	require.Equal(t, []*proxy.Service{
		configOnlyFirst, &expectedMerged, configOnlyLast,
		expectedDBOnly, expectedAddedLater,
	}, merged)
}

// TestGetConfigKeepsSessionKnobsFromFile asserts that the session deposit
// multiplier and idle timeout set in the config file survive the second flag
// pass. That pass exists so command line flags win over the file, but a flag
// carrying a default tag re-applies its default on every parse, which silently
// threw away whatever the file had set for these two.
func TestGetConfigKeepsSessionKnobsFromFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "aperture.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
listenaddr: "127.0.0.1:0"
authenticator:
  # Disabled so the fixture does not depend on any authenticator
  # backend validation.
  disable: true
  sessiondepositmultiplier: 2000
  sessionidletimeout: 900
`), 0600))

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"aperture", "--configfile=" + configPath}

	cfg, err := getConfig()
	require.NoError(t, err)
	require.Equal(t, 2000, cfg.Authenticator.SessionDepositMultiplier)
	require.Equal(t, 900, cfg.Authenticator.SessionIdleTimeout)

	// A flag still overrides the file, for both knobs.
	os.Args = []string{
		"aperture", "--configfile=" + configPath,
		"--authenticator.sessiondepositmultiplier=50",
		"--authenticator.sessionidletimeout=60",
	}
	cfg, err = getConfig()
	require.NoError(t, err)
	require.Equal(t, 50, cfg.Authenticator.SessionDepositMultiplier)
	require.Equal(t, 60, cfg.Authenticator.SessionIdleTimeout)
}
