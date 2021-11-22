package aperture

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"sync"
	"time"

	"github.com/lightninglabs/lightning-node-connect/hashmailrpc"
	"github.com/lightningnetwork/lnd/tlv"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// DefaultMsgRate is the default message rate for a given mailbox that
	// we'll allow. We'll allow one message every 500 milliseconds, or 2
	// messages per second.
	DefaultMsgRate = time.Millisecond * 500

	// DefaultMsgBurstAllowance is the default burst rate that we'll allow
	// for messages. If a new message is about to exceed the burst rate,
	// then we'll allow it up to this burst allowance.
	DefaultMsgBurstAllowance = 10
)

// streamID is the identifier of a stream.
type streamID [64]byte

// newStreamID creates a new stream given an ID as a byte slice.
func newStreamID(id []byte) streamID {
	var s streamID
	copy(s[:], id)

	return s
}

// readStream is the read side of the read pipe, which is implemented a
// buffered wrapper around the core reader.
type readStream struct {
	io.Reader

	// parentStream is a pointer to the parent stream. We keep this around
	// so we can return the stream after we're done using it.
	parentStream *stream

	// scratchBuf is a scratch buffer we'll use for decoding message from
	// the stream.
	scratchBuf [8]byte
}

// ReadNextMsg attempts to read the next message in the stream.
//
// NOTE: This will *block* until a new message is available.
func (r *readStream) ReadNextMsg() ([]byte, error) {
	// First, we'll decode the length of the next message from the stream
	// so we know how many bytes we need to read.
	msgLen, err := tlv.ReadVarInt(r, &r.scratchBuf)
	if err != nil {
		return nil, err
	}

	// Now that we know the length of the message, we'll make a limit
	// reader, then read all the encoded bytes until the EOF is emitted by
	// the reader.
	msgReader := io.LimitReader(r, int64(msgLen))
	return ioutil.ReadAll(msgReader)
}

// ReturnStream gives up the read stream by passing it back up through the
// payment stream.
func (r *readStream) ReturnStream() {
	log.Debugf("Returning read stream %x", r.parentStream.id[:])
	r.parentStream.ReturnReadStream(r)
}

// writeStream is the write side of the read pipe. The stream itself is a
// buffered I/O wrapper around the write end of the io.Writer pipe.
type writeStream struct {
	io.Writer

	// parentStream is a pointer to the parent stream. We keep this around
	// so we can return the stream after we're done using it.
	parentStream *stream

	// scratchBuf is a scratch buffer we'll use for decoding message from
	// the stream.
	scratchBuf [8]byte
}

// WriteMsg attempts to write a message to the stream so it can be read using
// the read end of the stream.
//
// NOTE: If the buffer is full, then this call will block until the reader
// consumes bytes from the other end.
func (w *writeStream) WriteMsg(ctx context.Context, msg []byte) error {
	// Wait until until we have enough available event slots to write to
	// the stream. This'll return an error if the referneded context has
	// been cancelled.
	if err := w.parentStream.limiter.Wait(ctx); err != nil {
		return err
	}

	// As we're writing to a stream, we need to delimit each message with a
	// length prefix so the reader knows how many bytes to consume for each
	// message.
	//
	// TODO(roasbeef): actually needs to be single write?
	msgSize := uint64(len(msg))
	err := tlv.WriteVarInt(
		w, msgSize, &w.scratchBuf,
	)
	if err != nil {
		return err
	}

	// Next, we'll write the message directly to the stream.
	_, err = w.Write(msg)
	if err != nil {
		return err
	}

	return nil
}

// ReturnStream returns the write stream back to the parent stream.
func (w *writeStream) ReturnStream() {
	w.parentStream.ReturnWriteStream(w)
}

// stream is a unique pipe implemented using a subscription server, and expose
// over gRPC. Only a single writer and reader can exist within the stream at
// any given time.
type stream struct {
	sync.Mutex

	id streamID

	readStreamChan  chan *readStream
	writeStreamChan chan *writeStream

	// equivAuth is a method used to determine if an authentication
	// mechanism to tear down a stream is equivalent to the one used to
	// create it in the first place. WE use this to ensure that only the
	// original creator of a stream can tear it down.
	equivAuth func(auth *hashmailrpc.CipherBoxAuth) error

	tearDown func() error

	wg sync.WaitGroup

	limiter *rate.Limiter
}

