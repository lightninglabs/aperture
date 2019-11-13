package freebie

import (
	"net"
	"net/http"
)

// DB is the main interface of the package freebie. It represents a store that
// keeps track of how many free requests a certain IP address can make to a
// certain resource.
type DB interface {
	CanPass(*http.Request, net.IP) (bool, error)

	TallyFreebie(*http.Request, net.IP) (bool, error)
}
