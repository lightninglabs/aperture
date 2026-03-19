package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
)

const (
	// defaultDepositMultiplier is the default number of service units per
	// deposit.
	defaultDepositMultiplier = 20

	// defaultIdleTimeout is the default idle timeout for sessions.
	defaultIdleTimeout = 5 * time.Minute
)

// MPPSessionAuthenticator is an authenticator that implements the Payment HTTP
// Authentication Scheme for the Lightning "session" intent. It manages prepaid
// sessions with deposit, bearer, top-up, and close operations.
type MPPSessionAuthenticator struct {
	// challenger creates new Lightning invoices for deposit challenges.
	challenger mint.Challenger

	// checker verifies that invoices have been settled.
	checker InvoiceChecker

	// sessionStore persists session state.
	sessionStore SessionStore

	// paymentSender sends Lightning payments for session refunds.
	paymentSender PaymentSender

	// realm is the protection space identifier used in challenges.
	realm string

	// hmacSecret is the server secret for HMAC-SHA256 challenge binding.
	hmacSecret []byte

	// network identifies the Lightning Network.
	network string

	// depositMultiplier is the number of service units per deposit.
	depositMultiplier int

	// idleTimeout is the idle timeout for open sessions.
	idleTimeout time.Duration

	// lastCloseResult caches the refund outcome from the most recent
	// close action so ReceiptHeader can include it. Protected by mu.
	mu              sync.Mutex
	lastCloseResult *closeResult
}

// closeResult stores the outcome of a session close for receipt generation.
type closeResult struct {
	sessionID    string
	refundSats   int64
	refundStatus string // "succeeded", "failed", or "skipped"
}

// Compile-time interface checks.
var _ Authenticator = (*MPPSessionAuthenticator)(nil)
var _ ReceiptProvider = (*MPPSessionAuthenticator)(nil)

// MPPSessionConfig holds the configuration for the session authenticator.
type MPPSessionConfig struct {
	Challenger        mint.Challenger
	Checker           InvoiceChecker
	SessionStore      SessionStore
	PaymentSender     PaymentSender
	Realm             string
	HMACSecret        []byte
	Network           string
	DepositMultiplier int
	IdleTimeout       time.Duration
}

// NewMPPSessionAuthenticator creates a new session intent authenticator.
func NewMPPSessionAuthenticator(
	cfg *MPPSessionConfig) *MPPSessionAuthenticator {

	multiplier := cfg.DepositMultiplier
	if multiplier <= 0 {
		multiplier = defaultDepositMultiplier
	}

	timeout := cfg.IdleTimeout
	if timeout <= 0 {
		timeout = defaultIdleTimeout
	}

	return &MPPSessionAuthenticator{
		challenger:        cfg.Challenger,
		checker:           cfg.Checker,
		sessionStore:      cfg.SessionStore,
		paymentSender:     cfg.PaymentSender,
		realm:             cfg.Realm,
		hmacSecret:        cfg.HMACSecret,
		network:           cfg.Network,
		depositMultiplier: multiplier,
		idleTimeout:       timeout,
	}
}

// Accept returns whether the header contains a valid Payment credential for
// the Lightning session intent.
//
// NOTE: This is part of the Authenticator interface.
func (a *MPPSessionAuthenticator) Accept(header *http.Header,
	serviceName string) bool {

	cred, err := mpp.ParseCredential(header)
	if err != nil {
		return false
	}

	// Only handle Lightning session intent.
	if cred.Challenge.Method != mpp.MethodLightning ||
		cred.Challenge.Intent != mpp.IntentSession {

		return false
	}

	// Decode the session payload to determine the action.
	var payload mpp.SessionPayload
	if err := json.Unmarshal(cred.Payload, &payload); err != nil {
		log.Debugf("MPP Session: Failed to decode payload: %v", err)
		return false
	}

	ctx := context.Background()

	switch payload.Action {
	case mpp.SessionActionOpen:
		return a.handleOpen(ctx, cred, &payload)

	case mpp.SessionActionBearer:
		return a.handleBearer(ctx, &payload)

	case mpp.SessionActionTopUp:
		return a.handleTopUp(ctx, cred, &payload)

	case mpp.SessionActionClose:
		return a.handleClose(ctx, &payload)

	default:
		log.Debugf("MPP Session: Unknown action: %s", payload.Action)
		return false
	}
}

