package auth

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
)

const (
	// paymentAuthScheme is the HTTP authentication scheme name per
	// draft-httpauth-payment-00.
	paymentAuthScheme = "Payment"
)

// MPPAuthenticator is an authenticator that implements the Payment HTTP
// Authentication Scheme for the Lightning "charge" intent. It issues BOLT11
// invoices as challenges and verifies payment preimages as credentials.
type MPPAuthenticator struct {
	// challenger creates new Lightning invoices for payment challenges.
	challenger mint.Challenger

	// checker verifies that invoices have been settled.
	checker InvoiceChecker

	// realm is the protection space identifier used in challenges.
	realm string

	// hmacSecret is the server secret used for stateless HMAC-SHA256
	// challenge ID binding per spec Section 5.1.2.1.1.
	hmacSecret []byte

	// network identifies the Lightning Network (e.g., "mainnet",
	// "regtest", "signet").
	network string
}

// Compile-time interface checks.
var _ Authenticator = (*MPPAuthenticator)(nil)
var _ ReceiptProvider = (*MPPAuthenticator)(nil)

// NewMPPAuthenticator creates a new authenticator for the Payment HTTP
// Authentication Scheme with the "charge" intent.
func NewMPPAuthenticator(challenger mint.Challenger, checker InvoiceChecker,
	realm string, hmacSecret []byte, network string) *MPPAuthenticator {

	return &MPPAuthenticator{
		challenger: challenger,
		checker:    checker,
		realm:      realm,
		hmacSecret: hmacSecret,
		network:    network,
	}
}

// Accept returns whether the header contains a valid Payment credential for
// the Lightning charge intent.
//
// NOTE: This is part of the Authenticator interface.
func (a *MPPAuthenticator) Accept(header *http.Header,
	serviceName string) bool {

	// Try to parse a Payment credential from the header.
	cred, err := mpp.ParseCredential(header)
	if err != nil {
		// Not an MPP credential, silently return false so other
		// authenticators can try.
		return false
	}

	// Only handle Lightning charge intent.
	if cred.Challenge.Method != mpp.MethodLightning ||
		cred.Challenge.Intent != mpp.IntentCharge {

		log.Debugf("MPP: Ignoring credential with method=%s "+
			"intent=%s", cred.Challenge.Method,
			cred.Challenge.Intent)
		return false
	}

	// Reconstruct the ChallengeParams from the echoed challenge to verify
	// the HMAC binding.
	params := cred.Challenge.ToChallengeParams()
	if !mpp.VerifyChallengeID(a.hmacSecret, params, cred.Challenge.ID) {
		log.Debugf("MPP: Challenge ID verification failed")
		return false
	}

	// Check expiry if set.
	if cred.Challenge.Expires != "" {
		expiresAt, err := time.Parse(time.RFC3339,
			cred.Challenge.Expires)
		if err != nil {
			log.Debugf("MPP: Invalid expires format: %v", err)
			return false
		}
		if time.Now().After(expiresAt) {
			log.Debugf("MPP: Challenge expired at %v", expiresAt)
			return false
		}
	}

	// Decode the charge payload to get the preimage.
	var payload mpp.ChargePayload
	if err := json.Unmarshal(cred.Payload, &payload); err != nil {
		log.Debugf("MPP: Failed to decode charge payload: %v", err)
		return false
	}

	if payload.Preimage == "" {
		log.Debugf("MPP: Missing preimage in payload")
		return false
	}

	// Parse the preimage from hex.
	preimage, err := lntypes.MakePreimageFromStr(payload.Preimage)
	if err != nil {
		log.Debugf("MPP: Invalid preimage hex: %v", err)
		return false
	}

	// Decode the charge request to get the payment hash.
	var chargeReq mpp.ChargeRequest
	if err := mpp.DecodeRequest(
		cred.Challenge.Request, &chargeReq,
	); err != nil {
		log.Debugf("MPP: Failed to decode charge request: %v", err)
		return false
	}

	// Get the payment hash from the charge request.
	paymentHash, err := lntypes.MakeHashFromStr(
		chargeReq.MethodDetails.PaymentHash,
	)
	if err != nil {
		log.Debugf("MPP: Invalid payment hash in request: %v", err)
		return false
	}

	// Verify SHA256(preimage) == paymentHash.
	if !preimage.Matches(paymentHash) {
		log.Debugf("MPP: Preimage does not match payment hash")
		return false
	}

	// Verify the invoice is settled in the Lightning backend.
	err = a.checker.VerifyInvoiceStatus(
		paymentHash, lnrpc.Invoice_SETTLED,
		DefaultInvoiceLookupTimeout,
	)
	if err != nil {
		log.Debugf("MPP: Invoice verification failed: %v", err)
		return false
	}

	log.Debugf("MPP: Charge credential accepted for service %s",
		serviceName)
	return true
}

// FreshChallengeHeader returns a WWW-Authenticate: Payment header containing a
// charge challenge with a fresh BOLT11 invoice.
//
// NOTE: This is part of the Authenticator interface.
func (a *MPPAuthenticator) FreshChallengeHeader(serviceName string,
	servicePrice int64) (http.Header, error) {

	// Create a new Lightning invoice.
	paymentRequest, paymentHash, err := a.challenger.NewChallenge(
		servicePrice,
	)
	if err != nil {
		return nil, fmt.Errorf("MPP: failed to create invoice: %w",
			err)
	}

	// Build the charge request.
	chargeReq := &mpp.ChargeRequest{
		Amount:   strconv.FormatInt(servicePrice, 10),
		Currency: mpp.CurrencySat,
		MethodDetails: mpp.ChargeMethodDetails{
			Invoice:     paymentRequest,
			PaymentHash: hex.EncodeToString(paymentHash[:]),
			Network:     a.network,
		},
	}

	// Encode the request using JCS + base64url.
	encodedRequest, err := mpp.EncodeRequest(chargeReq)
	if err != nil {
		return nil, fmt.Errorf("MPP: failed to encode charge "+
			"request: %w", err)
	}

	// Build challenge params.
	params := &mpp.ChallengeParams{
		Realm:   a.realm,
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedRequest,
	}

	// Compute the HMAC challenge ID.
	params.ID = mpp.ComputeChallengeID(a.hmacSecret, params)

	// Set the challenge header.
	header := make(http.Header)
	mpp.SetChallengeHeader(header, params)

	log.Debugf("MPP: Created charge challenge with payment hash %x",
		paymentHash[:])

	return header, nil
}

// ReceiptHeader returns a Payment-Receipt header for a successfully
// authenticated charge request.
//
// NOTE: This is part of the ReceiptProvider interface.
func (a *MPPAuthenticator) ReceiptHeader(header *http.Header,
	serviceName string) http.Header {

	// Parse the credential again to extract the payment hash for the
	// receipt reference.
	cred, err := mpp.ParseCredential(header)
	if err != nil {
		return nil
	}

	var chargeReq mpp.ChargeRequest
	if err := mpp.DecodeRequest(
		cred.Challenge.Request, &chargeReq,
	); err != nil {
		return nil
	}

	receipt := &mpp.Receipt{
		Status:      mpp.ReceiptStatusSuccess,
		Method:      mpp.MethodLightning,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Reference:   chargeReq.MethodDetails.PaymentHash,
		ChallengeID: cred.Challenge.ID,
	}

	receiptHeader := make(http.Header)
	if err := mpp.SetReceiptHeader(receiptHeader, receipt); err != nil {
		log.Errorf("MPP: Failed to set receipt header: %v", err)
		return nil
	}

	return receiptHeader
}
