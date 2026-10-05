package freebie

import (
	"net"
	"net/http"
)

// DB is the main interface of the package freebie. It represents a store that
// keeps track of how many free requests a certain IP address can make to a
// certain resource.
type DB interface {
	// TakeFreebie atomically checks and consumes the allowance for a
	// request. It returns false without incrementing if none remains.
	// Implementations must be safe for concurrent use.
	TakeFreebie(*http.Request, net.IP) (bool, error)
}
