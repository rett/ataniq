package seatgate

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

// Errors a [Client] returns.
var (
	// ErrDisconnected is returned by Call when there is no connection to the
	// gate now, or when the one it used was lost before the answer came. The
	// client is reconnecting; the request may or may not have been executed.
	ErrDisconnected = errors.New("seatgate: not connected")
	// ErrClosed is returned once the client is closed.
	ErrClosed = errors.New("seatgate: the client is closed")
	// ErrReserved is returned by Call for an operation the transport owns.
	ErrReserved = errors.New("seatgate: seat.hello and seat.ping belong to the transport")
	// ErrBadCall is returned by Call for an operation name outside its
	// grammar, or params that do not encode as one JSON object.
	ErrBadCall = errors.New("seatgate: the operation or its params are not valid")
)

// RemoteError is what [Client.Call] returns when the peer refused the request
// with a code: the code of a [CodedError] of the handler, or one of the codes
// of the transport (see the Code constants).
type RemoteError struct {
	Code string
}

func (e *RemoteError) Error() string { return "seatgate: the peer refused the request: " + e.Code }

// ClientConfig is the configuration of a [Client]. TLS is required; every other
// field with a zero value takes the default in its comment.
type ClientConfig struct {
	// TLS is the configuration of the dialling end: use [ClientTLS].
	TLS *tls.Config
	// Caps is the capability list the client says in its hello: names of the
	// grammar [a-z0-9.-]{1,40}, at most 32, each once.
	Caps []string

	PingInterval time.Duration // between pings, 15 s
	DeadAfter    time.Duration // silence after which the connection is taken for dead, 45 s
	DialTimeout  time.Duration // TCP connect and TLS handshake, 10 s
	HelloTimeout time.Duration // the answer to the hello, 10 s
	FrameTimeout time.Duration // from the first byte of a frame to its last, 10 s
	WriteTimeout time.Duration // one write, 10 s
	BackoffMin   time.Duration // the first wait between connection attempts, 1 s
	BackoffMax   time.Duration // the longest wait, 30 s

	// Rand gives a number in [0, 1) for the jitter of the waits; math/rand/v2
	// when nil.
	Rand func() float64
	// Sleep waits for d or until ctx is done, returning ctx's error then; a
	// timer when nil. Tests replace it to run the backoff without waiting.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock of [Client.Connected]; time.Now when nil.
	Now func() time.Time
	// Dial opens the TCP connection; a net.Dialer with DialTimeout when nil.
	Dial func(ctx context.Context, addr string) (net.Conn, error)
}

func (c ClientConfig) withDefaults() ClientConfig {
	d := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	d(&c.PingInterval, 15*time.Second)
	d(&c.DeadAfter, 45*time.Second)
	d(&c.DialTimeout, 10*time.Second)
	d(&c.HelloTimeout, 10*time.Second)
	d(&c.FrameTimeout, 10*time.Second)
	d(&c.WriteTimeout, 10*time.Second)
	d(&c.BackoffMin, time.Second)
	d(&c.BackoffMax, 30*time.Second)
	if c.BackoffMax < c.BackoffMin {
		c.BackoffMax = c.BackoffMin
	}
	if c.Rand == nil {
		c.Rand = rand.Float64
	}
	if c.Sleep == nil {
		c.Sleep = sleep
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Dial == nil {
		timeout := c.DialTimeout
		c.Dial = func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "tcp", addr)
		}
	}
	return c
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backoff is the wait before connection attempt n (0 for the first retry): min
// doubled n times, held to max, then jittered into its upper half, [d/2, d),
// by r in [0, 1). So no two clients of one restarted gate retry in step, and
// the wait is never below half of min.
func backoff(min, max time.Duration, n int, r float64) time.Duration {
	d := min
	for i := 0; i < n && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d/2 + time.Duration(r*float64(d/2))
}

// Client is the dialling end: one connection to the gate at a time, kept up by
// [Client.Run] (or by the goroutine [Dial] starts), and requests made over it
// with [Client.Call]. It is safe for concurrent use.
type Client struct {
	addr string
	cfg  ClientConfig

	stop    chan struct{} // closed by Close
	runDone chan struct{} // closed when the goroutine of Dial ends; nil for Run

	mu      sync.Mutex
	sess    *session
	since   time.Time
	caps    []string
	lastErr error
	closed  bool
	running bool
	closeMu sync.Once
}

