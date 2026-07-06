package pricer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"

	"github.com/lightninglabs/aperture/pricesrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// maxPricerBodyBytes is the maximum number of leading request body bytes
// serialized into the pricer RPCs. The pricer only needs the JSON head with
// the model field, so the body is capped rather than dumped in full. This
// keeps a large request off the pricer's hot path and clear of the gRPC
// message size limit, while the full body still reaches the backend.
const maxPricerBodyBytes = 64 * 1024

// Config holds all the config values required to initialise the GRPCPricer.
type Config struct {
	// Enabled indicates if the grpcPricer is to be used.
	Enabled bool `long:"enabled" description:"Set to true if a gRPC server is available to query for price data"`

	// GRPCAddress is the address that the pricer gRPC server is serving on.
	GRPCAddress string `long:"grpcaddress" description:"gRPC addr to use for price info for service resources"`

	// Insecure indicates if the connection to the gRPC server should use
	// TLS encryption or not.
	Insecure bool `long:"insecure" description:"Set to true if no TLS encryption is to be used"`

	// TLSCertPath is the path the the tls cert used by the price server.
	TLSCertPath string `long:"tlscertpath" description:"Path to the servers tls cert"`

	// Metered indicates that the pricer should also be consulted on every
	// authenticated request (AuthorizeRequest) and receive response usage
	// reports (ReportUsage), enabling draw-down of prepaid token balances.
	// A metered price server must implement the full pricesrpc.Prices
	// service, including ChallengeMinted, AuthorizeRequest and
	// ReportUsage.
	Metered bool `long:"metered" description:"Set to true to consult the pricer on every authenticated request and report response usage back to it"`

	// UsageTailBytes is the maximum number of trailing response body bytes
	// captured for usage reports. Defaults to 16384 if unset.
	UsageTailBytes int `long:"usagetailbytes" description:"Maximum number of trailing response body bytes captured for usage reports"`
}

// DefaultUsageTailBytes is the default cap for the trailing response body
// bytes captured for usage reports.
const DefaultUsageTailBytes = 16384

// MaxUsageTailBytes is the upper bound accepted for UsageTailBytes, guarding
// against an unbounded per-request response capture.
const MaxUsageTailBytes = 1 << 20

// Validate checks the pricer configuration for consistency. It is safe to call
// on a disabled pricer, so a metered-but-disabled misconfiguration is caught.
func (c *Config) Validate() error {
	// Metering rides on top of the dynamic pricer, so requesting metering
	// without enabling the pricer would silently turn metering off.
	if c.Metered && !c.Enabled {
		return fmt.Errorf("dynamicprice.metered requires " +
			"dynamicprice.enabled")
	}

	if c.UsageTailBytes < 0 {
		return fmt.Errorf("dynamicprice.usagetailbytes must not be "+
			"negative, got %d", c.UsageTailBytes)
	}

	if c.UsageTailBytes > MaxUsageTailBytes {
		return fmt.Errorf("dynamicprice.usagetailbytes %d exceeds the "+
			"maximum of %d", c.UsageTailBytes, MaxUsageTailBytes)
	}

	return nil
}

// GRPCPricer uses the pricesrpc PricesClient to query a backend server for
// the price of a service resource given the resource path. It implements the
// Pricer interface.
type GRPCPricer struct {
	rpcConn   *grpc.ClientConn
	rpcClient pricesrpc.PricesClient
}

// NewGRPCPricer initialises a Pricer backed by a gRPC backend server.
func NewGRPCPricer(cfg *Config) (*GRPCPricer, error) {
	var (
		c   GRPCPricer
		err error
		opt grpc.DialOption
	)

	if cfg.Insecure {
		opt = grpc.WithTransportCredentials(insecure.NewCredentials())
	} else {
		tlsCredentials, err := credentials.NewClientTLSFromFile(
			cfg.TLSCertPath, "",
		)
		if err != nil {
			return nil, fmt.Errorf(
				"unable to load TLS cert %s: %v",
				cfg.TLSCertPath, err,
			)
		}
		opt = grpc.WithTransportCredentials(tlsCredentials)
	}

	c.rpcConn, err = grpc.NewClient(cfg.GRPCAddress, opt)
	if err != nil {
		return nil, err
	}

	c.rpcClient = pricesrpc.NewPricesClient(c.rpcConn)

	return &c, nil
}