// newStream creates a new stream independent of any given stream ID.
func newStream(id streamID, limiter *rate.Limiter,
	equivAuth func(auth *hashmailrpc.CipherBoxAuth) error) *stream {

	// Our stream is actually just a plain io.Pipe. This allows us to avoid
	// having to do things like rate limiting, etc as we can limit the
	// buffer size. In order to allow non-blocking writes (up to the buffer
	// size), but blocking reads, we'll utilize a series of two pipes.
	writeReadPipe, writeWritePipe := io.Pipe()
	readReadPipe, readWritePipe := io.Pipe()

	s := &stream{
		readStreamChan:  make(chan *readStream, 1),
		writeStreamChan: make(chan *writeStream, 1),
		id:              id,
		equivAuth:       equivAuth,
		limiter:         limiter,
	}

	// Our tear down function will close the write side of the pipe, which
	// will cause the goroutine below to get an EOF error when reading,
	// which will cause it to close the other ends of the pipe.
	s.tearDown = func() error {
		err := writeWritePipe.Close()
		if err != nil {
			return err
		}
		s.wg.Wait()
		return nil
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		// Next, we'll launch a goroutine to copy over the bytes from
		// the pipe the writer will write to into the pipe the reader
		// will read from.
		_, err := io.Copy(
			readWritePipe,
			// This is where the buffering will happen, as the
			// writer writes to the write end of the pipe, this
			// goroutine will copy the bytes into the buffer until
			// its full, then attempt to write it to the write end
			// of the read pipe.
			bufio.NewReader(writeReadPipe),
		)
		_ = readWritePipe.CloseWithError(err)
		_ = writeReadPipe.CloseWithError(err)
	}()

	// We'll now initialize our stream by sending the read and write ends
	// to their respective holding channels.
	s.readStreamChan <- &readStream{
		Reader:       readReadPipe,
		parentStream: s,
	}
	s.writeStreamChan <- &writeStream{
		Writer:       writeWritePipe,
		parentStream: s,
	}

	return s
}

// ReturnReadStream returns the target read stream back to its holding channel.
func (s *stream) ReturnReadStream(r *readStream) {
	s.readStreamChan <- r
}

// ReturnWriteStream returns the target write stream back to its holding
// channel.
func (s *stream) ReturnWriteStream(w *writeStream) {
	s.writeStreamChan <- w
}

// RequestReadStream attempts to request the read stream from the main backing
// stream. If we're unable to obtain it before the timeout, then an error is
// returned.
func (s *stream) RequestReadStream() (*readStream, error) {
	log.Tracef("HashMailStream(%x): requesting read stream", s.id[:])

	select {
	case r := <-s.readStreamChan:
		return r, nil
	default:
		return nil, fmt.Errorf("read stream occupied")
	}
}

// RequestWriteStream attempts to request the read stream from the main backing
// stream. If we're unable to obtain it before the timeout, then an error is
// returned.
func (s *stream) RequestWriteStream() (*writeStream, error) {
	log.Tracef("HashMailStream(%x): requesting write stream", s.id[:])

	select {
	case w := <-s.writeStreamChan:
		return w, nil
	default:
		return nil, fmt.Errorf("write stream occupied")
	}
}

// hashMailServerConfig is the main config of the mail server.
type hashMailServerConfig struct {
	msgRate           time.Duration
	msgBurstAllowance int
}

// hashMailServer is an implementation of the HashMailServer gRPC service that
// implements a simple encrypted mailbox implemented as a series of read and
// write pipes.
type hashMailServer struct {
	hashmailrpc.UnimplementedHashMailServer
	
	sync.RWMutex
	streams map[streamID]*stream

	// TODO(roasbeef): index to keep track of total stream tallies

	quit chan struct{}

	cfg hashMailServerConfig
}

// newHashMailServer returns a new mail server instance given a valid config.
func newHashMailServer(cfg hashMailServerConfig) *hashMailServer {
	if cfg.msgRate == 0 {
		cfg.msgRate = DefaultMsgRate
	}
	if cfg.msgBurstAllowance == 0 {
		cfg.msgBurstAllowance = DefaultMsgBurstAllowance
	}

	return &hashMailServer{
		streams: make(map[streamID]*stream),
		quit:    make(chan struct{}),
		cfg:     cfg,
	}
}

// Stop attempts to gracefully stop the server by cancelling all pending user
// streams and any goroutines active feeding off them.
func (h *hashMailServer) Stop() {
	h.Lock()
	defer h.Unlock()

	for _, stream := range h.streams {
		if err := stream.tearDown(); err != nil {
			log.Warnf("unable to tear down stream: %v", err)
		}
	}

}

// ValidateStreamAuth attempts to validate the authentication mechanism that is
// being used to claim or revoke a stream within the mail server.
func (h *hashMailServer) ValidateStreamAuth(ctx context.Context,
	init *hashmailrpc.CipherBoxAuth) error {

	// TODO(guggero): Implement auth.
	if true {
		return nil
	}

	// TODO(roasbeef): throttle the number of streams a given
	// ticket/account can have

	return nil
}