// NewClient makes a client for the gate at addr (host:port) without connecting.
// Call [Client.Run] to connect and stay connected.
func NewClient(addr string, cfg ClientConfig) (*Client, error) {
	if cfg.TLS == nil || addr == "" {
		return nil, errors.New("seatgate: a client needs an address and a TLS configuration")
	}
	if !checkCaps(cfg.Caps) {
		return nil, errors.New("seatgate: the capability list is not valid")
	}
	return &Client{addr: addr, cfg: cfg.withDefaults(), stop: make(chan struct{})}, nil
}

// Dial makes a client, connects it to the gate (TLS, then hello) and keeps it
// connected from a goroutine that [Client.Close] ends. It returns the error of
// that first attempt, and no client, if it fails; a later loss is retried by
// the client, with the waits of [ClientConfig].
func Dial(ctx context.Context, addr string, cfg ClientConfig) (*Client, error) {
	c, err := NewClient(addr, cfg)
	if err != nil {
		return nil, err
	}
	s, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.install(s)
	c.mu.Unlock()
	c.runDone = make(chan struct{})
	c.running = true
	go func() {
		defer close(c.runDone)
		c.run(context.Background(), s)
	}()
	return c, nil
}

// Run connects and reconnects until ctx is done or the client is closed, and
// returns then, with every connection closed. Between attempts it waits with
// exponential backoff from BackoffMin to BackoffMax, jittered; a lost
// connection is retried after the first wait. Run is for a client made by
// [NewClient]; call it once.
func (c *Client) Run(ctx context.Context) error {
	c.mu.Lock()
	if c.running || c.closed {
		c.mu.Unlock()
		return errors.New("seatgate: the client is already running or closed")
	}
	c.running = true
	c.mu.Unlock()
	c.run(ctx, nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrClosed
}

// Close ends the client: its connection is closed, calls in progress return
// ErrClosed, and so does every later call. It waits for the goroutine of
// [Dial], if the client has one.
func (c *Client) Close() error {
	c.closeMu.Do(func() {
		c.mu.Lock()
		c.closed = true
		s := c.sess
		c.mu.Unlock()
		close(c.stop)
		if s != nil {
			s.fail(ErrClosed)
		}
	})
	if c.runDone != nil {
		<-c.runDone
	}
	return nil
}

// Connected reports whether there is a connection to the gate now (its hello
// answered) and since when.
func (c *Client) Connected() (since time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return time.Time{}, false
	}
	return c.since, true
}

// Caps is the capability list of the gate, from the hello of the current
// connection, or of the last one; nil if there has been none.
func (c *Client) Caps() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.caps...)
}

// LastError is the reason the client is not connected, or last was not: the
// error of the latest failed attempt or lost connection. It holds only fixed
// texts. It is nil before the first failure.
func (c *Client) LastError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// install makes s the connection of the client. The caller holds c.mu.
func (c *Client) install(s *session) {
	c.sess, c.since, c.caps, c.lastErr = s, c.cfg.Now(), s.caps, nil
}

func (c *Client) setLast(err error) {
	c.mu.Lock()
	c.lastErr = err
	c.mu.Unlock()
}

// run is the supervisor. first is a session already connected, or nil.
func (c *Client) run(ctx context.Context, first *session) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	attempt := 0
	for ctx.Err() == nil {
		s := first
		first = nil
		if s == nil {
			var err error
			if s, err = c.connect(ctx); err != nil {
				c.setLast(err)
				if c.cfg.Sleep(ctx, backoff(c.cfg.BackoffMin, c.cfg.BackoffMax, attempt, c.cfg.Rand())) != nil {
					return
				}
				attempt++
				continue
			}
		}
		attempt = 0
		c.mu.Lock()
		if c.sess != s {
			if c.closed {
				c.mu.Unlock()
				s.fail(ErrClosed)
				s.wait()
				return
			}
			c.install(s)
		}
		c.mu.Unlock()
		select {
		case <-s.dead:
		case <-ctx.Done():
			s.fail(ErrClosed)
		}
		c.mu.Lock()
		c.sess = nil
		if err := s.cause(); err != nil && !errors.Is(err, ErrClosed) {
			c.lastErr = err
		}
		c.mu.Unlock()
		s.wait()
		if ctx.Err() != nil {
			return
		}
		if c.cfg.Sleep(ctx, backoff(c.cfg.BackoffMin, c.cfg.BackoffMax, 0, c.cfg.Rand())) != nil {
			return
		}
	}
}

