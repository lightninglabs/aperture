package l402

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lightninglabs/aperture/internal/test"
	"github.com/lightninglabs/lndclient"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lntypes"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
)

type interceptTestCase struct {
	name                string
	initialPreimage     *lntypes.Preimage
	interceptor         *ClientInterceptor
	resetCb             func(addL402 bool)
	expectLndCall       bool
	expectSecondLndCall bool
	sendPaymentCb       func(*testing.T, test.PaymentChannelMessage)
	trackPaymentCb      func(*testing.T, test.TrackPaymentMessage)
	expectToken         bool
	expectInterceptErr  string
	expectBackendCalls  int
	expectMacaroonCall1 bool
	expectMacaroonCall2 bool
}

type mockStore struct {
	token *Token

	// currentTokenFn overrides CurrentToken when set.
	currentTokenFn func() (*Token, error)
}

func (s *mockStore) CurrentToken() (*Token, error) {
	if s.currentTokenFn != nil {
		return s.currentTokenFn()
	}

	if s.token == nil {
		return nil, ErrNoToken
	}
	return s.token, nil
}

func (s *mockStore) AllTokens() (map[string]*Token, error) {
	return map[string]*Token{"foo": s.token}, nil
}

func (s *mockStore) StoreToken(token *Token) error {
	s.token = token
	return nil
}

func (s *mockStore) RemovePendingToken() error {
	s.token = nil
	return nil
}

// interceptorTestHarness provides shared L402 state and transport behavior for
// interceptor concurrency tests.
type interceptorTestHarness struct {
	// lnd provides payment and tracking test channels.
	lnd *test.LndMockServices

	// store holds the token shared by concurrent calls.
	store *mockStore

	// interceptor is the client interceptor under test.
	interceptor *ClientInterceptor

	// authenticatedCalls counts transports with L402 credentials.
	authenticatedCalls atomic.Int32

	// unauthenticatedCalls counts transports without L402 credentials.
	unauthenticatedCalls atomic.Int32

	// onAuthenticated runs during authenticated transport calls.
	onAuthenticated func(context.Context, []grpc.CallOption) error

	// onUnauthenticated runs during unauthenticated transport calls.
	onUnauthenticated func(context.Context, []grpc.CallOption) error
}

// interceptorTestStream is a client stream used to identify individual stream
// establishment attempts.
type interceptorTestStream struct {
	// ClientStream supplies the stream methods that these tests don't call.
	grpc.ClientStream
}

// newInterceptorTestHarness creates an interceptor test harness with the given
// initial token.
func newInterceptorTestHarness(token *Token) *interceptorTestHarness {
	lnd := test.NewMockLnd()
	store := &mockStore{token: token}

	return &interceptorTestHarness{
		lnd:   lnd,
		store: store,
		interceptor: NewInterceptor(
			&lnd.LndServices, store, testTimeout,
			DefaultMaxCostSats, DefaultMaxRoutingFeeSats, false,
		),
	}
}

// transport simulates an Aperture transport and runs the configured hook for
// authenticated or unauthenticated calls.
func (h *interceptorTestHarness) transport(ctx context.Context,
	opts ...grpc.CallOption) error {

	md, authenticated, err := requestMetadata(ctx, opts)
	if err != nil {
		return err
	}

	var hook func(context.Context, []grpc.CallOption) error
	if authenticated {
		if md["macaroon"] == "" {
			return errors.New("empty L402 credential")
		}

		h.authenticatedCalls.Add(1)
		hook = h.onAuthenticated
	} else {
		h.unauthenticatedCalls.Add(1)
		hook = h.onUnauthenticated
	}

	if hook != nil {
		err = hook(ctx, opts)
	}
	if err != nil && !IsPaymentRequired(err) {
		return err
	}
	if authenticated && err == nil {
		return nil
	}

	setAuthHeaders(opts, makeAuthHeaders(testMacBytes, true))

	return status.Error(GRPCErrCode, GRPCErrMessage)
}

// call invokes the unary or stream interceptor through the test transport.
func (h *interceptorTestHarness) call(ctx context.Context, stream bool,
	opts ...grpc.CallOption) error {

	if stream {
		_, err := h.interceptor.StreamInterceptor(
			ctx, nil, nil, "test",
			func(ctx context.Context, _ *grpc.StreamDesc,
				_ *grpc.ClientConn, _ string,
				opts ...grpc.CallOption) (
				grpc.ClientStream, error) {

				return nil, h.transport(ctx, opts...)
			}, opts...,
		)

		return err
	}

	return h.interceptor.UnaryInterceptor(
		ctx, "test", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ interface{},
			_ *grpc.ClientConn, opts ...grpc.CallOption) error {

			return h.transport(ctx, opts...)
		}, opts...,
	)
}

// start invokes a test interceptor call asynchronously.
func (h *interceptorTestHarness) start(ctx context.Context,
	stream bool) <-chan error {

	callErr := make(chan error, 1)
	go func() {
		callErr <- h.call(ctx, stream)
	}()

	return callErr
}

