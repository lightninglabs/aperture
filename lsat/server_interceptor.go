package lsat

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

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
	token, err := tokenFromContext(ctx)
	if err != nil {
		log.Debugf("No token extracted, error was: %v", err)
		return handler(ctx, req)
	}

	// We got a token, create a new context that wraps its value and
	// continue the call chain by invoking the handler.
	idCtx := AddToContext(ctx, KeyTokenID, *token)
	return handler(idCtx, req)
}

// wrappedStream is a thin wrapper around the grpc.ServerStream that allows us
// to overwrite the context of the stream.
type wrappedStream struct {
	grpc.ServerStream
	WrappedContext context.Context
}

// Context returns the context for this stream.
func (w *wrappedStream) Context() context.Context {
	return w.WrappedContext
}

// StreamInterceptor is an stream gRPC server interceptor that inspects incoming
// streams for authentication tokens. If an LSAT authentication token is found
// in the initial stream establishment request, its token ID is extracted and
// treated as client ID. The extracted ID is then attached to the request
// context in a format that is easy to extract by request handlers.
func (i *ServerInterceptor) StreamInterceptor(srv interface{},
	ss grpc.ServerStream, _ *grpc.StreamServerInfo,
	handler grpc.StreamHandler) error {

	// Try getting the authentication header embedded in the context meta
	// data and parse it. We ignore all errors that happen and just forward
	// the request to the handler if anything fails. Incoming calls with
	// invalid metadata will therefore just be treated as non-identified or
	// non-authenticated.
	ctx := ss.Context()
	token, err := tokenFromContext(ctx)
	if err != nil {
		log.Debugf("No token extracted, error was: %v", err)
		return handler(srv, ss)
	}

	// We got a token, create a new context that wraps its value and
	// continue the call chain by invoking the handler. We can't directly
	// modify the server stream so we have to wrap it.
	idCtx := AddToContext(ctx, KeyTokenID, *token)
	wrappedStream := &wrappedStream{ss, idCtx}
	return handler(srv, wrappedStream)
}

// tokenFromContext tries to extract the LSAT from a context.
func tokenFromContext(ctx context.Context) (*TokenID, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, fmt.Errorf("context contains no metadata")
	}
	header := &http.Header{
		HeaderAuthorization: md.Get(HeaderAuthorization),
	}
	log.Debugf("Auth header present in request: %s",
		md.Get(HeaderAuthorization))
	macaroon, _, err := FromHeader(header)
	if err != nil {
		return nil, fmt.Errorf("auth header extraction failed: %v", err)
	}

	// If there is an LSAT, decode and add it to the context.
	identifier, err := DecodeIdentifier(bytes.NewBuffer(macaroon.Id()))
	if err != nil {
		return nil, fmt.Errorf("token ID decoding failed: %v", err)
	}
	var clientID TokenID
	copy(clientID[:], identifier.TokenID[:])
	log.Debugf("Decoded client/token ID %s from auth header",
		clientID.String())
	return &clientID, nil
}
