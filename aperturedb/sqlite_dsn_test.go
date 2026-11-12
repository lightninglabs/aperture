package aperturedb

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSqliteStoreURIKeepsPragmas asserts that the pragma options are applied
// when the database name is a file: URI that already has a query.
func TestSqliteStoreURIKeepsPragmas(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "aperture.db")
	store, err := NewSqliteStore(&SqliteConfig{
		DatabaseFileName: "file:" + path + "?cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.DB.Close()) })

	var journalMode string
	row := store.DB.QueryRow("PRAGMA journal_mode")
	require.NoError(t, row.Scan(&journalMode))
	require.Equal(t, "wal", journalMode)

	var busyTimeout int
	row = store.DB.QueryRow("PRAGMA busy_timeout")
	require.NoError(t, row.Scan(&busyTimeout))
	require.Equal(t, 5000, busyTimeout)

	var fullFsync int
	row = store.DB.QueryRow("PRAGMA fullfsync")
	require.NoError(t, row.Scan(&fullFsync))
	require.Equal(t, 1, fullFsync)
}
