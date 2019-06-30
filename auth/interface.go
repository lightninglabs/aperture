package auth

import "net/http"

// Authenticator is the generic interface for validating client headers and
// returning new challenge headers.
type Authenticator interface {
	// Accept returns whether or not the header successfully authenticates the user
	// to a given backend service.
	Accept(*http.Header) bool

	// FreshChallengeHeader returns a header containing a challenge for the user to
	// complete.
	FreshChallengeHeader(r *http.Request) (http.Header, error)
}
