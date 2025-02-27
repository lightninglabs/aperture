package aperture

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/signal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

var (
	testApertureAddress = "localhost:8082"
	testSID             = streamID{1, 2, 3}
	testStreamDesc      = &hashmailrpc.CipherBoxDesc{
		StreamId: testSID[:],
	}
	testMessage          = []byte("I'm a message!")
	apertureStartTimeout = 3 * time.Second
)

func init() {
	logWriter := build.NewRotatingLogWriter()
	SetupLoggers(logWriter, signal.Interceptor{})
	_ = build.ParseAndSetDebugLevels("trace,PRXY=warn", logWriter)
}

func TestHashMailServerReturnStream(t *testing.T) {
	ctxb := context.Background()

	setupAperture(t)

	// Create a client and connect it to the server.
	conn, err := grpc.Dial(
		testApertureAddress, grpc.WithTransportCredentials(
			insecure.NewCredentials(),
		),
	)
	require.NoError(t, err)
	client := hashmailrpc.NewHashMailClient(conn)

	// We'll create a new cipher box that we're going to subscribe to
	// multiple times to check disconnecting returns the read stream.
	resp, err := client.NewCipherBox(ctxb, &hashmailrpc.CipherBoxAuth{
		Auth: &hashmailrpc.CipherBoxAuth_LndAuth{},
		Desc: testStreamDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetSuccess())

	// First we make sure there is something to read on the other end of
	// that stream by writing something to it.
	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()

	writeStream, err := client.SendStream(sendCtx)
	require.NoError(t, err)
	err = writeStream.Send(&hashmailrpc.CipherBox{
		Desc: testStreamDesc,
		Msg:  testMessage,
	})
	require.NoError(t, err)

	// We need to wait a bit to make sure the message is really sent.
	time.Sleep(100 * time.Millisecond)

	// Connect, wait for the stream to be ready, read something, then
	// disconnect immediately.
	msg, err := readMsgFromStream(t, client)
	require.NoError(t, err)
	require.Equal(t, testMessage, msg.Msg)

	// Make sure we can connect again immediately and try to read something.
	// There is no message to read before we cancel the request so we expect
	// an EOF error to be returned upon connection close/context cancel.
	_, err = readMsgFromStream(t, client)
	require.Error(t, err)
	require.Contains(t, err.Error(), "context canceled")

	// Send then receive yet another message to make sure the stream is
	// still operational.
	testMessage2 := append(testMessage, []byte("test")...) //nolint:gocritic
	err = writeStream.Send(&hashmailrpc.CipherBox{
		Desc: testStreamDesc,
		Msg:  testMessage2,
	})
	require.NoError(t, err)

	// We need to wait a bit to make sure the message is really sent.
	time.Sleep(100 * time.Millisecond)

	msg, err = readMsgFromStream(t, client)
	require.NoError(t, err)
	require.Equal(t, testMessage2, msg.Msg)

	// Clean up the stream now.
	_, err = client.DelCipherBox(ctxb, &hashmailrpc.CipherBoxAuth{
		Auth: &hashmailrpc.CipherBoxAuth_LndAuth{},
		Desc: testStreamDesc,
	})
	require.NoError(t, err)
}

func TestHashMailServerLargeMessage(t *testing.T) {
	ctxb := context.Background()

	setupAperture(t)

	// Create a client and connect it to the server.
	conn, err := grpc.Dial(
		testApertureAddress, grpc.WithTransportCredentials(
			insecure.NewCredentials(),
		),
	)
	require.NoError(t, err)
	client := hashmailrpc.NewHashMailClient(conn)

	// We'll create a new cipher box that we're going to subscribe to
	// multiple times to check disconnecting returns the read stream.
	resp, err := client.NewCipherBox(ctxb, &hashmailrpc.CipherBoxAuth{
		Auth: &hashmailrpc.CipherBoxAuth_LndAuth{},
		Desc: testStreamDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetSuccess())

	// Let's create a long message and try to send it.
	var largeMessage [512 * DefaultBufSize]byte
	_, err = rand.Read(largeMessage[:])
	require.NoError(t, err)

	sendCtx, sendCancel := context.WithCancel(context.Background())
	defer sendCancel()

	writeStream, err := client.SendStream(sendCtx)
	require.NoError(t, err)
	err = writeStream.Send(&hashmailrpc.CipherBox{
		Desc: testStreamDesc,
		Msg:  largeMessage[:],
	})
	require.NoError(t, err)

	// We need to wait a bit to make sure the message is really sent.
	time.Sleep(100 * time.Millisecond)

	// Connect, wait for the stream to be ready, read something, then
	// disconnect immediately.
	msg, err := readMsgFromStream(t, client)
	require.NoError(t, err)
	require.Equal(t, largeMessage[:], msg.Msg)
}

func setupAperture(t *testing.T) {
	apertureCfg := &Config{
		Insecure:   true,
		ListenAddr: testApertureAddress,
		Authenticator: &AuthConfig{
			Disable: true,
		},
		DatabaseBackend: "etcd",
		Etcd:            &EtcdConfig{},
		HashMail: &HashMailConfig{
			Enabled:               true,
			MessageRate:           time.Millisecond,
			MessageBurstAllowance: math.MaxUint32,
		},
		Prometheus: &PrometheusConfig{},
		Tor:        &TorConfig{},
	}
	aperture := NewAperture(apertureCfg)
	errChan := make(chan error)
	require.NoError(t, aperture.Start(errChan))

	// Any error while starting?
	select {
	case err := <-errChan:
		t.Fatalf("error starting aperture: %v", err)
	default:
	}

	err := wait.NoError(func() error {
		apertureAddr := fmt.Sprintf("http://%s/dummy",
			testApertureAddress)

		resp, err := http.Get(apertureAddr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			return fmt.Errorf("invalid status: %d", resp.StatusCode)
		}

		return nil
	}, apertureStartTimeout)
	require.NoError(t, err)
}

func readMsgFromStream(t *testing.T,
	client hashmailrpc.HashMailClient) (*hashmailrpc.CipherBox, error) {

	ctxc, cancel := context.WithCancel(context.Background())
	readStream, err := client.RecvStream(ctxc, testStreamDesc)
	require.NoError(t, err)

	// Wait a bit again to make sure the request is actually sent before our
	// context is canceled already again.
	time.Sleep(100 * time.Millisecond)

	// We'll start a read on the stream in the background.
	var (
		goroutineStarted = make(chan struct{})
		resultChan       = make(chan *hashmailrpc.CipherBox)
		errChan          = make(chan error)
	)
	go func() {
		close(goroutineStarted)
		box, err := readStream.Recv()
		if err != nil {
			errChan <- err
			return
		}
		resultChan <- box
	}()

	// Give the goroutine a chance to actually run, so block the main thread
	// until it did.
	<-goroutineStarted

	time.Sleep(200 * time.Millisecond)

	// Now close and cancel the stream to make sure the server can clean it
	// up and release it.
	require.NoError(t, readStream.CloseSend())
	cancel()

	// Interpret the result.
	select {
	case err := <-errChan:
		return nil, err

	case box := <-resultChan:
		return box, nil
	}
}

type statusState struct {
	readOccupied  bool
	writeOccupied bool
}

// TestStaleMailboxCleanup tests that the streamStatus behaves as expected and
// that it correctly tears down a mailbox if it becomes stale.
func TestStaleMailboxCleanup(t *testing.T) {
	tests := []struct {
		name                      string
		staleTimeout              time.Duration
		senderConnected           statusState
		readerConnected           statusState
		senderDisconnected        statusState
		expectStaleMailboxRemoval bool
	}{
		{
			name:         "tear down stale mailbox",
			staleTimeout: 500 * time.Millisecond,
			senderConnected: statusState{
				writeOccupied: true,
			},
			readerConnected: statusState{
				writeOccupied: true,
				readOccupied:  true,
			},
			senderDisconnected: statusState{
				writeOccupied: false,
				readOccupied:  true,
			},
			expectStaleMailboxRemoval: true,
		},
		{
			name:         "dont tear down stale mailbox",
			staleTimeout: -1,
			senderConnected: statusState{
				writeOccupied: false,
				readOccupied:  false,
			},
			readerConnected: statusState{
				writeOccupied: false,
				readOccupied:  false,
			},
			senderDisconnected: statusState{
				writeOccupied: false,
				readOccupied:  false,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()

			// Set up a new hashmail server.
			hm := newHashMailHarness(t, hashMailServerConfig{
				staleTimeout: test.staleTimeout,
			})

			// Create two clients of the hashmail server.
			conn1 := hm.newClientConn()
			conn2 := hm.newClientConn()

			client1 := hashmailrpc.NewHashMailClient(conn1)
			client2 := hashmailrpc.NewHashMailClient(conn2)

			// Let client 1 create a mailbox on the server.
			resp, err := client1.NewCipherBox(
				ctx, &hashmailrpc.CipherBoxAuth{
					Auth: &hashmailrpc.CipherBoxAuth_LndAuth{},
					Desc: testStreamDesc,
				},
			)
			require.NoError(t, err)
			require.NotNil(t, resp.GetSuccess())

			// Assert that neither of the mailbox streams are
			// occupied to start with.
			hm.assertStreamsOccupied(statusState{
				readOccupied:  false,
				writeOccupied: false,
			})

			// Let client 1 take the send-stream and write to it.
			err = sendToStream(client1)
			require.NoError(t, err)

			hm.assertStreamsOccupied(test.senderConnected)

			// Let client 2 take the read stream and receive from
			// it.
			err = recvFromStream(client2)
			require.NoError(t, err)

			hm.assertStreamsOccupied(test.readerConnected)

			// Ensure that attempting to take the read stream and
			// receive from it while it is currently occupied will
			// result in an error.
			err = recvFromStream(client2)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "read stream occupied")

			hm.assertStreamsOccupied(test.readerConnected)

			// Disconnect client 1. This should release the
			// send-stream.
			require.NoError(t, conn1.Close())
			hm.assertStreamsOccupied(test.senderDisconnected)

			// Disconnect client 1. This should release the
			// read-stream.
			require.NoError(t, conn2.Close())

			// Assert that neither of the streams are occupied.
			hm.assertStreamsOccupied(statusState{
				readOccupied:  false,
				writeOccupied: false,
			})

			// Assert that the stream is torn down.
			hm.assertStreamExists(!test.expectStaleMailboxRemoval)
		})
	}
}

// hashMailHarness is a test harness that spins up a hashmail server for
// testing purposes.
type hashMailHarness struct {
	t      *testing.T
	server *hashMailServer
	lis    *bufconn.Listener
}

// newHashMailHarness spins up a new hashmail server and serves it on a bufconn
// listener.
func newHashMailHarness(t *testing.T,
	cfg hashMailServerConfig) *hashMailHarness {

	hm := newHashMailServer(cfg)

	lis := bufconn.Listen(1024 * 1024)
	hashMailGRPC := grpc.NewServer()
	t.Cleanup(hashMailGRPC.Stop)

	hashmailrpc.RegisterHashMailServer(hashMailGRPC, hm)
	go func() {
		require.NoError(t, hashMailGRPC.Serve(lis))
	}()

	return &hashMailHarness{
		t:      t,
		server: hm,
		lis:    lis,
	}
}

// newClientConn creates a new client of the hashMailHarness server.
func (h *hashMailHarness) newClientConn() *grpc.ClientConn {
	conn, err := grpc.Dial("bufnet", grpc.WithContextDialer(
		func(ctx context.Context, s string) (net.Conn, error) {
			return h.lis.Dial()
		}), grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(h.t, err)
	h.t.Cleanup(func() {
		_ = conn.Close()
	})

	return conn
}

// assertStreamOccupied checks that the current state of the stream's read and
// writes streams are the same as the expected state.
func (h *hashMailHarness) assertStreamsOccupied(state statusState) {
	err := wait.Predicate(func() bool {
		h.server.Lock()
		defer h.server.Unlock()

		stream, ok := h.server.streams[testSID]
		if !ok {
			return false
		}

		stream.status.Lock()
		defer stream.status.Unlock()

		if stream.status.readStreamOccupied != state.readOccupied {
			return false
		}

		return stream.status.writeStreamOccupied == state.writeOccupied

	}, time.Second)
	require.NoError(h.t, err)
}

// assertStreamExists ensures that the test stream does or does not exist
// depending on the value of the boolean passed in.
func (h *hashMailHarness) assertStreamExists(exists bool) {
	err := wait.Predicate(func() bool {
		h.server.Lock()
		defer h.server.Unlock()

		_, ok := h.server.streams[testSID]
		return ok == exists

	}, time.Second)
	require.NoError(h.t, err)
}

// sendToStream is a helper function that attempts to send dummy data to the
// test stream using the given client.
func sendToStream(client hashmailrpc.HashMailClient) error {
	writeStream, err := client.SendStream(context.Background())
	if err != nil {
		return err
	}

	return writeStream.Send(&hashmailrpc.CipherBox{
		Desc: testStreamDesc,
		Msg:  testMessage,
	})
}

// recvFromStream is a helper function that attempts to receive dummy data from
// the test stream using the given client.
func recvFromStream(client hashmailrpc.HashMailClient) error {
	readStream, err := client.RecvStream(
		context.Background(), testStreamDesc,
	)
	if err != nil {
		return err
	}

	recvChan := make(chan *hashmailrpc.CipherBox)
	errChan := make(chan error)
	go func() {
		box, err := readStream.Recv()
		if err != nil {
			errChan <- err
		}
		recvChan <- box
	}()

	select {
	case <-time.After(time.Second):
		return fmt.Errorf("timed out waiting to receive from receive " +
			"stream")

	case err := <-errChan:
		return err

	case <-recvChan:
	}

	return nil
}
