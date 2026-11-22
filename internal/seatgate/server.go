package seatgate

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// Peer is who is on the other end of a connection, as the TLS handshake
// proved it: the fingerprint of the key it holds. It is one of the pinned
// fingerprints of the server's [ServerTLS].
type Peer struct {
	Fingerprint string
}

// Handler serves the operations the transport does not: every operation but
// seat.hello and seat.ping. It is called once per request, in its own
// goroutine, so it must be safe for concurrent use. ctx is done when the
// request's deadline passes, when the connection closes and when a shutdown
// outlasts its grace period; a handler that ignores it is never stopped, and
// holds its in-flight slot, and its place under the server's MaxHandlers,
// until it returns, even after its connection is gone. params is the
// request's params object, or nil when there were none; [DecodeStrict] reads
// it.
//
// A returned result must be a JSON object (nil is {}). An error is told to
// the peer only as a code: if it implements [CodedError] its Code, if that is
// of the grammar of [ValidCode], and "failed" otherwise. Nothing else of the
// error, and nothing of a panic, is sent.
type Handler interface {
	Handle(ctx context.Context, peer Peer, op string, params json.RawMessage) (json.RawMessage, error)
}

// HandlerFunc is a function that is a Handler.
type HandlerFunc func(ctx context.Context, peer Peer, op string, params json.RawMessage) (json.RawMessage, error)

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, peer Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	return f(ctx, peer, op, params)
}

// CodedError is an error that has a code for the wire. The code is what the
// peer is told, so it must be of the grammar [a-z0-9-]{1,40}; a text for the
// person goes in the error's own Error method, which is never sent.
type CodedError interface {
	error
	Code() string
}

// codedError is the CodedError that [NewCodedError] makes.
type codedError struct{ code string }

func (e codedError) Error() string { return "seatgate: refused: " + e.code }
func (e codedError) Code() string  { return e.code }

// NewCodedError returns a [CodedError] with the given code.
func NewCodedError(code string) CodedError { return codedError{code} }

// ServerConfig is the configuration of [Serve]. TLS is required; every other
// field with a zero value takes the default in its comment.
type ServerConfig struct {
	// TLS is the configuration of the listening end: use [ServerTLS].
	TLS *tls.Config
	// Caps is the capability list the server answers a hello with: names of
	// the grammar [a-z0-9.-]{1,40}, at most 32, each once.
	Caps []string

	MaxConns       int           // concurrent connections, 8
	MaxInFlight    int           // handlers running at once per connection, 16
	MaxHandlers    int           // handlers running at once over all connections, MaxConns × MaxInFlight
	RequestTimeout time.Duration // the deadline of one request, 30 s
	RateMax        int           // requests per RatePer per connection, 60
	RatePer        time.Duration // the rate window, 1 minute

	HandshakeTimeout time.Duration // TLS handshake of a new connection, 5 s
	HelloTimeout     time.Duration // from the handshake to the hello, 10 s
	FrameTimeout     time.Duration // from the first byte of a frame to its last, 10 s
	IdleTimeout      time.Duration // silence after which a connection is closed, 60 s
	WriteTimeout     time.Duration // one write to a peer, 10 s
	ShutdownGrace    time.Duration // how long a shutdown lets requests finish, 5 s

	// Now is the clock of the rate limit; time.Now when nil. Deadlines on the
	// network use the real clock.
	Now func() time.Time

	// hsWatch, for tests, runs as the watcher of a handshake starts, and what
	// it returns as the watcher ends.
	hsWatch func() func()
	// answerHook, for tests, runs when the answer of a request handed to the
	// handler is ready to be written (written false) and once it is written.
	answerHook func(id uint64, written bool)
	// reuseHook, for tests, runs when a request is found to reuse the id of
	// one whose answer is being written, before it waits for that write.
	reuseHook func(id uint64)
}

func (c ServerConfig) withDefaults() ServerConfig {
	d := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	n := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	n(&c.MaxConns, 8)
	n(&c.MaxInFlight, 16)
	n(&c.MaxHandlers, c.MaxConns*c.MaxInFlight)
	n(&c.RateMax, 60)
	d(&c.RequestTimeout, 30*time.Second)
	d(&c.RatePer, time.Minute)
	d(&c.HandshakeTimeout, 5*time.Second)
	d(&c.HelloTimeout, 10*time.Second)
	d(&c.FrameTimeout, 10*time.Second)
	d(&c.IdleTimeout, 60*time.Second)
	d(&c.WriteTimeout, 10*time.Second)
	d(&c.ShutdownGrace, 5*time.Second)
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

type server struct {
	cfg   ServerConfig
	h     Handler
	root  context.Context
	drain chan struct{} // closed when the server begins to shut down

	mu       sync.Mutex
	active   int // connections being served
	handlers int // handlers that have not returned, of any connection, gone or not
	wg       sync.WaitGroup
}

// takeHandler takes a place for a handler under MaxHandlers, or reports that
// there is none. The place is the handler's until it returns: a handler that
// ignores its context keeps it after its connection has gone, so a peer that
// reconnects cannot pile up handlers.
func (s *server) takeHandler() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handlers >= s.cfg.MaxHandlers {
		return false
	}
	s.handlers++
	return true
}

