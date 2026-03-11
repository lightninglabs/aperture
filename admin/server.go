package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
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

	// defaultProtocol is the default protocol for new services.
	defaultProtocol = "http"
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

	cfg         ServerConfig
	servicesMtx sync.RWMutex
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

	s.servicesMtx.RLock()
	defer s.servicesMtx.RUnlock()

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

	s.servicesMtx.Lock()
	defer s.servicesMtx.Unlock()

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
		protocol = defaultProtocol
	}
	if protocol != defaultProtocol && protocol != "https" {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"protocol must be 'http' or 'https', got %q",
			protocol,
		)
	}

	hostRegexp := req.HostRegexp
	if hostRegexp == "" {
		hostRegexp = ".*"
	}
	if _, err := regexp.Compile(hostRegexp); err != nil {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"invalid host_regexp: %v", err,
		)
	}
	if req.PathRegexp != "" {
		if _, err := regexp.Compile(req.PathRegexp); err != nil {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"invalid path_regexp: %v", err,
			)
		}
	}

	if req.Price < 0 {
		return nil, status.Error(
			codes.InvalidArgument, "price must be >= 0",
		)
	}

	var normalizedAuth string
	if req.Auth != "" {
		var err error
		normalizedAuth, err = validateAuthLevel(req.Auth)
		if err != nil {
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
	if normalizedAuth != "" {
		newSvc.Auth = auth.Level(normalizedAuth)
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

	s.servicesMtx.Lock()
	defer s.servicesMtx.Unlock()

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

	// Build an updated copy rather than mutating the original in place,
	// so the shared pointer is not corrupted if UpdateServices fails.
	updated := *found
	if req.Address != "" {
		updated.Address = req.Address
	}
	if req.Protocol != "" {
		if req.Protocol != defaultProtocol && req.Protocol != "https" {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"protocol must be 'http' or 'https', "+
					"got %q", req.Protocol,
			)
		}
		updated.Protocol = req.Protocol
	}
	if req.HostRegexp != "" {
		if _, err := regexp.Compile(req.HostRegexp); err != nil {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"invalid host_regexp: %v", err,
			)
		}
		updated.HostRegexp = req.HostRegexp
	}
	if req.PathRegexp != "" {
		if _, err := regexp.Compile(req.PathRegexp); err != nil {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"invalid path_regexp: %v", err,
			)
		}
		updated.PathRegexp = req.PathRegexp
	}
	if req.Price != nil {
		if req.GetPrice() < 0 {
			return nil, status.Error(
				codes.InvalidArgument,
				"price must be >= 0",
			)
		}
		updated.Price = req.GetPrice()
	}
	if req.Auth != "" {
		normalizedAuth, err := validateAuthLevel(req.Auth)
		if err != nil {
			return nil, status.Error(
				codes.InvalidArgument, err.Error(),
			)
		}
		updated.Auth = auth.Level(normalizedAuth)
	}

	// Replace the pointer in the slice with the updated copy.
	for i, svc := range services {
		if svc.Name == req.Name {
			services[i] = &updated
			break
		}
	}

	if err := s.cfg.UpdateServices(services); err != nil {
		log.Errorf("Error updating services: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to update services",
		)
	}

	return &adminrpc.Service{
		Name:       updated.Name,
		Address:    updated.Address,
		Protocol:   updated.Protocol,
		HostRegexp: updated.HostRegexp,
		PathRegexp: updated.PathRegexp,
		Price:      updated.Price,
		Auth:       string(updated.Auth),
	}, nil
}

