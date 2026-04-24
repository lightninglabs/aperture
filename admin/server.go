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

	// maxRegexpLen is the maximum length of a user-supplied regular
	// expression for host or path matching.
	maxRegexpLen = 1024
)

// reservedPaths are URL prefixes used by the admin API and dashboard.
// Services whose path_regexp matches any of these are rejected to prevent
// accidentally hijacking internal traffic.
var reservedPaths = []string{
	"/api/admin/",
	"/api/proxy/",
	"/_next/",
}

// pathRegexpConflictsWithReserved checks whether a compiled path_regexp
// would match any reserved internal path prefix. An empty pattern is also
// rejected because it matches everything.
func pathRegexpConflictsWithReserved(pattern string) bool {
	if pattern == "" {
		return true
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}

	for _, p := range reservedPaths {
		if re.MatchString(p) {
			return true
		}
	}

	return false
}

// ServiceStore is an interface for persisting service configurations across
// restarts. When provided, service CRUD operations write through to both the
// in-memory proxy and the persistent store.
type ServiceStore interface {
	// UpsertService creates or updates a persisted service configuration.
	UpsertService(ctx context.Context,
		params aperturedb.ServiceParams) error

	// DeleteService removes a persisted service by name.
	DeleteService(ctx context.Context, name string) error
}

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

	// ServiceStore is an optional persistent store for service
	// configurations. If nil, service changes are held in memory only.
	ServiceStore ServiceStore

	// MPPEnabled indicates whether MPP is enabled globally.
	MPPEnabled bool

	// SessionsEnabled indicates whether MPP sessions are enabled.
	SessionsEnabled bool

	// MPPRealm is the realm string used in MPP challenge headers.
	MPPRealm string

	// Chain identifies the blockchain the connected lnd is running on.
	// Populated from lnd GetInfo.chains[0].chain at aperture startup
	// (e.g. "bitcoin", "sui"). Empty if lnd could not be queried.
	Chain string

	// SessionStore exposes MPP prepaid session data to the admin API.
	// Optional — nil when sessions are disabled; the ListSessions and
	// GetSessionStats RPCs return Unimplemented in that case.
	SessionStore *aperturedb.MPPSessionsStore
}

// Server implements the adminrpc.AdminServer gRPC interface. Thread safety
// for service reads/updates is provided by the serviceHolder and proxy
// mutexes; the Server itself does not need its own lock.
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
		Network:         s.cfg.Network,
		ListenAddr:      s.cfg.ListenAddr,
		Insecure:        s.cfg.Insecure,
		MppEnabled:      s.cfg.MPPEnabled,
		SessionsEnabled: s.cfg.SessionsEnabled,
		MppRealm:        s.cfg.MPPRealm,
		Chain:           s.cfg.Chain,
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
		resp = append(resp, proxyServiceToProto(svc))
	}

	return &adminrpc.ListServicesResponse{Services: resp}, nil
}

// proxyServiceToProto converts an internal proxy.Service to its wire
// representation. Keeping this in one place so fields added to Service
// (like the payment override) reach every admin API response without
// needing to update each handler.
func proxyServiceToProto(svc *proxy.Service) *adminrpc.Service {
	out := &adminrpc.Service{
		Name:       svc.Name,
		Address:    svc.Address,
		Protocol:   svc.Protocol,
		HostRegexp: svc.HostRegexp,
		PathRegexp: svc.PathRegexp,
		Price:      svc.Price,
		Auth:       string(svc.Auth),
		AuthScheme: stringToAuthScheme(svc.AuthScheme),
	}
	if svc.Payment != nil {
		out.Payment = &adminrpc.PaymentBackend{
			LndHost: svc.Payment.LndHost,
			TlsPath: svc.Payment.TLSPath,
			MacPath: svc.Payment.MacPath,
		}
	}
	return out
}

// validatePaymentBackend enforces that a payment block is internally
// consistent: either all three fields are populated or all three are
// empty. Returns a user-friendly error message otherwise.
func validatePaymentBackend(p *adminrpc.PaymentBackend) error {
	if p == nil {
		return nil
	}
	set := 0
	if p.LndHost != "" {
		set++
	}
	if p.TlsPath != "" {
		set++
	}
	if p.MacPath != "" {
		set++
	}
	if set != 0 && set != 3 {
		return fmt.Errorf("payment block requires all of lnd_host, "+
			"tls_path, mac_path (got %d of 3 populated)", set)
	}
	return nil
}

