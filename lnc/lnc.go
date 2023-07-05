package lnc

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightninglabs/lightning-node-connect/mailbox"
	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/mwitkow/grpc-proxy/proxy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"gopkg.in/macaroon-bakery.v2/bakery/checkers"
	"gopkg.in/macaroon.v2"
)

var (
	// DefaultConnectionTimetout is the default timeout for a connection
	// attempt.
	DefaultConnectionTimetout = time.Second * 10

	// DefaultStoreTimetout is the default timeout for a db transaction.
	DefaultStoreTimetout = time.Second * 10
)

// HeaderMacaroon is the HTTP header field name that is used to send
// the macaroon.
const HeaderMacaroon = "Macaroon"

// conn is a connection to a remote LND node.
type conn struct {
	client     lnrpc.LightningClient
	grpcClient *grpc.ClientConn
	creds      credentials.PerRPCCredentials
	cancel     func()

	stop sync.Once
}

// Close closes the underlying gRPC connection.
func (c *conn) Close() error {
	var err error
	c.stop.Do(func() {
		err = c.grpcClient.Close()
		c.cancel()
	})

	return err
}

// NodeConn handles all the connection logic to a remote LND node using LNC.
type NodeConn struct {
	// store is the session store.
	store Store

	// session is the session that is currently open.
	session *Session

	// conn is the underlying connection to the remote node.
	conn *conn

	// macStr is the macaroon is used to authenticate the connection encoded
	// as a hex string.
	macStr string
}

// NewNodeConn creates a new NodeConn instance.
func NewNodeConn(session *Session, store Store) (*NodeConn, error) {
	ctxt, cancel := context.WithTimeout(
		context.Background(), DefaultStoreTimetout,
	)
	defer cancel()

	dbSession, err := store.GetSession(ctxt, session.PassphraseEntropy)
	switch {
	case errors.Is(err, ErrSessionNotFound):
		localStatic, err := btcec.NewPrivateKey()
		if err != nil {
			return nil, fmt.Errorf("unable to generate local "+
				"static key: %w", err)
		}

		session.LocalStaticPrivKey = localStatic

		err = store.AddSession(ctxt, session)
		if err != nil {
			return nil, fmt.Errorf("unable to add new session: %w",
				err)
		}

	case err != nil:
		return nil, fmt.Errorf("unable to get session(%v): %w",
			session.PassphraseEntropy, err)

	default:
		session = dbSession
	}

	nodeConn := &NodeConn{
		store:   store,
		session: session,
	}

	conn, err := nodeConn.newConn(session)
	if err != nil {
		return nil, err
	}

	nodeConn.conn = conn

	return nodeConn, nil
}

// CloseConn closes the connection with the remote node.
func (n *NodeConn) CloseConn() error {
	if n.conn == nil {
		return fmt.Errorf("connection not open")
	}

	err := n.conn.Close()
	if err != nil {
		return fmt.Errorf("unable to close connection: %w", err)
	}

	return nil
}

// Stop closes the connection with the remote node if it is open.
func (n *NodeConn) Stop() error {
	if n.conn != nil {
		return n.CloseConn()
	}

	return nil
}

// Client returns the gRPC client to the remote node.
func (n *NodeConn) Client() (lnrpc.LightningClient, error) {
	if n.conn == nil {
		return nil, fmt.Errorf("connection not open")
	}

	return n.conn.client, nil
}

// CtxFunc returns the context that needs to be used whenever the internal
// Client is used.
func (n *NodeConn) CtxFunc() context.Context {
	ctx := context.Background()
	return metadata.AppendToOutgoingContext(ctx, HeaderMacaroon, n.macStr)
}

// onRemoteStatic is called when the remote static key is received.
//
// NOTE: this function is a callback to be used by the mailbox package during
// the mailbox.NewConnData call.
func (n *NodeConn) onRemoteStatic(key *btcec.PublicKey) error {
	ctxt, cancel := context.WithTimeout(
		context.Background(), time.Second*10,
	)
	defer cancel()

	remoteKey := key.SerializeCompressed()

	err := n.store.SetRemotePubKey(
		ctxt, n.session.PassphraseEntropy, remoteKey,
	)
	if err != nil {
		log.Errorf("unable to set remote pub key for session(%x): %w",
			n.session.PassphraseEntropy, err)
	}

	return err
}

