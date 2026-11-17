package clientapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Defaults of [Options].
const (
	// DefaultMaxConns is the default connection cap.
	DefaultMaxConns = 32
	// DefaultIdleTimeout is the default time a connection may wait for a
	// complete request line.
	DefaultIdleTimeout = 30 * time.Second
	// DefaultWriteTimeout is the default time one response write may take.
	DefaultWriteTimeout = 5 * time.Second
)

// lingerLimit bounds how many refused connections are drained at once; past it
// a refused connection is closed at once.
const lingerLimit = 64

// PeerUIDFunc returns the UID of the process that connected conn.
type PeerUIDFunc func(conn net.Conn) (uint32, error)

// PeerUID is the default [PeerUIDFunc]: it reads SO_PEERCRED from a Unix
// connection. The credentials are those of the process that called connect(2).
func PeerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("clientapi: peer credentials need a Unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		cred, gerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if gerr != nil {
		return 0, gerr
	}
	return cred.Uid, nil
}

// Options configure [Serve]. The zero value is usable.
type Options struct {
	// MaxConns is the cap on concurrent connections; zero means
	// [DefaultMaxConns]. A connection over the cap is answered "busy" and
	// closed.
	MaxConns int
	// IdleTimeout is how long a connection may take to deliver one complete
	// request line, counted from the end of the previous response; zero means
	// [DefaultIdleTimeout]. The connection is closed without a response.
	IdleTimeout time.Duration
	// WriteTimeout bounds each response write; zero means
	// [DefaultWriteTimeout].
	WriteTimeout time.Duration
	// PeerUID reads the peer's UID; nil means [PeerUID]. A test replaces it to
	// simulate another user. An error from it counts as a refusal: the peer
	// is answered "unauthorized" and closed.
	PeerUID PeerUIDFunc
	// Counters receives the server's counters; nil means the server keeps
	// private ones nobody can read.
	Counters *Counters
}

// Counters count what a server did. The zero value is ready to use, and it is
// safe to read while the server runs.
type Counters struct {
	accepted     atomic.Uint64
	refusedUID   atomic.Uint64
	peerErrors   atomic.Uint64
	overCap      atomic.Uint64
	invalid      atomic.Uint64
	tooLarge     atomic.Uint64
	unsupported  atomic.Uint64
	internal     atomic.Uint64
	idleTimeouts atomic.Uint64
	servedHello  atomic.Uint64
	servedStatus atomic.Uint64
}

// CounterSnapshot is a copy of the counters at one moment.
type CounterSnapshot struct {
	// Accepted is the connections accepted, refused ones included.
	Accepted uint64
	// RefusedUID is the connections refused for a UID different from the
	// server's.
	RefusedUID uint64
	// PeerErrors is the connections refused because the peer UID could not be
	// read.
	PeerErrors uint64
	// OverCap is the connections refused with "busy".
	OverCap uint64
	// Invalid is the requests answered "invalid-request".
	Invalid uint64
	// TooLarge is the requests answered "too-large".
	TooLarge uint64
	// Unsupported is the requests answered "unsupported-protocol" or
	// "unsupported-operation".
	Unsupported uint64
	// Internal is the requests answered "internal".
	Internal uint64
	// IdleTimeouts is the connections closed for idling.
	IdleTimeouts uint64
	// Served is the successful responses per operation.
	Served map[string]uint64
}

// Snapshot returns the current counts.
func (c *Counters) Snapshot() CounterSnapshot {
	return CounterSnapshot{
		Accepted:     c.accepted.Load(),
		RefusedUID:   c.refusedUID.Load(),
		PeerErrors:   c.peerErrors.Load(),
		OverCap:      c.overCap.Load(),
		Invalid:      c.invalid.Load(),
		TooLarge:     c.tooLarge.Load(),
		Unsupported:  c.unsupported.Load(),
		Internal:     c.internal.Load(),
		IdleTimeouts: c.idleTimeouts.Load(),
		Served: map[string]uint64{
			OpHello:  c.servedHello.Load(),
			OpStatus: c.servedStatus.Load(),
		},
	}
}

type server struct {
	handler  Handler
	maxConns int
	idle     time.Duration
	write    time.Duration
	peerUID  PeerUIDFunc
	uid      uint32
	counters *Counters

	mu      sync.Mutex
	conns   map[net.Conn]struct{} // admitted connections, counted against the cap
	drains  map[net.Conn]struct{} // refused connections being drained
	closing bool
	wg      sync.WaitGroup
}