// handleOpen verifies an open action credential and creates a new session.
func (a *MPPSessionAuthenticator) handleOpen(ctx context.Context,
	cred *mpp.Credential, payload *mpp.SessionPayload) bool {

	// Verify challenge HMAC binding.
	params := cred.Challenge.ToChallengeParams()
	if !mpp.VerifyChallengeID(a.hmacSecret, params, cred.Challenge.ID) {
		log.Debugf("MPP Session: Challenge ID verification failed " +
			"for open")
		return false
	}

	// Check expiry.
	if cred.Challenge.Expires != "" {
		expiresAt, err := time.Parse(
			time.RFC3339, cred.Challenge.Expires,
		)
		if err != nil || time.Now().After(expiresAt) {
			log.Debugf("MPP Session: Challenge expired for open")
			return false
		}
	}

	// Parse preimage.
	preimage, err := lntypes.MakePreimageFromStr(payload.Preimage)
	if err != nil {
		log.Debugf("MPP Session: Invalid preimage hex: %v", err)
		return false
	}

	// Decode the session request to get the payment hash.
	var sessReq mpp.SessionRequest
	if err := mpp.DecodeRequest(
		cred.Challenge.Request, &sessReq,
	); err != nil {
		log.Debugf("MPP Session: Failed to decode request: %v", err)
		return false
	}

	paymentHash, err := lntypes.MakeHashFromStr(sessReq.PaymentHash)
	if err != nil {
		log.Debugf("MPP Session: Invalid payment hash: %v", err)
		return false
	}

	// Verify SHA256(preimage) == paymentHash.
	if !preimage.Matches(paymentHash) {
		log.Debugf("MPP Session: Preimage mismatch for open")
		return false
	}

	// Verify invoice settled.
	err = a.checker.VerifyInvoiceStatus(
		paymentHash, lnrpc.Invoice_SETTLED,
		DefaultInvoiceLookupTimeout,
	)
	if err != nil {
		log.Debugf("MPP Session: Invoice not settled for open: %v",
			err)
		return false
	}

	// Validate return invoice. We just check it's non-empty for now.
	if payload.ReturnInvoice == "" {
		log.Debugf("MPP Session: Missing return invoice for open")
		return false
	}

	// Parse deposit amount from the request.
	var depositSats int64
	if sessReq.DepositAmount != "" {
		depositSats, err = strconv.ParseInt(
			sessReq.DepositAmount, 10, 64,
		)
		if err != nil {
			log.Debugf("MPP Session: Invalid deposit amount: %v",
				err)
			return false
		}
	}

	// Create the session.
	sessionID := hex.EncodeToString(paymentHash[:])
	session := &Session{
		SessionID:     sessionID,
		PaymentHash:   paymentHash,
		DepositSats:   depositSats,
		ReturnInvoice: payload.ReturnInvoice,
		Status:        "open",
	}

	if err := a.sessionStore.CreateSession(ctx, session); err != nil {
		log.Errorf("MPP Session: Failed to create session: %v", err)
		return false
	}

	log.Infof("MPP Session: Opened session %s with deposit %d sats",
		sessionID, depositSats)
	return true
}

// handleBearer verifies a bearer action credential against an existing
// session.
func (a *MPPSessionAuthenticator) handleBearer(ctx context.Context,
	payload *mpp.SessionPayload) bool {

	if payload.SessionID == "" || payload.Preimage == "" {
		log.Debugf("MPP Session: Missing sessionId or preimage " +
			"for bearer")
		return false
	}

	// Look up session.
	session, err := a.sessionStore.GetSession(ctx, payload.SessionID)
	if err != nil {
		log.Debugf("MPP Session: Session not found: %v", err)
		return false
	}

	if session.Status != "open" {
		log.Debugf("MPP Session: Session %s is closed",
			payload.SessionID)
		return false
	}

	// Verify preimage matches session payment hash.
	preimage, err := lntypes.MakePreimageFromStr(payload.Preimage)
	if err != nil {
		log.Debugf("MPP Session: Invalid preimage: %v", err)
		return false
	}

	if !preimage.Matches(session.PaymentHash) {
		log.Debugf("MPP Session: Preimage mismatch for bearer")
		return false
	}

	log.Tracef("MPP Session: Bearer accepted for session %s",
		payload.SessionID)
	return true
}

