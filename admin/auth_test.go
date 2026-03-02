package admin

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestMacaroonInterceptor(t *testing.T) {
	t.Parallel()

	rootKey := []byte("test-root-key-for-admin-macaroon")

	// Generate a valid macaroon.
	mac, err := GenerateAdminMacaroon(rootKey)
	require.NoError(t, err)

	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)
	macHex := hex.EncodeToString(macBytes)

	interceptor := MacaroonInterceptor(rootKey)

	handler := func(ctx context.Context, req interface{}) (
		interface{}, error) {

		return "ok", nil
	}

	// Test with valid macaroon.
	md := metadata.Pairs(macaroonMetadataKey, macHex)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := interceptor(ctx, nil, nil, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)

	// Test with missing metadata.
	_, err = interceptor(context.Background(), nil, nil, handler)
	require.Error(t, err)
	s, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.Unauthenticated, s.Code())

	// Test with missing macaroon key.
	md = metadata.Pairs("other-key", "value")
	ctx = metadata.NewIncomingContext(context.Background(), md)
	_, err = interceptor(ctx, nil, nil, handler)
	require.Error(t, err)
	s, _ = status.FromError(err)
	require.Equal(t, codes.Unauthenticated, s.Code())

	// Test with invalid hex.
	md = metadata.Pairs(macaroonMetadataKey, "not-hex!")
	ctx = metadata.NewIncomingContext(context.Background(), md)
	_, err = interceptor(ctx, nil, nil, handler)
	require.Error(t, err)
	s, _ = status.FromError(err)
	require.Equal(t, codes.Unauthenticated, s.Code())

	// Test with wrong root key.
	wrongInterceptor := MacaroonInterceptor([]byte("wrong-key-wrong-key-wrong-key!!"))
	md = metadata.Pairs(macaroonMetadataKey, macHex)
	ctx = metadata.NewIncomingContext(context.Background(), md)
	_, err = wrongInterceptor(ctx, nil, nil, handler)
	require.Error(t, err)
	s, _ = status.FromError(err)
	require.Equal(t, codes.Unauthenticated, s.Code())
}

func TestGenerateAndWriteReadMacaroon(t *testing.T) {
	t.Parallel()

	rootKey := []byte("another-test-root-key-32-bytes!")

	mac, err := GenerateAdminMacaroon(rootKey)
	require.NoError(t, err)
	require.NotNil(t, mac)

	// Verify the macaroon can be verified with the root key.
	err = mac.Verify(rootKey, nil, nil)
	require.NoError(t, err)

	// Write and read roundtrip.
	tmpDir := t.TempDir()
	macPath := filepath.Join(tmpDir, "test.macaroon")

	err = WriteAdminMacaroon(mac, macPath)
	require.NoError(t, err)

	// Verify file exists with correct permissions.
	info, err := os.Stat(macPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	// Read it back.
	readMac, err := ReadAdminMacaroon(macPath)
	require.NoError(t, err)

	// Verify the read macaroon matches.
	err = readMac.Verify(rootKey, nil, nil)
	require.NoError(t, err)

	origBytes, err := mac.MarshalBinary()
	require.NoError(t, err)
	readBytes, err := readMac.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, origBytes, readBytes)
}
