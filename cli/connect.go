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
	"time"

	"github.com/lightninglabs/aperture/adminrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcInsecure "google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
)

const (
	// defaultRPCTimeout is the default timeout for gRPC calls.
	defaultRPCTimeout = 30 * time.Second
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

// expandPath replaces a leading ~/ with the user's home directory.
// Only ~/... is expanded; ~user/... paths are left as-is.
func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}

		return filepath.Join(home, path[2:])
	}

	return path
}

// rpcTimeout returns a context with the configured RPC timeout applied.
func rpcTimeout(
	parent context.Context) (context.Context, context.CancelFunc) {

	timeout := flags.timeout
	if timeout == 0 {
		timeout = defaultRPCTimeout
	}

	return context.WithTimeout(parent, timeout)
}

// mapGRPCError inspects a gRPC error's status code and returns the
// appropriate CLIError with the correct semantic exit code.
func mapGRPCError(err error) error {
	if err == nil {
		return nil
	}

	st, ok := status.FromError(err)
	if !ok {
		return ErrConnectionWrap(err)
	}

	switch st.Code() {
	case codes.NotFound:
		return ErrNotFoundf("%s", st.Message())

	case codes.InvalidArgument:
		return ErrInvalidArgsf("%s", st.Message())

	case codes.Unauthenticated, codes.PermissionDenied:
		return ErrAuthWrap(err)

	case codes.Unavailable, codes.DeadlineExceeded:
		return ErrConnectionWrap(err)

	default:
		return WrapCLIError(
			ExitGeneralError, "rpc_error", err,
		)
	}
}

// getAdminClient creates a gRPC connection to the Aperture admin
// server and returns an AdminClient along with a cleanup function.
func getAdminClient() (adminrpc.AdminClient, func(), error) {
	macPath := expandPath(flags.macaroon)

	// Read and deserialize the macaroon file.
	macBytes, err := os.ReadFile(macPath)
	if err != nil {
		return nil, nil, ErrAuthWrap(fmt.Errorf(
			"unable to read macaroon: %w", err,
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

	// Warn when sending credentials over plaintext.
	if flags.insecure {
		fmt.Fprintln(os.Stderr,
			"WARNING: --insecure is set, macaroon "+
				"will be sent over plaintext",
		)
	}

	// Build transport credentials.
	var transportCreds credentials.TransportCredentials

	switch {
	case flags.insecure:
		transportCreds = grpcInsecure.NewCredentials()

	default:
		// Try loading the TLS certificate from the configured
		// path. When the file exists we pin to it; otherwise we
		// fall through to the system certificate pool (e.g. for
		// Let's Encrypt certs on a remote server).
		certPath := expandPath(flags.tlsCert)
		certBytes, err := os.ReadFile(certPath)

		switch {
		case err == nil:
			certPool := x509.NewCertPool()
			if !certPool.AppendCertsFromPEM(certBytes) {
				return nil, nil, ErrConnectionWrap(
					fmt.Errorf("unable to parse "+
						"TLS cert"),
				)
			}

			transportCreds = credentials.NewTLS(&tls.Config{
				RootCAs:    certPool,
				MinVersion: tls.VersionTLS12,
			})

		case os.IsNotExist(err):
			// Certificate not found at the default path,
			// fall through to system cert pool.
			transportCreds = credentials.NewTLS(&tls.Config{
				MinVersion: tls.VersionTLS12,
			})

		default:
			return nil, nil, ErrConnectionWrap(fmt.Errorf(
				"unable to read TLS cert: %w", err,
			))
		}
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
