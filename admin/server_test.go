package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/lightninglabs/aperture/aperturedb"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
)

func newTestServer() *Server {
	var mu sync.Mutex
	services := []*proxy.Service{
		{
			Name:     "test-svc",
			Address:  "localhost:8080",
			Protocol: "http",
			Price:    100,
		},
	}

	return NewServer(ServerConfig{
		Network:    "regtest",
		ListenAddr: "localhost:9090",
		Insecure:   true,
		Services: func() []*proxy.Service {
			mu.Lock()
			defer mu.Unlock()
			cpy := make([]*proxy.Service, len(services))
			copy(cpy, services)
			return cpy
		},
		UpdateServices: func(s []*proxy.Service) error {
			mu.Lock()
			defer mu.Unlock()
			services = s
			return nil
		},
	})
}

type mockSecretStore struct {
	revoked map[[sha256.Size]byte]struct{}
}

var _ mint.SecretStore = (*mockSecretStore)(nil)

func newMockSecretStore() *mockSecretStore {
	return &mockSecretStore{
		revoked: make(map[[sha256.Size]byte]struct{}),
	}
}

func (m *mockSecretStore) NewSecret(_ context.Context,
	_ [sha256.Size]byte) ([l402.SecretSize]byte, error) {

	var secret [l402.SecretSize]byte
	return secret, nil
}

func (m *mockSecretStore) GetSecret(_ context.Context,
	_ [sha256.Size]byte) ([l402.SecretSize]byte, error) {

	var secret [l402.SecretSize]byte
	return secret, nil
}

func (m *mockSecretStore) RevokeSecret(_ context.Context,
	id [sha256.Size]byte) error {

	m.revoked[id] = struct{}{}
	return nil
}

func newTestServerWithStores(t *testing.T) (*Server,
	*aperturedb.L402TransactionsStore, *mockSecretStore) {

	t.Helper()

	db := aperturedb.NewTestDB(t)
	dbTxer := aperturedb.NewTransactionExecutor(
		db.BaseDB, func(tx *sql.Tx) aperturedb.L402TransactionsDB {
			return db.WithTx(tx)
		},
	)
	txStore := aperturedb.NewL402TransactionsStore(dbTxer)
	secretStore := newMockSecretStore()

	var mu sync.Mutex
	svc := []*proxy.Service{
		{
			Name:     "test-svc",
			Address:  "localhost:8080",
			Protocol: "http",
			Price:    100,
		},
	}

	server := NewServer(ServerConfig{
		Network:          "regtest",
		ListenAddr:       "localhost:9090",
		Insecure:         true,
		TransactionStore: txStore,
		SecretStore:      secretStore,
		Services: func() []*proxy.Service {
			mu.Lock()
			defer mu.Unlock()
			cpy := make([]*proxy.Service, len(svc))
			copy(cpy, svc)
			return cpy
		},
		UpdateServices: func(s []*proxy.Service) error {
			mu.Lock()
			defer mu.Unlock()
			svc = s
			return nil
		},
	})

	return server, txStore, secretStore
}

func recordSettledTransaction(t *testing.T, store *aperturedb.L402TransactionsStore,
	tokenID, paymentHash []byte, price int64) {

	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	err := store.RecordTransaction(
		ctx, tokenID, paymentHash, "test-svc", price, nil,
	)
	require.NoError(t, err)

	err = store.SettleTransaction(ctx, paymentHash)
	require.NoError(t, err)
}

func TestGetInfo(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	resp, err := s.GetInfo(context.Background(), &adminrpc.GetInfoRequest{})
	require.NoError(t, err)
	require.Equal(t, "regtest", resp.Network)
	require.Equal(t, "localhost:9090", resp.ListenAddr)
	require.True(t, resp.Insecure)
}

func TestGetHealth(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	resp, err := s.GetHealth(
		context.Background(), &adminrpc.GetHealthRequest{},
	)
	require.NoError(t, err)
	require.Equal(t, "ok", resp.Status)
}

func TestListServices(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	resp, err := s.ListServices(
		context.Background(), &adminrpc.ListServicesRequest{},
	)
	require.NoError(t, err)
	require.Len(t, resp.Services, 1)
	require.Equal(t, "test-svc", resp.Services[0].Name)
	require.Equal(t, int64(100), resp.Services[0].Price)
}