var (
	lnd         = test.NewMockLnd()
	store       = &mockStore{}
	testTimeout = 5 * time.Second
	interceptor = NewInterceptor(
		&lnd.LndServices, store, testTimeout,
		DefaultMaxCostSats, DefaultMaxRoutingFeeSats, false,
	)
	testMac         = makeMac()
	testMacBytes    = serializeMac(testMac)
	testMacHex      = hex.EncodeToString(testMacBytes)
	paidPreimage    = lntypes.Preimage{1, 2, 3, 4, 5}
	backendErr      error
	backendAuth     = []string{}
	callMD          map[string]string
	numBackendCalls = 0
	overallWg       sync.WaitGroup
	backendWg       sync.WaitGroup

	testCases = []interceptTestCase{{
		name:            "no auth required happy path",
		initialPreimage: nil,
		interceptor:     interceptor,
		resetCb: func(addL402 bool) {
			resetBackend(nil, []string{})
		},
		expectLndCall:       false,
		expectToken:         false,
		expectBackendCalls:  1,
		expectMacaroonCall1: false,
		expectMacaroonCall2: false,
	}, {
		name:            "auth required, no token yet",
		initialPreimage: nil,
		interceptor:     interceptor,
		resetCb: func(addL402 bool) {
			resetBackend(
				status.New(GRPCErrCode, GRPCErrMessage).Err(),
				makeAuthHeaders(testMacBytes, addL402),
			)
		},
		expectLndCall: true,
		sendPaymentCb: func(t *testing.T,
			msg test.PaymentChannelMessage) {

			require.Len(t, callMD, 0)

			// The next call to the "backend" shouldn't return an
			// error.
			resetBackend(nil, []string{})
			msg.Done <- lndclient.PaymentResult{
				Preimage: paidPreimage,
				PaidAmt:  123,
				PaidFee:  345,
			}
		},
		trackPaymentCb: func(t *testing.T,
			msg test.TrackPaymentMessage) {

			t.Fatal("didn't expect call to trackPayment")
		},
		expectToken:         true,
		expectBackendCalls:  2,
		expectMacaroonCall1: false,
		expectMacaroonCall2: true,
	}, {
		name:            "auth required, has token",
		initialPreimage: &paidPreimage,
		interceptor:     interceptor,
		resetCb: func(addL402 bool) {
			resetBackend(nil, []string{})
		},
		expectLndCall:       false,
		expectToken:         true,
		expectBackendCalls:  1,
		expectMacaroonCall1: true,
		expectMacaroonCall2: false,
	}, {
		name:            "auth required, has pending token",
		initialPreimage: &zeroPreimage,
		interceptor:     interceptor,
		resetCb: func(addL402 bool) {
			resetBackend(
				status.New(GRPCErrCode, GRPCErrMessage).Err(),
				makeAuthHeaders(testMacBytes, addL402),
			)
		},
		expectLndCall: true,
		sendPaymentCb: func(t *testing.T,
			msg test.PaymentChannelMessage) {

			t.Fatal("didn't expect call to sendPayment")
		},
		trackPaymentCb: func(t *testing.T,
			msg test.TrackPaymentMessage) {

			// The next call to the "backend" shouldn't return an
			// error.
			resetBackend(nil, []string{})
			msg.Updates <- lndclient.PaymentStatus{
				State:    lnrpc.Payment_SUCCEEDED,
				Preimage: paidPreimage,
			}
		},
		expectToken:         true,
		expectBackendCalls:  2,
		expectMacaroonCall1: false,
		expectMacaroonCall2: true,
	}, {
		name:            "auth required, has pending but expired token",
		initialPreimage: &zeroPreimage,
		interceptor:     interceptor,
		resetCb: func(addL402 bool) {
			resetBackend(
				status.New(GRPCErrCode, GRPCErrMessage).Err(),
				makeAuthHeaders(testMacBytes, addL402),
			)
		},
		expectLndCall:       true,
		expectSecondLndCall: true,
		sendPaymentCb: func(t *testing.T,
			msg test.PaymentChannelMessage) {

			require.Len(t, callMD, 0)

			// The next call to the "backend" shouldn't return an
			// error.
			resetBackend(nil, []string{})
			msg.Done <- lndclient.PaymentResult{
				Preimage: paidPreimage,
				PaidAmt:  123,
				PaidFee:  345,
			}
		},
		trackPaymentCb: func(t *testing.T,
			msg test.TrackPaymentMessage) {

			// The next call to the "backend" shouldn't return an
			// error.
			resetBackend(nil, []string{})
			msg.Updates <- lndclient.PaymentStatus{
				State: lnrpc.Payment_FAILED,
			}
		},
		expectToken:         true,
		expectBackendCalls:  2,
		expectMacaroonCall1: false,
		expectMacaroonCall2: true,
	}, {
		name:            "auth required, no token yet, cost limit",
		initialPreimage: nil,
		interceptor: NewInterceptor(
			&lnd.LndServices, store, testTimeout, 100,
			DefaultMaxRoutingFeeSats, false,
		),
		resetCb: func(addL402 bool) {
			resetBackend(
				status.New(GRPCErrCode, GRPCErrMessage).Err(),
				makeAuthHeaders(testMacBytes, addL402),
			)
		},
		expectLndCall: false,
		expectToken:   false,
		expectInterceptErr: "cannot pay for L402 automatically, cost " +
			"of 500000 msat exceeds configured max cost of " +
			"100000 msat",
		expectBackendCalls:  1,
		expectMacaroonCall1: false,
		expectMacaroonCall2: false,
	}}
)

