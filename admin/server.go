package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lightninglabs/aperture/adminrpc"
	"github.com/lightninglabs/aperture/aperturedb"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/proxy"
	"github.com/lightningnetwork/lnd/lntypes"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// defaultLimit is the default pagination limit.
	defaultLimit = 50

	// maxLimit is the maximum pagination limit.
	maxLimit = 1000
)

// ServerConfig holds the dependencies for the admin gRPC server.
type ServerConfig struct {
	// Network is the Lightning Network being used (e.g. "mainnet",
	// "testnet", "regtest").
	Network string

	// ListenAddr is the address Aperture is listening on.
	ListenAddr string

	// Insecure indicates whether Aperture is running without TLS.
	Insecure bool

	// TransactionStore provides access to L402 transaction data.
	TransactionStore *aperturedb.L402TransactionsStore

	// Services returns the current list of configured services.
	Services func() []*proxy.Service

	// UpdateServices updates the proxy's service configuration.
	UpdateServices func([]*proxy.Service) error

	// SecretStore is the secret store for token revocation.
	SecretStore mint.SecretStore
}

// Server implements the adminrpc.AdminServer gRPC interface.
type Server struct {
	adminrpc.UnimplementedAdminServer

	cfg ServerConfig
}

// NewServer creates a new admin gRPC server with the given configuration.
func NewServer(cfg ServerConfig) *Server {
	return &Server{cfg: cfg}
}

// GetInfo returns basic server information.
func (s *Server) GetInfo(_ context.Context,
	_ *adminrpc.GetInfoRequest) (*adminrpc.GetInfoResponse, error) {

	return &adminrpc.GetInfoResponse{
		Network:    s.cfg.Network,
		ListenAddr: s.cfg.ListenAddr,
		Insecure:   s.cfg.Insecure,
	}, nil
}

// GetHealth returns a simple health check response.
func (s *Server) GetHealth(_ context.Context,
	_ *adminrpc.GetHealthRequest) (*adminrpc.GetHealthResponse, error) {

	return &adminrpc.GetHealthResponse{
		Status: "ok",
	}, nil
}

// ListServices returns the current list of configured services.
func (s *Server) ListServices(_ context.Context,
	_ *adminrpc.ListServicesRequest) (
	*adminrpc.ListServicesResponse, error) {

	services := s.cfg.Services()
	resp := make([]*adminrpc.Service, 0, len(services))

	for _, svc := range services {
		resp = append(resp, &adminrpc.Service{
			Name:       svc.Name,
			Address:    svc.Address,
			Protocol:   svc.Protocol,
			HostRegexp: svc.HostRegexp,
			PathRegexp: svc.PathRegexp,
			Price:      svc.Price,
			Auth:       string(svc.Auth),
		})
	}

	return &adminrpc.ListServicesResponse{Services: resp}, nil
}

// CreateService creates a new backend service.
func (s *Server) CreateService(_ context.Context,
	req *adminrpc.CreateServiceRequest) (*adminrpc.Service, error) {

	if req.Name == "" {
		return nil, status.Error(
			codes.InvalidArgument, "name is required",
		)
	}
	if req.Address == "" {
		return nil, status.Error(
			codes.InvalidArgument, "address is required",
		)
	}

	protocol := req.Protocol
	if protocol == "" {
		protocol = "http"
	}

	hostRegexp := req.HostRegexp
	if hostRegexp == "" {
		hostRegexp = ".*"
	}

	if req.Price < 0 {
		return nil, status.Error(
			codes.InvalidArgument, "price must be >= 0",
		)
	}

	if req.Auth != "" {
		if err := validateAuthLevel(req.Auth); err != nil {
			return nil, status.Error(
				codes.InvalidArgument, err.Error(),
			)
		}
	}

	services := s.cfg.Services()
	for _, svc := range services {
		if svc.Name == req.Name {
			return nil, status.Error(
				codes.AlreadyExists,
				"service already exists",
			)
		}
	}

	newSvc := &proxy.Service{
		Name:       req.Name,
		Address:    req.Address,
		Protocol:   protocol,
		HostRegexp: hostRegexp,
		PathRegexp: req.PathRegexp,
		Price:      req.Price,
	}
	if req.Auth != "" {
		newSvc.Auth = auth.Level(req.Auth)
	}

	services = append(services, newSvc)
	if err := s.cfg.UpdateServices(services); err != nil {
		log.Errorf("Error creating service: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to create service",
		)
	}

	return &adminrpc.Service{
		Name:       newSvc.Name,
		Address:    newSvc.Address,
		Protocol:   newSvc.Protocol,
		HostRegexp: newSvc.HostRegexp,
		PathRegexp: newSvc.PathRegexp,
		Price:      newSvc.Price,
		Auth:       string(newSvc.Auth),
	}, nil
}

