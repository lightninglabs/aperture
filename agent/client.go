package agent

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/zpay32"
	"gopkg.in/macaroon.v2"
)

var (
	// challengeRegex parses the WWW-Authenticate header from an L402
	// challenge response.
	challengeRegex = regexp.MustCompile(
		`(LSAT|L402) macaroon="(.*?)", invoice="(.*?)"`,
	)
)

// Client is an L402-aware HTTP client for AI agents. It automatically handles
// 402 Payment Required responses by paying Lightning invoices and retrying
// with the acquired L402 token. This enables fully autonomous agent-to-agent
// payments with no human interaction.
//
// The minimum information needed for authentication is:
//   - A Lightning invoice payment (proof of payment = preimage)
//   - The resulting L402 token (macaroon + preimage)
//
// No usernames, passwords, API keys, or OAuth flows required.
type Client struct {
	lnd        *lndclient.GrpcLndServices
	httpClient *http.Client
	store      *TokenStore
	maxCost    btcutil.Amount
	maxFee     btcutil.Amount
	payTimeout time.Duration
}

// NewClient creates a new L402-aware HTTP client from the given LND services
// and token store.
func NewClient(lnd *lndclient.GrpcLndServices, store *TokenStore,
	maxCost, maxFee btcutil.Amount, payTimeout time.Duration,
	requestTimeout time.Duration, allowInsecure bool) *Client {

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if allowInsecure {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
	}

	return &Client{
		lnd: lnd,
		httpClient: &http.Client{
			Timeout:   requestTimeout,
			Transport: transport,
		},
		store:      store,
		maxCost:    maxCost,
		maxFee:     maxFee,
		payTimeout: payTimeout,
	}
}

// Do executes an HTTP request with automatic L402 handling. If the server
// returns 402 Payment Required, the client will:
//  1. Parse the L402 challenge from WWW-Authenticate header
//  2. Pay the Lightning invoice
//  3. Retry the request with the L402 token attached
//
// This is the core method that enables machine-to-machine payments.
func (c *Client) Do(ctx context.Context, req *http.Request) (
	*http.Response, error) {

	// Check if we already have a valid credential for this service.
	serviceURL := serviceKeyFromRequest(req)
	if cred := c.store.Get(serviceURL); cred != nil {
		if err := cred.ApplyToRequest(req); err != nil {
			log.Warnf("Failed to attach cached credential: %v",
				err)
		} else {
			resp, err := c.httpClient.Do(req)
			if err != nil {
				return nil, err
			}

			// If the credential is still valid, return.
			if resp.StatusCode != http.StatusPaymentRequired {
				return resp, nil
			}

			// Credential expired or invalid, discard it.
			resp.Body.Close()
			c.store.Delete(serviceURL)
			log.Infof("Cached L402 credential expired for %s, "+
				"re-acquiring", serviceURL)
		}
	}

	// Make the initial request without auth.
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("initial request failed: %w", err)
	}

	// If no payment required, return as-is.
	if resp.StatusCode != http.StatusPaymentRequired {
		return resp, nil
	}

	// We got a 402 — extract the L402 challenge.
	defer resp.Body.Close()

	mac, invoiceStr, paymentHash, err := parseChallenge(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse L402 challenge: %w",
			err)
	}

	log.Infof("Received L402 challenge for %s, paying invoice "+
		"(hash=%s)", serviceURL, paymentHash)

	// Verify the cost doesn't exceed our limit.
	if err := c.checkCost(invoiceStr); err != nil {
		return nil, err
	}

	// Pay the Lightning invoice.
	preimage, amountPaid, feePaid, err := c.payInvoice(
		ctx, invoiceStr,
	)
	if err != nil {
		return nil, fmt.Errorf("payment failed: %w", err)
	}

	log.Infof("Payment successful for %s (amount=%d msat, fee=%d msat)",
		serviceURL, amountPaid, feePaid)

	// Build the paid credential and cache it.
	cred := &Credential{
		Macaroon:       mac,
		Preimage:       preimage,
		PaymentHash:    paymentHash,
		AmountPaid:     amountPaid,
		RoutingFeePaid: feePaid,
		CreatedAt:      time.Now(),
	}
	c.store.Put(serviceURL, cred)

	// Retry the original request with the L402 token.
	retryReq := req.Clone(ctx)
	if err := cred.ApplyToRequest(retryReq); err != nil {
		return nil, fmt.Errorf("apply L402 credential: %w", err)
	}

	return c.httpClient.Do(retryReq)
}

