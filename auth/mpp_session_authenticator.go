package auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightninglabs/aperture/mpp"
	"github.com/lightninglabs/neutrino/cache/lru"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/zpay32"
)

const (
	// defaultDepositMultiplier is the default number of service units per
	// deposit.
	defaultDepositMultiplier = 20

	// defaultIdleTimeout is the default idle timeout for sessions.
	defaultIdleTimeout = 5 * time.Minute

	// sessionStatusOpen is the status string for an open session.
	sessionStatusOpen = "open"
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

	// chainParams identifies the Bitcoin network for BOLT11 decoding.
	chainParams *chaincfg.Params

	// closeResults caches refund outcomes from close actions keyed by
	// sessionID so ReceiptHeader can include the correct refund data.
	// The LRU cache bounds memory usage — old entries are evicted when
	// capacity is reached, preventing leaks if ReceiptHeader is never
	// called (e.g., connection drops).
	closeResults *lru.Cache[string, *closeResult]
}

// closeResult stores the outcome of a session close for receipt generation.
type closeResult struct {
	sessionID    string
	refundSats   int64
	refundStatus string // "succeeded", "failed", or "skipped"
}

// Size returns the size of the close result for the LRU cache. Each entry
// counts as 1 unit since we bound by count, not bytes.
//
// NOTE: This implements the cache.Value interface.
func (c *closeResult) Size() (uint64, error) {
	return 1, nil
}

const (
	// maxCloseResults is the maximum number of close results to cache.
	// This bounds memory usage in case ReceiptHeader is never called.
	maxCloseResults = 1000
)

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
		chainParams:       networkToChainParams(cfg.Network),
		closeResults: lru.NewCache[string, *closeResult](
			maxCloseResults,
		),
	}
}

