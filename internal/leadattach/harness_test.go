package leadattach_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/sessionpane"
)

const wait = 5 * time.Second

// ---------------------------------------------------------------------------
// The scripted holder: the holder's side of the attach protocol, written from
// wire and sessionpane, with a pane of its own whose screen it snapshots.

type holder struct {
	t     *testing.T
	path  string
	ln    net.Listener
	conns chan *hconn
	// pipes, when set, makes Dial hand out in-memory pipes instead of unix
	// sockets: writes block until the holder reads, which a test of a stuck
	// holder needs.
	pipes bool
	mu    sync.Mutex
	all   []*hconn
	panes []*sessionpane.Pane
	done  bool // closeAll ran: no pane is made after it
}

func newHolder(t *testing.T) *holder {
	t.Helper()
	dir, err := os.MkdirTemp("", "la")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &holder{t: t, path: filepath.Join(dir, "a.sock"), conns: make(chan *hconn, 16)}
	ln, err := net.Listen("unix", h.path)
	if err != nil {
		t.Fatal(err)
	}
	h.ln = ln
	accepting := make(chan struct{})
	t.Cleanup(func() {
		ln.Close()
		<-accepting
		h.closeAll()
	})
	go func() {
		defer close(accepting)
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			h.adopt(nc)
		}
	}()
	return h
}

// newPipeHolder is a holder that Dial reaches over net.Pipe.
func newPipeHolder(t *testing.T) *holder {
	t.Helper()
	h := &holder{t: t, path: "pipe", conns: make(chan *hconn, 16), pipes: true}
	t.Cleanup(h.closeAll)
	return h
}

// closeAll closes every connection and pane the holder made.
func (h *holder) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.done = true
	for _, c := range h.all {
		c.resume()
		c.nc.Close()
	}
	for _, p := range h.panes {
		p.Close()
	}
}