// Get is a convenience method for making GET requests to L402-protected
// endpoints.
func (c *Client) Get(ctx context.Context, url string) (
	*http.Response, error) {

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	return c.Do(ctx, req)
}

// Post is a convenience method for making POST requests to L402-protected
// endpoints.
func (c *Client) Post(ctx context.Context, url string,
	contentType string, body io.Reader) (*http.Response, error) {

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, url, body,
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", contentType)

	return c.Do(ctx, req)
}

// parseChallenge extracts the macaroon, invoice string, and payment hash from
// an HTTP 402 response's WWW-Authenticate header.
func parseChallenge(resp *http.Response) (*macaroon.Macaroon, string,
	lntypes.Hash, error) {

	var (
		matches  []string
		zeroHash lntypes.Hash
	)

	authHeaders := resp.Header.Values("WWW-Authenticate")
	for _, h := range authHeaders {
		matches = challengeRegex.FindStringSubmatch(h)
		if len(matches) == 4 {
			break
		}
	}

	if len(matches) != 4 {
		return nil, "", zeroHash, fmt.Errorf(
			"no valid L402 challenge in WWW-Authenticate header",
		)
	}

	macB64, invoiceStr := matches[2], matches[3]

	macBytes, err := base64.StdEncoding.DecodeString(macB64)
	if err != nil {
		return nil, "", zeroHash, fmt.Errorf(
			"base64 decode macaroon: %w", err,
		)
	}

	mac := &macaroon.Macaroon{}
	if err := mac.UnmarshalBinary(macBytes); err != nil {
		return nil, "", zeroHash, fmt.Errorf(
			"unmarshal macaroon: %w", err,
		)
	}

	// Decode the invoice to extract the payment hash.
	invoice, err := zpay32.Decode(invoiceStr, nil)
	if err != nil {
		return nil, "", zeroHash, fmt.Errorf(
			"decode invoice: %w", err,
		)
	}

	if invoice.PaymentHash == nil {
		return nil, "", zeroHash, fmt.Errorf(
			"invoice missing payment hash",
		)
	}

	return mac, invoiceStr, *invoice.PaymentHash, nil
}

// checkCost verifies the invoice amount doesn't exceed the agent's configured
// maximum.
func (c *Client) checkCost(invoiceStr string) error {
	invoice, err := zpay32.Decode(invoiceStr, nil)
	if err != nil {
		return fmt.Errorf("decode invoice for cost check: %w", err)
	}

	maxCostMsat := lnwire.NewMSatFromSatoshis(c.maxCost)
	if invoice.MilliSat != nil && *invoice.MilliSat > maxCostMsat {
		return fmt.Errorf(
			"L402 cost %d msat exceeds max %d msat",
			*invoice.MilliSat, maxCostMsat,
		)
	}

	return nil
}

// payInvoice pays a Lightning invoice and returns the preimage as proof of
// payment. This is the atomic operation that converts money into
// authentication.
func (c *Client) payInvoice(ctx context.Context,
	invoiceStr string) (lntypes.Preimage, lnwire.MilliSatoshi,
	lnwire.MilliSatoshi, error) {

	var zeroPreimage lntypes.Preimage

	payCtx, cancel := context.WithTimeout(ctx, c.payTimeout)
	defer cancel()

	respChan := c.lnd.Client.PayInvoice(
		payCtx, invoiceStr, c.maxFee, nil,
	)

	select {
	case result := <-respChan:
		if result.Err != nil {
			return zeroPreimage, 0, 0, result.Err
		}

		return result.Preimage,
			lnwire.NewMSatFromSatoshis(result.PaidAmt),
			lnwire.NewMSatFromSatoshis(result.PaidFee),
			nil

	case <-payCtx.Done():
		return zeroPreimage, 0, 0, fmt.Errorf(
			"payment timed out after %v", c.payTimeout,
		)

	case <-ctx.Done():
		return zeroPreimage, 0, 0, ctx.Err()
	}
}

// serviceKeyFromRequest extracts a cache key for the service from the request.
// Uses scheme + host to scope tokens per service.
func serviceKeyFromRequest(req *http.Request) string {
	return req.URL.Scheme + "://" + req.URL.Host
}