// InitStream attempts to initialize a new stream given a valid descriptor.
func (h *hashMailServer) InitStream(
	init *hashmailrpc.CipherBoxAuth) (*hashmailrpc.CipherInitResp, error) {

	h.Lock()
	defer h.Unlock()

	streamID := newStreamID(init.Desc.StreamId)

	log.Debugf("Creating new HashMail Stream: %x", streamID)

	// The stream is already active, and we only allow a single session for
	// a given stream to exist.
	if _, ok := h.streams[streamID]; ok {
		return nil, status.Error(codes.AlreadyExists, "stream "+
			"already active")
	}

	// TODO(roasbeef): validate that ticket or node doesn't already have
	// the same stream going

	limiter := rate.NewLimiter(
		rate.Every(h.cfg.msgRate), h.cfg.msgBurstAllowance,
	)
	freshStream := newStream(
		streamID, limiter, func(auth *hashmailrpc.CipherBoxAuth) error {
			return nil
		},
	)

	h.streams[streamID] = freshStream

	return &hashmailrpc.CipherInitResp{
		Resp: &hashmailrpc.CipherInitResp_Success{},
	}, nil
}

// LookUpReadStream attempts to loop up a new stream. If the stream is found, then
// the stream is marked as being active. Otherwise, an error is returned.
func (h *hashMailServer) LookUpReadStream(streamID []byte) (*readStream, error) {

	h.RLock()
	defer h.RUnlock()

	stream, ok := h.streams[newStreamID(streamID)]
	if !ok {
		return nil, fmt.Errorf("stream not found")
	}

	return stream.RequestReadStream()
}

// LookUpWriteStream attempts to loop up a new stream. If the stream is found,
// then the stream is marked as being active. Otherwise, an error is returned.
func (h *hashMailServer) LookUpWriteStream(streamID []byte) (*writeStream, error) {

	h.RLock()
	defer h.RUnlock()

	stream, ok := h.streams[newStreamID(streamID)]
	if !ok {
		return nil, fmt.Errorf("stream not found")
	}

	return stream.RequestWriteStream()
}

// TearDownStream attempts to tear down a stream which renders both sides of
// the stream unusable and also reclaims resources.
func (h *hashMailServer) TearDownStream(ctx context.Context, streamID []byte,
	auth *hashmailrpc.CipherBoxAuth) error {

	h.Lock()
	defer h.Unlock()

	sid := newStreamID(streamID)
	stream, ok := h.streams[sid]
	if !ok {
		return fmt.Errorf("stream not found")
	}

	// We'll ensure that the same authentication type is used, to ensure
	// only the creator can tear down a stream they created.
	if err := stream.equivAuth(auth); err != nil {
		return fmt.Errorf("invalid auth: %v", err)
	}

	// Now that we know the auth type has matched up, we'll validate the
	// authentication mechanism as normal.
	if err := h.ValidateStreamAuth(ctx, auth); err != nil {
		return err
	}

	log.Debugf("Tearing down HashMail stream: id=%x, auth=%v",
		auth.Desc.StreamId, auth.Auth)

	// At this point we know the auth was valid, so we'll tear down the
	// stream.
	if err := stream.tearDown(); err != nil {
		return err
	}

	delete(h.streams, sid)

	return nil
}

// validateAuthReq does some basic sanity checks on incoming auth methods.
func validateAuthReq(req *hashmailrpc.CipherBoxAuth) error {
	switch {
	case req.Desc == nil:
		return fmt.Errorf("cipher box descriptor required")

	case req.Desc.StreamId == nil:
		return fmt.Errorf("stream_id required")

	case req.Auth == nil:
		return fmt.Errorf("auth type required")

	default:
		return nil
	}
}

