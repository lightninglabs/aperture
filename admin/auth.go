package admin

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
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

// MacaroonInterceptor returns a gRPC unary server interceptor that validates
// requests using macaroon-based authentication. The macaroon is expected in the
// gRPC metadata under the key "macaroon" as a hex-encoded string.
func MacaroonInterceptor(
	rootKey []byte) grpc.UnaryServerInterceptor {

	return func(ctx context.Context, req interface{},
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(
				codes.Unauthenticated,
				"missing metadata",
			)
		}

		macHexSlice := md.Get(macaroonMetadataKey)
		if len(macHexSlice) == 0 {
			return nil, status.Error(
				codes.Unauthenticated,
				"missing macaroon",
			)
		}

		macBytes, err := hex.DecodeString(macHexSlice[0])
		if err != nil {
			return nil, status.Error(
				codes.Unauthenticated,
				"invalid macaroon encoding",
			)
		}

		mac := &macaroon.Macaroon{}
		if err := mac.UnmarshalBinary(macBytes); err != nil {
			return nil, status.Error(
				codes.Unauthenticated,
				"invalid macaroon",
			)
		}

		// Verify the macaroon signature against the root key with
		// no additional caveats.
		if err := mac.Verify(rootKey, nil, nil); err != nil {
			return nil, status.Error(
				codes.Unauthenticated,
				"macaroon verification failed",
			)
		}

		return handler(ctx, req)
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