// onAuthData is called when the auth data is received.
//
// NOTE: this function is a callback to be used by the mailbox package during
// the mailbox.NewConnData call.
func (n *NodeConn) onAuthData(data []byte) error {
	mac, err := extractMacaroon(data)
	if err != nil {
		log.Errorf("unable to extract macaroon for session(%x): %w",
			n.session.PassphraseEntropy, err)

		return err
	}

	macBytes, err := mac.MarshalBinary()
	if err != nil {
		log.Errorf("unable to marshal macaroon for session(%x): %w",
			n.session.PassphraseEntropy, err)

		return err
	}

	// TODO(positiveblue): check that the macaroon has all the needed
	// permissions.
	n.macStr = hex.EncodeToString(macBytes)

	// If we already know the expiry time for this session there is no need
	// to parse the macaroon to obtain it.
	if n.session.Expiry != nil {
		return nil
	}

	// If the macaroon does not contain an expiry time there is nothing to
	// do.
	expiry, found := checkers.ExpiryTime(nil, mac.Caveats())
	if !found {
		return nil
	}

	// We always store time in the db in UTC.
	expiry = expiry.UTC()

	// When we store the expiry time in the db we lose the precision to
	// microseconds, but we can store the correct one here.
	n.session.Expiry = &expiry

	ctxb := context.Background()
	err = n.store.SetExpiry(ctxb, n.session.PassphraseEntropy, expiry)
	if err != nil {
		log.Errorf("unable to set expiry for session(%x): %w",
			n.session.PassphraseEntropy, err)
	}

	return nil
}

// newConn creates an LNC connection.
func (n *NodeConn) newConn(session *Session, opts ...grpc.DialOption) (*conn,
	error) {

	localKey := &keychain.PrivKeyECDH{PrivKey: session.LocalStaticPrivKey}

	// remoteKey can be nil if this is the first time the session is used.
	remoteKey := session.RemoteStaticPubKey
	entropy := session.PassphraseEntropy

	connData := mailbox.NewConnData(
		localKey, remoteKey, entropy, nil, n.onRemoteStatic,
		n.onAuthData,
	)

	noiseConn := mailbox.NewNoiseGrpcConn(connData)

	tlsConfig := &tls.Config{}
	if session.DevServer {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	}

	ctxc, cancel := context.WithCancel(context.Background())
	transportConn, err := mailbox.NewGrpcClient(
		ctxc, session.MailboxAddr, connData,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	)
	if err != nil {
		cancel()
		return nil, err
	}

	dialOpts := []grpc.DialOption{
		// From the grpcProxy doc: This codec is *crucial* to the
		// functioning of the proxy.
		grpc.WithCodec(proxy.Codec()), // nolint
		grpc.WithContextDialer(transportConn.Dial),
		grpc.WithTransportCredentials(noiseConn),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(1024 * 1024 * 200),
		),
		grpc.WithBlock(),
	}
	dialOpts = append(dialOpts, opts...)

	grpcClient, err := grpc.DialContext(
		ctxc, session.MailboxAddr, dialOpts...,
	)
	if err != nil {
		cancel()
		return nil, err
	}

	return &conn{
		client:     lnrpc.NewLightningClient(grpcClient),
		grpcClient: grpcClient,
		creds:      noiseConn,
		cancel:     cancel,
	}, nil
}

// extractMacaroon is a helper function that extracts a macaroon from raw bytes.
func extractMacaroon(authData []byte) (*macaroon.Macaroon, error) {
	// The format of the authData is "Macaroon: <hex data>".
	parts := strings.Split(string(authData), ": ")
	if len(parts) != 2 || parts[0] != HeaderMacaroon {
		return nil, fmt.Errorf("authdata does not contain a macaroon")
	}

	macBytes, err := hex.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}

	if len(macBytes) == 0 {
		return nil, fmt.Errorf("no macaroon received during connman")
	}

	mac := &macaroon.Macaroon{}
	if err = mac.UnmarshalBinary(macBytes); err != nil {
		return nil, fmt.Errorf("unable to decode macaroon: %v", err)
	}

	return mac, nil
}