// handleTopUp verifies a top-up action credential and adds funds to the
// session.
func (a *MPPSessionAuthenticator) handleTopUp(ctx context.Context,
	cred *mpp.Credential, payload *mpp.SessionPayload) bool {

	// Verify challenge HMAC binding for the top-up challenge.
	params := cred.Challenge.ToChallengeParams()
	if !mpp.VerifyChallengeID(a.hmacSecret, params, cred.Challenge.ID) {
		log.Debugf("MPP Session: Challenge ID verification failed " +
			"for topUp")
		return false
	}

	// Check expiry.
	if cred.Challenge.Expires != "" {
		expiresAt, err := time.Parse(
			time.RFC3339, cred.Challenge.Expires,
		)
		if err != nil || time.Now().After(expiresAt) {
			log.Debugf("MPP Session: Challenge expired for topUp")
			return false
		}
	}

	if payload.SessionID == "" || payload.TopUpPreimage == "" {
		log.Debugf("MPP Session: Missing sessionId or " +
			"topUpPreimage for topUp")
		return false
	}

	// Verify session exists and is open.
	session, err := a.sessionStore.GetSession(ctx, payload.SessionID)
	if err != nil {
		log.Debugf("MPP Session: Session not found for topUp: %v",
			err)
		return false
	}
	if session.Status != "open" {
		log.Debugf("MPP Session: Session %s is closed for topUp",
			payload.SessionID)
		return false
	}

	// Verify the top-up preimage against the challenge's payment hash.
	topUpPreimage, err := lntypes.MakePreimageFromStr(
		payload.TopUpPreimage,
	)
	if err != nil {
		log.Debugf("MPP Session: Invalid topUp preimage: %v", err)
		return false
	}

	var sessReq mpp.SessionRequest
	if err := mpp.DecodeRequest(
		cred.Challenge.Request, &sessReq,
	); err != nil {
		log.Debugf("MPP Session: Failed to decode topUp request: %v",
			err)
		return false
	}

	topUpHash, err := lntypes.MakeHashFromStr(sessReq.PaymentHash)
	if err != nil {
		log.Debugf("MPP Session: Invalid topUp payment hash: %v",
			err)
		return false
	}

	if !topUpPreimage.Matches(topUpHash) {
		log.Debugf("MPP Session: TopUp preimage mismatch")
		return false
	}

	// Verify top-up invoice settled.
	err = a.checker.VerifyInvoiceStatus(
		topUpHash, lnrpc.Invoice_SETTLED,
		DefaultInvoiceLookupTimeout,
	)
	if err != nil {
		log.Debugf("MPP Session: TopUp invoice not settled: %v", err)
		return false
	}

	// Parse top-up amount.
	var topUpSats int64
	if sessReq.DepositAmount != "" {
		topUpSats, err = strconv.ParseInt(
			sessReq.DepositAmount, 10, 64,
		)
		if err != nil {
			log.Debugf("MPP Session: Invalid topUp amount: %v",
				err)
			return false
		}
	}

	// Atomically add to session balance.
	if err := a.sessionStore.UpdateSessionBalance(
		ctx, payload.SessionID, topUpSats,
	); err != nil {
		log.Errorf("MPP Session: Failed to update balance: %v", err)
		return false
	}

	log.Infof("MPP Session: TopUp %d sats to session %s",
		topUpSats, payload.SessionID)
	return true
}

// handleClose verifies a close action credential and initiates the refund.
func (a *MPPSessionAuthenticator) handleClose(ctx context.Context,
	payload *mpp.SessionPayload) bool {

	if payload.SessionID == "" || payload.Preimage == "" {
		log.Debugf("MPP Session: Missing sessionId or preimage " +
			"for close")
		return false
	}

	// Look up session.
	session, err := a.sessionStore.GetSession(ctx, payload.SessionID)
	if err != nil {
		log.Debugf("MPP Session: Session not found for close: %v",
			err)
		return false
	}

	if session.Status != "open" {
		log.Debugf("MPP Session: Session %s already closed",
			payload.SessionID)
		return false
	}

	// Verify preimage.
	preimage, err := lntypes.MakePreimageFromStr(payload.Preimage)
	if err != nil {
		log.Debugf("MPP Session: Invalid preimage for close: %v",
			err)
		return false
	}

	if !preimage.Matches(session.PaymentHash) {
		log.Debugf("MPP Session: Preimage mismatch for close")
		return false
	}

	// Mark session as closed first (before attempting refund).
	if err := a.sessionStore.CloseSession(
		ctx, payload.SessionID,
	); err != nil {
		log.Errorf("MPP Session: Failed to close session: %v", err)
		return false
	}

	// Compute refund and track the result for receipt generation.
	refundSats := session.DepositSats - session.SpentSats
	refundStatus := "skipped"

	if refundSats > 0 && a.paymentSender != nil {
		_, err := a.paymentSender.SendPayment(
			ctx, session.ReturnInvoice, refundSats,
		)
		if err != nil {
			log.Errorf("MPP Session: Refund failed for "+
				"session %s (%d sats): %v",
				payload.SessionID, refundSats, err)
			refundStatus = "failed"
		} else {
			log.Infof("MPP Session: Refunded %d sats to "+
				"session %s", refundSats, payload.SessionID)
			refundStatus = "succeeded"
		}
	} else if refundSats > 0 {
		// PaymentSender not configured, can't refund.
		log.Warnf("MPP Session: No payment sender configured, "+
			"cannot refund %d sats for session %s",
			refundSats, payload.SessionID)
		refundStatus = "failed"
	}

	// Cache the close result for ReceiptHeader.
	a.mu.Lock()
	a.lastCloseResult = &closeResult{
		sessionID:    payload.SessionID,
		refundSats:   refundSats,
		refundStatus: refundStatus,
	}
	a.mu.Unlock()

	log.Infof("MPP Session: Closed session %s (refund=%d status=%s)",
		payload.SessionID, refundSats, refundStatus)
	return true
}