func (h *holder) dial(ctx context.Context, path string) (net.Conn, error) {
	if !h.pipes {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
	a, b := net.Pipe()
	h.adopt(b)
	return a, nil
}

func (h *holder) adopt(nc net.Conn) {
	hc := &hconn{t: h.t, nc: nc, frames: make(chan hframe, 256)}
	h.mu.Lock()
	h.all = append(h.all, hc)
	h.mu.Unlock()
	// A connection is published only when its first frame has come: the
	// client dials before it asks for a ticket, and a connection it closes
	// for want of one has no Hello and is not a connection of the test. The
	// pane is made then, and closed with the holder.
	hc.publish = func() {
		h.mu.Lock()
		if h.done {
			h.mu.Unlock()
			return
		}
		hc.pane = sessionpane.New(80, 24, sessionpane.Options{})
		h.panes = append(h.panes, hc.pane)
		h.mu.Unlock()
		h.conns <- hc
	}
	go hc.readLoop()
}

// accept returns the next connection the client made, with its Hello read.
func (h *holder) accept() *hconn {
	h.t.Helper()
	select {
	case hc := <-h.conns:
		f := hc.next()
		if f.typ != wire.TypeHello {
			h.t.Fatalf("first frame is %s, want hello", wire.TypeName(f.typ))
		}
		hello, err := wire.DecodeHello(f.payload)
		if err != nil {
			h.t.Fatalf("hello: %v", err)
		}
		hc.hello = hello
		return hc
	case <-time.After(wait):
		h.t.Fatal("the client did not connect")
	}
	return nil
}

// noConnection fails when the client connects within d.
func (h *holder) noConnection(d time.Duration) {
	h.t.Helper()
	select {
	case <-h.conns:
		h.t.Fatal("the client connected")
	case <-time.After(d):
	}
}

type hframe struct {
	typ     byte
	payload []byte
	err     error
}

type hconn struct {
	gate   atomic.Pointer[chan struct{}]
	t      *testing.T
	nc     net.Conn
	pane   *sessionpane.Pane
	hello  wire.Hello
	pos    uint64
	frames chan hframe
	// publish hands the connection to accept, once, with its first frame.
	publish func()
}

// pause makes the holder stop reading after the frame it is reading; a client
// that writes more blocks on a pipe. resume lets it read again.
func (hc *hconn) pause() {
	ch := make(chan struct{})
	hc.gate.Store(&ch)
}

func (hc *hconn) resume() {
	if p := hc.gate.Swap(nil); p != nil {
		close(*p)
	}
}

func (hc *hconn) readLoop() {
	rd := wire.NewReader(hc.nc, wire.ToHolder)
	first := true
	for {
		if p := hc.gate.Load(); p != nil {
			<-*p
		}
		f, err := rd.Next()
		if err != nil {
			hc.frames <- hframe{err: err}
			return
		}
		hc.frames <- hframe{typ: f.Type, payload: f.Payload}
		if first {
			first = false
			hc.publish()
		}
	}
}

// next returns the next frame the client sent.
func (hc *hconn) next() hframe {
	hc.t.Helper()
	select {
	case f := <-hc.frames:
		if f.err != nil {
			hc.t.Fatalf("connection ended: %v", f.err)
		}
		return f
	case <-time.After(wait):
		hc.t.Fatal("the client sent no frame")
	}
	return hframe{}
}

// closed waits for the client to close the connection, and returns the types
// of the frames that came before.
func (hc *hconn) closed() []byte {
	hc.t.Helper()
	var types []byte
	for {
		select {
		case f := <-hc.frames:
			if f.err != nil {
				return types
			}
			types = append(types, f.typ)
		case <-time.After(wait):
			hc.t.Fatal("the client did not close the connection")
		}
	}
}

func (hc *hconn) send(typ byte, payload []byte) {
	hc.t.Helper()
	if err := wire.Write(hc.nc, wire.ToClient, typ, payload); err != nil {
		hc.t.Fatalf("write %s: %v", wire.TypeName(typ), err)
	}
}

// enc is for encoding a payload in one expression: enc(t)(wire.EncodeX(...)).
func enc(t *testing.T) func([]byte, error) []byte {
	return func(b []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
}

func (hc *hconn) welcome(role wire.Role) {
	hc.t.Helper()
	w := wire.Welcome{Version: wire.ProtocolVersion, Role: role, Lead: "lead-one", Epoch: 3, Runtime: "claude", RuntimeVersion: "1.2.3", Caps: wire.DefaultCaps()}
	hc.send(wire.TypeWelcome, enc(hc.t)(wire.EncodeWelcome(w)))
}

func (hc *hconn) snapshot() {
	hc.t.Helper()
	snap, err := hc.pane.AppendSnapshot(nil)
	if err != nil {
		hc.t.Fatalf("snapshot: %v", err)
	}
	hc.send(wire.TypeSnapshot, enc(hc.t)(wire.EncodeSnapshot(hc.pos, snap)))
}

// write feeds the holder's pane and the client the same bytes.
func (hc *hconn) write(data string) { hc.corrupt(data, data) }

// corrupt feeds the holder's pane applied and tells the client sent.
func (hc *hconn) corrupt(applied, sent string) {
	hc.t.Helper()
	hc.pane.Write([]byte(applied))
	hc.send(wire.TypeOutput, enc(hc.t)(wire.EncodeOutput(hc.pos, []byte(sent))))
	hc.pos += uint64(len(sent))
}

func (hc *hconn) check() {
	hc.t.Helper()
	hc.checkAt(hc.pos)
}

func (hc *hconn) checkAt(pos uint64) {
	hc.t.Helper()
	c := wire.Check{Position: pos, Digest: wire.DigestHex(hc.pane.Digest())}
	hc.send(wire.TypeCheck, enc(hc.t)(wire.EncodeCheck(c)))
}

func (hc *hconn) modes(dec, ansi []int) {
	hc.t.Helper()
	hc.send(wire.TypeModes, enc(hc.t)(wire.EncodeModes(wire.Modes{DEC: dec, ANSI: ansi})))
}

func (hc *hconn) notice(r wire.Reason, text string) {
	hc.t.Helper()
	hc.send(wire.TypeNotice, enc(hc.t)(wire.EncodeNotice(wire.Notice{Reason: r, Text: text})))
}

func (hc *hconn) roleFrame(role wire.Role, writer string) {
	hc.t.Helper()
	ri := wire.RoleInfo{WriterName: writer, Role: role}
	if writer != "" {
		ri.WriterPID, ri.WriterCommand = 42, "console --attach"
	}
	hc.send(wire.TypeRole, enc(hc.t)(wire.EncodeRole(ri)))
}

func (hc *hconn) size(cols, rows int) {
	hc.t.Helper()
	hc.send(wire.TypeSize, enc(hc.t)(wire.EncodeSize(wire.Size{Columns: cols, Rows: rows})))
}

// ---------------------------------------------------------------------------
// The client under test.

// scaledClock runs timers at a hundredth of the time asked, and records what
// was asked.
type scaledClock struct {
	mu   sync.Mutex
	durs []time.Duration
}

type scaledTimer struct{ t *time.Timer }

func (s scaledTimer) C() <-chan time.Time { return s.t.C }
func (s scaledTimer) Stop() bool          { return s.t.Stop() }

func (c *scaledClock) NewTimer(d time.Duration) leadattach.Timer {
	c.mu.Lock()
	c.durs = append(c.durs, d)
	c.mu.Unlock()
	return scaledTimer{time.NewTimer(d / 100)}
}

func (c *scaledClock) asked() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.durs...)
}

// tickets is a ticket source the test fills.
type tickets struct {
	mu    sync.Mutex
	queue []string
	calls int
	err   error
	given []string
}

func (s *tickets) give(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < n; i++ {
		t, err := wire.NewTicket()
		if err != nil {
			panic(err)
		}
		s.queue = append(s.queue, t)
		s.given = append(s.given, t)
	}
}

func (s *tickets) get() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(s.queue) == 0 {
		if s.err != nil {
			return "", s.err
		}
		return "", leadattach.ErrNoTicket
	}
	t := s.queue[0]
	s.queue = s.queue[1:]
	return t, nil
}