// dumpRequest serializes the request's headers and a bounded prefix of its
// body, while leaving the request forwardable: the full body is buffered and
// restored on the request so a later reverse-proxy pass still sees it. Only a
// prefix of the body is serialized because the pricer needs no more than the
// JSON head with the model field, and dumping a large body in full would put
// it on the pricer's hot path and risk the gRPC message size limit.
func dumpRequest(r *http.Request) (string, error) {
	// Dump the request line and headers without the body.
	head, err := httputil.DumpRequest(r, false)
	if err != nil {
		return "", err
	}

	// With no body there is nothing more to serialize, but the head
	// already carries the trailing blank line.
	if r.Body == nil {
		return string(head), nil
	}

	// Read a bounded prefix of the body for the pricer, while buffering the
	// full body so it can be restored on the request for the backend.
	prefix, full, err := readBodyPrefix(r.Body, maxPricerBodyBytes)
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(full))

	return string(head) + string(prefix), nil
}

// readBodyPrefix reads up to max leading bytes of the body for the pricer and
// also returns the full body so the caller can restore it on the request. It
// reads the whole body once and slices the prefix from it, so the backend
// still receives every byte.
func readBodyPrefix(body io.ReadCloser, max int) (prefix []byte, full []byte,
	err error) {

	defer func() {
		_ = body.Close()
	}()

	full, err = io.ReadAll(body)
	if err != nil {
		return nil, nil, err
	}

	if len(full) > max {
		return full[:max], full, nil
	}

	return full, full, nil
}

// GetPrice queries the server for the price of a resource path and returns the
// price. GetPrice is part of the Pricer interface.
func (c GRPCPricer) GetPrice(ctx context.Context,
	r *http.Request) (int64, error) {

	reqText, err := dumpRequest(r)
	if err != nil {
		return 0, err
	}

	resp, err := c.rpcClient.GetPrice(ctx, &pricesrpc.GetPriceRequest{
		Path:            r.URL.Path,
		HttpRequestText: reqText,
	})
	if err != nil {
		return 0, err
	}

	return resp.PriceSats, nil
}

// ChallengeMinted notifies the price server that a fresh challenge macaroon
// with the given token ID was minted at the given price. It is part of the
// MeteredPricer interface.
func (c GRPCPricer) ChallengeMinted(ctx context.Context, r *http.Request,
	tokenID string, serviceName string, priceSats int64) error {

	reqText, err := dumpRequest(r)
	if err != nil {
		return err
	}

	_, err = c.rpcClient.ChallengeMinted(
		ctx, &pricesrpc.ChallengeMintedRequest{
			Path:            r.URL.Path,
			HttpRequestText: reqText,
			TokenId:         tokenID,
			PriceSats:       priceSats,
			ServiceName:     serviceName,
		},
	)

	return err
}

// AuthorizeRequest asks the price server whether an authenticated request may
// proceed, based on the token's remaining balance. It is part of the
// MeteredPricer interface.
func (c GRPCPricer) AuthorizeRequest(ctx context.Context, r *http.Request,
	tokenID string, serviceName string) (*AuthorizeResult, error) {

	reqText, err := dumpRequest(r)
	if err != nil {
		return nil, err
	}

	resp, err := c.rpcClient.AuthorizeRequest(
		ctx, &pricesrpc.AuthorizeRequestRequest{
			Path:            r.URL.Path,
			HttpRequestText: reqText,
			TokenId:         tokenID,
			ServiceName:     serviceName,
		},
	)
	if err != nil {
		return nil, err
	}

	return &AuthorizeResult{
		Allowed:   resp.Allowed,
		PriceSats: resp.PriceSats,
		Reason:    resp.Reason,
	}, nil
}

// ReportUsage reports the outcome of a proxied request so the price server
// can debit the token's balance. It is part of the MeteredPricer interface.
func (c GRPCPricer) ReportUsage(ctx context.Context, usage *Usage) error {
	_, err := c.rpcClient.ReportUsage(ctx, &pricesrpc.ReportUsageRequest{
		TokenId:         usage.TokenID,
		Path:            usage.Path,
		ServiceName:     usage.ServiceName,
		HttpStatus:      int32(usage.HTTPStatus),
		ContentType:     usage.ContentType,
		ContentEncoding: usage.ContentEncoding,
		Complete:        usage.Complete,
		ResponseTail:    usage.ResponseTail,
	})

	return err
}

// Close closes the gRPC connection. It is part of the Pricer interface.
func (c GRPCPricer) Close() error {
	return c.rpcConn.Close()
}