// UpdateService updates a service's mutable fields.
func (s *Server) UpdateService(_ context.Context,
	req *adminrpc.UpdateServiceRequest) (*adminrpc.Service, error) {

	if req.Name == "" {
		return nil, status.Error(
			codes.InvalidArgument, "missing service name",
		)
	}

	services := s.cfg.Services()
	var found *proxy.Service
	for _, svc := range services {
		if svc.Name == req.Name {
			found = svc
			break
		}
	}

	if found == nil {
		return nil, status.Error(
			codes.NotFound, "service not found",
		)
	}

	if req.Address != "" {
		found.Address = req.Address
	}
	if req.Protocol != "" {
		found.Protocol = req.Protocol
	}
	if req.HostRegexp != "" {
		found.HostRegexp = req.HostRegexp
	}
	if req.PathRegexp != "" {
		found.PathRegexp = req.PathRegexp
	}
	if req.Price != 0 {
		found.Price = req.Price
	}
	if req.Auth != "" {
		found.Auth = auth.Level(req.Auth)
	}

	if err := s.cfg.UpdateServices(services); err != nil {
		log.Errorf("Error updating services: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to update services",
		)
	}

	return &adminrpc.Service{
		Name:       found.Name,
		Address:    found.Address,
		Protocol:   found.Protocol,
		HostRegexp: found.HostRegexp,
		PathRegexp: found.PathRegexp,
		Price:      found.Price,
		Auth:       string(found.Auth),
	}, nil
}

// DeleteService removes a backend service by name.
func (s *Server) DeleteService(_ context.Context,
	req *adminrpc.DeleteServiceRequest) (
	*adminrpc.DeleteServiceResponse, error) {

	if req.Name == "" {
		return nil, status.Error(
			codes.InvalidArgument, "missing service name",
		)
	}

	services := s.cfg.Services()
	filtered := make([]*proxy.Service, 0, len(services))
	found := false
	for _, svc := range services {
		if svc.Name == req.Name {
			found = true
			continue
		}
		filtered = append(filtered, svc)
	}

	if !found {
		return nil, status.Error(
			codes.NotFound, "service not found",
		)
	}

	if err := s.cfg.UpdateServices(filtered); err != nil {
		log.Errorf("Error deleting service: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to delete service",
		)
	}

	return &adminrpc.DeleteServiceResponse{Status: "deleted"}, nil
}

