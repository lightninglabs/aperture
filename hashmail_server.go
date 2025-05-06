package aperture

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/btcsuite/btclog/v2"
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

	// DefaultStaleTimeout is the time after which a mailbox will be torn
	// down if neither of its streams are occupied.
	DefaultStaleTimeout = time.Hour

	// DefaultBufSize is the default number of bytes that are read in a
	// single operation.
	DefaultBufSize = 4096

	// streamTTL is the amount of time that a stream needs to be exist without
	// reads for it to be considered for pruning. Otherwise, memory will grow
	// unbounded.
	streamTTL = 24 * time.Hour
)

// streamID is the identifier of a stream.
type streamID [64]byte

// newStreamID creates a new stream given an ID as a byte slice.
func newStreamID(id []byte) streamID {
	var s streamID
	copy(s[:], id)

	return s
}

// baseID returns the first 16 bytes of the streamID. This part of the ID will
// overlap for the two streams in a bidirectional pair.
func (s *streamID) baseID() [16]byte {
	var id [16]byte
	copy(id[:], s[:16])
	return id
}

// isOdd returns true if the streamID is an odd number.
func (s *streamID) isOdd() bool {
	return s[63]&0x01 == 0x01
}

// readStream is the read side of the read pipe, which is implemented a
// buffered wrapper around the core reader.
type readStream struct {
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
func (r *readStream) ReadNextMsg(ctx context.Context) ([]byte, error) {
	var reader io.Reader
	select {
	case b := <-r.parentStream.readBytesChan:
		reader = bytes.NewReader(b)

	case <-ctx.Done():
		return nil, ctx.Err()

	case err := <-r.parentStream.readErrChan:
		return nil, err
	}

	// First, we'll decode the length of the next message from the stream
	// so we know how many bytes we need to read.
	msgLen, err := tlv.ReadVarInt(reader, &r.scratchBuf)
	if err != nil {
		return nil, err
	}

	// Now that we know the length of the message, we'll make a limit
	// reader, then read all the encoded bytes until the EOF is emitted by
	// the reader.
	msgReader := io.LimitReader(reader, int64(msgLen))
	return io.ReadAll(msgReader)
}

// ReturnStream gives up the read stream by passing it back up through the
// payment stream.
func (r *readStream) ReturnStream(ctx context.Context) {
	log.DebugS(ctx, "Returning read stream")
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
	var buf bytes.Buffer
	msgSize := uint64(len(msg))
	if err := tlv.WriteVarInt(&buf, msgSize, &w.scratchBuf); err != nil {
		return err
	}

	// Next, we'll write the message directly to the stream.
	if _, err := buf.Write(msg); err != nil {
		return err
	}

	if _, err := w.Write(buf.Bytes()); err != nil {
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

	readBytesChan chan []byte
	readErrChan   chan error
	quit          chan struct{}

	// equivAuth is a method used to determine if an authentication
	// mechanism to tear down a stream is equivalent to the one used to
	// create it in the first place. WE use this to ensure that only the
	// original creator of a stream can tear it down.
	equivAuth func(auth *hashmailrpc.CipherBoxAuth) error

	tearDown func() error

	wg sync.WaitGroup

	limiter *rate.Limiter

	status *streamStatus
}

// newStream creates a new stream independent of any given stream ID.
func newStream(ctx context.Context, id streamID, limiter *rate.Limiter,
	equivAuth func(auth *hashmailrpc.CipherBoxAuth) error,
	onStale func() error, staleTimeout time.Duration) *stream {

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
		status:          newStreamStatus(ctx, onStale, staleTimeout),
		readBytesChan:   make(chan []byte),
		readErrChan:     make(chan error, 1),
		quit:            make(chan struct{}),
	}

	// Our tear down function will close the write side of the pipe, which
	// will cause the goroutine below to get an EOF error when reading,
	// which will cause it to close the other ends of the pipe.
	s.tearDown = func() error {
		s.status.stop()
		err := writeWritePipe.Close()
		if err != nil {
			return err
		}
		close(s.quit)
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
			writeReadPipe,
		)
		_ = readWritePipe.CloseWithError(err)
		_ = writeReadPipe.CloseWithError(err)
	}()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		var buf [DefaultBufSize]byte
		for {
			numBytes, err := readReadPipe.Read(buf[:])
			if err != nil {
				s.readErrChan <- err
				return
			}

			c := make([]byte, numBytes)
			copy(c, buf[0:numBytes])

			for numBytes == DefaultBufSize {
				numBytes, err = readReadPipe.Read(buf[:])
				if err != nil {
					s.readErrChan <- err
					return
				}
				c = append(c, buf[0:numBytes]...)
			}

			select {
			case s.readBytesChan <- c:
			case <-s.quit:
			}
		}
	}()

	// We'll now initialize our stream by sending the read and write ends
	// to their respective holding channels.
	s.readStreamChan <- &readStream{
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
	s.status.streamReturned(true)
}