// resetBackend is used by the test cases to define the behaviour of the
// simulated backend and reset its starting conditions.
func resetBackend(expectedErr error, expectedAuth []string) {
	backendErr = expectedErr
	backendAuth = expectedAuth
	callMD = nil
}

// invoker is a simple function that simulates the actual call to the server.
// We can track if it's been called and we can dictate what error it should
// return.
func invoker(opts []grpc.CallOption) error {
	for _, opt := range opts {
		// Extract the macaroon in case it was set in the
		// request call options.
		creds, ok := opt.(grpc.PerRPCCredsCallOption)
		if ok {
			callMD, _ = creds.Creds.GetRequestMetadata(
				context.Background(),
			)
		}

		// Should we simulate an auth header response?
		trailer, ok := opt.(grpc.TrailerCallOption)
		if ok && len(backendAuth) != 0 {
			trailer.TrailerAddr.Set(
				AuthHeader, backendAuth...,
			)
		}
	}
	numBackendCalls++
	return backendErr
}

// TestUnaryInterceptor tests that the interceptor can handle L402 protocol
// responses for unary calls and pay the token.
func TestUnaryInterceptor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	unaryInvoker := func(_ context.Context, _ string,
		_ interface{}, _ interface{}, _ *grpc.ClientConn,
		opts ...grpc.CallOption) error {

		defer backendWg.Done()
		return invoker(opts)
	}

	// Run through the test cases.
	for _, tc := range testCases {
		intercept := func() error {
			return tc.interceptor.UnaryInterceptor(
				ctx, "", nil, nil, nil, unaryInvoker, nil,
			)
		}
		t.Run(tc.name+" with LSAT header only", func(t *testing.T) {
			testInterceptor(t, tc, false, intercept)
		})
		t.Run(tc.name+" with LSAT+L402 headers", func(t *testing.T) {
			testInterceptor(t, tc, true, intercept)
		})
	}
}

// TestStreamInterceptor tests that the interceptor can handle L402 protocol
// responses in streams and pay the token.
func TestStreamInterceptor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	streamInvoker := func(_ context.Context,
		_ *grpc.StreamDesc, _ *grpc.ClientConn,
		_ string, opts ...grpc.CallOption) (
		grpc.ClientStream, error) { // nolint: unparam

		defer backendWg.Done()
		return nil, invoker(opts)
	}

	// Run through the test cases.
	for _, tc := range testCases {
		intercept := func() error {
			_, err := tc.interceptor.StreamInterceptor(
				ctx, nil, nil, "", streamInvoker,
			)
			return err
		}
		t.Run(tc.name+" with LSAT header only", func(t *testing.T) {
			testInterceptor(t, tc, false, intercept)
		})
		t.Run(tc.name+" with LSAT+L402 headers", func(t *testing.T) {
			testInterceptor(t, tc, true, intercept)
		})
	}
}

// TestInterceptorConcurrentPaidCalls verifies that unary calls and stream
// establishment are not serialized when the store contains a paid token.
func TestInterceptorConcurrentPaidCalls(t *testing.T) {
	testCases := []struct {
		name   string
		stream bool
	}{
		{
			name: "unary",
		},
		{
			name:   "stream",
			stream: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newInterceptorTestHarness(
				makeToken(&paidPreimage),
			)
			callStarted := make(chan struct{}, 2)
			releaseCalls := make(chan struct{})
			release := sync.OnceFunc(func() {
				close(releaseCalls)
			})
			t.Cleanup(release)

			h.onAuthenticated = func(context.Context,
				[]grpc.CallOption) error {

				callStarted <- struct{}{}
				<-releaseCalls

				return nil
			}

			callErrs := []<-chan error{
				h.start(t.Context(), testCase.stream),
				h.start(t.Context(), testCase.stream),
			}

			// Both transports must start before either is
			// released. A transport call holding the payment lock
			// would deadlock here.
			for range 2 {
				receiveTestValue(
					t, callStarted, "concurrent call",
				)
			}
			release()

			for _, callErr := range callErrs {
				err := receiveTestValue(
					t, callErr, "call result",
				)
				require.NoError(t, err)
			}
		})
	}
}