// ListTransactions returns a paginated list of L402 transactions.
func (s *Server) ListTransactions(ctx context.Context,
	req *adminrpc.ListTransactionsRequest) (
	*adminrpc.ListTransactionsResponse, error) {

	if s.cfg.TransactionStore == nil {
		return nil, status.Error(
			codes.Unavailable,
			"transaction store not available",
		)
	}

	limit := int32(defaultLimit)
	if req.Limit > 0 {
		limit = req.Limit
		if limit > maxLimit {
			limit = maxLimit
		}
	}

	offset := req.Offset

	var (
		txns []aperturedb.L402Transaction
		err  error
	)

	switch {
	case req.Service != "":
		txns, err = s.cfg.TransactionStore.ListByService(
			ctx, req.Service, limit, offset,
		)

	case req.State != "":
		txns, err = s.cfg.TransactionStore.ListByState(
			ctx, req.State, limit, offset,
		)

	case req.StartDate != "" && req.EndDate != "":
		from, parseErr := time.Parse(time.RFC3339, req.StartDate)
		if parseErr != nil {
			return nil, status.Error(
				codes.InvalidArgument,
				"invalid start_date format",
			)
		}
		to, parseErr := time.Parse(time.RFC3339, req.EndDate)
		if parseErr != nil {
			return nil, status.Error(
				codes.InvalidArgument,
				"invalid end_date format",
			)
		}
		txns, err = s.cfg.TransactionStore.ListByDateRange(
			ctx, from, to, limit, offset,
		)

	default:
		txns, err = s.cfg.TransactionStore.ListTransactions(
			ctx, limit, offset,
		)
	}

	if err != nil {
		log.Errorf("Error listing transactions: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to list transactions",
		)
	}

	return &adminrpc.ListTransactionsResponse{
		Transactions: txnsToProto(txns),
	}, nil
}

// ListTokens returns settled transactions representing active L402 tokens.
func (s *Server) ListTokens(ctx context.Context,
	_ *adminrpc.ListTokensRequest) (
	*adminrpc.ListTokensResponse, error) {

	if s.cfg.TransactionStore == nil {
		return nil, status.Error(
			codes.Unavailable,
			"transaction store not available",
		)
	}

	txns, err := s.cfg.TransactionStore.ListByState(
		ctx, "settled", int32(maxLimit), 0,
	)
	if err != nil {
		log.Errorf("Error listing tokens: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to list tokens",
		)
	}

	return &adminrpc.ListTokensResponse{
		Tokens: txnsToProto(txns),
	}, nil
}

// RevokeToken revokes an L402 token by revoking its secret and deleting the
// transaction record.
func (s *Server) RevokeToken(ctx context.Context,
	req *adminrpc.RevokeTokenRequest) (
	*adminrpc.RevokeTokenResponse, error) {

	if s.cfg.TransactionStore == nil || s.cfg.SecretStore == nil {
		return nil, status.Error(
			codes.Unavailable,
			"required stores not available",
		)
	}

	if req.TokenId == "" {
		return nil, status.Error(
			codes.InvalidArgument, "missing token ID",
		)
	}

	tokenID, err := hex.DecodeString(req.TokenId)
	if err != nil {
		return nil, status.Error(
			codes.InvalidArgument, "invalid token ID",
		)
	}

	// Look up the transaction to get the payment hash.
	txns, err := s.cfg.TransactionStore.ListByState(
		ctx, "settled", int32(maxLimit), 0,
	)
	if err != nil {
		log.Errorf("Error looking up token: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to look up token",
		)
	}

	found := false
	for _, txn := range txns {
		if hex.EncodeToString(txn.TokenID) != req.TokenId {
			continue
		}

		err = revokeSecretByTokenIDAndHash(
			ctx, s.cfg.SecretStore,
			txn.TokenID, txn.PaymentHash,
		)
		if err != nil {
			log.Errorf("Error revoking secret: %v", err)
			return nil, status.Error(
				codes.Internal, "failed to revoke token",
			)
		}

		found = true
		break
	}

	if !found {
		return nil, status.Error(
			codes.NotFound, "token not found",
		)
	}

	if err := s.cfg.TransactionStore.DeleteByTokenID(
		ctx, tokenID,
	); err != nil {
		log.Errorf("Error deleting transaction: %v", err)
		return nil, status.Error(
			codes.Internal,
			"token revoked but failed to delete transaction",
		)
	}

	return &adminrpc.RevokeTokenResponse{Status: "revoked"}, nil
}