// CreateService creates a new backend service. When a ServiceStore is
// configured, the service is persisted to the database and will survive
// restarts.
func (s *Server) CreateService(ctx context.Context,
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
	if len(hostRegexp) > maxRegexpLen {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"host_regexp exceeds max length %d",
			maxRegexpLen,
		)
	}
	if _, err := regexp.Compile(hostRegexp); err != nil {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"invalid host_regexp: %v", err,
		)
	}
	if req.PathRegexp != "" {
		if len(req.PathRegexp) > maxRegexpLen {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"path_regexp exceeds max length %d",
				maxRegexpLen,
			)
		}
		if _, err := regexp.Compile(req.PathRegexp); err != nil {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"invalid path_regexp: %v", err,
			)
		}
	}
	if pathRegexpConflictsWithReserved(req.PathRegexp) {
		return nil, status.Error(
			codes.InvalidArgument,
			"path_regexp must not match reserved paths "+
				"(/api/admin/, /api/proxy/, /_next/)",
		)
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

	authScheme := authSchemeToString(req.AuthScheme)

	if err := validatePaymentBackend(req.Payment); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	newSvc := &proxy.Service{
		Name:       req.Name,
		Address:    req.Address,
		Protocol:   protocol,
		HostRegexp: hostRegexp,
		PathRegexp: req.PathRegexp,
		Price:      req.Price,
		AuthScheme: authScheme,
	}
	if normalizedAuth != "" {
		newSvc.Auth = auth.Level(normalizedAuth)
	}
	// Attach the payment override when the client supplied a fully
	// populated block. Takes effect on the next prism restart — the
	// in-memory challenger router is built during startup only.
	if req.Payment != nil && req.Payment.LndHost != "" {
		newSvc.Payment = &proxy.PaymentBackend{
			LndHost: req.Payment.LndHost,
			TLSPath: req.Payment.TlsPath,
			MacPath: req.Payment.MacPath,
		}
	}

	services = append(services, newSvc)
	if err := s.cfg.UpdateServices(services); err != nil {
		log.Errorf("Error creating service: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to create service",
		)
	}

	// Persist the new service to the database if a store is
	// configured.
	if s.cfg.ServiceStore != nil {
		params := aperturedb.ServiceParams{
			Name:       newSvc.Name,
			Address:    newSvc.Address,
			Protocol:   newSvc.Protocol,
			HostRegexp: newSvc.HostRegexp,
			PathRegexp: newSvc.PathRegexp,
			Auth:       string(newSvc.Auth),
			AuthScheme: newSvc.AuthScheme,
			Price:      newSvc.Price,
		}
		if newSvc.Payment != nil {
			params.PaymentLndHost = newSvc.Payment.LndHost
			params.PaymentTLSPath = newSvc.Payment.TLSPath
			params.PaymentMacPath = newSvc.Payment.MacPath
		}
		if err := s.cfg.ServiceStore.UpsertService(
			ctx, params,
		); err != nil {
			log.Errorf("Error persisting service: %v", err)
		}
	}

	return proxyServiceToProto(newSvc), nil
}

