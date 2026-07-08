package lnc

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/cert"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gopkg.in/macaroon.v2"
)

// TestNewConnInitialRPCIsAuthenticated verifies the first RPC after newConn
// succeeds with authentication. This reproduces the behavior regression where
// newConn returned before auth data was initialized.
func TestNewConnInitialRPCIsAuthenticated(t *testing.T) {
	mailboxAddr := startTestHashMailServer(t)

	authMacHex, authPayload := testMacaroonPayload(t)
	passphraseEntropy := make([]byte, 32)
	for i := range passphraseEntropy {
		passphraseEntropy[i] = 1
	}

	serverStatic, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	serverConnData := mailbox.NewConnData(
		&keychain.PrivKeyECDH{PrivKey: serverStatic}, nil,
		passphraseEntropy, authPayload, nil, nil,
	)

	mailboxListener, err := mailbox.NewServer(
		mailboxAddr, serverConnData, nil,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: true,
		})),
	)
	require.NoError(t, err)

	lnServer := grpc.NewServer(
		grpc.Creds(mailbox.NewNoiseGrpcConn(serverConnData)),
	)
	lnrpc.RegisterLightningServer(lnServer, &testLightningServer{
		expectedMacHex: authMacHex,
	})
	t.Cleanup(lnServer.Stop)
	go func() {
		_ = lnServer.Serve(mailboxListener)
	}()

	clientStatic, err := btcec.NewPrivateKey()
	require.NoError(t, err)

	session := &Session{
		LocalStaticPrivKey: clientStatic,
		PassphraseEntropy:  passphraseEntropy,
		MailboxAddr:        mailboxAddr,
		DevServer:          true,
	}
	nodeConn := &NodeConn{
		store:   &testStore{},
		session: session,
	}

	conn, err := nodeConn.newConn(
		session,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
	})

	rpcCtx, rpcCancel := context.WithTimeout(nodeConn.CtxFunc(), 5*time.Second)
	defer rpcCancel()

	_, err = conn.client.ListInvoices(rpcCtx, &lnrpc.ListInvoiceRequest{
		NumMaxInvoices: 1,
	})
	require.NoError(t, err)
}

func startTestHashMailServer(t *testing.T) string {
	t.Helper()

	certBytes, keyBytes, err := cert.GenCertPair(
		"localhost", nil, nil, false, 24*time.Hour,
	)
	require.NoError(t, err)

	pair, err := tls.X509KeyPair(certBytes, keyBytes)
	require.NoError(t, err)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pair},
	})))
	hashmailrpc.RegisterHashMailServer(server, newTestHashMailServer())

	go func() {
		_ = server.Serve(listener)
	}()

	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	return listener.Addr().String()
}

func testMacaroonPayload(t *testing.T) (string, []byte) {
	t.Helper()

	mac, err := macaroon.New(
		[]byte("test-root-key"), []byte("id"), "test", macaroon.LatestVersion,
	)
	require.NoError(t, err)

	macBytes, err := mac.MarshalBinary()
	require.NoError(t, err)

	macHex := hex.EncodeToString(macBytes)
	return macHex, []byte(fmt.Sprintf("%s: %s", HeaderMacaroon, macHex))
}

type testLightningServer struct {
	lnrpc.UnimplementedLightningServer

	expectedMacHex string
}

func (s *testLightningServer) ListInvoices(ctx context.Context,
	_ *lnrpc.ListInvoiceRequest) (*lnrpc.ListInvoiceResponse, error) {

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	macVals := md.Get("macaroon")
	if len(macVals) == 0 || macVals[0] != s.expectedMacHex {
		return nil, status.Error(
			codes.Unauthenticated, "missing or invalid macaroon",
		)
	}

	return &lnrpc.ListInvoiceResponse{}, nil
}

type testStore struct{}

func (s *testStore) AddSession(context.Context, *Session) error {
	return nil
}

func (s *testStore) GetSession(context.Context, []byte) (*Session, error) {
	return nil, ErrSessionNotFound
}

func (s *testStore) SetRemotePubKey(context.Context, []byte, []byte) error {
	return nil
}

func (s *testStore) SetExpiry(context.Context, []byte, time.Time) error {
	return nil
}

type testHashMailServer struct {
	hashmailrpc.UnimplementedHashMailServer

	mu      sync.Mutex
	streams map[string]chan []byte
}

func newTestHashMailServer() *testHashMailServer {
	return &testHashMailServer{
		streams: make(map[string]chan []byte),
	}
}

func (s *testHashMailServer) NewCipherBox(_ context.Context,
	req *hashmailrpc.CipherBoxAuth) (*hashmailrpc.CipherInitResp, error) {

	if req == nil || req.Desc == nil {
		return nil, status.Error(codes.InvalidArgument, "missing stream desc")
	}

	id := string(req.Desc.StreamId)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.streams[id]; ok {
		return nil, status.Error(codes.AlreadyExists, "stream already exists")
	}

	s.streams[id] = make(chan []byte, 64)

	return &hashmailrpc.CipherInitResp{
		Resp: &hashmailrpc.CipherInitResp_Success{
			Success: &hashmailrpc.CipherSuccess{
				Desc: req.Desc,
			},
		},
	}, nil
}

func (s *testHashMailServer) DelCipherBox(_ context.Context,
	req *hashmailrpc.CipherBoxAuth) (*hashmailrpc.DelCipherBoxResp, error) {

	if req == nil || req.Desc == nil {
		return nil, status.Error(codes.InvalidArgument, "missing stream desc")
	}

	id := string(req.Desc.StreamId)

	s.mu.Lock()
	ch, ok := s.streams[id]
	if ok {
		delete(s.streams, id)
	}
	s.mu.Unlock()

	if !ok {
		return nil, status.Error(codes.NotFound, "stream not found")
	}

	close(ch)
	return &hashmailrpc.DelCipherBoxResp{}, nil
}

func (s *testHashMailServer) SendStream(
	stream hashmailrpc.HashMail_SendStreamServer) error {

	for {
		msg, err := stream.Recv()
		switch {
		case err == io.EOF:
			return stream.SendAndClose(&hashmailrpc.CipherBoxDesc{})

		case err != nil:
			return err
		}

		if msg == nil || msg.Desc == nil {
			return status.Error(codes.InvalidArgument, "missing stream desc")
		}

		s.mu.Lock()
		ch, ok := s.streams[string(msg.Desc.StreamId)]
		s.mu.Unlock()
		if !ok {
			return status.Error(codes.NotFound, "stream not found")
		}

		payload := append([]byte(nil), msg.Msg...)

		select {
		case ch <- payload:
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (s *testHashMailServer) RecvStream(desc *hashmailrpc.CipherBoxDesc,
	stream hashmailrpc.HashMail_RecvStreamServer) error {

	if desc == nil {
		return status.Error(codes.InvalidArgument, "missing stream desc")
	}

	s.mu.Lock()
	ch, ok := s.streams[string(desc.StreamId)]
	s.mu.Unlock()
	if !ok {
		return status.Error(codes.NotFound, "stream not found")
	}

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil
			}

			err := stream.Send(&hashmailrpc.CipherBox{
				Desc: desc,
				Msg:  msg,
			})
			if err != nil {
				return err
			}

		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}