// TestInterceptorConcurrentPayment verifies that a unary call and stream share
// one new or pending token transition without requesting redundant challenges.
func TestInterceptorConcurrentPayment(t *testing.T) {
	testCases := []struct {
		name     string
		preimage *lntypes.Preimage
	}{
		{
			name: "new payment",
		},
		{
			name:     "pending payment",
			preimage: &zeroPreimage,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newInterceptorTestHarness(
				makeToken(testCase.preimage),
			)

			unauthenticatedCall := make(chan struct{}, 2)
			releaseInitialCall := make(chan struct{})
			release := sync.OnceFunc(func() {
				close(releaseInitialCall)
			})
			t.Cleanup(release)
			h.onUnauthenticated = func(context.Context,
				[]grpc.CallOption) error {

				unauthenticatedCall <- struct{}{}
				<-releaseInitialCall

				return nil
			}

			firstErr := h.start(t.Context(), false)

			// Block the first unauthenticated transport before it
			// returns the challenge, then start the stream
			// concurrently.
			receiveTestValue(
				t, unauthenticatedCall,
				"initial unauthenticated call",
			)

			// The initial unauthenticated transport must retain
			// the payment lock. This prevents another call from
			// obtaining a redundant challenge before the token
			// transition completes.
			if h.interceptor.paymentLock.TryLock() {
				h.interceptor.paymentLock.Unlock()
				t.Fatal(
					"unauthenticated transport " +
						"did not hold payment lock",
				)
			}

			secondErr := h.start(t.Context(), true)
			release()

			finishPayment := paymentFinisher(
				t, h.lnd, testCase.preimage != nil,
			)
			finishPayment()

			for _, callErr := range []<-chan error{
				firstErr, secondErr,
			} {
				err := receiveTestValue(
					t, callErr, "call result",
				)
				require.NoError(t, err)
			}

			// The queued stream must reuse the token instead of
			// obtaining a second challenge and initiating another
			// payment.
			require.EqualValues(
				t, 1, h.unauthenticatedCalls.Load(),
			)
			require.EqualValues(
				t, 2, h.authenticatedCalls.Load(),
			)
			token, err := h.store.CurrentToken()
			require.NoError(t, err)
			require.Equal(t, paidPreimage, token.Preimage)
			require.False(t, token.isPending())
			require.NoError(t, h.lnd.IsDone())
		})
	}
}

// TestInterceptorConcurrentPaidTokenRefresh verifies that concurrent calls
// rejected with a paid token share one replacement payment after it is removed.
func TestInterceptorConcurrentPaidTokenRefresh(t *testing.T) {
	// Give the rejected and replacement tokens distinct credentials
	// so stale retries cannot be accepted as successful refreshes.
	oldPreimage := lntypes.Preimage{9, 8, 7, 6, 5}
	oldToken := makeToken(&oldPreimage)
	h := newInterceptorTestHarness(oldToken)

	credentialMacaroon := func(token *Token) string {
		mac, err := token.PaidMacaroon()
		require.NoError(t, err)

		return hex.EncodeToString(serializeMac(mac))
	}
	oldCredential := credentialMacaroon(oldToken)
	replacementCredential := credentialMacaroon(
		makeToken(&paidPreimage),
	)
	require.NotEqual(t, oldCredential, replacementCredential)

	initialCalls := make(chan struct{}, 2)
	releaseInitialCalls := make(chan struct{})
	release := sync.OnceFunc(func() {
		close(releaseInitialCalls)
	})
	t.Cleanup(release)

	var (
		calls            atomic.Int32
		credentials      [4]string
		credentialErrors [4]error
	)
	h.onAuthenticated = func(ctx context.Context,
		opts []grpc.CallOption) error {

		call := calls.Add(1)
		if call > int32(len(credentials)) {
			return fmt.Errorf(
				"unexpected authenticated call %d", call,
			)
		}

		md, authenticated, err := requestMetadata(ctx, opts)
		idx := int(call - 1)
		if err == nil {
			if !authenticated {
				err = errors.New("missing L402 credential")
			} else {
				credentials[idx] = md["macaroon"]
			}
		}
		credentialErrors[idx] = err

		if call > 2 {
			return nil
		}

		initialCalls <- struct{}{}
		<-releaseInitialCalls

		return status.Error(GRPCErrCode, GRPCErrMessage)
	}

	callErrs := []<-chan error{
		h.start(t.Context(), false),
		h.start(t.Context(), true),
	}

	// Ensure both calls loaded the old token before simulating its removal.
	for range 2 {
		receiveTestValue(t, initialCalls, "rejected paid-token call")
	}
	if !h.interceptor.paymentLock.TryLock() {
		t.Fatal("paid-token transports retained payment lock")
	}
	h.store.token = nil
	h.interceptor.paymentLock.Unlock()
	release()

	// The first refresh pays while the second waits, then reuses its
	// token.
	payment := receiveTestPaymentRequest(
		t, h.lnd, "replacement token payment",
	)
	sendTestValue(
		t, payment.Done, lndclient.PaymentResult{
			Preimage: paidPreimage,
			PaidAmt:  123,
			PaidFee:  345,
		}, "replacement payment result",
	)

	for _, callErr := range callErrs {
		err := receiveTestValue(t, callErr, "refreshed call result")
		require.NoError(t, err)
	}

	// Both initial attempts must use the rejected credential, while both
	// retries must use the single replacement purchased above.
	expectedCredentials := [4]string{
		oldCredential, oldCredential,
		replacementCredential, replacementCredential,
	}
	for idx, expectedCredential := range expectedCredentials {
		require.NoError(t, credentialErrors[idx])
		require.Equal(t, expectedCredential, credentials[idx])
	}

	require.EqualValues(t, 4, h.authenticatedCalls.Load())
	require.EqualValues(t, 0, h.unauthenticatedCalls.Load())
	token, err := h.store.CurrentToken()
	require.NoError(t, err)
	require.Equal(t, paidPreimage, token.Preimage)
	require.False(t, token.isPending())
	require.NoError(t, h.lnd.IsDone())
}

