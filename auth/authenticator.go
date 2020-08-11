package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/lightninglabs/aperture/lsat"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lnrpc"
)

// LsatAuthenticator is an authenticator that uses the LSAT protocol to
// authenticate requests.
type LsatAuthenticator struct {
	minter  Minter
	checker InvoiceChecker
}

// A compile time flag to ensure the LsatAuthenticator satisfies the
// Authenticator interface.
var _ Authenticator = (*LsatAuthenticator)(nil)

// NewLsatAuthenticator creates a new authenticator that authenticates requests
// based on LSAT tokens.
func NewLsatAuthenticator(minter Minter,
	checker InvoiceChecker) *LsatAuthenticator {

	return &LsatAuthenticator{
		minter:  minter,
		checker: checker,
	}
}

// Accept returns whether or not the header successfully authenticates the user
// to a given backend service.
//
// NOTE: This is part of the Authenticator interface.
func (l *LsatAuthenticator) Accept(header *http.Header, serviceName string) bool {
	// Try reading the macaroon and preimage from the HTTP header. This can
	// be in different header fields depending on the implementation and/or
	// protocol.
	mac, preimage, err := lsat.FromHeader(header)
	if err != nil {
		log.Debugf("Deny: %v", err)
		return false
	}

	verificationParams := &mint.VerificationParams{
		Macaroon:      mac,
		Preimage:      preimage,
		TargetService: serviceName,
	}
	err = l.minter.VerifyLSAT(context.Background(), verificationParams)
	if err != nil {
		log.Debugf("Deny: LSAT validation failed: %v", err)
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

// FreshChallengeHeader returns a header containing a challenge for the user to
// complete.
//
// NOTE: This is part of the Authenticator interface.
func (l *LsatAuthenticator) FreshChallengeHeader(r *http.Request,
	serviceName string, servicePrice int64) (http.Header, error) {

	service := lsat.Service{
		Name:  serviceName,
		Tier:  lsat.BaseTier,
		Price: servicePrice,
	}
	mac, paymentRequest, err := l.minter.MintLSAT(
		context.Background(), service,
	)
	if err != nil {
		log.Errorf("Error minting LSAT: %v", err)
		return nil, err
	}
	macBytes, err := mac.MarshalBinary()
	if err != nil {
		log.Errorf("Error serializing LSAT: %v", err)
	}

	str := fmt.Sprintf("LSAT macaroon=\"%s\", invoice=\"%s\"",
		base64.StdEncoding.EncodeToString(macBytes), paymentRequest)
	header := r.Header
	header.Set("WWW-Authenticate", str)

	log.Debugf("Created new challenge header: [%s]", str)
	return header, nil
}