// Scheme returns the authentication scheme identifier for the MPP session
// authenticator.
//
// NOTE: This implements the SchemeTagged interface.
func (a *MPPSessionAuthenticator) Scheme() string {
	return AuthSchemeMPP
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

	ctx, cancel := context.WithTimeout(
		context.Background(), 10*time.Second,
	)
	defer cancel()

	switch payload.Action {
	case mpp.SessionActionOpen:
		return a.handleOpen(ctx, cred, &payload)

	case mpp.SessionActionBearer:
		return a.handleBearer(ctx, cred, &payload)

	case mpp.SessionActionTopUp:
		return a.handleTopUp(ctx, cred, &payload)

	case mpp.SessionActionClose:
		return a.handleClose(ctx, cred, &payload)

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

	// Validate return invoice is a valid BOLT11 invoice with no
	// encoded amount per the spec.
	if payload.ReturnInvoice == "" {
		log.Debugf("MPP Session: Missing return invoice for open")
		return false
	}
	inv, err := zpay32.Decode(
		payload.ReturnInvoice, a.chainParams,
	)
	if err != nil {
		log.Debugf("MPP Session: Invalid return invoice: %v", err)
		return false
	}
	if inv.MilliSat != nil {
		log.Debugf("MPP Session: Return invoice must not have " +
			"an encoded amount")
		return false
	}

	// Reject already-expired return invoices so the refund on close has
	// a chance of succeeding.
	invoiceExpiry := inv.Timestamp.Add(inv.Expiry())
	if time.Now().After(invoiceExpiry) {
		log.Debugf("MPP Session: Return invoice already expired "+
			"at %v", invoiceExpiry)
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
	cred *mpp.Credential, payload *mpp.SessionPayload) bool {

	// Verify challenge HMAC binding.
	params := cred.Challenge.ToChallengeParams()
	if !mpp.VerifyChallengeID(a.hmacSecret, params, cred.Challenge.ID) {
		log.Debugf("MPP Session: Challenge ID verification " +
			"failed for bearer")
		return false
	}

	// Check expiry.
	if cred.Challenge.Expires != "" {
		expiresAt, err := time.Parse(
			time.RFC3339, cred.Challenge.Expires,
		)
		if err != nil || time.Now().After(expiresAt) {
			log.Debugf("MPP Session: Challenge expired " +
				"for bearer")
			return false
		}
	}

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

	if session.Status != sessionStatusOpen {
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

	// Deduct the service price from the session balance. The price is
	// embedded in the challenge request's amount field.
	var sessReq mpp.SessionRequest
	if err := mpp.DecodeRequest(
		cred.Challenge.Request, &sessReq,
	); err != nil {
		log.Debugf("MPP Session: Failed to decode bearer "+
			"request: %v", err)
		return false
	}

	price, err := strconv.ParseInt(sessReq.Amount, 10, 64)
	if err != nil || price <= 0 {
		log.Debugf("MPP Session: Invalid price in bearer "+
			"request: %v", err)
		return false
	}

	if err := a.sessionStore.DeductSessionBalance(
		ctx, payload.SessionID, price,
	); err != nil {
		log.Debugf("MPP Session: Insufficient balance for "+
			"session %s: %v", payload.SessionID, err)
		return false
	}

	log.Tracef("MPP Session: Bearer accepted for session %s "+
		"(deducted %d sats)", payload.SessionID, price)
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
	if session.Status != sessionStatusOpen {
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
	cred *mpp.Credential, payload *mpp.SessionPayload) bool {

	// Verify challenge HMAC binding.
	params := cred.Challenge.ToChallengeParams()
	if !mpp.VerifyChallengeID(a.hmacSecret, params, cred.Challenge.ID) {
		log.Debugf("MPP Session: Challenge ID verification " +
			"failed for close")
		return false
	}

	// Check expiry.
	if cred.Challenge.Expires != "" {
		expiresAt, err := time.Parse(
			time.RFC3339, cred.Challenge.Expires,
		)
		if err != nil || time.Now().After(expiresAt) {
			log.Debugf("MPP Session: Challenge expired " +
				"for close")
			return false
		}
	}

	if payload.SessionID == "" || payload.Preimage == "" {
		log.Debugf("MPP Session: Missing sessionId or preimage " +
			"for close")
		return false
	}

	// Look up session to verify preimage before closing.
	session, err := a.sessionStore.GetSession(ctx, payload.SessionID)
	if err != nil {
		log.Debugf("MPP Session: Session not found for close: %v",
			err)
		return false
	}

	if session.Status != sessionStatusOpen {
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

	// Atomically close the session and get the remaining balance. This
	// prevents the TOCTOU race where a concurrent bearer request could
	// deduct balance between a separate GetSession and CloseSession.
	refundSats, err := a.sessionStore.CloseSessionAndGetBalance(
		ctx, payload.SessionID,
	)
	if err != nil {
		log.Errorf("MPP Session: Failed to close session: %v", err)
		return false
	}

	// Cache an initial close result for ReceiptHeader immediately so the
	// HTTP response can proceed. The refund runs asynchronously.
	_, _ = a.closeResults.Put(payload.SessionID, &closeResult{
		sessionID:    payload.SessionID,
		refundSats:   refundSats,
		refundStatus: "pending",
	})

	// Fire the refund asynchronously so the close response isn't blocked
	// by LN payment routing (which can take many seconds).
	if refundSats > 0 && a.paymentSender != nil {
		go func() {
			// Use a dedicated context with a generous timeout
			// for payment routing, independent of the HTTP
			// handler's context.
			refundCtx, cancel := context.WithTimeout(
				context.Background(), 60*time.Second,
			)
			defer cancel()

			_, err := a.paymentSender.SendPayment(
				refundCtx, session.ReturnInvoice,
				refundSats,
			)

			status := "succeeded"
			if err != nil {
				log.Errorf("MPP Session: Refund failed "+
					"for session %s (%d sats): %v",
					payload.SessionID, refundSats,
					err)
				status = "failed"
			} else {
				log.Infof("MPP Session: Refunded %d "+
					"sats to session %s",
					refundSats, payload.SessionID)
			}

			// Update the cached result with the final status.
			_, _ = a.closeResults.Put(
				payload.SessionID, &closeResult{
					sessionID:    payload.SessionID,
					refundSats:   refundSats,
					refundStatus: status,
				},
			)
		}()
	} else if refundSats > 0 {
		// PaymentSender not configured, can't refund.
		log.Warnf("MPP Session: No payment sender configured, "+
			"cannot refund %d sats for session %s",
			refundSats, payload.SessionID)

		_, _ = a.closeResults.Put(
			payload.SessionID, &closeResult{
				sessionID:    payload.SessionID,
				refundSats:   refundSats,
				refundStatus: "failed",
			},
		)
	}

	log.Infof("MPP Session: Closed session %s (refund=%d sats)",
		payload.SessionID, refundSats)
	return true
}

// FreshChallengeHeader returns a WWW-Authenticate: Payment header containing a
// session challenge with a fresh deposit invoice.
//
// NOTE: This is part of the Authenticator interface.
func (a *MPPSessionAuthenticator) FreshChallengeHeader(serviceName string,
	servicePrice int64) (http.Header, error) {

	// Compute deposit amount with overflow check.
	mult := int64(a.depositMultiplier)
	if servicePrice > 0 && mult > math.MaxInt64/servicePrice {
		return nil, fmt.Errorf("MPP Session: deposit overflow: "+
			"price=%d multiplier=%d", servicePrice, mult)
	}
	depositSats := servicePrice * mult

	// Create a deposit invoice, routing through the service's own lnd
	// if the challenger supports multi-merchant dispatch.
	paymentRequest, paymentHash, err := newChallengeFor(
		a.challenger, serviceName, depositSats,
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

	expires := time.Now().Add(defaultChallengeExpiry).UTC().Format(
		time.RFC3339,
	)
	params := &mpp.ChallengeParams{
		Realm:   a.realm,
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentSession,
		Request: encodedRequest,
		Expires: expires,
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

		// Retrieve the cached close result for this session. The
		// LRU cache naturally evicts old entries, so there is no
		// need for explicit deletion.
		cr, err := a.closeResults.Get(payload.SessionID)
		if err == nil {
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

// networkToChainParams converts a network name string to the corresponding
// btcd chain parameters for BOLT11 invoice decoding.
//
// Sui-adapted lnd exposes non-Bitcoin network identifiers ("devnet",
// "localnet") but still emits BOLT11 invoices with the Bitcoin regtest
// HRP ("lnbcrt...") because the underlying invoice encoder is reused
// unchanged. Map those identifiers to RegressionNetParams so zpay32
// can decode the return invoice clients send back on session open.
func networkToChainParams(network string) *chaincfg.Params {
	switch network {
	case "mainnet":
		return &chaincfg.MainNetParams
	case "testnet":
		return &chaincfg.TestNet3Params
	case "regtest", "devnet", "localnet":
		return &chaincfg.RegressionNetParams
	case "simnet":
		return &chaincfg.SimNetParams
	case "signet":
		return &chaincfg.SigNetParams
	default:
		return &chaincfg.MainNetParams
	}
}
