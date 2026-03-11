package aperture

import (
	"testing"

	"github.com/lightninglabs/aperture/aperturedb"
	"github.com/stretchr/testify/require"
)

// TestGetTLSConfigAllowsEmptyServerName ensures that generating a default
// self-signed TLS cert without a server name succeeds. This used to work
// before Go 1.25 tightened SAN validation, so we rely on Aperture handling it.
func TestGetTLSConfigAllowsEmptyServerName(t *testing.T) {
	t.Parallel()

	cfg, err := getTLSConfig("", t.TempDir(), false)
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

func TestAsMintTransactionStoreNilPointer(t *testing.T) {
	t.Parallel()

	var txnStore *aperturedb.L402TransactionsStore
	mintTxnStore := asMintTransactionStore(txnStore)

	require.Nil(t, mintTxnStore)
}
