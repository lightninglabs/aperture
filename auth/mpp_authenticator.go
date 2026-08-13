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
	// consumeTimeout bounds the store call that decides a request. It is
	// short on purpose: the client is waiting on it.
	consumeTimeout = 5 * time.Second

	// pruneTimeout bounds a single background sweep of expired consumption
	// records.
	pruneTimeout = 30 * time.Second
)

// MPPAuthenticator is an authenticator that implements the Payment HTTP
// Authentication Scheme for the Lightning "charge" intent. It issues BOLT11
// invoices as challenges and verifies payment preimages as credentials.
type MPPAuthenticator struct {
	// challenger creates new Lightning invoices for payment challenges.
	challenger mint.Challenger

	// checker verifies that invoices have been settled.
	checker InvoiceChecker

	// txnRecorder optionally records MPP transactions for the admin
	// dashboard. If nil, transactions are not tracked.
	txnRecorder TransactionRecorder

	// realm is the protection space identifier used in challenges.
	realm string

	// hmacSecret is the server secret used for stateless HMAC-SHA256
	// challenge ID binding per spec Section 5.1.2.1.1.
	hmacSecret []byte

	// network identifies the Lightning Network (e.g., "mainnet",
	// "regtest", "signet").
	network string

	// challengeExpiry is the duration after which a challenge expires.
	challengeExpiry time.Duration

	// chargeStore remembers which payments have already bought a request,
	// which is what keeps a credential from being replayed. It is never
	// nil: an authenticator without one would serve every replay, so the
	// constructor refuses to build one.
	chargeStore ChargeStore

	// reusableChargePolicy reports whether a service treats charge
	// credentials as re-presentable rather than single use. Nil means no
	// service does, which is the spec's strict rule.
	//
	// The one deployment that wants re-presentation is a metered service.
	// There, the payment buys a usage bundle rather than a single request,
	// and the metering pipeline debits the bundle per request until it is
	// exhausted, at which point the pricer refuses the request and a fresh
	// 402 is minted. The bundle draw-down is the spend, so consuming the
	// credential on first use would strand everything the buyer paid for
	// beyond one request. This mirrors exactly how an L402 token behaves
	// on the same service.
	reusableChargePolicy func(serviceName string) bool

	// pruneInterval is how often expired consumption records are swept.
	pruneInterval time.Duration

	// retentionMargin is how far past a challenge's expiry its consumption
	// record is kept before it may be pruned.
	retentionMargin time.Duration

	// quit is closed to stop the pruner, and wg waits for it to leave.
	quit chan struct{}
	wg   sync.WaitGroup

	// started and stopped guard Start and Stop against being run twice.
	started sync.Once
	stopped sync.Once
}

// Compile-time interface checks.
var _ Authenticator = (*MPPAuthenticator)(nil)
var _ ReceiptProvider = (*MPPAuthenticator)(nil)

const (
	// defaultChallengeExpiry is the default challenge expiration duration.
	defaultChallengeExpiry = 15 * time.Minute

	// defaultPruneInterval is how often the authenticator sweeps
	// consumption records whose challenges can no longer be presented.
	defaultPruneInterval = 5 * time.Minute

	// defaultRetentionMargin is how long a consumption record outlives the
	// expiry of the challenge it records.
	//
	// A record is only ever needed while its challenge could still be
	// accepted, and that stops at the expiry the challenge carries. The
	// margin exists because the clock that decides a challenge has expired
	// and the clock that decides a record may go need not be the same one:
	// several proxies can share a database. It is deliberately generous and
	// deliberately one-sided, since keeping a dead record costs a row and
	// dropping a live one costs a payment.
	defaultRetentionMargin = time.Hour
)

// NewMPPAuthenticator creates a new authenticator for the Payment HTTP
// Authentication Scheme with the "charge" intent.
//
// The charge store is required. Everything else the authenticator checks is a
// property of the payment rather than of the request carrying it, so without
// somewhere to record that a payment has been spent there is no way to tell a
// first presentation from a replay, and one payment would buy unlimited
// service. Refusing to build is the only honest answer for a deployment that
// cannot offer one.
func NewMPPAuthenticator(challenger mint.Challenger, checker InvoiceChecker,
	realm string, hmacSecret []byte, network string,
	txnRecorder TransactionRecorder,
	chargeStore ChargeStore) (*MPPAuthenticator, error) {

	if chargeStore == nil {
		return nil, fmt.Errorf("MPP: a charge store is required, " +
			"without one every charge credential can be replayed")
	}

	return &MPPAuthenticator{
		challenger:      challenger,
		checker:         checker,
		txnRecorder:     txnRecorder,
		realm:           realm,
		hmacSecret:      hmacSecret,
		network:         network,
		challengeExpiry: defaultChallengeExpiry,
		chargeStore:     chargeStore,
		pruneInterval:   defaultPruneInterval,
		retentionMargin: defaultRetentionMargin,
		quit:            make(chan struct{}),
	}, nil
}

// Start launches the background sweep that keeps the consumption record table
// from growing without bound. It is safe to call more than once.
// SetReusableChargePolicy installs the predicate that decides which services
// treat charge credentials as re-presentable rather than single use. It is
// meant to be called once, between construction and Start, with a predicate
// naming the metered services: on those, the payment buys a bundle whose
// draw-down is the spend, so the credential must stay presentable until the
// pricer refuses it. Passing nil restores the strict single-use rule
// everywhere.
func (a *MPPAuthenticator) SetReusableChargePolicy(
	policy func(serviceName string) bool) {

	a.reusableChargePolicy = policy
}

func (a *MPPAuthenticator) Start() {
	a.started.Do(func() {
		a.wg.Add(1)
		go a.pruneLoop()
	})
}