// UpdateService updates a service's mutable fields. When a ServiceStore is
// configured, the updated service is persisted to the database.
func (s *Server) UpdateService(ctx context.Context,
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
		if len(req.HostRegexp) > maxRegexpLen {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"host_regexp exceeds max length %d",
				maxRegexpLen,
			)
		}
		if _, err := regexp.Compile(req.HostRegexp); err != nil {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"invalid host_regexp: %v", err,
			)
		}
		updated.HostRegexp = req.HostRegexp
	}
	if req.PathRegexp != "" {
		if len(req.PathRegexp) > maxRegexpLen {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"path_regexp exceeds max length %d",
				maxRegexpLen,
			)
		}
		if _, err := regexp.Compile(req.PathRegexp); err != nil {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"invalid path_regexp: %v", err,
			)
		}
		if pathRegexpConflictsWithReserved(req.PathRegexp) {
			return nil, status.Error(
				codes.InvalidArgument,
				"path_regexp must not match reserved "+
					"paths (/api/admin/, "+
					"/api/proxy/, /_next/)",
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

	// Only apply auth_scheme when explicitly set. Since AUTH_SCHEME_L402
	// is the proto enum zero value, using `optional` ensures we can
	// distinguish "not set" from "set to L402".
	if req.AuthScheme != nil {
		updated.AuthScheme = authSchemeToString(*req.AuthScheme)
	}

	// Update payment block. Three cases: ClearPayment=true removes any
	// existing override; Payment populated installs or replaces one;
	// both unset leaves existing config untouched. We explicitly reject
	// passing both together.
	if req.ClearPayment && req.Payment != nil {
		return nil, status.Error(
			codes.InvalidArgument,
			"clear_payment and payment are mutually exclusive",
		)
	}
	switch {
	case req.ClearPayment:
		updated.Payment = nil
	case req.Payment != nil && req.Payment.LndHost != "":
		if err := validatePaymentBackend(req.Payment); err != nil {
			return nil, status.Error(
				codes.InvalidArgument, err.Error(),
			)
		}
		updated.Payment = &proxy.PaymentBackend{
			LndHost: req.Payment.LndHost,
			TLSPath: req.Payment.TlsPath,
			MacPath: req.Payment.MacPath,
		}
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

	// Persist the updated service to the database if a store is
	// configured.
	if s.cfg.ServiceStore != nil {
		params := aperturedb.ServiceParams{
			Name:       updated.Name,
			Address:    updated.Address,
			Protocol:   updated.Protocol,
			HostRegexp: updated.HostRegexp,
			PathRegexp: updated.PathRegexp,
			Auth:       string(updated.Auth),
			AuthScheme: updated.AuthScheme,
			Price:      updated.Price,
		}
		if updated.Payment != nil {
			params.PaymentLndHost = updated.Payment.LndHost
			params.PaymentTLSPath = updated.Payment.TLSPath
			params.PaymentMacPath = updated.Payment.MacPath
		}
		// When ClearPayment was honored, updated.Payment is nil and
		// params.Payment* fields are empty strings — the UPSERT will
		// overwrite the old values with empties, which is the
		// desired effect.
		if err := s.cfg.ServiceStore.UpsertService(
			ctx, params,
		); err != nil {
			log.Errorf("Error persisting updated service: %v",
				err)
		}
	}

	return proxyServiceToProto(&updated), nil
}

// DeleteService removes a backend service by name.
func (s *Server) DeleteService(ctx context.Context,
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

	// Remove the service from the persistent store if configured.
	if s.cfg.ServiceStore != nil {
		if err := s.cfg.ServiceStore.DeleteService(
			ctx, req.Name,
		); err != nil {
			log.Errorf("Error deleting persisted service: %v",
				err)
		}
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

	// Validate state filter if provided.
	if req.State != "" {
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
	}

	// Parse date range if provided.
	var (
		from, to     time.Time
		hasDateRange bool
	)
	if req.StartDate != "" || req.EndDate != "" {
		if req.StartDate == "" || req.EndDate == "" {
			return nil, status.Error(
				codes.InvalidArgument,
				"both start_date and end_date must be "+
					"set together",
			)
		}

		var parseErr error
		from, parseErr = time.Parse(time.RFC3339, req.StartDate)
		if parseErr != nil {
			return nil, status.Error(
				codes.InvalidArgument,
				"invalid start_date format",
			)
		}
		to, parseErr = time.Parse(time.RFC3339, req.EndDate)
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
		hasDateRange = true
	}

	// Use combined filter query that supports any combination of
	// service, state, and date range filters.
	txns, err := s.cfg.TransactionStore.ListFiltered(
		ctx, aperturedb.TransactionFilter{
			Service:      req.Service,
			State:        req.State,
			HasDateRange: hasDateRange,
			From:         from,
			To:           to,
			Limit:        limit,
			Offset:       offset,
		},
	)
	if err != nil {
		log.Errorf("Error listing transactions: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to list transactions",
		)
	}

	// Get total count matching the same filters for pagination.
	totalCount, countErr := s.cfg.TransactionStore.CountFiltered(
		ctx, aperturedb.TransactionFilter{
			Service:      req.Service,
			State:        req.State,
			HasDateRange: hasDateRange,
			From:         from,
			To:           to,
		},
	)
	if countErr != nil {
		log.Errorf("Error counting transactions: %v", countErr)
	}

	return &adminrpc.ListTransactionsResponse{
		Transactions: txnsToProto(txns),
		TotalCount:   totalCount,
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

	// Total count for settled tokens (same as CountTransactions which
	// counts settled only).
	totalCount, countErr := s.cfg.TransactionStore.CountTransactions(ctx)
	if countErr != nil {
		log.Errorf("Error counting tokens: %v", countErr)
	}

	return &adminrpc.ListTokensResponse{
		Tokens:     txnsToProto(txns),
		TotalCount: totalCount,
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

	// Look up the transaction to get the payment hash before deleting.
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

	// Delete the transaction record first so it no longer appears in
	// the dashboard. If the subsequent secret revocation fails, the
	// token is gone from listings but the secret still exists -- a
	// safer partial-failure state than the reverse.
	if err := s.cfg.TransactionStore.DeleteByTokenID(
		ctx, tokenID,
	); err != nil {
		log.Errorf("Error deleting transaction: %v", err)
		return nil, status.Error(
			codes.Internal, "failed to delete transaction",
		)
	}

	err = revokeSecretByTokenIDAndHash(
		ctx, s.cfg.SecretStore, txn.TokenID, txn.PaymentHash,
	)
	if err != nil {
		log.Errorf("Error revoking secret (transaction already "+
			"deleted): %v", err)

		return nil, status.Error(
			codes.Internal,
			"transaction deleted but failed to revoke secret",
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

// authSchemeToString converts a proto AuthScheme enum to its string
// representation for database storage and proxy.Service.AuthScheme.
func authSchemeToString(scheme adminrpc.AuthScheme) string {
	switch scheme {
	case adminrpc.AuthScheme_AUTH_SCHEME_MPP:
		return "mpp"
	case adminrpc.AuthScheme_AUTH_SCHEME_L402_MPP:
		return "l402+mpp"
	default:
		return "l402"
	}
}

// stringToAuthScheme converts a string auth scheme to the proto enum.
func stringToAuthScheme(s string) adminrpc.AuthScheme {
	switch s {
	case "mpp":
		return adminrpc.AuthScheme_AUTH_SCHEME_MPP
	case "l402+mpp":
		return adminrpc.AuthScheme_AUTH_SCHEME_L402_MPP
	default:
		return adminrpc.AuthScheme_AUTH_SCHEME_L402
	}
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

// ListSessions returns paginated MPP prepaid sessions with an optional
// status filter ("open" | "closed" | "").
//
// NOTE: This is part of the adminrpc.AdminServer interface.
func (s *Server) ListSessions(ctx context.Context,
	req *adminrpc.ListSessionsRequest) (*adminrpc.ListSessionsResponse,
	error) {

	if s.cfg.SessionStore == nil {
		return nil, status.Error(codes.Unimplemented,
			"MPP sessions not enabled on this server")
	}

	// Validate status filter up front so we don't run a query that
	// would return zero rows for a typo. Empty string means no filter.
	switch req.Status {
	case "", "open", "closed":
	default:
		return nil, status.Errorf(codes.InvalidArgument,
			"status must be \"open\", \"closed\", or empty; got %q",
			req.Status)
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	sessions, total, err := s.cfg.SessionStore.ListSessions(
		ctx, req.Status, limit, offset,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal,
			"list sessions: %v", err)
	}

	resp := &adminrpc.ListSessionsResponse{
		Sessions: make([]*adminrpc.MPPSession, 0, len(sessions)),
		Total:    total,
	}
	for _, sess := range sessions {
		resp.Sessions = append(
			resp.Sessions, sessionToProto(sess),
		)
	}
	return resp, nil
}

// GetSessionStats returns aggregate counters across all MPP sessions.
//
// NOTE: This is part of the adminrpc.AdminServer interface.
func (s *Server) GetSessionStats(ctx context.Context,
	_ *adminrpc.GetSessionStatsRequest) (
	*adminrpc.GetSessionStatsResponse, error) {

	if s.cfg.SessionStore == nil {
		return nil, status.Error(codes.Unimplemented,
			"MPP sessions not enabled on this server")
	}

	stats, err := s.cfg.SessionStore.GetStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal,
			"get session stats: %v", err)
	}

	return &adminrpc.GetSessionStatsResponse{
		TotalSessions:    stats.TotalSessions,
		OpenSessions:     stats.OpenSessions,
		ClosedSessions:   stats.ClosedSessions,
		TotalDepositSats: stats.TotalDepositSats,
		TotalSpentSats:   stats.TotalSpentSats,
		OpenBalanceSats:  stats.OpenBalanceSats,
	}, nil
}

// sessionToProto converts an auth.Session to its wire representation. The
// balance_sats field is computed as deposit-spent so callers don't have to
// do it themselves; it's meaningful for open sessions (what's still owed
// back to the client) and equal to what was refunded on close for closed
// ones (well, ignoring route fees).
func sessionToProto(sess *auth.Session) *adminrpc.MPPSession {
	return &adminrpc.MPPSession{
		SessionId:     sess.SessionID,
		PaymentHash:   hex.EncodeToString(sess.PaymentHash[:]),
		DepositSats:   sess.DepositSats,
		SpentSats:     sess.SpentSats,
		BalanceSats:   sess.DepositSats - sess.SpentSats,
		ReturnInvoice: sess.ReturnInvoice,
		Status:        sess.Status,
		CreatedAt:     sess.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     sess.UpdatedAt.UTC().Format(time.RFC3339),
	}
}