// TestInterceptorPaymentErrorUnlocks verifies that a failed payment releases
// the lock so a queued caller can resume the persisted pending payment.
func TestInterceptorPaymentErrorUnlocks(t *testing.T) {
	h := newInterceptorTestHarness(nil)
	firstErr := h.start(t.Context(), false)

	payment := receiveTestPaymentRequest(t, h.lnd, "token payment")

	secondErr := h.start(t.Context(), true)

	// Fail the first payment after the second call starts. The
	// pending token remains available for the queued call to track.
	paymentErr := errors.New("payment failed")
	payment.Done <- lndclient.PaymentResult{Err: paymentErr}
	err := receiveTestValue(t, firstErr, "failed payment result")
	require.ErrorIs(t, err, paymentErr)

	track := receiveTestTrackRequest(t, h.lnd, "payment tracking")
	sendTestValue(
		t, track.Updates, lndclient.PaymentStatus{
			State:    lnrpc.Payment_SUCCEEDED,
			Preimage: paidPreimage,
		}, "tracked payment result",
	)

	err = receiveTestValue(t, secondErr, "queued call")
	require.NoError(t, err)
	require.NoError(t, h.lnd.IsDone())
}

// TestInterceptorCanceledInitialChallenge verifies that cancellation after an
// initial challenge prevents token storage and payment.
func TestInterceptorCanceledInitialChallenge(t *testing.T) {
	h := newInterceptorTestHarness(nil)
	ctx, cancel := context.WithCancel(t.Context())
	h.onUnauthenticated = func(context.Context, []grpc.CallOption) error {
		// Cancel before returning the challenge to exercise the guard
		// between challenge receipt and payment handling.
		cancel()

		return nil
	}

	callErr := h.start(ctx, false)

	// A regression in the cancellation guard must report the
	// unexpected LND request instead of blocking the test on the
	// mock's unbuffered channel.
	err := receiveCallWithoutPayment(t, callErr, h.lnd, "canceled call")

	require.Equal(t, codes.Canceled, status.Code(err))
	_, err = h.store.CurrentToken()
	require.ErrorIs(t, err, ErrNoToken)
	require.NoError(t, h.lnd.IsDone())
}