// ReturnWriteStream returns the target write stream back to its holding
// channel.
func (s *stream) ReturnWriteStream(w *writeStream) {
	s.writeStreamChan <- w
	s.status.streamReturned(false)
}

// RequestReadStream attempts to request the read stream from the main backing
// stream. If we're unable to obtain it before the timeout, then an error is
// returned.
func (s *stream) RequestReadStream(ctx context.Context) (*readStream, error) {
	log.TraceS(ctx, "Requested read stream")

	select {
	case r := <-s.readStreamChan:
		s.status.streamTaken(true)
		return r, nil
	default:
		return nil, fmt.Errorf("read stream occupied")
	}
}

// RequestWriteStream attempts to request the read stream from the main backing
// stream. If we're unable to obtain it before the timeout, then an error is
// returned.
func (s *stream) RequestWriteStream(ctx context.Context) (*writeStream, error) {
	log.TraceS(ctx, "Requesting write stream")

	select {
	case w := <-s.writeStreamChan:
		s.status.streamTaken(false)
		return w, nil
	default:
		return nil, fmt.Errorf("write stream occupied")
	}
}

// hashMailServerConfig is the main config of the mail server.
type hashMailServerConfig struct {
	msgRate           time.Duration
	msgBurstAllowance int
	staleTimeout      time.Duration
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
	if cfg.staleTimeout == 0 {
		cfg.staleTimeout = DefaultStaleTimeout
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

// tearDownStaleStream can be used to tear down a stale mailbox stream.
func (h *hashMailServer) tearDownStaleStream(ctx context.Context,
	id streamID) error {

	log.DebugS(ctx, "Tearing down stale HashMail stream")

	h.Lock()
	defer h.Unlock()

	stream, ok := h.streams[id]
	if !ok {
		return fmt.Errorf("stream not found")
	}

	if err := stream.tearDown(); err != nil {
		return err
	}

	delete(h.streams, id)

	mailboxCount.Set(float64(len(h.streams)))

	return nil
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
func (h *hashMailServer) InitStream(ctx context.Context,
	init *hashmailrpc.CipherBoxAuth) (*hashmailrpc.CipherInitResp, error) {

	h.Lock()
	defer h.Unlock()

	streamID := newStreamID(init.Desc.StreamId)

	log.DebugS(ctx, "Creating new HashMail Stream")

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
		ctx, streamID, limiter,
		func(auth *hashmailrpc.CipherBoxAuth) error {
			return nil
		}, func() error {
			return h.tearDownStaleStream(ctx, streamID)
		}, h.cfg.staleTimeout,
	)

	h.streams[streamID] = freshStream

	mailboxCount.Set(float64(len(h.streams)))

	return &hashmailrpc.CipherInitResp{
		Resp: &hashmailrpc.CipherInitResp_Success{},
	}, nil
}

// LookUpReadStream attempts to loop up a new stream. If the stream is found, then
// the stream is marked as being active. Otherwise, an error is returned.
func (h *hashMailServer) LookUpReadStream(ctx context.Context,
	streamID []byte) (*readStream, error) {

	h.RLock()
	defer h.RUnlock()

	stream, ok := h.streams[newStreamID(streamID)]
	if !ok {
		return nil, fmt.Errorf("stream not found")
	}

	return stream.RequestReadStream(ctx)
}

// LookUpWriteStream attempts to loop up a new stream. If the stream is found,
// then the stream is marked as being active. Otherwise, an error is returned.
func (h *hashMailServer) LookUpWriteStream(ctx context.Context,
	streamID []byte) (*writeStream, error) {

	h.RLock()
	defer h.RUnlock()

	stream, ok := h.streams[newStreamID(streamID)]
	if !ok {
		return nil, fmt.Errorf("stream not found")
	}

	return stream.RequestWriteStream(ctx)
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

	log.DebugS(ctx, "Tearing down HashMail stream", "auth", auth.Auth)

	// At this point we know the auth was valid, so we'll tear down the
	// stream.
	if err := stream.tearDown(); err != nil {
		return err
	}

	delete(h.streams, sid)

	mailboxCount.Set(float64(len(h.streams)))

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

	ctxl := btclog.WithCtx(ctx, btclog.Hex("stream_id", init.Desc.StreamId))

	log.DebugS(ctxl, "New HashMail stream init", "auth", init.Auth)

	if err := h.ValidateStreamAuth(ctxl, init); err != nil {
		log.DebugS(ctxl, "Stream creation validation failed",
			"err", err)
		return nil, err
	}

	resp, err := h.InitStream(ctxl, init)
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

	ctxl := btclog.WithCtx(ctx, btclog.Hex("stream_id", auth.Desc.StreamId))

	log.DebugS(ctxl, "New HashMail stream deletion", "auth", auth.Auth)

	if err := h.TearDownStream(ctx, auth.Desc.StreamId, auth); err != nil {
		return nil, err
	}

	return &hashmailrpc.DelCipherBoxResp{}, nil
}

// SendStream implements the client streaming call to utilize the write end of
// a stream to send a message to the read end.
func (h *hashMailServer) SendStream(readStream hashmailrpc.HashMail_SendStreamServer) error {
	log.Debug("New HashMail write stream pending...")

	// We'll need to receive the first message in order to determine if
	// this stream exists or not
	//
	// TODO(roasbeef): better way to control?
	cipherBox, err := readStream.Recv()
	if err != nil {
		return err
	}

	ctx := btclog.WithCtx(
		readStream.Context(),
		btclog.Hex("stream_id", cipherBox.Desc.StreamId),
	)

	switch {
	case cipherBox.Desc == nil:
		return fmt.Errorf("cipher box descriptor required")

	case cipherBox.Desc.StreamId == nil:
		return fmt.Errorf("stream_id required")
	}

	log.DebugS(ctx, "New HashMail write stream")

	// Now that we have the first message, we can attempt to look up the
	// given stream.
	writeStream, err := h.LookUpWriteStream(ctx, cipherBox.Desc.StreamId)
	if err != nil {
		return err
	}

	// Now that we know the stream is found, we'll make sure to mark the
	// write inactive if the client hangs up on their end.
	defer writeStream.ReturnStream()

	log.TraceS(ctx, "Sending message to stream",
		"msg_len", len(cipherBox.Msg))

	// We'll send the first message into the stream, then enter our loop
	// below to continue to read from the stream and send it to the read
	// end.
	if err := writeStream.WriteMsg(ctx, cipherBox.Msg); err != nil {
		return err
	}

	for {
		// Check to see if the stream has been closed or if we need to
		// exit before shutting down.
		select {
		case <-ctx.Done():
			log.DebugS(ctx, "SendStream: Context done, exiting")
			return nil
		case <-h.quit:
			return fmt.Errorf("server shutting down")

		default:
		}

		cipherBox, err := readStream.Recv()
		if err != nil {
			log.DebugS(ctx, "SendStream: Exiting write stream RPC "+
				"stream read", err)
			return err
		}

		log.TraceS(ctx, "Sending message to stream",
			"msg_len", len(cipherBox.Msg))

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

	ctx := btclog.WithCtx(
		reader.Context(),
		btclog.Hex("stream_id", desc.StreamId),
	)

	// First, we'll attempt to locate the stream. We allow any single
	// entity that knows of the full stream ID to access the read end.
	readStream, err := h.LookUpReadStream(ctx, desc.StreamId)
	if err != nil {
		return err
	}

	log.DebugS(ctx, "New HashMail read stream")

	// If the reader hangs up, then we'll mark the stream as inactive so
	// another can take its place.
	defer readStream.ReturnStream(ctx)

	for {
		// Check to see if the stream has been closed or if we need to
		// exit before shutting d[own.
		select {
		case <-reader.Context().Done():
			log.DebugS(ctx, "Read stream context done.")
			return nil
		case <-h.quit:
			return fmt.Errorf("server shutting down")

		default:
		}

		nextMsg, err := readStream.ReadNextMsg(reader.Context())
		if err != nil {
			log.ErrorS(ctx, "Got error on read stream read", err)
			return err
		}

		log.TraceS(ctx, "Read bytes", "msg_len", len(nextMsg))

		// In order not to duplicate metric data, we only record this
		// read if its streamID is odd. We use the base stream ID as the
		// label. For this to work, it is expected that the read and
		// write streams of bidirectional pair have the same IDs with
		// the last bit flipped for one of them.
		streamID := newStreamID(desc.StreamId)
		if streamID.isOdd() {
			baseID := streamID.baseID()
			streamActivityTracker.Record(fmt.Sprintf("%x", baseID))
		}

		err = reader.Send(&hashmailrpc.CipherBox{
			Desc: desc,
			Msg:  nextMsg,
		})
		if err != nil {
			log.DebugS(ctx, "Got error when sending on read stream",
				"err", err)
			return err
		}
	}
}

var _ hashmailrpc.HashMailServer = (*hashMailServer)(nil)

// streamActivity tracks per-session read activity for classifying mailbox
// sessions as active, standby, or in-use. It maintains an in-memory map
// of stream IDs to counters and timestamps.
type streamActivity struct {
	sync.Mutex
	streams map[string]*activityEntry
}

// activityEntry holds the read count and last update time for a single mailbox
// session.
type activityEntry struct {
	count      uint64
	lastUpdate time.Time
}

// newStreamActivity creates a new streamActivity tracker used to monitor
// mailbox read activity per stream ID.
func newStreamActivity() *streamActivity {
	return &streamActivity{
		streams: make(map[string]*activityEntry),
	}
}

// Record logs a read event for the given base stream ID.
// It increments the read count and updates the last activity timestamp.
func (sa *streamActivity) Record(baseID string) {
	sa.Lock()
	defer sa.Unlock()

	entry, ok := sa.streams[baseID]
	if !ok {
		entry = &activityEntry{}
		sa.streams[baseID] = entry
	}
	entry.count++
	entry.lastUpdate = time.Now()
}

// ClassifyAndReset categorizes each tracked stream based on its recent read
// rate and returns aggregate counts of active, standby, and in-use sessions.
// A stream is classified as:
// - In-use:   if read rate ≥ 0.5 reads/sec
// - Standby:  if 0 < read rate < 0.5 reads/sec
// - Active:   if read rate > 0 (includes standby and in-use)
func (sa *streamActivity) ClassifyAndReset() (active, standby, inuse int) {
	sa.Lock()
	defer sa.Unlock()

	now := time.Now()

	for baseID, e := range sa.streams {
		inactiveDuration := now.Sub(e.lastUpdate)

		// Prune if idle for >24h and no new reads.
		if e.count == 0 && inactiveDuration > streamTTL {
			delete(sa.streams, baseID)
			continue
		}

		elapsed := inactiveDuration.Seconds()
		if elapsed <= 0 {
			// Prevent divide-by-zero, treat as 1s interval.
			elapsed = 1
		}

		rate := float64(e.count) / elapsed

		switch {
		case rate >= 0.5:
			inuse++
		case rate > 0:
			standby++
		}
		if rate > 0 {
			active++
		}

		// Reset for next window.
		e.count = 0
		e.lastUpdate = now
	}

	return active, standby, inuse
}

// streamStatus keeps track of the occupancy status of a stream's read and
// write sub-streams. It is initialised with callback functions to call on the
// event of the streams being occupied (either or both of the streams are
// occupied) or fully idle (both streams are unoccupied).
type streamStatus struct {
	disabled bool

	staleTimeout time.Duration
	staleTimer   *time.Timer

	readStreamOccupied  bool
	writeStreamOccupied bool
	sync.Mutex
}

// newStreamStatus constructs a new streamStatus instance.
func newStreamStatus(ctx context.Context, onStale func() error,
	staleTimeout time.Duration) *streamStatus {

	if staleTimeout < 0 {
		return &streamStatus{
			disabled: true,
		}
	}

	staleTimer := time.AfterFunc(staleTimeout, func() {
		if err := onStale(); err != nil {
			log.ErrorS(ctx, "Error from onStale callback", err)
		}
	})

	return &streamStatus{
		staleTimer:   staleTimer,
		staleTimeout: staleTimeout,
	}
}

// stop cleans up any resources held by streamStatus.
func (s *streamStatus) stop() {
	if s.disabled {
		return
	}

	s.Lock()
	defer s.Unlock()

	_ = s.staleTimer.Stop()
}

// streamTaken should be called when one of the sub-streams (read or write)
// become occupied. This will stop the staleTimer. The read parameter should be
// true if the stream being returned is the read stream.
func (s *streamStatus) streamTaken(read bool) {
	if s.disabled {
		return
	}

	s.Lock()
	defer s.Unlock()

	if read {
		s.readStreamOccupied = true
	} else {
		s.writeStreamOccupied = true
	}
	_ = s.staleTimer.Stop()
}

// streamReturned should be called when one of the sub-streams are released.
// If the occupancy count after this call is zero, then the staleTimer is reset.
// The read parameter should be true if the stream being returned is the read
// stream.
func (s *streamStatus) streamReturned(read bool) {
	if s.disabled {
		return
	}

	s.Lock()
	defer s.Unlock()

	if read {
		s.readStreamOccupied = false
	} else {
		s.writeStreamOccupied = false
	}

	if !s.readStreamOccupied && !s.writeStreamOccupied {
		_ = s.staleTimer.Reset(s.staleTimeout)
	}
}