// Call makes one request over the current connection and waits for its answer.
// params is encoded as a JSON object (nil for none); result, if not nil, is
// decoded from the answer with [DecodeStrict].
//
// It returns [ErrDisconnected] at once if there is no connection, or when the
// connection is lost while it waits; a [*RemoteError] if the gate refused with
// a code; ctx's error if ctx is done first (the answer, if it comes, is
// dropped); and [ErrClosed] after Close. It never waits longer than ctx allows.
func (c *Client) Call(ctx context.Context, op string, params any, result any) error {
	if op == OpHello || op == OpPing {
		return ErrReserved
	}
	return c.call(ctx, op, params, result)
}

func (c *Client) call(ctx context.Context, op string, params any, result any) error {
	if !ValidOp(op) {
		return ErrBadCall
	}
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil || !isObject(b) {
			return ErrBadCall
		}
		raw = b
	}
	c.mu.Lock()
	s, closed := c.sess, c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if s == nil {
		return ErrDisconnected
	}
	r, err := s.call(ctx, op, raw)
	if err != nil {
		return err
	}
	if r.Code != "" {
		return &RemoteError{Code: r.Code}
	}
	if result != nil {
		return DecodeStrict(r.Result, result)
	}
	return nil
}

// session is one connection, from after its hello.
type session struct {
	c    *Client
	tc   *tls.Conn
	raw  net.Conn // closed to end the session: a TLS close would wait to send an alert to a peer that does not read
	caps []string

	wsem chan struct{} // holds one token; whoever has it may write

	mu      sync.Mutex
	pending map[uint64]chan response
	issued  uint64 // the highest id used

	dead chan struct{}
	err  error
	wg   sync.WaitGroup
}

// connect opens a connection, says hello and, if the gate answers it, starts
// the reader and the pinger of the new session.
func (c *Client) connect(ctx context.Context) (*session, error) {
	cfg := c.cfg
	dctx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	raw, err := cfg.Dial(dctx, c.addr)
	if err != nil {
		cancel()
		return nil, errors.New("seatgate: cannot connect")
	}
	tc := tls.Client(raw, cfg.TLS)
	raw.SetDeadline(time.Now().Add(cfg.DialTimeout))
	err = tc.HandshakeContext(dctx)
	cancel()
	if err != nil {
		raw.Close()
		return nil, errors.New("seatgate: the TLS handshake failed")
	}
	// The hello: the one request outside the session's own bookkeeping, id 1.
	req, _ := request{ID: 1, Op: OpHello, Params: hello{Proto: ProtoMajor, Caps: cfg.Caps, Role: "seat"}.encode()}.encode()
	raw.SetDeadline(time.Time{})
	raw.SetWriteDeadline(time.Now().Add(cfg.WriteTimeout))
	fail := func(err error) (*session, error) { raw.Close(); return nil, err }
	if err := writeFrame(tc, req); err != nil {
		return fail(errors.New("seatgate: cannot send the hello"))
	}
	// The wait for the answer ends with ctx too.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			raw.Close()
		case <-stopWatch:
		}
	}()
	p, err := readFrameTimed(tc, time.Now, cfg.HelloTimeout, cfg.FrameTimeout)
	if err != nil {
		return fail(errors.New("seatgate: no answer to the hello"))
	}
	resp, err := decodeResponse(p)
	if err != nil || resp.ID != 1 {
		return fail(violation("bad answer to the hello"))
	}
	if resp.Code != "" {
		return fail(&RemoteError{Code: resp.Code})
	}
	h, err := decodeHelloResult(resp.Result)
	if err != nil {
		return fail(err)
	}
	s := &session{
		c:       c,
		tc:      tc,
		raw:     raw,
		caps:    h.Caps,
		wsem:    make(chan struct{}, 1),
		pending: map[uint64]chan response{},
		issued:  1,
		dead:    make(chan struct{}),
	}
	s.wsem <- struct{}{}
	s.wg.Add(2)
	go s.reader()
	go s.pinger()
	return s, nil
}

