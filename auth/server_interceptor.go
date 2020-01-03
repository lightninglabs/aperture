package auth

import (
	"bytes"
	"context"
	"net/http"

	"github.com/lightninglabs/loop/lsat"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ServerInterceptor is a gRPC server interceptor that extracts the token ID
// from the request context if an LSAT is present.
type ServerInterceptor struct{}

// UnaryInterceptor is an unary gRPC server interceptor that inspects incoming
// calls for authentication tokens. If an LSAT authentication token is found in
// the request, its token ID is extracted and treated as client ID. The
// extracted ID is then attached to the request context in a format that is easy
// to extract by request handlers.
func (i *ServerInterceptor) UnaryInterceptor(ctx context.Context,
	req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (
	resp interface{}, err error) {

	// Try getting the authentication header embedded in the context meta
	// data and parse it. We ignore all errors that happen and just forward
	// the request to the handler if anything fails. Incoming calls with
	// invalid metadata will therefore just be treated as non-identified or
	// non-authenticated.
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return handler(ctx, req)
	}
	header := &http.Header{
		HeaderAuthorization: md.Get(HeaderAuthorization),
	}
	log.Debugf("Auth header present in request: %s",
		md.Get(HeaderAuthorization))
	macaroon, _, err := FromHeader(header)
	if err != nil {
		return handler(ctx, req)
	}

	// If there is an LSAT, decode and add it to the context.
	identifier, err := lsat.DecodeIdentifier(bytes.NewBuffer(macaroon.Id()))
	if err != nil {
		return handler(ctx, req)
	}
	var clientID lsat.TokenID
	copy(clientID[:], identifier.TokenID[:])
	idCtx := AddToContext(ctx, KeyTokenID, clientID)
	log.Debugf("Decoded client/token ID %s from auth header",
		clientID.String())

	// Continue the call chain by invoking the handler.
	return handler(idCtx, req)
}
