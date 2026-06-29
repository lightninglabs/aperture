package aperturedb

import (
	"context"
	"fmt"

	"github.com/lightninglabs/aperture/aperturedb/sqlc"
	"github.com/lightningnetwork/lnd/clock"
)

type (
	// UpsertServiceParams contains the parameters for upserting a service.
	UpsertServiceParams = sqlc.UpsertServiceParams

	// ServiceRow is the database model for a service.
	ServiceRow = sqlc.Service
)

// ServicesDB is an interface that defines the set of operations that can be
// executed against the services database table.
type ServicesDB interface {
	// UpsertService inserts or updates a service by name.
	UpsertService(ctx context.Context, arg UpsertServiceParams) error

	// DeleteService deletes a service by name.
	DeleteService(ctx context.Context, name string) (int64, error)

	// ListServices returns all services ordered by name.
	ListServices(ctx context.Context) ([]ServiceRow, error)
}

// ServicesDBTxOptions defines the set of db txn options the ServicesStore
// understands.
type ServicesDBTxOptions struct {
	readOnly bool
}

// ReadOnly returns true if the transaction should be read only.
//
// NOTE: This implements the TxOptions interface.
func (a *ServicesDBTxOptions) ReadOnly() bool {
	return a.readOnly
}

// BatchedServicesDB is a version of ServicesDB that supports batched
// transactions.
type BatchedServicesDB interface {
	ServicesDB

	BatchedTx[ServicesDB]
}

// ServicesStore represents a storage backend for service configurations.
type ServicesStore struct {
	db    BatchedServicesDB
	clock clock.Clock
}

// NewServicesStore creates a new ServicesStore instance.
func NewServicesStore(db BatchedServicesDB) *ServicesStore {
	return &ServicesStore{
		db:    db,
		clock: clock.NewDefaultClock(),
	}
}

// ServiceParams contains the fields for creating or updating a service.
type ServiceParams struct {
	Name       string
	Address    string
	Protocol   string
	HostRegexp string
	PathRegexp string
	Auth       string
	AuthScheme string
	Price      int64

	// PaymentLndHost, PaymentTLSPath, PaymentMacPath together configure
	// an optional per-service lnd override. When any of them is set,
	// all three must be set — invoices for this service are routed
	// through that lnd so payments land in the merchant's wallet.
	// Empty on all three means the service uses the global
	// authenticator.lndhost (legacy single-lnd mode).
	PaymentLndHost string
	PaymentTLSPath string
	PaymentMacPath string
}

// UpsertService inserts or updates a service configuration.
func (s *ServicesStore) UpsertService(ctx context.Context,
	params ServiceParams) error {

	var writeTxOpts ServicesDBTxOptions
	now := s.clock.Now().UTC()
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx ServicesDB) error {
		return tx.UpsertService(ctx, UpsertServiceParams{
			Name:           params.Name,
			Address:        params.Address,
			Protocol:       params.Protocol,
			HostRegexp:     params.HostRegexp,
			PathRegexp:     params.PathRegexp,
			Price:          params.Price,
			Auth:           params.Auth,
			AuthScheme:     params.AuthScheme,
			PaymentLndhost: params.PaymentLndHost,
			PaymentTlspath: params.PaymentTLSPath,
			PaymentMacpath: params.PaymentMacPath,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	})

	if err != nil {
		return fmt.Errorf("unable to upsert service %q: %w",
			params.Name, err)
	}

	return nil
}

// DeleteService removes a service by name.
func (s *ServicesStore) DeleteService(ctx context.Context,
	name string) error {

	var writeTxOpts ServicesDBTxOptions
	err := s.db.ExecTx(ctx, &writeTxOpts, func(tx ServicesDB) error {
		_, err := tx.DeleteService(ctx, name)
		return err
	})

	if err != nil {
		return fmt.Errorf("unable to delete service %q: %w",
			name, err)
	}

	return nil
}

// ListServices returns all persisted services.
func (s *ServicesStore) ListServices(ctx context.Context) ([]ServiceRow,
	error) {

	var rows []ServiceRow
	readOpts := ServicesDBTxOptions{readOnly: true}
	err := s.db.ExecTx(ctx, &readOpts, func(tx ServicesDB) error {
		var err error
		rows, err = tx.ListServices(ctx)
		return err
	})

	if err != nil {
		return nil, fmt.Errorf("unable to list services: %w", err)
	}

	return rows, nil
}
