package aperture

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"github.com/lightningnetwork/lnd/build"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"github.com/lightningnetwork/lnd/signal"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
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
	conn, err := grpc.Dial(testApertureAddress, grpc.WithInsecure())
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
	testMessage2 := append(testMessage, []byte("test")...)
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
	conn, err := grpc.Dial(testApertureAddress, grpc.WithInsecure())
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
		Etcd: &EtcdConfig{},
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