func (s *server) releaseHandler() {
	s.mu.Lock()
	s.handlers--
	s.mu.Unlock()
}

// Serve accepts connections on ln until ctx is done, serving each with h. It
// takes ownership of ln and closes it. A connection is TLS (cfg.TLS), then a
// hello, then requests; see the package documentation for the rules. At most
// cfg.MaxConns connections are open at once: another is closed as it arrives.
//
// When ctx is done Serve stops accepting, lets the requests in flight finish
// and be answered for up to cfg.ShutdownGrace, closes every connection and
// returns nil once all are gone. If the listener fails otherwise Serve shuts
// down in the same way and returns the error.
func Serve(ctx context.Context, ln net.Listener, cfg ServerConfig, h Handler) error {
	if cfg.TLS == nil || h == nil {
		ln.Close()
		return errors.New("seatgate: Serve needs a TLS configuration and a handler")
	}
	if !checkCaps(cfg.Caps) {
		ln.Close()
		return errors.New("seatgate: the capability list is not valid")
	}
	s := &server{
		cfg:   cfg.withDefaults(),
		h:     h,
		root:  context.WithoutCancel(ctx),
		drain: make(chan struct{}),
	}
	var once sync.Once
	begin := func() { once.Do(func() { close(s.drain); ln.Close() }) }
	stop := make(chan struct{})
	watcher := make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-ctx.Done():
			begin()
		case <-stop:
		}
	}()

	var failure error
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					time.Sleep(50 * time.Millisecond)
					continue
				}
				failure = errors.New("seatgate: the listener failed")
			}
			break
		}
		s.admit(c)
	}
	begin()
	close(stop)
	<-watcher
	s.wg.Wait()
	return failure
}

// admit starts serving c, or closes it if the server is full.
func (s *server) admit(c net.Conn) {
	s.mu.Lock()
	if s.active >= s.cfg.MaxConns {
		s.mu.Unlock()
		c.Close()
		return
	}
	s.active++
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer func() {
			s.mu.Lock()
			s.active--
			s.mu.Unlock()
			s.wg.Done()
		}()
		s.serveConn(c)
	}()
}

// frameMsg is what the reader goroutine of a connection hands over: a frame's
// payload, or the error that ended the reading.
type frameMsg struct {
	payload []byte
	err     error
}

// srvConn is one connection, from after the TLS handshake.
type srvConn struct {
	s      *server
	tc     *tls.Conn
	raw    net.Conn // closed to end the connection: a TLS close would wait to send an alert to a peer that does not read
	peer   Peer
	ctx    context.Context
	cancel context.CancelFunc

	frames     chan frameMsg
	readerDone chan struct{}
	waiters    sync.WaitGroup
	rate       *limiter
	helloDone  bool

	wmu sync.Mutex // serialises writes

	mu      sync.Mutex
	ids     map[uint64]bool // requests asked and not yet answered: true while the answer is written
	running int             // handlers that have not returned
}

func (s *server) serveConn(raw net.Conn) {
	defer raw.Close()
	select {
	case <-s.drain:
		return
	default:
	}
	raw.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	tc := tls.Server(raw, s.cfg.TLS)
	// A shutdown does not wait for a handshake: a watcher closes the
	// connection if the drain comes first. It is waited for once the handshake
	// is over, and says which it saw: one that runs late, when the handshake
	// has ended and the drain has begun both, may take either, and if it took
	// the drain the connection is closed now, before any request, and not
	// under a request later.
	hsDone, hsCut := make(chan struct{}), make(chan bool, 1)
	go func() {
		if h := s.cfg.hsWatch; h != nil {
			defer h()()
		}
		select {
		case <-s.drain:
			raw.Close()
			hsCut <- true
		case <-hsDone:
			hsCut <- false
		}
	}()
	err := tc.Handshake()
	close(hsDone)
	if <-hsCut || err != nil {
		return
	}
	raw.SetDeadline(time.Time{})
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) != 1 {
		return
	}
	ctx, cancel := context.WithCancel(s.root)
	c := &srvConn{
		s:          s,
		tc:         tc,
		raw:        raw,
		peer:       Peer{Fingerprint: spkiFingerprint(certs[0])},
		ctx:        ctx,
		cancel:     cancel,
		frames:     make(chan frameMsg),
		readerDone: make(chan struct{}),
		rate:       &limiter{max: s.cfg.RateMax, per: s.cfg.RatePer},
		ids:        map[uint64]bool{},
	}
	// The grace of a shutdown is counted here, from the drain, and not by the
	// loop, which may be held in a write to a peer that does not read and see
	// the drain only when that write ends: at the end of the grace the
	// connection is cut whatever the loop is doing.
	graceDone := make(chan struct{})
	go func() {
		defer close(graceDone)
		select {
		case <-s.drain:
		case <-ctx.Done():
			return
		}
		t := time.NewTimer(s.cfg.ShutdownGrace)
		defer t.Stop()
		select {
		case <-t.C:
			c.kill()
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(); <-graceDone }()
	go c.reader()
	c.loop()
}