// NewCipherBox attempts to create a new cipher box stream given a valid
// authentication mechanism. This call may fail if the stream is already
// active, or the authentication mechanism invalid.
func (h *hashMailServer) NewCipherBox(ctx context.Context,
	init *hashmailrpc.CipherBoxAuth) (*hashmailrpc.CipherInitResp, error) {

	// Before we try to process the request, we'll do some basic user input
	// validation.
	if err := validateAuthReq(init); err != nil {
		return nil, err
	}

	log.Debugf("New HashMail stream init: id=%x, auth=%v",
		init.Desc.StreamId, init.Auth)

	if err := h.ValidateStreamAuth(ctx, init); err != nil {
		log.Debugf("Stream creation validation failed (id=%x): %v",
			init.Desc.StreamId, err)
		return nil, err
	}

	resp, err := h.InitStream(init)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// DelCipherBox attempts to tear down an existing cipher box pipe. The same
// authentication mechanism used to initially create the stream MUST be
// specified.
func (h *hashMailServer) DelCipherBox(ctx context.Context,
	auth *hashmailrpc.CipherBoxAuth) (*hashmailrpc.DelCipherBoxResp, error) {

	// Before we try to process the request, we'll do some basic user input
	// validation.
	if err := validateAuthReq(auth); err != nil {
		return nil, err
	}

	log.Debugf("New HashMail stream deletion: id=%x, auth=%v",
		auth.Desc.StreamId, auth.Auth)

	if err := h.TearDownStream(ctx, auth.Desc.StreamId, auth); err != nil {
		return nil, err
	}

	return &hashmailrpc.DelCipherBoxResp{}, nil
}

// SendStream implements the client streaming call to utilize the write end of
// a stream to send a message to the read end.
func (h *hashMailServer) SendStream(readStream hashmailrpc.HashMail_SendStreamServer) error {
	log.Debugf("New HashMail write stream pending...")

	// We'll need to receive the first message in order to determine if
	// this stream exists or not
	//
	// TODO(roasbeef): better way to control?
	cipherBox, err := readStream.Recv()
	if err != nil {
		return err
	}

	switch {
	case cipherBox.Desc == nil:
		return fmt.Errorf("cipher box descriptor required")

	case cipherBox.Desc.StreamId == nil:
		return fmt.Errorf("stream_id required")
	}

	log.Debugf("New HashMail write stream: id=%x",
		cipherBox.Desc.StreamId)

	// Now that we have the first message, we can attempt to look up the
	// given stream.
	writeStream, err := h.LookUpWriteStream(cipherBox.Desc.StreamId)
	if err != nil {
		return err
	}

	// Now that we know the stream is found, we'll make sure to mark the
	// write inactive if the client hangs up on their end.
	defer writeStream.ReturnStream()

	log.Tracef("Sending msg_len=%v to stream_id=%x", len(cipherBox.Msg),
		cipherBox.Desc.StreamId)

	// We'll send the first message into the stream, then enter our loop
	// below to continue to read from the stream and send it to the read
	// end.
	ctx := readStream.Context()
	if err := writeStream.WriteMsg(ctx, cipherBox.Msg); err != nil {
		return err
	}

	for {
		// Check to see if the stream has been closed or if we need to
		// exit before shutting down.
		select {
		case <-ctx.Done():
			return nil
		case <-h.quit:
			return fmt.Errorf("server shutting down")

		default:
		}

		cipherBox, err := readStream.Recv()
		if err != nil {
			return err
		}

		log.Tracef("Sending msg_len=%v to stream_id=%x",
			len(cipherBox.Msg), cipherBox.Desc.StreamId)

		if err := writeStream.WriteMsg(ctx, cipherBox.Msg); err != nil {
			return err
		}
	}
}

// RecvStream implements the read end of the stream. A single client will have
// all messages written to the opposite side of the stream written to it for
// consumption.
func (h *hashMailServer) RecvStream(desc *hashmailrpc.CipherBoxDesc,
	reader hashmailrpc.HashMail_RecvStreamServer) error {

	// First, we'll attempt to locate the stream. We allow any single
	// entity that knows of the full stream ID to access the read end.
	readStream, err := h.LookUpReadStream(desc.StreamId)
	if err != nil {
		return err
	}

	log.Debugf("New HashMail read stream: id=%x", desc.StreamId)

	// If the reader hangs up, then we'll mark the stream as inactive so
	// another can take its place.
	defer readStream.ReturnStream()

	for {
		// Check to see if the stream has been closed or if we need to
		// exit before shutting down.
		select {
		case <-reader.Context().Done():
			log.Debugf("Read stream context done.")
			return nil
		case <-h.quit:
			return fmt.Errorf("server shutting down")

		default:
		}

		nextMsg, err := readStream.ReadNextMsg()
		if err != nil {
			log.Debugf("Got error an read stream read: %v", err)
			return err
		}

		log.Tracef("Read %v bytes for HashMail stream_id=%x",
			len(nextMsg), desc.StreamId)

		err = reader.Send(&hashmailrpc.CipherBox{
			Desc: desc,
			Msg:  nextMsg,
		})
		if err != nil {
			log.Debugf("Got error when sending on read stream: %v",
				err)
			return err
		}
	}
}

var _ hashmailrpc.HashMailServer = (*hashMailServer)(nil)
