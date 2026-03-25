package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
)

const (
	// rootKeySize is the size in bytes of the admin root key.
	rootKeySize = 32
)

const (
	// macaroonMetadataKey is the gRPC metadata key used to pass the
	// hex-encoded macaroon, following the lnd convention.
	macaroonMetadataKey = "macaroon"

	// adminMacaroonLocation is the macaroon location string.
	adminMacaroonLocation = "aperture"

	// adminMacaroonID is the identifier embedded in the admin macaroon.
	adminMacaroonID = "admin"
)

// verifyMacaroon extracts and verifies a macaroon from gRPC metadata.
func verifyMacaroon(ctx context.Context, rootKey []byte) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(
			codes.Unauthenticated,
			"missing metadata",
		)
	}

	macHexSlice := md.Get(macaroonMetadataKey)
	if len(macHexSlice) == 0 {
		return status.Error(
			codes.Unauthenticated,
			"missing macaroon",
		)
	}

	macBytes, err := hex.DecodeString(macHexSlice[0])
	if err != nil {
		return status.Error(
			codes.Unauthenticated,
			"invalid macaroon encoding",
		)
	}

	mac := &macaroon.Macaroon{}
	if err := mac.UnmarshalBinary(macBytes); err != nil {
		return status.Error(
			codes.Unauthenticated,
			"invalid macaroon",
		)
	}

	// Verify the macaroon signature against the root key with
	// no additional caveats.
	if err := mac.Verify(rootKey, nil, nil); err != nil {
		return status.Error(
			codes.Unauthenticated,
			"macaroon verification failed",
		)
	}

	return nil
}

// unauthenticatedMethods lists gRPC full-method paths that should bypass
// macaroon authentication, allowing health probes from load balancers and
// monitoring systems without credentials.
var unauthenticatedMethods = map[string]struct{}{
	"/adminrpc.Admin/GetHealth": {},
}

// MacaroonInterceptor returns a gRPC unary server interceptor that validates
// requests using macaroon-based authentication. The macaroon is expected in the
// gRPC metadata under the key "macaroon" as a hex-encoded string.
func MacaroonInterceptor(
	rootKey []byte) grpc.UnaryServerInterceptor {

	return func(ctx context.Context, req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {

		if _, ok := unauthenticatedMethods[info.FullMethod]; ok {
			return handler(ctx, req)
		}

		if err := verifyMacaroon(ctx, rootKey); err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// MacaroonStreamInterceptor returns a gRPC stream server interceptor that
// validates requests using macaroon-based authentication.
func MacaroonStreamInterceptor(
	rootKey []byte) grpc.StreamServerInterceptor {

	return func(srv interface{}, ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler) error {

		if _, ok := unauthenticatedMethods[info.FullMethod]; ok {
			return handler(srv, ss)
		}

		if err := verifyMacaroon(
			ss.Context(), rootKey,
		); err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

// GenerateAdminMacaroon creates a new admin macaroon signed with the given
// root key.
func GenerateAdminMacaroon(
	rootKey []byte) (*macaroon.Macaroon, error) {

	mac, err := macaroon.New(
		rootKey, []byte(adminMacaroonID),
		adminMacaroonLocation, macaroon.LatestVersion,
	)
	if err != nil {
		return nil, err
	}

	return mac, nil
}

// WriteAdminMacaroon serializes and writes the macaroon to the given path.
func WriteAdminMacaroon(mac *macaroon.Macaroon, path string) error {
	macBytes, err := mac.MarshalBinary()
	if err != nil {
		return err
	}

	// Ensure the directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	return os.WriteFile(path, macBytes, 0600)
}

// ReadOrCreateRootKey reads a root key from the given file path, or generates
// a new random 32-byte key and writes it to disk if the file does not exist.
// Storing the root key on disk rather than in the shared SecretStore avoids
// the root key being accessible via a well-known deterministic hash.
func ReadOrCreateRootKey(path string) ([]byte, error) {
	keyBytes, err := os.ReadFile(path)
	if err == nil && len(keyBytes) == rootKeySize {
		return keyBytes, nil
	}

	// Generate a new random root key.
	rootKey := make([]byte, rootKeySize)
	if _, err := rand.Read(rootKey); err != nil {
		return nil, fmt.Errorf("unable to generate random root "+
			"key: %w", err)
	}

	// Ensure the directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("unable to create directory for "+
			"root key: %w", err)
	}

	if err := os.WriteFile(path, rootKey, 0600); err != nil {
		return nil, fmt.Errorf("unable to write root key: %w", err)
	}

	return rootKey, nil
}

// ReadAdminMacaroon reads and deserializes a macaroon from the given path.
func ReadAdminMacaroon(path string) (*macaroon.Macaroon, error) {
	macBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	mac := &macaroon.Macaroon{}
	if err := mac.UnmarshalBinary(macBytes); err != nil {
		return nil, err
	}

	return mac, nil
}
