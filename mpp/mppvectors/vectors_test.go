package mppvectors

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/lightninglabs/aperture/mpp"
	"github.com/stretchr/testify/require"
)

// committedVectors is where the generated file lives relative to this package.
const committedVectors = "../testdata/vectors.json"

// TestVectorsAreCurrent fails when the committed vector file no longer matches
// what the generator produces.
//
// This is the check that was missing. The vector file the TypeScript
// implementation replays was generated from Go, but nothing regenerated it when
// Go changed, so a divergence in number rendering and another in string
// escaping both sat in the file unnoticed. A stale file is worse than no file,
// because it reads as evidence that the two sides agree.
func TestVectorsAreCurrent(t *testing.T) {
	generated, err := Generate()
	require.NoError(t, err)

	committed, err := os.ReadFile(committedVectors)
	require.NoError(
		t, err, "run go run ./cmd/gen-mpp-vectors to create it",
	)

	require.Equal(
		t, string(committed), string(generated),
		"%s is stale; regenerate it with "+
			"go run ./cmd/gen-mpp-vectors",
		filepath.Clean(committedVectors),
	)
}

// TestVectorsAreDeterministic verifies that two runs of the generator agree,
// since a file that changes on every run cannot serve as a golden record.
func TestVectorsAreDeterministic(t *testing.T) {
	first, err := Generate()
	require.NoError(t, err)

	second, err := Generate()
	require.NoError(t, err)

	require.Equal(t, string(first), string(second))
}

// loadVectors reads the committed file, so that the replay tests below check
// the file rather than the generator that wrote it.
func loadVectors(t *testing.T) *File {
	t.Helper()

	raw, err := os.ReadFile(committedVectors)
	require.NoError(t, err)

	var file File
	require.NoError(t, json.Unmarshal(raw, &file))
	require.Equal(t, Version, file.Version)

	return &file
}

// TestReplayCanonicalization replays the canonicalization vectors, which is the
// same thing an independent implementation does with the file.
func TestReplayCanonicalization(t *testing.T) {
	file := loadVectors(t)
	require.NotEmpty(t, file.Canonicalization)

	for _, v := range file.Canonicalization {
		t.Run(v.Name, func(t *testing.T) {
			canonical, err := mpp.CanonicalizeJSON([]byte(v.Input))
			if v.Error {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, v.Canonical, string(canonical))

			// Canonicalizing a canonical document has to be a
			// no-op, which is the fixed point every peer converges
			// on.
			again, err := mpp.CanonicalizeJSON(canonical)
			require.NoError(t, err)
			require.Equal(t, v.Canonical, string(again))
		})
	}
}

// TestReplayBase64URL replays the base64url vectors in both directions.
func TestReplayBase64URL(t *testing.T) {
	file := loadVectors(t)
	require.NotEmpty(t, file.Base64URL)

	for _, v := range file.Base64URL {
		t.Run(v.Name, func(t *testing.T) {
			decoded, err := mpp.Base64URLDecode(v.Encoded)
			if v.Error {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(
				t, v.DecodedHex, hex.EncodeToString(decoded),
			)

			if !v.Canonical {
				return
			}

			require.Equal(
				t, v.Encoded, mpp.Base64URLEncode(decoded),
			)
		})
	}
}

// TestReplayChallengeHeaders replays the emit-and-reparse vectors.
func TestReplayChallengeHeaders(t *testing.T) {
	file := loadVectors(t)
	require.NotEmpty(t, file.ChallengeHeaders)

	for _, v := range file.ChallengeHeaders {
		t.Run(v.Name, func(t *testing.T) {
			h := make(http.Header)
			mpp.SetChallengeHeader(h, fromParamsJSON(v.Params))
			require.Equal(
				t, v.Header, h.Get("WWW-Authenticate"),
			)

			parsed, err := mpp.ParseChallengeHeader(v.Header)
			require.NoError(t, err)
			require.Equal(
				t, fromParamsJSON(v.Parsed), parsed,
			)

			// Every vector here uses values the grammar can carry,
			// so the round trip has to be lossless. That is the
			// property the challenge HMAC depends on.
			require.Equal(t, v.Params, v.Parsed)
		})
	}
}

// TestReplayChallengeLists replays the multi-challenge parse vectors.
func TestReplayChallengeLists(t *testing.T) {
	file := loadVectors(t)
	require.NotEmpty(t, file.ChallengeLists)

	for _, v := range file.ChallengeLists {
		t.Run(v.Name, func(t *testing.T) {
			challenges, err := mpp.ParseChallengeList(v.Header)
			if v.Error {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Len(t, challenges, len(v.Challenges))
			for i, want := range v.Challenges {
				require.Equal(
					t, fromParamsJSON(want), challenges[i],
				)
			}
		})
	}
}

// TestReplayChallengeIDs replays the HMAC binding vectors, and checks that the
// recorded HMAC input is the one the identifier was actually computed over.
func TestReplayChallengeIDs(t *testing.T) {
	file := loadVectors(t)
	require.NotEmpty(t, file.ChallengeIDs)

	secret, err := hex.DecodeString(file.HMACSecretHex)
	require.NoError(t, err)

	for _, v := range file.ChallengeIDs {
		t.Run(v.Name, func(t *testing.T) {
			params := fromParamsJSON(v.Params)
			require.True(
				t, mpp.VerifyChallengeID(
					secret, params, v.Params.ID,
				),
			)

			// Recomputing over the recorded slot string has to
			// yield the same identifier, which localizes a
			// mismatch to the slot layout rather than the HMAC.
			require.Equal(
				t, v.Params.ID,
				mpp.ComputeChallengeID(secret, params),
			)
		})
	}
}

// TestReplayCredentials replays the Authorization header vectors.
func TestReplayCredentials(t *testing.T) {
	file := loadVectors(t)
	require.NotEmpty(t, file.Credentials)

	for _, v := range file.Credentials {
		t.Run(v.Name, func(t *testing.T) {
			h := make(http.Header)
			h.Set("Authorization", v.Header)

			cred, err := mpp.ParseCredential(&h)
			if v.Error {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			var want mpp.Credential
			require.NoError(t, json.Unmarshal(
				[]byte(v.CredentialJSON), &want,
			))
			require.Equal(t, want.Challenge, cred.Challenge)
			require.Equal(t, want.Source, cred.Source)
			require.JSONEq(
				t, string(want.Payload), string(cred.Payload),
			)
		})
	}
}

// TestReplayReceipts replays the Payment-Receipt vectors.
func TestReplayReceipts(t *testing.T) {
	file := loadVectors(t)
	require.NotEmpty(t, file.Receipts)

	for _, v := range file.Receipts {
		t.Run(v.Name, func(t *testing.T) {
			decoded, err := mpp.Base64URLDecode(v.Header)
			require.NoError(t, err)
			require.Equal(t, v.ReceiptJSON, string(decoded))
		})
	}
}

// fromParamsJSON converts the wire-named shape back into challenge parameters.
func fromParamsJSON(p ChallengeParamsJSON) *mpp.ChallengeParams {
	return &mpp.ChallengeParams{
		ID:          p.ID,
		Realm:       p.Realm,
		Method:      p.Method,
		Intent:      p.Intent,
		Request:     p.Request,
		Expires:     p.Expires,
		Digest:      p.Digest,
		Description: p.Description,
		Opaque:      p.Opaque,
	}
}
