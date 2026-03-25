package mpp

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestComputeChallengeIDDeterministic verifies that the same parameters always
// produce the same challenge ID.
func TestComputeChallengeIDDeterministic(t *testing.T) {
	secret := []byte("test-server-secret-key-32-bytes!")

	params := &ChallengeParams{
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJhbW91bnQiOiIxMDAiLCJjdXJyZW5jeSI6InNhdCJ9",
		Expires: "2026-03-15T12:05:00Z",
	}

	id1 := ComputeChallengeID(secret, params)
	id2 := ComputeChallengeID(secret, params)
	require.Equal(t, id1, id2)
	require.NotEmpty(t, id1)

	// Verify no padding in base64url output.
	require.NotContains(t, id1, "=")
}

// TestVerifyChallengeID verifies the challenge ID verification function.
func TestVerifyChallengeID(t *testing.T) {
	secret := []byte("test-server-secret-key-32-bytes!")

	params := &ChallengeParams{
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJhbW91bnQiOiIxMDAiLCJjdXJyZW5jeSI6InNhdCJ9",
		Expires: "2026-03-15T12:05:00Z",
	}

	id := ComputeChallengeID(secret, params)

	// Should verify with correct params.
	require.True(t, VerifyChallengeID(secret, params, id))

	// Should fail with wrong ID.
	require.False(t, VerifyChallengeID(secret, params, "wrongid"))

	// Should fail with different secret.
	require.False(t, VerifyChallengeID(
		[]byte("different-secret-key-32-bytes!!"), params, id,
	))
}

// TestChallengeIDTamperDetection verifies that modifying any parameter changes
// the challenge ID (i.e., HMAC detects tampering).
func TestChallengeIDTamperDetection(t *testing.T) {
	secret := []byte("test-server-secret-key-32-bytes!")

	original := &ChallengeParams{
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJhbW91bnQiOiIxMDAifQ",
		Expires: "2026-03-15T12:05:00Z",
		Digest:  "sha-256=:abc:",
		Opaque:  "eyJvcmRlciI6IjEyMyJ9",
	}
	originalID := ComputeChallengeID(secret, original)

	// Each modification should produce a different ID.
	tests := []struct {
		name   string
		modify func(p *ChallengeParams)
	}{
		{
			name:   "change realm",
			modify: func(p *ChallengeParams) { p.Realm = "other.com" },
		},
		{
			name: "change method",
			modify: func(p *ChallengeParams) {
				p.Method = "other"
			},
		},
		{
			name: "change intent",
			modify: func(p *ChallengeParams) {
				p.Intent = "session"
			},
		},
		{
			name: "change request",
			modify: func(p *ChallengeParams) {
				p.Request = "eyJhbW91bnQiOiIyMDAifQ"
			},
		},
		{
			name: "change expires",
			modify: func(p *ChallengeParams) {
				p.Expires = "2026-03-16T12:05:00Z"
			},
		},
		{
			name: "change digest",
			modify: func(p *ChallengeParams) {
				p.Digest = "sha-256=:xyz:"
			},
		},
		{
			name: "change opaque",
			modify: func(p *ChallengeParams) {
				p.Opaque = "eyJvcmRlciI6IjQ1NiJ9"
			},
		},
		{
			name: "remove expires",
			modify: func(p *ChallengeParams) {
				p.Expires = ""
			},
		},
		{
			name: "remove digest",
			modify: func(p *ChallengeParams) {
				p.Digest = ""
			},
		},
		{
			name: "remove opaque",
			modify: func(p *ChallengeParams) {
				p.Opaque = ""
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modified := *original
			tc.modify(&modified)
			modifiedID := ComputeChallengeID(secret, &modified)
			require.NotEqual(t, originalID, modifiedID,
				"changing %s should produce different ID",
				tc.name)

			// Original ID should not verify with modified params.
			require.False(t, VerifyChallengeID(
				secret, &modified, originalID,
			))
		})
	}
}