func TestCreateService(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	// Create a new service.
	svc, err := s.CreateService(context.Background(),
		&adminrpc.CreateServiceRequest{
			Name:       "new-svc",
			Address:    "localhost:9999",
			PathRegexp: "^/api/new/.*",
			Price:      200,
		},
	)
	require.NoError(t, err)
	require.Equal(t, "new-svc", svc.Name)
	require.Equal(t, "http", svc.Protocol)

	// List should now have 2 services.
	resp, err := s.ListServices(
		context.Background(), &adminrpc.ListServicesRequest{},
	)
	require.NoError(t, err)
	require.Len(t, resp.Services, 2)

	// Duplicate should fail.
	_, err = s.CreateService(context.Background(),
		&adminrpc.CreateServiceRequest{
			Name:       "new-svc",
			Address:    "localhost:9999",
			PathRegexp: "^/api/new/.*",
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")

	// Missing name should fail.
	_, err = s.CreateService(context.Background(),
		&adminrpc.CreateServiceRequest{
			Address: "localhost:9999",
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "name is required")
}

func TestUpdateService(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	svc, err := s.UpdateService(context.Background(),
		&adminrpc.UpdateServiceRequest{
			Name:    "test-svc",
			Address: "localhost:7777",
		},
	)
	require.NoError(t, err)
	require.Equal(t, "localhost:7777", svc.Address)

	// Non-existent service should fail.
	_, err = s.UpdateService(context.Background(),
		&adminrpc.UpdateServiceRequest{
			Name:    "nonexistent",
			Address: "localhost:1234",
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestUpdateServiceCanSetPriceToZero(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	price := int64(0)
	svc, err := s.UpdateService(context.Background(),
		&adminrpc.UpdateServiceRequest{
			Name:  "test-svc",
			Price: &price,
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), svc.Price)
}

func TestCreateServiceRejectsInvalidAuth(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	_, err := s.CreateService(context.Background(),
		&adminrpc.CreateServiceRequest{
			Name:       "bad-auth-svc",
			Address:    "localhost:1234",
			PathRegexp: "^/api/bad/.*",
			Auth:       "freebie -5",
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid freebie count")
}

func TestUpdateServiceRejectsInvalidAuth(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	_, err := s.UpdateService(context.Background(),
		&adminrpc.UpdateServiceRequest{
			Name: "test-svc",
			Auth: "freebie -5",
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid freebie count")
}

func TestCreateServiceWithTimeout(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	svc, err := s.CreateService(context.Background(),
		&adminrpc.CreateServiceRequest{
			Name:       "timed-svc",
			Address:    "localhost:9999",
			PathRegexp: "^/api/timed/.*",
			Price:      100,
			Timeout:    60,
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(60), svc.Timeout)

	// Verify timeout is returned by ListServices.
	resp, err := s.ListServices(
		context.Background(), &adminrpc.ListServicesRequest{},
	)
	require.NoError(t, err)
	var found *adminrpc.Service
	for _, s := range resp.Services {
		if s.Name == "timed-svc" {
			found = s
			break
		}
	}
	require.NotNil(t, found)
	require.Equal(t, int64(60), found.Timeout)
}

func TestUpdateServiceTimeout(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	timeout := int64(120)
	svc, err := s.UpdateService(context.Background(),
		&adminrpc.UpdateServiceRequest{
			Name:    "test-svc",
			Timeout: &timeout,
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(120), svc.Timeout)
}

func TestUpdateServiceCanSetTimeoutToZero(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	// First set a non-zero timeout.
	timeout := int64(60)
	_, err := s.UpdateService(context.Background(),
		&adminrpc.UpdateServiceRequest{
			Name:    "test-svc",
			Timeout: &timeout,
		},
	)
	require.NoError(t, err)

	// Now reset to 0 (no expiry) via optional field.
	zero := int64(0)
	svc, err := s.UpdateService(context.Background(),
		&adminrpc.UpdateServiceRequest{
			Name:    "test-svc",
			Timeout: &zero,
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), svc.Timeout)
}

func TestCreateServiceRejectsNegativeTimeout(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	_, err := s.CreateService(context.Background(),
		&adminrpc.CreateServiceRequest{
			Name:       "bad-timeout-svc",
			Address:    "localhost:1234",
			PathRegexp: "^/api/bad/.*",
			Timeout:    -1,
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout must be >= 0")
}

func TestUpdateServiceRejectsNegativeTimeout(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	timeout := int64(-5)
	_, err := s.UpdateService(context.Background(),
		&adminrpc.UpdateServiceRequest{
			Name:    "test-svc",
			Timeout: &timeout,
		},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout must be >= 0")
}

func TestDeleteService(t *testing.T) {
	t.Parallel()

	s := newTestServer()

	resp, err := s.DeleteService(context.Background(),
		&adminrpc.DeleteServiceRequest{Name: "test-svc"},
	)
	require.NoError(t, err)
	require.Equal(t, "deleted", resp.Status)

	// List should be empty now.
	listResp, err := s.ListServices(
		context.Background(), &adminrpc.ListServicesRequest{},
	)
	require.NoError(t, err)
	require.Len(t, listResp.Services, 0)

	// Deleting again should fail.
	_, err = s.DeleteService(context.Background(),
		&adminrpc.DeleteServiceRequest{Name: "test-svc"},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestListTransactionsNoStore(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	_, err := s.ListTransactions(context.Background(),
		&adminrpc.ListTransactionsRequest{},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available")
}

func TestListTransactionsRejectsInvalidState(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestServerWithStores(t)

	_, err := s.ListTransactions(context.Background(),
		&adminrpc.ListTransactionsRequest{State: "bogus"},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid state")

	// Valid states should not error.
	_, err = s.ListTransactions(context.Background(),
		&adminrpc.ListTransactionsRequest{State: "settled"},
	)
	require.NoError(t, err)

	_, err = s.ListTransactions(context.Background(),
		&adminrpc.ListTransactionsRequest{State: "pending"},
	)
	require.NoError(t, err)
}

func TestGetStatsNoStore(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	_, err := s.GetStats(context.Background(),
		&adminrpc.GetStatsRequest{},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available")
}

func TestGetStatsDateRangeScopesTotals(t *testing.T) {
	t.Parallel()

	s, txStore, _ := newTestServerWithStores(t)

	recordSettledTransaction(
		t, txStore, testTokenID(9000), testHash(9000), 500,
	)

	noRangeResp, err := s.GetStats(
		context.Background(), &adminrpc.GetStatsRequest{},
	)
	require.NoError(t, err)
	require.Equal(t, int64(500), noRangeResp.TotalRevenueSats)
	require.Equal(t, int64(1), noRangeResp.TransactionCount)

	from := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Add(25 * time.Hour).Format(time.RFC3339)
	rangeResp, err := s.GetStats(context.Background(), &adminrpc.GetStatsRequest{
		From: from,
		To:   to,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), rangeResp.TotalRevenueSats)
	require.Equal(t, int64(0), rangeResp.TransactionCount)
	require.Len(t, rangeResp.ServiceBreakdown, 0)
}

// testTokenID returns a 32-byte token ID derived from an index.
func testTokenID(i int) []byte {
	id := make([]byte, l402.TokenIDSize)
	copy(id, fmt.Sprintf("token-%04d", i))
	return id
}

// testHash returns a 32-byte payment hash derived from an index.
func testHash(i int) []byte {
	h := make([]byte, lntypes.HashSize)
	copy(h, fmt.Sprintf("hash-%04d", i))
	return h
}

func TestRevokeTokenBeyondOldScanLimit(t *testing.T) {
	t.Parallel()

	s, txStore, secretStore := newTestServerWithStores(t)

	targetTokenID := testTokenID(0)
	targetHash := testHash(0)

	for i := 0; i < 1001; i++ {
		recordSettledTransaction(
			t, txStore, testTokenID(i), testHash(i), 10,
		)
	}

	resp, err := s.RevokeToken(context.Background(),
		&adminrpc.RevokeTokenRequest{
			TokenId: hex.EncodeToString(targetTokenID),
		},
	)
	require.NoError(t, err)
	require.Equal(t, "revoked", resp.Status)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	_, err = txStore.GetSettledByTokenID(ctx, targetTokenID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no rows")

	// Ensure secret revocation was attempted for the revoked token.
	var paymentHash lntypes.Hash
	copy(paymentHash[:], targetHash)
	var token l402.TokenID
	copy(token[:], targetTokenID)
	idBytes := l402.EncodeIdentifierBytes(paymentHash, token)
	idHash := sha256.Sum256(idBytes)
	_, ok := secretStore.revoked[idHash]
	require.True(t, ok)
}

func TestListTokensPagination(t *testing.T) {
	t.Parallel()

	s, txStore, _ := newTestServerWithStores(t)

	for i := 0; i < 3; i++ {
		recordSettledTransaction(
			t, txStore, testTokenID(8000+i), testHash(8000+i), 10,
		)
	}

	resp, err := s.ListTokens(context.Background(), &adminrpc.ListTokensRequest{
		Limit:  2,
		Offset: 0,
	})
	require.NoError(t, err)
	require.Len(t, resp.Tokens, 2)

	resp, err = s.ListTokens(context.Background(), &adminrpc.ListTokensRequest{
		Limit:  2,
		Offset: 2,
	})
	require.NoError(t, err)
	require.Len(t, resp.Tokens, 1)
}
