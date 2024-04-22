package l402

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

// TestFileStore tests the basic functionality of the file based store.
func TestFileStore(t *testing.T) {
	t.Parallel()

	tempDirName, err := os.MkdirTemp("", "l402store")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDirName)

	var (
		paidPreimage = lntypes.Preimage{1, 2, 3, 4, 5}
		paidToken    = &Token{
			Preimage: paidPreimage,
			baseMac:  makeMac(),
		}
		pendingToken = &Token{
			Preimage: zeroPreimage,
			baseMac:  makeMac(),
		}
	)

	store, err := NewFileStore(tempDirName)
	if err != nil {
		t.Fatalf("could not create test store: %v", err)
	}

	// Make sure the current store is empty.
	_, err = store.CurrentToken()
	if err != ErrNoToken {
		t.Fatalf("expected store to be empty but error was: %v", err)
	}
	tokens, err := store.AllTokens()
	if err != nil {
		t.Fatalf("unexpected error listing all tokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected store to be empty but got %v", tokens)
	}

	// Store a pending token and make sure we can read it again.
	err = store.StoreToken(pendingToken)
	if err != nil {
		t.Fatalf("could not save pending token: %v", err)
	}
	if !fileExists(filepath.Join(tempDirName, storeFileNamePending)) {
		t.Fatalf("expected file %s/%s to exist but it didn't",
			tempDirName, storeFileNamePending)
	}
	token, err := store.CurrentToken()
	if err != nil {
		t.Fatalf("could not read pending token: %v", err)
	}
	if !token.baseMac.Equal(pendingToken.baseMac) {
		t.Fatalf("expected macaroon to match")
	}
	tokens, err = store.AllTokens()
	if err != nil {
		t.Fatalf("unexpected error listing all tokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("unexpected number of tokens, got %d expected %d",
			len(tokens), 1)
	}
	for key := range tokens {
		if !tokens[key].baseMac.Equal(pendingToken.baseMac) {
			t.Fatalf("expected macaroon to match")
		}
	}

	// Replace the pending token with a final one and make sure the pending
	// token was replaced.
	err = store.StoreToken(paidToken)
	if err != nil {
		t.Fatalf("could not save pending token: %v", err)
	}
	if !fileExists(filepath.Join(tempDirName, storeFileName)) {
		t.Fatalf("expected file %s/%s to exist but it didn't",
			tempDirName, storeFileName)
	}
	if fileExists(filepath.Join(tempDirName, storeFileNamePending)) {
		t.Fatalf("expected file %s/%s to be removed but it wasn't",
			tempDirName, storeFileNamePending)
	}
	token, err = store.CurrentToken()
	if err != nil {
		t.Fatalf("could not read pending token: %v", err)
	}
	if !token.baseMac.Equal(paidToken.baseMac) {
		t.Fatalf("expected macaroon to match")
	}
	tokens, err = store.AllTokens()
	if err != nil {
		t.Fatalf("unexpected error listing all tokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("unexpected number of tokens, got %d expected %d",
			len(tokens), 1)
	}
	for key := range tokens {
		if !tokens[key].baseMac.Equal(paidToken.baseMac) {
			t.Fatalf("expected macaroon to match")
		}
	}

	// Make sure we can't replace the existing paid token with a pending.
	err = store.StoreToken(pendingToken)
	if err != errNoReplace {
		t.Fatalf("unexpected error. got %v, expected %v", err,
			errNoReplace)
	}

	// Make sure we can also not overwrite the existing paid token with a
	// new paid one.
	err = store.StoreToken(paidToken)
	if err != errNoReplace {
		t.Fatalf("unexpected error. got %v, expected %v", err,
			errNoReplace)
	}
}

// TestFileStorePendingMigration tests migration from lsat.token.pending
// to l402.token.pending.
func TestFileStorePendingMigration(t *testing.T) {
	t.Parallel()

	tempDirName := t.TempDir()

	pendingToken := &Token{
		Preimage: zeroPreimage,
		baseMac:  makeMac(),
	}

	store, err := NewFileStore(tempDirName)
	require.NoError(t, err)

	// Write a pending token.
	require.NoError(t, store.StoreToken(pendingToken))

	// Rename file on disk to lsat.token.pending to emulate
	// a pre-migration state.
	newPath := filepath.Join(tempDirName, storeFileNamePending)
	oldPath := filepath.Join(tempDirName, "lsat.token.pending")
	require.NoError(t, os.Rename(newPath, oldPath))

	// Open the same directory again.
	store1, err := NewFileStore(tempDirName)
	require.NoError(t, err)

	// Read the token and compare its value.
	token, err := store1.CurrentToken()
	require.NoError(t, err)
	require.Equal(t, pendingToken.baseMac, token.baseMac)
}

// TestFileStoreMigration tests migration from lsat.token to l402.token.
func TestFileStoreMigration(t *testing.T) {
	t.Parallel()

	tempDirName := t.TempDir()

	paidPreimage := lntypes.Preimage{1, 2, 3, 4, 5}
	paidToken := &Token{
		Preimage: paidPreimage,
		baseMac:  makeMac(),
	}

	store, err := NewFileStore(tempDirName)
	require.NoError(t, err)

	// Write a token.
	require.NoError(t, store.StoreToken(paidToken))

	// Rename file on disk to lsat.token.pending to emulate
	// a pre-migration state.
	newPath := filepath.Join(tempDirName, storeFileName)
	oldPath := filepath.Join(tempDirName, "lsat.token")
	require.NoError(t, os.Rename(newPath, oldPath))

	// Open the same directory again.
	store1, err := NewFileStore(tempDirName)
	require.NoError(t, err)

	// Read the token and compare its value.
	token, err := store1.CurrentToken()
	require.NoError(t, err)
	require.Equal(t, paidToken.baseMac, token.baseMac)
}