// TestChallengeIDOptionalFieldCombinations verifies that different
// combinations of present/absent optional fields produce distinct IDs. This
// tests the fixed positional slot design from spec §5.1.2.1.1.
func TestChallengeIDOptionalFieldCombinations(t *testing.T) {
	secret := []byte("test-server-secret-key-32-bytes!")

	base := ChallengeParams{
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJ0ZXN0IjoiMSJ9",
	}

	// All these combinations should produce unique IDs.
	combos := []struct {
		name    string
		expires string
		digest  string
		opaque  string
	}{
		{"none", "", "", ""},
		{"expires only", "2026-01-01T00:00:00Z", "", ""},
		{"digest only", "", "sha-256=:abc:", ""},
		{"opaque only", "", "", "eyJ0ZXN0IjoiMSJ9"},
		{"expires+digest", "2026-01-01T00:00:00Z", "sha-256=:abc:", ""},
		{"expires+opaque", "2026-01-01T00:00:00Z", "", "eyJ0ZXN0IjoiMSJ9"},
		{"digest+opaque", "", "sha-256=:abc:", "eyJ0ZXN0IjoiMSJ9"},
		{"all three", "2026-01-01T00:00:00Z", "sha-256=:abc:", "eyJ0ZXN0IjoiMSJ9"},
	}

	ids := make(map[string]string)
	for _, combo := range combos {
		p := base
		p.Expires = combo.expires
		p.Digest = combo.digest
		p.Opaque = combo.opaque
		id := ComputeChallengeID(secret, &p)

		// Check uniqueness.
		if existing, ok := ids[id]; ok {
			t.Fatalf("collision between %q and %q: both "+
				"produced ID %s", existing, combo.name, id)
		}
		ids[id] = combo.name
	}
}

// TestBuildHMACInput verifies the pipe-delimited input construction.
func TestBuildHMACInput(t *testing.T) {
	tests := []struct {
		name     string
		params   *ChallengeParams
		expected string
	}{
		{
			name: "all fields present",
			params: &ChallengeParams{
				Realm:   "api.example.com",
				Method:  "lightning",
				Intent:  "charge",
				Request: "req_b64",
				Expires: "2026-01-01T00:00:00Z",
				Digest:  "sha-256=:abc:",
				Opaque:  "opaque_b64",
			},
			expected: "api.example.com|lightning|charge|req_b64|2026-01-01T00:00:00Z|sha-256=:abc:|opaque_b64",
		},
		{
			name: "no optional fields",
			params: &ChallengeParams{
				Realm:   "api.example.com",
				Method:  "lightning",
				Intent:  "charge",
				Request: "req_b64",
			},
			expected: "api.example.com|lightning|charge|req_b64|||",
		},
		{
			name: "only expires set",
			params: &ChallengeParams{
				Realm:   "api.example.com",
				Method:  "lightning",
				Intent:  "charge",
				Request: "req_b64",
				Expires: "2026-01-01T00:00:00Z",
			},
			expected: "api.example.com|lightning|charge|req_b64|2026-01-01T00:00:00Z||",
		},
		{
			name: "only digest set (not expires)",
			params: &ChallengeParams{
				Realm:   "api.example.com",
				Method:  "lightning",
				Intent:  "charge",
				Request: "req_b64",
				Digest:  "sha-256=:abc:",
			},
			// Empty expires slot, then digest, then empty opaque.
			expected: "api.example.com|lightning|charge|req_b64||sha-256=:abc:|",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := buildHMACInput(tc.params)
			require.Equal(t, tc.expected, result)
		})
	}
}

// TestChallengeIDRandomSecret verifies that random secrets produce valid IDs.
func TestChallengeIDRandomSecret(t *testing.T) {
	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	require.NoError(t, err)

	params := &ChallengeParams{
		Realm:   "api.example.com",
		Method:  MethodLightning,
		Intent:  IntentCharge,
		Request: "eyJ0ZXN0IjoiMSJ9",
	}

	id := ComputeChallengeID(secret, params)
	require.NotEmpty(t, id)
	require.True(t, VerifyChallengeID(secret, params, id))
}