func (s *tickets) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *tickets) ticket(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.given[i]
}

type env struct {
	t       *testing.T
	h       *holder
	c       *leadattach.Client
	pane    *sessionpane.Pane
	tickets *tickets
	clock   *scaledClock
	wake    chan struct{}
	sink    sessionpane.InputSink
}

type setup struct {
	mode       leadattach.Mode
	pipe       bool
	shiftEnter leadattach.ShiftEnter
	write      time.Duration
	handshake  time.Duration
	noStart    bool
	tickets    int
	mutate     func(*leadattach.Config)
}

func newEnv(t *testing.T, s setup) *env {
	t.Helper()
	e := &env{t: t, tickets: &tickets{}, clock: &scaledClock{}, wake: make(chan struct{}, 1)}
	if s.pipe {
		e.h = newPipeHolder(t)
	} else {
		e.h = newHolder(t)
	}
	e.pane = sessionpane.New(80, 24, sessionpane.Options{})
	t.Cleanup(e.pane.Close)
	cfg := leadattach.Config{
		Socket: e.h.path, Mode: s.mode, Name: "test console", Tickets: e.tickets.get, Dial: e.h.dial,
		Clock: e.clock, ShiftEnter: s.shiftEnter, WriteTimeout: s.write, HandshakeTimeout: s.handshake,
	}
	if s.mutate != nil {
		s.mutate(&cfg)
	}
	e.c = leadattach.New(cfg)
	t.Cleanup(e.c.Close)
	e.tickets.give(s.tickets)
	if s.noStart {
		return e
	}
	e.start()
	return e
}

func (e *env) start() {
	e.t.Helper()
	sink, err := e.c.Start(terminalconsole.LiveStart{
		Pane: e.pane, Cols: 80, Rows: 24,
		Colours: sessionpane.NeutralColours,
		Wake: func() {
			// What the cockpit does: read the status on a wake-up.
			e.c.Status()
			select {
			case e.wake <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.sink = sink
}

// until waits for cond on the client's status.
func (e *env) until(what string, cond func(terminalconsole.LiveStatus) bool) terminalconsole.LiveStatus {
	e.t.Helper()
	deadline := time.After(wait)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		st := e.c.Status()
		if cond(st) {
			return st
		}
		select {
		case <-e.wake:
		case <-tick.C:
		case <-deadline:
			e.t.Fatalf("timed out waiting for %s; status %+v", what, st)
		}
	}
}

func (e *env) untilConn(c terminalconsole.LiveConn) terminalconsole.LiveStatus {
	e.t.Helper()
	return e.until(c.String(), func(st terminalconsole.LiveStatus) bool { return st.Conn == c })
}

// attach gives a ticket, accepts the connection, welcomes it with role and
// sends the first snapshot (of the holder's empty pane) at position 0.
func (e *env) attach(role wire.Role) *hconn {
	e.t.Helper()
	e.tickets.give(1)
	hc := e.h.accept()
	hc.welcome(role)
	hc.snapshot()
	e.untilConn(terminalconsole.LiveAttached)
	e.until("the snapshot", func(st terminalconsole.LiveStatus) bool {
		for _, c := range st.Counters {
			if c.Name == "client-snapshots" && c.Value > 0 {
				return true
			}
		}
		return false
	})
	return hc
}

func counter(st terminalconsole.LiveStatus, name string) uint64 {
	for _, c := range st.Counters {
		if c.Name == name {
			return c.Value
		}
	}
	return 0
}

func (e *env) sameScreen(hc *hconn) {
	e.t.Helper()
	deadline := time.Now().Add(wait)
	for e.pane.Digest() != hc.pane.Digest() {
		if time.Now().After(deadline) {
			e.t.Fatal("the client's screen is not the holder's")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func paneText(p *sessionpane.Pane) string {
	cols, rows := p.Size()
	s, _ := p.Text(uvPos(0, 0), uvPos(cols, rows-1), 1<<20)
	return s
}

// leakCheck fails the test when goroutines or descriptors remain after the
// test's cleanups.
func leakCheck(t *testing.T) {
	t.Helper()
	g0, f0 := runtime.NumGoroutine(), openFDs()
	t.Cleanup(func() {
		deadline := time.Now().Add(wait)
		for {
			g, f := runtime.NumGoroutine(), openFDs()
			if g <= g0 && f <= f0 {
				return
			}
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<16)
				buf = buf[:runtime.Stack(buf, true)]
				t.Errorf("leak: goroutines %d -> %d, descriptors %d -> %d\n%s", g0, g, f0, f, buf)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func openFDs() int {
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(ents)
}

var errTest = errors.New("test error")

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func uvPos(x, y int) uv.Position { return uv.Pos(x, y) }

// notices waits until n notices have come, and returns them.
func (e *env) notices(n int) []terminalconsole.LiveNotice {
	e.t.Helper()
	var got []terminalconsole.LiveNotice
	e.until("notices", func(terminalconsole.LiveStatus) bool {
		got = append(got, e.c.Notices()...)
		return len(got) >= n
	})
	return got
}
