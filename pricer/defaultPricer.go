package pricer

import "context"

// DefaultPricer provides the same price for any service path. It implements
// the Pricer interface.
type DefaultPricer struct {
	Price int64
}

// NewDefaultPricer initialises a new DefaultPricer provider where each resource
// for the service will have the same price.
func NewDefaultPricer(price int64) *DefaultPricer {
	return &DefaultPricer{Price: price}
}

// GetPrice returns the price charged for all resources of a service.
// It is part of the Pricer interface.
func (d *DefaultPricer) GetPrice(_ context.Context, _ string) (int64,
	error) {

	return d.Price, nil
}

// Close is part of the Pricer interface. For the DefaultPricer, the method does
// nothing.
func (d *DefaultPricer) Close() error {
	return nil
}