func newServer(h Handler, opts Options) *server {
	s := &server{
		handler:  h,
		maxConns: opts.MaxConns,
		idle:     opts.IdleTimeout,
		write:    opts.WriteTimeout,
		peerUID:  opts.PeerUID,
		uid:      uint32(os.Geteuid()),
		counters: opts.Counters,
		conns:    map[net.Conn]struct{}{},
		drains:   map[net.Conn]struct{}{},
	}
	if s.maxConns <= 0 {
		s.maxConns = DefaultMaxConns
	}
	if s.idle <= 0 {
		s.idle = DefaultIdleTimeout
	}
	if s.write <= 0 {
		s.write = DefaultWriteTimeout
	}
	if s.peerUID == nil {
		s.peerUID = PeerUID
	}
	if s.counters == nil {
		s.counters = &Counters{}
	}
	return s
}

// Serve accepts connections on l until ctx ends, and answers the requests of
// each with h. It takes ownership of l and closes it before it returns; a
// socket file that [Listen] bound is unlinked by that close. Serve returns nil
// when ctx ended, and the accept error otherwise. When it returns, every
// connection goroutine has ended.
//
// For each connection Serve reads the peer UID before it reads any byte. A
// peer whose UID differs from the server's effective UID gets one
// "unauthorized" response and is closed; so is a peer whose UID cannot be read.
// A connection over the cap gets "busy" and is closed. Each admitted connection
// has one goroutine, an idle read deadline per request line and a write
// deadline per response. The handler runs in that goroutine, with a context
// that ends with ctx; a panic in it becomes an "internal" response.
//
// The refusals are written by the accept loop, under the write deadline; a
// refused connection is then drained for a moment so that the response is not
// lost to a reset.
func Serve(ctx context.Context, l net.Listener, h Handler, opts Options) error {
	s := newServer(h, opts)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	shutdown := func() {
		l.Close()
		s.mu.Lock()
		s.closing = true
		for c := range s.conns {
			c.Close()
		}
		for c := range s.drains {
			c.Close()
		}
		s.mu.Unlock()
	}
	stop := context.AfterFunc(ctx, shutdown)
	defer func() {
		stop()
		shutdown()
		s.wg.Wait()
	}()

	var delay time.Duration
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			if retryableAccept(err) {
				if delay == 0 {
					delay = 5 * time.Millisecond
				} else if delay *= 2; delay > time.Second {
					delay = time.Second
				}
				select {
				case <-time.After(delay):
					continue
				case <-ctx.Done():
					return nil
				}
			}
			return err
		}
		delay = 0
		s.counters.accepted.Add(1)
		s.admit(ctx, conn)
	}
}

func retryableAccept(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.ENOMEM) ||
		errors.Is(err, syscall.ECONNABORTED)
}

// admit runs the connection-level checks, in the accept loop, and starts the
// connection's goroutine when they pass.
func (s *server) admit(ctx context.Context, conn net.Conn) {
	uid, err := s.peerUID(conn)
	if err != nil {
		s.counters.peerErrors.Add(1)
		s.refuse(conn, CodeUnauthorized, msgUnauthorized)
		return
	}
	if uid != s.uid {
		s.counters.refusedUID.Add(1)
		s.refuse(conn, CodeUnauthorized, msgUnauthorized)
		return
	}
	s.mu.Lock()
	switch {
	case s.closing:
		s.mu.Unlock()
		conn.Close()
		return
	case len(s.conns) >= s.maxConns:
		s.mu.Unlock()
		s.counters.overCap.Add(1)
		s.refuse(conn, CodeBusy, msgBusy)
		return
	}
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() {
			conn.Close()
			s.mu.Lock()
			delete(s.conns, conn)
			s.mu.Unlock()
		}()
		s.serveConn(ctx, conn)
	}()
}

// refuse answers a connection-level refusal and ends the connection. The
// request id is empty: no request was read.
func (s *server) refuse(conn net.Conn, code ErrorCode, msg string) {
	line := encodeError("", &ErrorBody{Code: code, Message: msg})
	conn.SetWriteDeadline(time.Now().Add(minDuration(s.write, time.Second)))
	conn.Write(line)
	s.lingerClose(conn)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// A response to a peer that may not have been read to the end of its input
// must not be lost to a reset: closing with unread input makes the kernel reset
// the connection, and the reset can destroy the response before the peer reads
// it. So the write side is shut first and the input is discarded for a bounded
// time and size, then the connection is closed. The discarded bytes are never
// parsed.

// finish shuts the write side of conn and discards its input for a bounded
// time and size. The caller closes conn.
func finish(conn net.Conn) {
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	io.CopyN(io.Discard, conn, 2*MaxRequestBytes)
}

// lingerClose ends a refused connection that has no goroutine: the drain runs
// in one, up to [lingerLimit] at a time; past that the connection is closed at
// once.
func (s *server) lingerClose(conn net.Conn) {
	s.mu.Lock()
	if s.closing || len(s.drains) >= lingerLimit {
		s.mu.Unlock()
		conn.Close()
		return
	}
	s.drains[conn] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		finish(conn)
		conn.Close()
		s.mu.Lock()
		delete(s.drains, conn)
		s.mu.Unlock()
	}()
}