// reader reads frames off the connection and hands them to the loop. It is the
// only reader; it ends with the first error.
func (c *srvConn) reader() {
	defer close(c.readerDone)
	cfg := c.s.cfg
	for {
		p, err := readFrameTimed(c.tc, time.Now, cfg.IdleTimeout, cfg.FrameTimeout)
		select {
		case c.frames <- frameMsg{p, err}:
		case <-c.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

// loop is the connection's one decision-maker: it takes frames in order, answers
// the ones the transport serves and starts a handler for the rest.
func (c *srvConn) loop() {
	helloTimer := time.NewTimer(c.s.cfg.HelloTimeout)
	defer helloTimer.Stop()
	helloC := helloTimer.C
	for {
		select {
		case m := <-c.frames:
			if m.err != nil {
				c.finish(false)
				return
			}
			req, err := decodeRequest(m.payload)
			if err != nil || !c.handle(req) {
				c.finish(false)
				return
			}
			if c.helloDone {
				helloC = nil
			}
		case <-helloC:
			c.finish(false)
			return
		case <-c.s.drain:
			c.finish(true)
			return
		case <-c.ctx.Done():
			c.finish(false)
			return
		}
	}
}

// finish ends the connection. When graceful, the requests in flight are first
// given the rest of the shutdown grace to be answered: serveConn cuts the
// connection, and so ends the wait, when the grace is over. Then everything is
// cut: the context of the handlers, the connection, the reader and the waiting
// goroutines.
func (c *srvConn) finish(graceful bool) {
	if graceful {
		done := make(chan struct{})
		go func() { c.waiters.Wait(); close(done) }()
		select {
		case <-done:
		case <-c.ctx.Done():
		}
	}
	c.cancel()
	c.raw.Close()
	c.waiters.Wait()
	<-c.readerDone
}

// kill ends the connection from another goroutine than the loop, after a
// failed write: the loop sees the context done and finishes.
func (c *srvConn) kill() {
	c.cancel()
	c.raw.Close()
}

// reply writes one response. A write that fails ends the connection.
func (c *srvConn) reply(r response) {
	payload := r.frame()
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.write(payload)
}

// answer writes the response to a request handed to the handler and then
// releases its id. The id is the request's until its answer is written, so a
// request that reuses it before then closes the connection (it could not be
// told from this one); while the answer is being written it is marked, and
// handle waits for the write before it decides, since the client may have the
// answer already and reuse the id, as it may. A write that fails keeps the id
// taken: the connection is going and the peer never had the answer, so a
// request waiting on the write with the id is refused and not served.
func (c *srvConn) answer(r response) {
	payload := r.frame()
	if h := c.s.cfg.answerHook; h != nil {
		h(r.ID, false)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.mu.Lock()
	c.ids[r.ID] = true
	c.mu.Unlock()
	if !c.write(payload) {
		return
	}
	if h := c.s.cfg.answerHook; h != nil {
		h(r.ID, true)
	}
	c.mu.Lock()
	delete(c.ids, r.ID)
	c.mu.Unlock()
}

// write writes one frame and reports whether it was written; the caller holds
// wmu. A write that fails ends the connection.
func (c *srvConn) write(payload []byte) bool {
	c.tc.SetWriteDeadline(time.Now().Add(c.s.cfg.WriteTimeout))
	if err := writeFrame(c.tc, payload); err != nil {
		c.kill()
		return false
	}
	return true
}

// frame is r encoded for the wire: too-large for a result that does not fit a
// frame, failed if it cannot be encoded.
func (r response) frame() []byte {
	payload, err := r.encode()
	if err == nil && len(payload) > MaxFrame {
		payload, err = response{ID: r.ID, Code: CodeTooLarge}.encode()
	}
	if err != nil {
		payload, _ = response{ID: r.ID, Code: CodeFailed}.encode()
	}
	return payload
}

// handle takes one decoded request. It returns false when the connection is
// to be closed: after a refused hello, a request before the hello, and a
// request that reuses the id of one still in flight.
func (c *srvConn) handle(req request) bool {
	if !c.helloDone {
		if req.Op != OpHello {
			c.reply(response{ID: req.ID, Code: CodeHelloRequired})
			return false
		}
		return c.hello(req)
	}
	// The id first: a reuse gets no answer of any kind, since it would go
	// under the id of the request in flight.
	c.mu.Lock()
	writing, dup := c.ids[req.ID]
	c.mu.Unlock()
	if writing { // the answer is on its way out, and the id with it
		if h := c.s.cfg.reuseHook; h != nil {
			h(req.ID)
		}
		c.wmu.Lock()
		c.wmu.Unlock()
		c.mu.Lock()
		_, dup = c.ids[req.ID]
		c.mu.Unlock()
	}
	if dup {
		return false
	}
	if !c.rate.allow(c.s.cfg.Now()) {
		c.reply(response{ID: req.ID, Code: CodeRateLimited})
		return true
	}
	switch req.Op {
	case OpHello:
		c.reply(response{ID: req.ID, Code: CodeHelloRepeated})
		return true
	case OpPing:
		c.reply(response{ID: req.ID})
		return true
	}
	c.mu.Lock()
	if c.running >= c.s.cfg.MaxInFlight || !c.s.takeHandler() {
		c.mu.Unlock()
		c.reply(response{ID: req.ID, Code: CodeBusy})
		return true
	}
	c.running++
	c.ids[req.ID] = false
	c.mu.Unlock()
	c.waiters.Add(1)
	go c.serve(req)
	return true
}

// hello answers the first request of a connection.
func (c *srvConn) hello(req request) bool {
	if _, err := decodeHello(req.Params); err != nil {
		code := CodeBadHello
		if err == errProto {
			code = CodeProtoUnsupported
		}
		c.reply(response{ID: req.ID, Code: code})
		return false
	}
	c.helloDone = true
	c.reply(response{ID: req.ID, Result: hello{Proto: ProtoMajor, Caps: c.s.cfg.Caps}.encode()})
	return true
}

// serve runs one request: the handler in a goroutine of its own, and the wait
// for it, which ends at the request's deadline whether the handler has
// returned or not. The id is released once the response is written (see
// answer).
func (c *srvConn) serve(req request) {
	defer c.waiters.Done()
	ctx, cancel := context.WithTimeout(c.ctx, c.s.cfg.RequestTimeout)
	defer cancel()
	done := make(chan response, 1)
	go func() { // the one goroutine that returns from Handle gives back its places
		defer func() {
			c.mu.Lock()
			c.running--
			c.mu.Unlock()
			c.s.releaseHandler()
		}()
		done <- c.invoke(ctx, req)
	}()
	var r response
	select {
	case r = <-done:
	case <-ctx.Done():
		if c.ctx.Err() != nil { // the connection is going: nobody to answer
			return
		}
		r = response{ID: req.ID, Code: CodeDeadline}
	}
	c.answer(r)
}

// invoke calls the handler and turns what comes back, or a panic, into the
// response to send.
func (c *srvConn) invoke(ctx context.Context, req request) (r response) {
	r.ID = req.ID
	defer func() {
		if recover() != nil {
			r = response{ID: req.ID, Code: CodeFailed}
		}
	}()
	out, err := c.s.h.Handle(ctx, c.peer, req.Op, req.Params)
	if err != nil {
		r.Code = codeOf(err)
		return r
	}
	if len(out) == 0 {
		out = json.RawMessage("{}")
	}
	if !isObject(out) || !json.Valid(out) {
		r.Code = CodeFailed
		return r
	}
	r.Result = out
	return r
}

// codeOf is the code to tell the peer of err.
func codeOf(err error) string {
	var ce CodedError
	if errors.As(err, &ce) {
		if code := ce.Code(); ValidCode(code) {
			return code
		}
	}
	return CodeFailed
}

// limiter is a sliding window: at most max events in any span of per.
type limiter struct {
	max   int
	per   time.Duration
	times []time.Time
}

func (l *limiter) allow(now time.Time) bool {
	i := 0
	for i < len(l.times) && now.Sub(l.times[i]) >= l.per {
		i++
	}
	l.times = l.times[i:]
	if len(l.times) >= l.max {
		return false
	}
	l.times = append(l.times, now)
	return true
}
