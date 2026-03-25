package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lightninglabs/aperture/adminrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpcInsecure "google.golang.org/grpc/credentials/insecure"
	"gopkg.in/macaroon.v2"
)

// macaroonCredential implements credentials.PerRPCCredentials by
// attaching a hex-encoded macaroon to every gRPC call.
type macaroonCredential struct {
	mac      *macaroon.Macaroon
	insecure bool
}

// GetRequestMetadata returns the macaroon as hex-encoded gRPC metadata.
func (m *macaroonCredential) GetRequestMetadata(
	ctx context.Context, uri ...string) (map[string]string, error) {

	macBytes, err := m.mac.MarshalBinary()
	if err != nil {
		return nil, err
	}

	return map[string]string{
		"macaroon": hex.EncodeToString(macBytes),
	}, nil
}

// RequireTransportSecurity reports whether transport security is
// required.
func (m *macaroonCredential) RequireTransportSecurity() bool {
	return !m.insecure
}

// expandPath replaces a leading ~ with the user's home directory.
func expandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}

		return filepath.Join(home, path[1:])
	}

	return path
}

// getAdminClient creates a gRPC connection to the Aperture admin
// server and returns an AdminClient along with a cleanup function.
func getAdminClient() (adminrpc.AdminClient, func(), error) {
	macPath := expandPath(flags.macaroon)

	// Read and deserialize the macaroon file.
	macBytes, err := os.ReadFile(macPath)
	if err != nil {
		return nil, nil, ErrAuthWrap(fmt.Errorf(
			"unable to read macaroon at %s: %w",
			macPath, err,
		))
	}

	mac := &macaroon.Macaroon{}
	if err := mac.UnmarshalBinary(macBytes); err != nil {
		return nil, nil, ErrAuthWrap(fmt.Errorf(
			"unable to decode macaroon: %w", err,
		))
	}

	macCred := &macaroonCredential{
		mac:      mac,
		insecure: flags.insecure,
	}

	// Build transport credentials.
	var transportCreds credentials.TransportCredentials

	switch {
	case flags.insecure:
		transportCreds = grpcInsecure.NewCredentials()

	case flags.tlsCert != "":
		certPath := expandPath(flags.tlsCert)
		certBytes, err := os.ReadFile(certPath)
		if err != nil {
			return nil, nil, ErrConnectionWrap(fmt.Errorf(
				"unable to read TLS cert at %s: %w",
				certPath, err,
			))
		}

		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(certBytes) {
			return nil, nil, ErrConnectionWrap(fmt.Errorf(
				"unable to parse TLS cert at %s",
				certPath,
			))
		}

		transportCreds = credentials.NewTLS(&tls.Config{
			RootCAs: certPool,
		})

	default:
		// Use system certificate pool for publicly trusted
		// certs (e.g. Let's Encrypt).
		transportCreds = credentials.NewTLS(&tls.Config{})
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(transportCreds),
		grpc.WithPerRPCCredentials(macCred),
	}

	conn, err := grpc.NewClient(flags.host, opts...)
	if err != nil {
		return nil, nil, ErrConnectionWrap(fmt.Errorf(
			"unable to connect to %s: %w", flags.host, err,
		))
	}

	client := adminrpc.NewAdminClient(conn)
	cleanup := func() {
		_ = conn.Close()
	}

	return client, cleanup, nil
}