func (s *server) serveConn(ctx context.Context, conn net.Conn) {
	br := bufio.NewReaderSize(conn, 4096)
	var buf []byte
	for {
		conn.SetReadDeadline(time.Now().Add(s.idle))
		var tooLarge bool
		var err error
		buf, tooLarge, err = readLine(br, buf[:0], MaxRequestBytes)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() && ctx.Err() == nil {
				s.counters.idleTimeouts.Add(1)
			}
			return
		}
		if tooLarge {
			s.counters.tooLarge.Add(1)
			conn.SetWriteDeadline(time.Now().Add(s.write))
			conn.Write(encodeError("", &ErrorBody{Code: CodeTooLarge, Message: msgTooLarge}))
			finish(conn)
			return
		}
		resp := s.respond(ctx, buf)
		conn.SetWriteDeadline(time.Now().Add(s.write))
		if _, err := conn.Write(resp); err != nil {
			return
		}
	}
}

// readLine appends the next line, without its LF, to dst. It reports tooLarge
// as soon as the line is known to be over max bytes, without reading the rest.
// A partial line at the end of the stream is an error.
func readLine(br *bufio.Reader, dst []byte, max int) (line []byte, tooLarge bool, err error) {
	for {
		chunk, err := br.ReadSlice('\n')
		payload := len(dst) + len(chunk)
		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			payload--
		}
		if payload > max {
			return dst, true, nil
		}
		switch {
		case err == nil:
			return append(dst, chunk[:len(chunk)-1]...), false, nil
		case errors.Is(err, bufio.ErrBufferFull):
			dst = append(dst, chunk...)
		default:
			return dst, false, err
		}
	}
}

// respond answers one request line with one response line.
func (s *server) respond(ctx context.Context, line []byte) []byte {
	id, op, fail := decodeRequest(line)
	if fail != nil {
		switch fail.Code {
		case CodeInvalidRequest:
			s.counters.invalid.Add(1)
		default:
			s.counters.unsupported.Add(1)
		}
		return encodeError(id, fail)
	}
	result, ok := s.call(ctx, op)
	if !ok {
		s.counters.internal.Add(1)
		return encodeError(id, &ErrorBody{Code: CodeInternal, Message: msgInternal})
	}
	out, err := marshalLine(successResponse{Protocol: Protocol, RequestID: id, OK: true, Result: result})
	if err != nil {
		s.counters.internal.Add(1)
		return encodeError(id, &ErrorBody{Code: CodeInternal, Message: msgInternal})
	}
	if len(out) > MaxResponseBytes {
		s.counters.internal.Add(1)
		return encodeError(id, &ErrorBody{Code: CodeInternal, Message: msgResponseTooLarge})
	}
	if op == OpHello {
		s.counters.servedHello.Add(1)
	} else {
		s.counters.servedStatus.Add(1)
	}
	return out
}

// call runs the handler for op; a panic or an error is reported as not ok.
func (s *server) call(ctx context.Context, op string) (result any, ok bool) {
	defer func() {
		if recover() != nil {
			result, ok = nil, false
		}
	}()
	switch op {
	case OpHello:
		r := s.handler.Hello(ctx)
		r.Protocols = SupportedProtocols()
		r.Limits = Limits{
			MaxRequestBytes:  MaxRequestBytes,
			MaxResponseBytes: MaxResponseBytes,
			// Rounded up: a timeout under a second must not read as 0, which a
			// client could take for "no limit".
			IdleTimeoutSeconds: int((s.idle + time.Second - 1) / time.Second),
			MaxConnections:     s.maxConns,
		}
		r.Operations = Operations()
		r.Mutations = false
		return r, true
	case OpStatus:
		r, err := s.handler.Status(ctx)
		if err != nil {
			return nil, false
		}
		if r.Leads == nil {
			r.Leads = []LeadStatus{}
		}
		return r, true
	}
	return nil, false
}

type successResponse struct {
	Protocol  string `json:"protocol"`
	RequestID string `json:"request_id"`
	OK        bool   `json:"ok"`
	Result    any    `json:"result"`
}

type errorResponse struct {
	Protocol  string     `json:"protocol"`
	RequestID string     `json:"request_id"`
	OK        bool       `json:"ok"`
	Error     *ErrorBody `json:"error"`
}

// marshalLine encodes v as one line ended by LF.
func marshalLine(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// encodeError encodes an error response line. id must be empty or a valid
// request id.
func encodeError(id string, body *ErrorBody) []byte {
	out, err := marshalLine(errorResponse{Protocol: Protocol, RequestID: id, OK: false, Error: body})
	if err != nil {
		// Unreachable: the body holds only strings.
		return []byte(`{"protocol":"` + Protocol + `","request_id":"","ok":false,"error":{"code":"internal","message":"` + msgInternal + `"}}` + "\n")
	}
	return out
}
