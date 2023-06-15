package pricer

import (
	"context"
	"net/http"
)

// Pricer is an interface used to query price data from a price provider.
type Pricer interface {
	// GetPrice should return the price in satoshis for the given
	// resource path.
	GetPrice(ctx context.Context, req *http.Request) (int64, error)

	// Close should clean up the Pricer implementation if needed.
	Close() error
}