// TestInterceptorCanceledPaymentWaiter verifies that a caller canceled while
// waiting for payment serialization cannot update the store or call lnd.
func TestInterceptorCanceledPaymentWaiter(t *testing.T) {
	h := newInterceptorTestHarness(makeToken(&paidPreimage))
	callStarted := make(chan struct{})
	releaseCall := make(chan struct{})
	release := sync.OnceFunc(func() {
		close(releaseCall)
	})
	t.Cleanup(release)
	h.onAuthenticated = func(context.Context, []grpc.CallOption) error {
		close(callStarted)
		<-releaseCall

		return status.Error(GRPCErrCode, GRPCErrMessage)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	callErr := h.start(ctx, false)

	receiveTestValue(t, callStarted, "authenticated call")
	if !h.interceptor.paymentLock.TryLock() {
		t.Fatal("authenticated transport retained payment lock")
	}
	h.store.token = nil
	release()
	cancel()
	h.interceptor.paymentLock.Unlock()

	// The context check after lock acquisition must run before the
	// refreshed empty store can trigger payment handling.
	err := receiveCallWithoutPayment(t, callErr, h.lnd, "canceled call")
	require.Equal(t, codes.Canceled, status.Code(err))
	_, err = h.store.CurrentToken()
	require.ErrorIs(t, err, ErrNoToken)
	require.NoError(t, h.lnd.IsDone())
}

// TestInterceptorRefreshesPaidToken verifies that payment retry reloads the
// token, preserves caller options and replaces the old L402 credentials.
func TestInterceptorRefreshesPaidToken(t *testing.T) {
	secondPreimage := lntypes.Preimage{5, 4, 3, 2, 1}
	tokens := []*Token{
		makeToken(&paidPreimage),
		makeToken(&secondPreimage),
	}
	h := newInterceptorTestHarness(tokens[0])
	callerMacaroon, err := makeToken(
		&lntypes.Preimage{9, 8, 7, 6, 5},
	).PaidMacaroon()
	require.NoError(t, err)
	callerCredential := NewMacaroonCredential(callerMacaroon, false)

	var reads int
	h.store.currentTokenFn = func() (*Token, error) {
		if reads == len(tokens) {
			return nil, errors.New("unexpected token read")
		}

		token := tokens[reads]
		reads++

		return token, nil
	}

	var macaroons []string
	h.onAuthenticated = func(ctx context.Context,
		opts []grpc.CallOption) error {

		md, ok, err := requestMetadata(ctx, opts)
		require.NoError(t, err)
		require.True(t, ok)

		var (
			credentialCount int
			waitForReady    bool
		)
		for _, opt := range opts {
			switch opt := opt.(type) {
			case grpc.PerRPCCredsCallOption:
				credentialCount++

			case grpc.FailFastCallOption:
				waitForReady = !opt.FailFast
			}
		}
		// The caller credential must remain while exactly one
		// current L402 credential stays last and therefore
		// effective.
		require.Equal(t, 2, credentialCount)
		require.True(t, waitForReady)
		macaroons = append(macaroons, md["macaroon"])

		if len(macaroons) == 1 {
			return status.Error(GRPCErrCode, GRPCErrMessage)
		}

		return nil
	}

	err = h.call(
		t.Context(), false,
		grpc.PerRPCCredentials(callerCredential),
		grpc.WaitForReady(true),
	)
	require.NoError(t, err)
	require.Len(t, macaroons, 2)
	require.NotEqual(t, macaroons[0], macaroons[1])
	require.Equal(t, 2, reads)
}

// TestInterceptorRefreshError verifies that a failed token reload aborts the
// retry and returns the store error without calling the transport again.
func TestInterceptorRefreshError(t *testing.T) {
	storeErr := errors.New("store unavailable")
	h := newInterceptorTestHarness(makeToken(&paidPreimage))
	var reads int
	h.store.currentTokenFn = func() (*Token, error) {
		reads++
		if reads == 1 {
			return makeToken(&paidPreimage), nil
		}

		return nil, storeErr
	}
	h.onAuthenticated = func(context.Context, []grpc.CallOption) error {
		return status.Error(GRPCErrCode, GRPCErrMessage)
	}

	err := h.call(t.Context(), false)
	require.ErrorContains(t, err, storeErr.Error())
	require.Equal(t, 2, reads)
	require.EqualValues(t, 1, h.authenticatedCalls.Load())
}

// TestInterceptorPaymentLockPanic verifies that panic unwinding releases the
// payment lock for later calls.
func TestInterceptorPaymentLockPanic(t *testing.T) {
	panicErr := errors.New("store panic")
	h := newInterceptorTestHarness(makeToken(&paidPreimage))
	var reads int
	h.store.currentTokenFn = func() (*Token, error) {
		reads++
		if reads == 1 {
			panic(panicErr)
		}

		return makeToken(&paidPreimage), nil
	}

	func() {
		defer func() {
			require.Equal(t, panicErr, recover())
		}()

		_ = h.call(t.Context(), false)
	}()

	callErr := h.start(t.Context(), false)
	err := receiveTestValue(t, callErr, "call after panic")
	require.NoError(t, err)
}

// TestInterceptorStreamRetryResult verifies that a stream retry returns only
// the final successful stream and clears an earlier stream on payment failure.
func TestInterceptorStreamRetryResult(t *testing.T) {
	paymentErr := errors.New("payment failed")
	testCases := []struct {
		name          string
		paymentErr    error
		expectSuccess bool
	}{
		{
			name:          "successful retry",
			expectSuccess: true,
		},
		{
			name:       "payment failure",
			paymentErr: paymentErr,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			h := newInterceptorTestHarness(nil)
			initialStream := &interceptorTestStream{}
			finalStream := &interceptorTestStream{}
			var calls int

			streamer := func(_ context.Context, _ *grpc.StreamDesc,
				_ *grpc.ClientConn, _ string,
				opts ...grpc.CallOption) (
				grpc.ClientStream, error) {

				calls++
				if calls == 1 {
					// Deliberately return a non-nil
					// rejected stream to ensure it cannot
					// escape as the result.
					setAuthHeaders(
						opts,
						makeAuthHeaders(
							testMacBytes, true,
						),
					)

					return initialStream, status.Error(
						GRPCErrCode, GRPCErrMessage,
					)
				}

				return finalStream, nil
			}

			resultChan := make(chan struct {
				stream grpc.ClientStream
				err    error
			}, 1)
			go func() {
				stream, err := h.interceptor.StreamInterceptor(
					t.Context(), nil, nil, "test", streamer,
				)
				resultChan <- struct {
					stream grpc.ClientStream
					err    error
				}{
					stream: stream,
					err:    err,
				}
			}()

			payment := receiveTestPaymentRequest(
				t, h.lnd, "token payment",
			)
			sendTestValue(
				t, payment.Done, lndclient.PaymentResult{
					Err:      testCase.paymentErr,
					Preimage: paidPreimage,
					PaidAmt:  123,
					PaidFee:  345,
				}, "payment result",
			)

			result := receiveTestValue(
				t, resultChan, "stream result",
			)
			if testCase.expectSuccess {
				require.NoError(t, result.err)
				require.Same(t, finalStream, result.stream)
				require.Equal(t, 2, calls)

				return
			}

			require.ErrorIs(t, result.err, paymentErr)
			require.Nil(t, result.stream)
			require.Equal(t, 1, calls)
		})
	}
}

