package admin

import (
	"context"
	"testing"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/stretchr/testify/require"
)

func newTestServer() *Server {
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
			return services
		},
		UpdateServices: func(s []*proxy.Service) error {
			services = s
			return nil
		},
	})
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
			Name:    "new-svc",
			Address: "localhost:9999",
			Price:   200,
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
			Name:    "new-svc",
			Address: "localhost:9999",
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

func TestGetStatsNoStore(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	_, err := s.GetStats(context.Background(),
		&adminrpc.GetStatsRequest{},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available")
}