// DeleteService removes a backend service by name.
func (s *Server) DeleteService(_ context.Context,
	req *adminrpc.DeleteServiceRequest) (
	*adminrpc.DeleteServiceResponse, error) {

	s.servicesMtx.Lock()
	defer s.servicesMtx.Unlock()

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
	if offset < 0 {
		return nil, status.Error(
			codes.InvalidArgument, "offset must be >= 0",
		)
	}

	// Ensure at most one filter is set to avoid silently dropping
	// filters.
	filterCount := 0
	if req.Service != "" {
		filterCount++
	}
	if req.State != "" {
		filterCount++
	}
	if req.StartDate != "" || req.EndDate != "" {
		if req.StartDate == "" || req.EndDate == "" {
			return nil, status.Error(
				codes.InvalidArgument,
				"both start_date and end_date must be "+
					"set together",
			)
		}
		filterCount++
	}
	if filterCount > 1 {
		return nil, status.Error(
			codes.InvalidArgument,
			"only one filter (service, state, or date range) "+
				"may be set at a time",
		)
	}

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
		switch req.State {
		case "pending", "settled":
		default:
			return nil, status.Errorf(
				codes.InvalidArgument,
				"invalid state %q, must be 'pending' "+
					"or 'settled'",
				req.State,
			)
		}
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
		if to.Before(from) {
			return nil, status.Error(
				codes.InvalidArgument,
				"end_date must be >= start_date",
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
	req *adminrpc.ListTokensRequest) (
	*adminrpc.ListTokensResponse, error) {

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
	if offset < 0 {
		return nil, status.Error(
			codes.InvalidArgument, "offset must be >= 0",
		)
	}

	txns, err := s.cfg.TransactionStore.ListByState(
		ctx, "settled", limit, offset,
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
	if len(tokenID) != l402.TokenIDSize {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"token ID must be %d bytes, got %d",
			l402.TokenIDSize, len(tokenID),
		)
	}

	// Look up the transaction to get the payment hash.
	txn, err := s.cfg.TransactionStore.GetSettledByTokenID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(
				codes.NotFound, "token not found",
			)
		}

		log.Errorf("Error looking up token: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to look up token",
		)
	}

	err = revokeSecretByTokenIDAndHash(
		ctx, s.cfg.SecretStore, txn.TokenID, txn.PaymentHash,
	)
	if err != nil {
		log.Errorf("Error revoking secret: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to revoke token",
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
	hasFrom := req.From != ""
	hasTo := req.To != ""
	if hasFrom != hasTo {
		return nil, status.Error(
			codes.InvalidArgument,
			"both 'from' and 'to' must be set together",
		)
	}

	if hasFrom {
		var err error
		from, err = time.Parse(time.RFC3339, req.From)
		if err != nil {
			return nil, status.Error(
				codes.InvalidArgument,
				"invalid 'from' time format",
			)
		}

		to, err = time.Parse(time.RFC3339, req.To)
		if err != nil {
			return nil, status.Error(
				codes.InvalidArgument,
				"invalid 'to' time format",
			)
		}

		if to.Before(from) {
			return nil, status.Error(
				codes.InvalidArgument,
				"'to' must be greater than or equal to 'from'",
			)
		}
	}

	var (
		totalRevenue int64
		count        int64
		err          error
	)
	if hasFrom {
		totalRevenue, err = s.cfg.TransactionStore.
			GetTotalRevenueByDateRange(ctx, from, to)
		if err != nil {
			log.Errorf("Error getting total revenue by date range: %v",
				err)
			return nil, status.Error(
				codes.Internal, "failed to get revenue stats",
			)
		}

		count, err = s.cfg.TransactionStore.CountTransactionsByDateRange(
			ctx, from, to,
		)
		if err != nil {
			log.Errorf("Error getting transaction count by date "+
				"range: %v", err)
			return nil, status.Error(
				codes.Internal, "failed to get transaction count",
			)
		}
	} else {
		totalRevenue, err = s.cfg.TransactionStore.GetTotalRevenue(ctx)
		if err != nil {
			log.Errorf("Error getting total revenue: %v", err)
			return nil, status.Error(
				codes.Internal, "failed to get revenue stats",
			)
		}

		count, err = s.cfg.TransactionStore.CountTransactions(ctx)
		if err != nil {
			log.Errorf("Error getting transaction count: %v", err)
			return nil, status.Error(
				codes.Internal, "failed to get transaction count",
			)
		}
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
		breakdown = append(breakdown, &adminrpc.ServiceRevenue{
			ServiceName:      row.ServiceName,
			TotalRevenueSats: row.TotalRevenue,
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

	if len(paymentHash) != lntypes.HashSize {
		return fmt.Errorf("payment hash must be %d bytes, got %d",
			lntypes.HashSize, len(paymentHash))
	}
	if len(tokenID) != l402.TokenIDSize {
		return fmt.Errorf("token ID must be %d bytes, got %d",
			l402.TokenIDSize, len(tokenID))
	}

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

// validateAuthLevel checks that an auth string is a valid auth.Level value
// and returns the normalized (lowercased) form.
func validateAuthLevel(s string) (string, error) {
	lower := strings.ToLower(s)

	switch {
	case lower == "on" || lower == "off" || lower == "true" ||
		lower == "false" || lower == "":

		return lower, nil

	case strings.HasPrefix(lower, "freebie "):
		parts := strings.SplitN(lower, " ", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid auth format, use " +
				"'freebie N'")
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid freebie count, must " +
				"be a positive integer")
		}
		return lower, nil

	default:
		return "", fmt.Errorf("invalid auth level %q, must be "+
			"'on', 'off', or 'freebie N'", s)
	}
}
