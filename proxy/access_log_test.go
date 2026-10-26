package proxy

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/btcsuite/btclog/v2"
	"github.com/lightninglabs/aperture/auth"
	"github.com/lightninglabs/aperture/l402"
	"github.com/stretchr/testify/require"
)

// TestAccessLogVerifiedL402 correlates the authenticated token with the
// remote IP and does not attribute an unverified credential to that IP.
func TestAccessLogVerifiedL402(t *testing.T) {
	oldLog := log
	t.Cleanup(func() { UseLogger(oldLog) })
	var output bytes.Buffer
	UseLogger(btclog.NewSLogger(btclog.NewDefaultHandler(&output)))

	valid := newPublicAuthToken(t, 1)
	forged := newPublicAuthToken(t, 2)
	authenticator := &publicAuthRecorder{
		scheme: auth.AuthSchemeL402,
		accepted: map[l402.TokenID]bool{
			valid.id: true,
		},
	}
	service := &Service{
		Auth:                         "on",
		AuthSkipInvoiceCreationPaths: []string{"^/resource$"},
	}
	p, received := newPublicAuthProxy(t, service, authenticator)

	response := servePublicAuthRequest(
		p, "/resource", "192.0.2.10", valid.header,
	)
	require.Equal(t, http.StatusNoContent, response.Code)
	<-received
	require.Contains(t, output.String(),
		`192.0.2.10 - - "GET http://proxy/resource HTTP/1.1"`)
	require.Contains(t, output.String(), "l402_id="+valid.id.String())

	output.Reset()
	response = servePublicAuthRequest(
		p, "/resource", "192.0.2.11", forged.header,
	)
	require.Equal(t, http.StatusUnauthorized, response.Code)
	require.Contains(t, output.String(),
		`192.0.2.11 - - "GET http://proxy/resource HTTP/1.1"`)
	require.NotContains(t, output.String(), "l402_id=")
}