func testInterceptor(t *testing.T, tc interceptTestCase, addL402 bool,
	intercept func() error) {

	// Initial condition and simulated backend call.
	store.token = makeToken(tc.initialPreimage)
	tc.resetCb(addL402)
	numBackendCalls = 0
	backendWg.Add(1)
	overallWg.Add(1)
	interceptErr := make(chan error, 1)
	go func() {
		defer overallWg.Done()
		interceptErr <- intercept()
	}()

	backendWg.Wait()
	if tc.expectMacaroonCall1 {
		require.Len(t, callMD, 1)

		// We expect the sent macaroon to be larger than the bare
		// macaroon as it should contain the preimage now.
		require.Greater(t, len(callMD["macaroon"]), len(testMacHex))
	}

	// Do we expect more calls? Then make sure we will wait for completion
	// before checking any results.
	if tc.expectBackendCalls > 1 {
		backendWg.Add(1)
	}

	// Simulate payment related calls to lnd, if there are any expected.
	if tc.expectLndCall {
		select {
		case payment := <-lnd.SendPaymentChannel:
			tc.sendPaymentCb(t, payment)

		case track := <-lnd.TrackPaymentChannel:
			tc.trackPaymentCb(t, track)

		case <-time.After(testTimeout):
			t.Fatalf("[%s]: no payment request received", tc.name)
		}
	}
	if tc.expectSecondLndCall {
		select {
		case payment := <-lnd.SendPaymentChannel:
			tc.sendPaymentCb(t, payment)

		case track := <-lnd.TrackPaymentChannel:
			tc.trackPaymentCb(t, track)

		case <-time.After(testTimeout):
			t.Fatalf("[%s]: no payment request received", tc.name)
		}
	}
	backendWg.Wait()
	overallWg.Wait()

	// Now that the intercept call must have completed, we can inspect the
	// error message.
	err := <-interceptErr
	if tc.expectInterceptErr == "" {
		require.NoError(t, err)
	} else {
		require.Error(t, err)
		require.Contains(t, err.Error(), tc.expectInterceptErr)
	}

	storeToken, err := store.CurrentToken()
	if tc.expectToken {
		require.NoError(t, err)
		require.Equal(t, paidPreimage, storeToken.Preimage)
	} else {
		require.Equal(t, ErrNoToken, err)
	}
	if tc.expectMacaroonCall2 {
		require.Len(t, callMD, 1)

		// We expect the sent macaroon to be larger than the bare
		// macaroon as it should contain the preimage now.
		require.Greater(t, len(callMD["macaroon"]), len(testMacHex))
	}
	require.Equal(t, tc.expectBackendCalls, numBackendCalls)
}

// paymentFinisher waits for a new or pending payment request and returns a
// function that completes it successfully.
func paymentFinisher(t *testing.T, lnd *test.LndMockServices,
	pending bool) func() {

	t.Helper()

	if pending {
		track := receiveTestTrackRequest(t, lnd, "payment tracking")

		return func() {
			sendTestValue(
				t, track.Updates, lndclient.PaymentStatus{
					State:    lnrpc.Payment_SUCCEEDED,
					Preimage: paidPreimage,
				}, "tracked payment result",
			)
		}
	}

	payment := receiveTestPaymentRequest(t, lnd, "token payment")

	return func() {
		sendTestValue(
			t, payment.Done, lndclient.PaymentResult{
				Preimage: paidPreimage,
				PaidAmt:  123,
				PaidFee:  345,
			}, "payment result",
		)
	}
}

// receiveTestPaymentRequest receives a payment request and fails if tracking
// was requested instead or the operation times out.
func receiveTestPaymentRequest(t *testing.T, lnd *test.LndMockServices,
	name string) test.PaymentChannelMessage {

	t.Helper()

	select {
	case payment := <-lnd.SendPaymentChannel:
		return payment

	case track := <-lnd.TrackPaymentChannel:
		// Release the synchronous mock before failing the test.
		close(track.Errors)
		t.Fatalf("received payment tracking while waiting for %s", name)

	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", name)
	}

	return test.PaymentChannelMessage{}
}

