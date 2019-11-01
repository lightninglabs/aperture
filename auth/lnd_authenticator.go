package auth

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"

	"github.com/lightninglabs/kirin/macaroons"
	"github.com/lightninglabs/loop/lndclient"
	"github.com/lightningnetwork/lnd/lnrpc"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon-bakery.v2/bakery/checkers"
)

var (
	authRegex  = regexp.MustCompile("LSAT (.*?):([a-f0-9]{64})")
	opWildcard = "*"
)

type LndAuthenticator struct {
	client     lnrpc.LightningClient
	macService *macaroons.Service
}

// A compile time flag to ensure the LndAuthenticator satisfies the
// Authenticator interface.
var _ Authenticator = (*LndAuthenticator)(nil)

// NewLndAuthenticator creates a new authenticator that is connected to an lnd
// backend and can create new invoices if required.
func NewLndAuthenticator(cfg *Config) (*LndAuthenticator, error) {
	client, err := lndclient.NewBasicClient(
		cfg.LndHost, cfg.TlsPath, cfg.MacDir, cfg.Network,
	)
	if err != nil {
		return nil, err
	}
	macService, err := macaroons.NewService()
	if err != nil {
		return nil, err
	}

	return &LndAuthenticator{
		client:     client,
		macService: macService,
	}, nil
}

// Accept returns whether or not the header successfully authenticates the user
// to a given backend service.
//
// NOTE: This is part of the Authenticator interface.
func (l *LndAuthenticator) Accept(header *http.Header) bool {
	authHeader := header.Get("Authorization")
	if authHeader == "" {
		return false
	}

	if !authRegex.MatchString(authHeader) {
		return false
	}

	matches := authRegex.FindStringSubmatch(authHeader)
	if len(matches) != 3 {
		return false
	}

	macBase64, preimageHex := matches[1], matches[2]
	macBytes, err := base64.StdEncoding.DecodeString(macBase64)
	if err != nil {
		return false
	}

	preimageBytes, err := hex.DecodeString(preimageHex)
	if err != nil {
		return false
	}

	// TODO(guggero): check preimage against payment hash caveat in the
	//  macaroon.
	if len(preimageBytes) != 32 {
		return false
	}

	err = l.macService.ValidateMacaroon(macBytes, []bakery.Op{})
	if err != nil {
		return false
	}
	return true
}

// FreshChallengeHeader returns a header containing a challenge for the user to
// complete.
//
// NOTE: This is part of the Authenticator interface.
func (l *LndAuthenticator) FreshChallengeHeader(r *http.Request) (
	http.Header, error) {

	// Obtain a new invoice from lnd first. We need to know the payment hash
	// so we can add it as a caveat to the macaroon.
	ctx := context.Background()
	invoice := &lnrpc.Invoice{
		Memo:  "LSAT",
		Value: 1,
	}
	response, err := l.client.AddInvoice(ctx, invoice)
	if err != nil {
		fmt.Printf("error adding invoice: %v\n", err)
		return nil, err
	}
	paymentHashHex := hex.EncodeToString(response.RHash)

	// Create a new macaroon and add the payment hash as a caveat.
	// The bakery requires at least one operation so we add an "allow all"
	// permission set for now.
	mac, err := l.macService.NewMacaroon(
		[]bakery.Op{{Entity: opWildcard, Action: opWildcard}}, []string{
			checkers.Condition(macaroons.CondRHash, paymentHashHex),
		},
	)
	if err != nil {
		fmt.Printf("error creating macaroon: %v\n", err)
		return nil, err
	}

	str := "LSAT macaroon='%s' invoice='%s'"
	str = fmt.Sprintf(
		str, base64.StdEncoding.EncodeToString(mac),
		response.GetPaymentRequest(),
	)
	header := r.Header
	header.Set("WWW-Authenticate", str)
	return header, nil
}
