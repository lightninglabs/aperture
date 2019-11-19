package auth

import "net/http"

// MockAuthenticator is a mock implementation of the authenticator.
type MockAuthenticator struct{}

// NewMockAuthenticator returns a new MockAuthenticator instance.
func NewMockAuthenticator() *MockAuthenticator {
	return &MockAuthenticator{}
}

// Accept returns whether or not the header successfully authenticates the user
// to a given backend service.
func (a MockAuthenticator) Accept(header *http.Header) bool {
	if header.Get("Authorization") != "" {
		return true
	}
	if header.Get("Grpc-Metadata-macaroon") != "" {
		return true
	}
	if header.Get("Macaroon") != "" {
		return true
	}
	return false
}

// FreshChallengeHeader returns a header containing a challenge for the user to
// complete.
func (a MockAuthenticator) FreshChallengeHeader(r *http.Request) (http.Header, error) {
	header := r.Header
	header.Set("WWW-Authenticate", "LSAT macaroon='AGIAJEemVQUTEyNCR0exk7ek90Cg==' invoice='lnbc1500n1pw5kjhmpp5fu6xhthlt2vucmzkx6c7wtlh2r625r30cyjsfqhu8rsx4xpz5lwqdpa2fjkzep6yptksct5yp5hxgrrv96hx6twvusycn3qv9jx7ur5d9hkugr5dusx6cqzpgxqr23s79ruapxc4j5uskt4htly2salw4drq979d7rcela9wz02elhypmdzmzlnxuknpgfyfm86pntt8vvkvffma5qc9n50h4mvqhngadqy3ngqjcym5a'")
	return header, nil
}