// receiveTestTrackRequest receives a tracking request and fails if a payment
// was requested instead or the operation times out.
func receiveTestTrackRequest(t *testing.T, lnd *test.LndMockServices,
	name string) test.TrackPaymentMessage {

	t.Helper()

	select {
	case track := <-lnd.TrackPaymentChannel:
		return track

	case payment := <-lnd.SendPaymentChannel:
		// Release the synchronous mock before failing the test.
		payment.Done <- lndclient.PaymentResult{
			Err: errors.New("unexpected token payment"),
		}
		t.Fatalf("received token payment while waiting for %s", name)

	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", name)
	}

	return test.TrackPaymentMessage{}
}

// receiveCallWithoutPayment receives a call result and fails if the call tries
// to pay or track a token before returning.
func receiveCallWithoutPayment(t *testing.T, callErr <-chan error,
	lnd *test.LndMockServices, name string) error {

	t.Helper()

	select {
	case err := <-callErr:
		return err

	case payment := <-lnd.SendPaymentChannel:
		// Release the synchronous mock before failing the test.
		payment.Done <- lndclient.PaymentResult{
			Err: errors.New("unexpected token payment"),
		}
		t.Fatalf("received token payment while waiting for %s", name)

	case track := <-lnd.TrackPaymentChannel:
		// Release the synchronous mock before failing the test.
		close(track.Errors)
		t.Fatalf("received payment tracking while waiting for %s", name)

	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", name)
	}

	return nil
}

// sendTestValue sends a test value or fails when the operation times out.
func sendTestValue[T any](t *testing.T, values chan<- T, value T,
	name string) {

	t.Helper()

	select {
	case values <- value:

	case <-time.After(testTimeout):
		t.Fatalf("timed out sending %s", name)
	}
}

// receiveTestValue receives a test value or fails when the operation times
// out.
func receiveTestValue[T any](t *testing.T, values <-chan T, name string) T {
	t.Helper()

	select {
	case value := <-values:
		return value

	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", name)
		var zero T

		return zero
	}
}

// requestMetadata returns the request metadata from the effective per-RPC
// credential and reports whether credentials were found. The last credential
// option wins, matching gRPC call option processing.
func requestMetadata(ctx context.Context, opts []grpc.CallOption) (
	map[string]string, bool, error) {

	for idx := len(opts) - 1; idx >= 0; idx-- {
		opt := opts[idx]
		creds, ok := opt.(grpc.PerRPCCredsCallOption)
		if !ok {
			continue
		}

		md, err := creds.Creds.GetRequestMetadata(ctx)

		return md, true, err
	}

	return nil, false, nil
}

// setAuthHeaders adds a payment challenge to every trailer call option.
func setAuthHeaders(opts []grpc.CallOption, authHeaders []string) {
	for _, opt := range opts {
		trailer, ok := opt.(grpc.TrailerCallOption)
		if ok {
			trailer.TrailerAddr.Set(AuthHeader, authHeaders...)
		}
	}
}

func makeToken(preimage *lntypes.Preimage) *Token {
	if preimage == nil {
		return nil
	}
	return &Token{
		Preimage: *preimage,
		baseMac:  testMac,
	}
}

func makeMac() *macaroon.Macaroon {
	dummyMac, err := macaroon.New(
		[]byte("aabbccddeeff00112233445566778899"), []byte("AA=="),
		"LSAT", macaroon.LatestVersion,
	)
	if err != nil {
		panic(fmt.Errorf("unable to create macaroon: %v", err))
	}
	return dummyMac
}

func serializeMac(mac *macaroon.Macaroon) []byte {
	macBytes, err := mac.MarshalBinary()
	if err != nil {
		panic(fmt.Errorf("unable to serialize macaroon: %v", err))
	}
	return macBytes
}

func makeAuthHeaders(macBytes []byte, addL402 bool) []string {
	// Testnet invoice over 500 sats.
	invoice := "lntb5u1p0pskpmpp5jzw9xvdast2g5lm5tswq6n64t2epe3f4xav43dyd" +
		"239qr8h3yllqdqqcqzpgsp5m8sfjqgugthk66q3tr4gsqr5rh740jrq9x4l0" +
		"kvj5e77nmwqvpnq9qy9qsq72afzu7sfuppzqg3q2pn49hlh66rv7w60h2rua" +
		"hx857g94s066yzxcjn4yccqc79779sd232v9ewluvu0tmusvht6r99rld8xs" +
		"k287cpyac79r"
	str := fmt.Sprintf("macaroon=\"%s\", invoice=\"%s\"",
		base64.StdEncoding.EncodeToString(macBytes), invoice)
	values := []string{"LSAT " + str}
	if addL402 {
		values = append(values, "L402 "+str)
	}

	return values
}
