package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/lightninglabs/aperture/l402"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
)

// L402Authenticator is an authenticator that uses the L402 protocol to
// authenticate requests.
type L402Authenticator struct {
	minter  Minter
	checker InvoiceChecker
}

// A compile time flag to ensure the L402Authenticator satisfies the
// Authenticator interface.
var _ Authenticator = (*L402Authenticator)(nil)

// NewL402Authenticator creates a new authenticator that authenticates requests
// based on L402 tokens.
func NewL402Authenticator(minter Minter,
	checker InvoiceChecker) *L402Authenticator {

	return &L402Authenticator{
		minter:  minter,
		checker: checker,
	}
}

// Accept returns whether or not the header successfully authenticates the user
// to a given backend service.
//
// NOTE: This is part of the Authenticator interface.
func (l *L402Authenticator) Accept(header *http.Header, serviceName string) bool {
	// Try reading the macaroon and preimage from the HTTP header. This can
	// be in different header fields depending on the implementation and/or
	// protocol.
	mac, preimage, err := l402.FromHeader(header)
	if err != nil {
		log.Debugf("Deny: %v", err)
		return false
	}

	verificationParams := &mint.VerificationParams{
		Macaroon:      mac,
		Preimage:      preimage,
		TargetService: serviceName,
	}
	err = l.minter.VerifyL402(context.Background(), verificationParams)
	if err != nil {
		log.Debugf("Deny: L402 validation failed: %v", err)
		return false
	}

	// Make sure the backend has the invoice recorded as settled.
	err = l.checker.VerifyInvoiceStatus(
		preimage.Hash(), lnrpc.Invoice_SETTLED,
		DefaultInvoiceLookupTimeout,
	)
	if err != nil {
		log.Debugf("Deny: Invoice status mismatch: %v", err)
		return false
	}

	return true
}

const (
	// lsatAuthScheme is an outdated RFC 7235 auth-scheme used by aperture.
	lsatAuthScheme = "LSAT"

	// l402AuthScheme is the current RFC 7235 auth-scheme used by aperture.
	l402AuthScheme = "L402"
)

// FreshChallengeHeader returns a header containing a challenge for the user to
// complete.
//
// NOTE: This is part of the Authenticator interface.
func (l *L402Authenticator) FreshChallengeHeader(serviceName string,
	servicePrice int64) (http.Header, error) {

	service := l402.Service{
		Name:  serviceName,
		Tier:  l402.BaseTier,
		Price: servicePrice,
	}
	mac, paymentRequest, err := l.minter.MintL402(
		context.Background(), service,
	)
	if err != nil {
		log.Errorf("Error minting L402: %v", err)
		return nil, err
	}
	macBytes, err := mac.MarshalBinary()
	if err != nil {
		log.Errorf("Error serializing L402: %v", err)
	}

	header := http.Header{
		"Content-Type": []string{"application/grpc"},
	}

	str := fmt.Sprintf("macaroon=\"%s\", invoice=\"%s\"",
		base64.StdEncoding.EncodeToString(macBytes), paymentRequest)

	// Old loop software (via ClientInterceptor code of aperture) looks
	// for "LSAT" in the first instance of WWW-Authenticate header, so
	// legacy header must go first not to break backward compatibility.
	lsatValue := lsatAuthScheme + " " + str
	l402Value := l402AuthScheme + " " + str
	header.Set("WWW-Authenticate", lsatValue)
	log.Debugf("Created new challenge header: [%s]", lsatValue)
	header.Add("WWW-Authenticate", l402Value)
	log.Debugf("Created new challenge header: [%s]", l402Value)

	return header, nil
}