// fail ends the session for err, once: the connection is closed, which ends
// the reader, and every call waiting is released.
func (s *session) fail(err error) {
	s.mu.Lock()
	select {
	case <-s.dead:
		s.mu.Unlock()
		return
	default:
	}
	s.err = err
	close(s.dead)
	s.mu.Unlock()
	s.raw.Close()
}

func (s *session) cause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// wait blocks until the goroutines of the session have ended.
func (s *session) wait() { s.wg.Wait() }

// gone is the error of a call whose session is over.
func (s *session) gone() error {
	if errors.Is(s.cause(), ErrClosed) {
		return ErrClosed
	}
	return ErrDisconnected
}

// reader reads responses until the connection ends or breaks a rule, handing
// each to the call that waits for it. Any frame resets the silence timer:
// readFrameTimed gives the next one DeadAfter to start.
func (s *session) reader() {
	defer s.wg.Done()
	cfg := s.c.cfg
	for {
		p, err := readFrameTimed(s.tc, time.Now, cfg.DeadAfter, cfg.FrameTimeout)
		if err != nil {
			s.fail(errors.New("seatgate: the connection was lost"))
			return
		}
		r, err := decodeResponse(p)
		if err != nil {
			s.fail(err)
			return
		}
		s.mu.Lock()
		ch, ok := s.pending[r.ID]
		delete(s.pending, r.ID)
		unknown := !ok && r.ID > s.issued
		s.mu.Unlock()
		if unknown {
			s.fail(violation("an answer to a request never made"))
			return
		}
		if ok {
			ch <- r
		}
	}
}

// pinger sends seat.ping every PingInterval. Its answers are what keeps the
// reader's silence timer from running out on an idle connection; it cares for
// nothing else, since a lost connection is found by the reader.
func (s *session) pinger() {
	defer s.wg.Done()
	cfg := s.c.cfg
	t := time.NewTicker(cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.dead:
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), cfg.DeadAfter)
		s.call(ctx, OpPing, nil)
		cancel()
	}
}

// call sends one request and waits for its answer, for ctx and for the
// session's end.
func (s *session) call(ctx context.Context, op string, params json.RawMessage) (response, error) {
	if err := ctx.Err(); err != nil {
		return response{}, err
	}
	ch := make(chan response, 1)
	s.mu.Lock()
	select {
	case <-s.dead:
		s.mu.Unlock()
		return response{}, s.gone()
	default:
	}
	s.issued++
	id := s.issued
	s.pending[id] = ch
	s.mu.Unlock()
	drop := func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}
	payload, err := request{ID: id, Op: op, Params: params}.encode()
	if err != nil || len(payload) > MaxFrame {
		drop()
		return response{}, ErrBadCall
	}
	select {
	case <-s.wsem:
	case <-ctx.Done():
		drop()
		return response{}, ctx.Err()
	case <-s.dead:
		drop()
		return response{}, s.gone()
	}
	if err := ctx.Err(); err != nil { // not a reason to give up the link
		s.wsem <- struct{}{}
		drop()
		return response{}, err
	}
	// A write that the peer does not take ends at WriteTimeout, and at once when
	// ctx is done: the deadline is moved to now, the write fails and, as a frame
	// may be half sent, the connection is given up.
	s.tc.SetWriteDeadline(time.Now().Add(s.c.cfg.WriteTimeout))
	stopWrite := context.AfterFunc(ctx, func() { s.tc.SetWriteDeadline(time.Now()) })
	err = writeFrame(s.tc, payload)
	stopWrite()
	s.wsem <- struct{}{}
	if err != nil {
		drop()
		s.fail(errors.New("seatgate: the connection was lost"))
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, ErrDisconnected
	}
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		drop()
		return response{}, ctx.Err()
	case <-s.dead:
		drop()
		select { // an answer that came with the end
		case r := <-ch:
			return r, nil
		default:
		}
		return response{}, s.gone()
	}
}