// FreshChallengeHeader returns a WWW-Authenticate: Payment header containing a
// session challenge with a fresh deposit invoice.
//
// NOTE: This is part of the Authenticator interface.
func (a *MPPSessionAuthenticator) FreshChallengeHeader(serviceName string,
	servicePrice int64) (http.Header, error) {

	// Compute deposit amount.
	depositSats := servicePrice * int64(a.depositMultiplier)

	// Create a deposit invoice.
	paymentRequest, paymentHash, err := a.challenger.NewChallenge(
		depositSats,
	)
	if err != nil {
		return nil, fmt.Errorf("MPP Session: failed to create "+
			"deposit invoice: %w", err)
	}

	// Build the session request.
	sessReq := &mpp.SessionRequest{
		Amount:         strconv.FormatInt(servicePrice, 10),
		Currency:       mpp.CurrencySat,
		DepositInvoice: paymentRequest,
		PaymentHash:    hex.EncodeToString(paymentHash[:]),
		DepositAmount:  strconv.FormatInt(depositSats, 10),
		IdleTimeout: strconv.FormatInt(
			int64(a.idleTimeout.Seconds()), 10,
		),
	}

	encodedRequest, err := mpp.EncodeRequest(sessReq)
	if err != nil {
		return nil, fmt.Errorf("MPP Session: failed to encode "+
			"request: %w", err)
	}

	params := &mpp.ChallengeParams{
		Realm:   a.realm,
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentSession,
		Request: encodedRequest,
	}
	params.ID = mpp.ComputeChallengeID(a.hmacSecret, params)

	header := make(http.Header)
	mpp.SetChallengeHeader(header, params)

	log.Debugf("MPP Session: Created session challenge with deposit "+
		"%d sats, payment hash %x", depositSats, paymentHash[:])

	return header, nil
}

// ReceiptHeader returns a Payment-Receipt header for a successfully
// authenticated session request.
//
// NOTE: This is part of the ReceiptProvider interface.
func (a *MPPSessionAuthenticator) ReceiptHeader(header *http.Header,
	serviceName string) http.Header {

	cred, err := mpp.ParseCredential(header)
	if err != nil {
		return nil
	}

	var payload mpp.SessionPayload
	if err := json.Unmarshal(cred.Payload, &payload); err != nil {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// For close actions, include refund information in a SessionReceipt.
	if payload.Action == mpp.SessionActionClose {
		sessReceipt := &mpp.SessionReceipt{
			Method:    mpp.MethodLightning,
			Reference: payload.SessionID,
			Status:    mpp.ReceiptStatusSuccess,
			Timestamp: now,
		}

		// Retrieve the cached close result.
		a.mu.Lock()
		cr := a.lastCloseResult
		a.mu.Unlock()

		if cr != nil && cr.sessionID == payload.SessionID {
			sessReceipt.RefundSats = cr.refundSats
			sessReceipt.RefundStatus = cr.refundStatus
		}

		receiptHeader := make(http.Header)
		data, err := json.Marshal(sessReceipt)
		if err != nil {
			log.Errorf("MPP Session: Failed to marshal "+
				"session receipt: %v", err)
			return nil
		}
		receiptHeader.Set(
			mpp.HeaderPaymentReceipt,
			mpp.Base64URLEncode(data),
		)
		return receiptHeader
	}

	// For non-close actions, use a standard receipt.
	receipt := &mpp.Receipt{
		Status:      mpp.ReceiptStatusSuccess,
		Method:      mpp.MethodLightning,
		Timestamp:   now,
		ChallengeID: cred.Challenge.ID,
	}

	switch payload.Action {
	case mpp.SessionActionOpen:
		var sessReq mpp.SessionRequest
		if err := mpp.DecodeRequest(
			cred.Challenge.Request, &sessReq,
		); err == nil {
			receipt.Reference = sessReq.PaymentHash
		}
	case mpp.SessionActionBearer, mpp.SessionActionTopUp:
		receipt.Reference = payload.SessionID
	}

	receiptHeader := make(http.Header)
	if err := mpp.SetReceiptHeader(receiptHeader, receipt); err != nil {
		log.Errorf("MPP Session: Failed to set receipt header: %v",
			err)
		return nil
	}

	return receiptHeader
}
