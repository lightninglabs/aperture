package l402

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

var (
	// powAuthHeaderRegex matches WWW-Authenticate headers for PoW
	// challenges. Format: L402 macaroon="<base64>", pow="<difficulty>".
	powAuthHeaderRegex = regexp.MustCompile(
		`(LSAT|L402) macaroon="(.*?)", pow="(\d+)"`,
	)
)

// PoWClientInterceptor is a gRPC client interceptor that handles L402
// proof-of-work authentication challenges. Unlike ClientInterceptor, it does
// not require an LND connection — it solves PoW challenges locally.
type PoWClientInterceptor struct {
	store         Store
	callTimeout   time.Duration
	lock          sync.Mutex
	allowInsecure bool
}

// NewPoWInterceptor creates a new gRPC client interceptor that solves PoW
// challenges to acquire L402 tokens.
func NewPoWInterceptor(store Store, rpcCallTimeout time.Duration,
	allowInsecure bool) *PoWClientInterceptor {

	return &PoWClientInterceptor{
		store:         store,
		callTimeout:   rpcCallTimeout,
		allowInsecure: allowInsecure,
	}
}

// UnaryInterceptor is an interceptor method that can be used directly by gRPC
// for unary calls. If the store contains a valid token, it is attached as
// credentials. If a 402 with a PoW challenge is returned, the challenge is
// solved and the request is retried.
func (i *PoWClientInterceptor) UnaryInterceptor(ctx context.Context,
	method string, req, reply interface{}, cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {

	i.lock.Lock()
	defer i.lock.Unlock()

	iCtx, err := i.newInterceptContext(ctx, opts)
	if err != nil {
		return err
	}

	rpcCtx, cancel := context.WithTimeout(ctx, i.callTimeout)
	defer cancel()
	err = invoker(rpcCtx, method, req, reply, cc, iCtx.opts...)
	if !IsPaymentRequired(err) {
		return err
	}

	err = i.handlePoWChallenge(iCtx)
	if err != nil {
		return err
	}

	rpcCtx2, cancel2 := context.WithTimeout(ctx, i.callTimeout)
	defer cancel2()
	return invoker(rpcCtx2, method, req, reply, cc, iCtx.opts...)
}

// StreamInterceptor is an interceptor method that can be used directly by gRPC
// for streaming calls. Works like UnaryInterceptor but for streams.
func (i *PoWClientInterceptor) StreamInterceptor(ctx context.Context,
	desc *grpc.StreamDesc, cc *grpc.ClientConn, method string,
	streamer grpc.Streamer,
	opts ...grpc.CallOption) (grpc.ClientStream, error) {

	i.lock.Lock()
	defer i.lock.Unlock()

	iCtx, err := i.newInterceptContext(ctx, opts)
	if err != nil {
		return nil, err
	}

	stream, err := streamer(ctx, desc, cc, method, iCtx.opts...)
	if !IsPaymentRequired(err) {
		return stream, err
	}

	err = i.handlePoWChallenge(iCtx)
	if err != nil {
		return nil, err
	}

	return streamer(ctx, desc, cc, method, iCtx.opts...)
}

// newInterceptContext creates the initial intercept context. If a valid token
// exists in the store, it is attached.
func (i *PoWClientInterceptor) newInterceptContext(ctx context.Context,
	opts []grpc.CallOption) (*interceptContext, error) {

	iCtx := &interceptContext{
		mainCtx:  ctx,
		opts:     opts,
		metadata: &metadata.MD{},
	}

	var err error
	iCtx.token, err = i.store.CurrentToken()
	switch {
	case err == ErrNoToken:
		// No token yet, nothing to do.

	case err != nil:
		return nil, fmt.Errorf("getting token from store failed: %v",
			err)

	case !iCtx.token.isPending():
		if err = i.addPoWCredentials(iCtx); err != nil {
			return nil, fmt.Errorf("adding macaroon failed: %v",
				err)
		}
	}

	iCtx.opts = append(iCtx.opts, grpc.Trailer(iCtx.metadata))
	return iCtx, nil
}

// handlePoWChallenge parses a PoW challenge from the response metadata, solves
// it, and stores the resulting token.
func (i *PoWClientInterceptor) handlePoWChallenge(
	iCtx *interceptContext) error {

	if iCtx.token != nil && !iCtx.token.isPending() {
		log.Debugf("Found valid PoW L402 token to add to request")
		return i.addPoWCredentials(iCtx)
	}

	authHeaders := iCtx.metadata.Get(AuthHeader)
	if len(authHeaders) == 0 {
		return fmt.Errorf("auth header not found in response")
	}

	// Try to match PoW challenge format.
	var matches []string
	for _, authHeader := range authHeaders {
		matches = powAuthHeaderRegex.FindStringSubmatch(authHeader)
		if len(matches) == 4 {
			break
		}
	}
	if len(matches) != 4 {
		return fmt.Errorf("invalid PoW auth header format: %s",
			authHeaders[0])
	}

	macBase64 := matches[2]
	difficultyStr := matches[3]

	macBytes, err := base64.StdEncoding.DecodeString(macBase64)
	if err != nil {
		return fmt.Errorf("base64 decode of macaroon failed: %v", err)
	}

	difficulty, err := strconv.ParseUint(difficultyStr, 10, 32)
	if err != nil {
		return fmt.Errorf("invalid PoW difficulty: %v", err)
	}

	// Create a token from the macaroon challenge.
	token, err := tokenFromChallenge(macBytes, &[32]byte{})
	if err != nil {
		return fmt.Errorf("unable to create token: %v", err)
	}

	// Extract the token ID from the macaroon identifier.
	id, err := DecodeIdentifier(
		bytes.NewReader(token.baseMac.Id()),
	)
	if err != nil {
		return fmt.Errorf("unable to decode identifier: %v", err)
	}
	token.PaymentHash = id.PaymentHash

	// Solve the PoW challenge.
	log.Infof("Solving PoW challenge with difficulty %d", difficulty)
	nonce, err := SolvePoW(id.TokenID, uint32(difficulty))
	if err != nil {
		return fmt.Errorf("failed to solve PoW: %v", err)
	}
	log.Infof("PoW solved with nonce %d", nonce)

	token.PowNonce = nonce
	token.PowDifficulty = uint32(difficulty)

	// Store the solved token.
	err = i.store.StoreToken(token)
	if err != nil {
		return fmt.Errorf("unable to store token: %v", err)
	}

	iCtx.token = token
	return i.addPoWCredentials(iCtx)
}

// addPoWCredentials adds a PoW-solved L402 token to the intercept context.
func (i *PoWClientInterceptor) addPoWCredentials(
	iCtx *interceptContext) error {

	if iCtx.token == nil {
		return fmt.Errorf("cannot add nil token to context")
	}

	mac, err := iCtx.token.SolvedMacaroon()
	if err != nil {
		return err
	}
	iCtx.opts = append(iCtx.opts, grpc.PerRPCCredentials(
		NewMacaroonCredential(mac, i.allowInsecure),
	))
	return nil
}
