package aperture

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcutil/bech32"
	"github.com/lightninglabs/aperture/mint"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/lightningnetwork/lnd/zpay32"
)

const (
	// lnurlHRP is the human readable part of a bech32 LNURL string.
	lnurlHRP = "lnurl"

	// payRequestTag is the tag expected in the response of the invoice
	// request.
	payRequestTag = "payRequest"
)

// A compile-time flag to ensure the LNURLChallenger satisfies the
// mint.Challenger and auth.InvoiceChecker interface.
var _ mint.Challenger = (*LNURLChallenger)(nil)

// LNURLChallenger uses LNURL for invoice retrieval. It will not do proper
// invoice checking.
type LNURLChallenger struct {
	url     string
	network *chaincfg.Params
}

// NewLNURLChallenger creates a new LNURLChallenger.
func NewLNURLChallenger(lnurl string, network string) (*LNURLChallenger,
	error) {

	// Parse the network name to get the correct parameters.
	var net *chaincfg.Params
	switch network {
	case "mainnet":
		net = &chaincfg.MainNetParams
	case "testnet":
		net = &chaincfg.TestNet3Params
	case "regtest":
		net = &chaincfg.RegressionNetParams
	default:
		return nil, fmt.Errorf("unsupported network: %s", network)
	}

	// Get the URL from the LNURL string.
	url, err := parseLNURL(lnurl)
	if err != nil {
		return nil, err
	}

	return &LNURLChallenger{
		url:     url,
		network: net,
	}, nil
}

// parseLNURL parses the given LNURL into the URL that should be queried when a
// new invoice is required.
func parseLNURL(lnurl string) (string, error) {
	var (
		url string
		err error
	)
	switch {
	// If the string starts with is "LNURL" then the string is just the
	// bech32 encoding of the URL to use.
	case strings.HasPrefix(lnurl, "LNURL"):
		url, err = decodeLNURL(lnurl)
		if err != nil {
			return "", fmt.Errorf("error decoding LNURL: %v", err)
		}

	// If the string prefix is "lightning:" then what follows should be the
	// bech32 encoding of the URL to use.
	case strings.HasPrefix(lnurl, "lightning:"):
		url, err = decodeLNURL(strings.TrimPrefix(lnurl, "lightning:"))
		if err != nil {
			return "", fmt.Errorf("error decoding LNURL: %w", err)
		}

	// If the string starts with "lnurlp" then this part just needs to be
	// replaced with "https" inorder to reconstruct the URL to use.
	case strings.HasPrefix(lnurl, "lnurlp"):
		url = strings.Replace(lnurl, "lnurlp", "https", 1)

	// If the string contains an "@" symbol then this is a Lightning
	// Address.
	case strings.Contains(lnurl, "@"):
		parts := strings.Split(lnurl, "@")
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid LN address. Expected" +
				"the form <username>@<domain>")
		}

		username, domain := parts[0], parts[1]
		url = fmt.Sprintf(
			"https://%s/.well-known/lnurlp/%s", domain, username,
		)

	default:
		return "", fmt.Errorf("unsupported LNURL address")
	}

	return url, nil
}

// NewChallenge fetches a new invoice for the given price from the LNURL
// server. This is part of the
func (l *LNURLChallenger) NewChallenge(price int64) (string, lntypes.Hash,
	error) {

	paymentRequest, paymentHash, err := l.fetchInvoice(price)
	if err != nil {
		return "", lntypes.Hash{}, err
	}

	hash, err := lntypes.MakeHash(paymentHash)
	if err != nil {
		return "", lntypes.Hash{}, err
	}

	return paymentRequest, hash, nil
}

// fetchInvoice attempts to fetch an invoice from the LNURL server for the
// given price. It returns the invoice string and payment hash.
func (l *LNURLChallenger) fetchInvoice(price int64) (string, []byte, error) {
	// Make a GET request to the decoded LNURL.
	var payResp PayResponse
	if err := get(l.url, &payResp); err != nil {
		return "", nil, err
	}

	// Ensure that the response has the correct tag.
	if payResp.Tag != payRequestTag {
		return "", nil, fmt.Errorf("incorrect tag received. "+
			"Expected %s, got %s", payRequestTag, payResp.Tag)
	}

	// Check that the LNURL server accepts the given price.
	if price < payResp.MinSendable || price > payResp.MaxSendable {
		return "", nil, fmt.Errorf("price out of range for lnurl " +
			"server min and max parameters")
	}

	delim := "?"
	if strings.Contains(payResp.Callback, "?") {
		delim = "&"
	}
	getInvoiceReq := fmt.Sprintf(
		"%s%samount=%d", payResp.Callback, delim, price,
	)

	// Now make a request to the callback URL with the parameters of the
	// invoice we want.
	var invoice InvoiceResponse
	if err := get(getInvoiceReq, &invoice); err != nil {
		return "", nil, err
	}

	inv, err := zpay32.Decode(invoice.PayRequest, l.network)
	if err != nil {
		return "", nil, err
	}

	// Ensure that the invoice description hash matches the metadata
	// received before.
	metaHash := sha256.Sum256([]byte(html.UnescapeString(payResp.Metadata)))
	if !bytes.Equal(inv.DescriptionHash[:], metaHash[:]) {
		return "", nil, fmt.Errorf("invalid invoice description " +
			"hash received from the LNURL server")
	}

	return invoice.PayRequest, inv.PaymentHash[:], nil
}

// PayResponse is the structure of the JSON response expected from the initial
// query to the LNURL server.
type PayResponse struct {
	// Callback is the URL from LN SERVICE which will accept the pay
	// request parameters.
	Callback string `json:"callback"`

	// MaxSendable is the max amount LN SERVICE is willing to receive.
	MaxSendable int64 `json:"maxSendable"`

	// MinSendable is the min amount LN SERVICE is willing to receive, can
	// not be less than 1 or more than MaxSendable.
	MinSendable int64 `json:"minSendable"`

	// Metadata json which must be presented as raw string here, this is
	// required to pass signature verification at a later step.
	Metadata string `json:"metadata"`

	// Type of LNURL.
	Tag string `json:"tag"`
}

// InvoiceResponse is the structure of the JSON response we expect from the
// query to the Callback received in the PayResponse.
type InvoiceResponse struct {
	// PayRequest is a bech32-serialized lightning invoice.
	PayRequest string `json:"pr"`

	// Routes is an empty array.
	Routes []string `json:"routes"`
}

// get makes an HTTP get request to the given URL and attempts to unmarshal
// the response.
func get(url string, out interface{}) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("GET request error: %v", err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("could not read response body: %v", err)
	}
	defer resp.Body.Close()

	return json.Unmarshal(body, &out)
}

// decodeLNURL does a bech32 decode of an LNURL string.
func decodeLNURL(lnurl string) (string, error) {
	hrp, data, err := bech32.Decode(lnurl)
	if err != nil {
		return "", err
	}

	if hrp != lnurlHRP {
		return "", fmt.Errorf("incorrect hrp for LNURL. Expected "+
			"'%s', got '%s'", lnurlHRP, hrp)
	}

	data, err = bech32.ConvertBits(data, 5, 8, false)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