// Stop halts the background sweep and waits for it to finish. It is safe to
// call more than once, and safe to call on an authenticator that was never
// started.
func (a *MPPAuthenticator) Stop() {
	a.stopped.Do(func() {
		close(a.quit)
		a.wg.Wait()
	})
}

// pruneLoop periodically drops consumption records whose challenges have been
// expired long enough that no credential naming them could be accepted again.
func (a *MPPAuthenticator) pruneLoop() {
	defer a.wg.Done()

	ticker := time.NewTicker(a.pruneInterval)
	defer ticker.Stop()

	for {
		// Sweep once on entry so a proxy that is restarted often still
		// clears out what the previous run left behind.
		a.prune()

		select {
		case <-ticker.C:

		case <-a.quit:
			return
		}
	}
}

// prune runs a single sweep.
func (a *MPPAuthenticator) prune() {
	ctx, cancel := context.WithTimeout(
		context.Background(), pruneTimeout,
	)
	defer cancel()

	// A record only matters while the challenge it records could still be
	// accepted, and Accept refuses an expired challenge before it ever
	// reaches the store. So a record whose expiry is far enough in the past
	// cannot change any future answer, and dropping it cannot open a
	// replay.
	cutoff := time.Now().UTC().Add(-a.retentionMargin)

	pruned, err := a.chargeStore.PruneConsumedCharges(ctx, cutoff)
	if err != nil {
		log.Errorf("MPP: Failed to prune consumed charges: %v", err)
		return
	}

	if pruned > 0 {
		log.Debugf("MPP: Pruned %d consumed charge records expired "+
			"before %v", pruned, cutoff)
	}
}

// Scheme returns the authentication scheme identifier for the MPP charge
// authenticator.
//
// NOTE: This implements the SchemeTagged interface.
func (a *MPPAuthenticator) Scheme() string {
	return AuthSchemeMPP
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

	// Every challenge this authenticator mints carries an expiry, and the
	// expiry is slot 4 of the challenge HMAC, so a client can neither drop
	// it nor push it out. A credential that arrives without one therefore
	// describes a challenge no deployment of this code has issued, and it
	// is refused rather than treated as valid forever.
	//
	// The requirement is not merely tidiness. The expiry is the last moment
	// at which this credential could be accepted, and that is what lets the
	// record of its consumption eventually be dropped. A credential with no
	// expiry would have to be remembered for all time.
	if cred.Challenge.Expires == "" {
		log.Debugf("MPP: Challenge carries no expiry")
		return false
	}

	expiresAt, err := time.Parse(time.RFC3339, cred.Challenge.Expires)
	if err != nil {
		log.Debugf("MPP: Invalid expires format: %v", err)
		return false
	}
	if time.Now().After(expiresAt) {
		log.Debugf("MPP: Challenge expired at %v", expiresAt)
		return false
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

	// Everything above is a statement about the payment, and a payment that
	// was made stays made. Spend it here, so that this credential buys this
	// request and no other.
	//
	// The claim comes last of the checks, which is what keeps an honest
	// client from burning its own payment: the invoice lookup polls the
	// backend and can time out while an HTLC is still settling, and a
	// client answered with a 402 in that window has to be able to present
	// the same credential again. Once the claim is made, though, it stands
	// whatever happens next. The spec is explicit that the invalidation and
	// the decision to serve are one act, and that a consumed challenge
	// stays consumed even if the response never reaches the client.
	ctx, cancel := context.WithTimeout(
		context.Background(), consumeTimeout,
	)
	defer cancel()

	consumed, err := a.chargeStore.ConsumeCharge(
		ctx, paymentHash, cred.Challenge.ID, expiresAt,
	)
	if err != nil {
		// Fail closed. A store that cannot answer is a store that
		// cannot tell a first presentation from a replay.
		log.Errorf("MPP: Unable to consume charge for payment hash "+
			"%x: %v", paymentHash[:], err)
		return false
	}
	if !consumed {
		// On a metered service the credential is the key to a prepaid
		// bundle, not a single request, so a repeat presentation is
		// the normal way the buyer spends the rest of what it paid
		// for. The metering pipeline debits each request against the
		// bundle and refuses when it runs dry; that draw-down, not
		// this record, is what bounds the spend there.
		if a.reusableChargePolicy != nil &&
			a.reusableChargePolicy(serviceName) {

			log.Debugf("MPP: Charge credential re-presented for "+
				"payment hash %x on metered service %s",
				paymentHash[:], serviceName)
		} else {
			log.Warnf("MPP: Refusing replayed charge credential "+
				"for payment hash %x on service %s",
				paymentHash[:], serviceName)
			return false
		}
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

	// Build challenge params with expiry.
	expires := time.Now().Add(a.challengeExpiry).UTC().Format(
		time.RFC3339,
	)
	params := &mpp.ChallengeParams{
		Realm:   a.realm,
		Method:  mpp.MethodLightning,
		Intent:  mpp.IntentCharge,
		Request: encodedRequest,
		Expires: expires,
	}

	// Compute the HMAC challenge ID.
	params.ID = mpp.ComputeChallengeID(a.hmacSecret, params)

	// Set the challenge header.
	header := make(http.Header)
	mpp.SetChallengeHeader(header, params)

	log.Debugf("MPP: Created charge challenge with payment hash %x",
		paymentHash[:])

	// Record the pending transaction for admin dashboard tracking.
	if a.txnRecorder != nil {
		ctx := context.Background()
		if err := a.txnRecorder.RecordMPPTransaction(
			ctx, paymentHash[:], serviceName, servicePrice,
			"mpp_charge",
		); err != nil {
			log.Warnf("MPP: Failed to record transaction: %v",
				err)
		}
	}

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