// GetStats returns aggregated revenue and transaction statistics.
func (s *Server) GetStats(ctx context.Context,
	req *adminrpc.GetStatsRequest) (*adminrpc.GetStatsResponse, error) {

	if s.cfg.TransactionStore == nil {
		return nil, status.Error(
			codes.Unavailable,
			"transaction store not available",
		)
	}

	var from, to time.Time
	if req.From != "" {
		var err error
		from, err = time.Parse(time.RFC3339, req.From)
		if err != nil {
			return nil, status.Error(
				codes.InvalidArgument,
				"invalid 'from' time format",
			)
		}
	}
	if req.To != "" {
		var err error
		to, err = time.Parse(time.RFC3339, req.To)
		if err != nil {
			return nil, status.Error(
				codes.InvalidArgument,
				"invalid 'to' time format",
			)
		}
	}

	totalRevenue, err := s.cfg.TransactionStore.GetTotalRevenue(ctx)
	if err != nil {
		log.Errorf("Error getting total revenue: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to get revenue stats",
		)
	}

	count, err := s.cfg.TransactionStore.CountTransactions(ctx)
	if err != nil {
		log.Errorf("Error getting transaction count: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to get transaction count",
		)
	}

	revenueRows, err := s.cfg.TransactionStore.GetRevenueStats(
		ctx, from, to,
	)
	if err != nil {
		log.Errorf("Error getting revenue stats: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to get revenue breakdown",
		)
	}

	breakdown := make(
		[]*adminrpc.ServiceRevenue, 0, len(revenueRows),
	)
	for _, row := range revenueRows {
		var rev int64
		switch v := row.TotalRevenue.(type) {
		case int64:
			rev = v
		case int32:
			rev = int64(v)
		}

		breakdown = append(breakdown, &adminrpc.ServiceRevenue{
			ServiceName:      row.ServiceName,
			TotalRevenueSats: rev,
		})
	}

	return &adminrpc.GetStatsResponse{
		TotalRevenueSats: totalRevenue,
		TransactionCount: count,
		ServiceBreakdown: breakdown,
	}, nil
}

// revokeSecretByTokenIDAndHash revokes the macaroon secret for a given
// token ID and payment hash combination.
func revokeSecretByTokenIDAndHash(ctx context.Context,
	secretStore mint.SecretStore,
	tokenID, paymentHash []byte) error {

	var hash lntypes.Hash
	copy(hash[:], paymentHash)

	var tokID l402.TokenID
	copy(tokID[:], tokenID)

	idBytes := l402.EncodeIdentifierBytes(hash, tokID)
	idHash := sha256.Sum256(idBytes)

	return secretStore.RevokeSecret(ctx, idHash)
}

// txnsToProto converts database transaction records to proto Transaction
// messages.
func txnsToProto(txns []aperturedb.L402Transaction) []*adminrpc.Transaction {
	resp := make([]*adminrpc.Transaction, 0, len(txns))
	for _, txn := range txns {
		tr := &adminrpc.Transaction{
			Id:          txn.ID,
			TokenId:     hex.EncodeToString(txn.TokenID),
			PaymentHash: hex.EncodeToString(txn.PaymentHash),
			ServiceName: txn.ServiceName,
			PriceSats:   txn.PriceSats,
			State:       txn.State,
			CreatedAt:   txn.CreatedAt.UTC().Format(time.RFC3339),
		}
		if txn.SettledAt.Valid {
			tr.SettledAt = txn.SettledAt.Time.UTC().Format(
				time.RFC3339,
			)
		}
		resp = append(resp, tr)
	}

	return resp
}

// validateAuthLevel checks that an auth string is a valid auth.Level value.
func validateAuthLevel(s string) error {
	lower := strings.ToLower(s)

	switch {
	case lower == "on" || lower == "off" || lower == "true" ||
		lower == "false" || lower == "":

		return nil

	case strings.HasPrefix(lower, "freebie "):
		parts := strings.SplitN(lower, " ", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid auth format, use " +
				"'freebie N'")
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid freebie count, must be " +
				"a positive integer")
		}
		return nil

	default:
		return fmt.Errorf("invalid auth level %q, must be 'on', "+
			"'off', or 'freebie N'", s)
	}
}
