//go:build linux

package serve

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

func TestMain(m *testing.M) {
	fakeagent.RunIfChild()
	os.Exit(m.Run())
}

const patience = 20 * time.Second

const testEpoch = 7

// env is one holder under test: a real session on a fake agent, its event log
// and the service on its sockets.
type env struct {
	t        *testing.T
	root     string
	dir      string // the runtime folder
	logDir   string
	lockPath string // the daemon's lock file
	daemon   *os.File
	log      *eventlog.Log
	sess     *session.Session
	srv      *Server
	served   chan error
	cfg      Config
}

type envOpts struct {
	cfg     func(*Config)
	session func(*session.Config)
	// noServe makes the server but does not run Serve.
	noServe bool
}

// newBase starts the fake agent on script and prepares the folders, the log and
// the config, but not the service.
func newBase(t *testing.T, script string, mods ...func(*envOpts)) (*env, *envOpts) {
	t.Helper()
	o := &envOpts{}
	for _, m := range mods {
		m(o)
	}
	root := t.TempDir()
	e := &env{t: t, root: root, served: make(chan error, 1)}
	for _, d := range []struct {
		p *string
		n string
	}{{&e.dir, "rt"}, {&e.logDir, "log"}} {
		*d.p = filepath.Join(root, d.n)
		if err := os.Mkdir(*d.p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	var err error
	if e.log, err = eventlog.Open(e.logDir, eventlog.Options{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.log.Close() })

	argv, env := fakeagent.Command(script)
	sc := session.Config{
		Role: "proj/lead", Epoch: strconv.Itoa(testEpoch), Argv: argv, Env: env, Cols: 100, Rows: 30,
		RoleLock: filepath.Join(root, "lead.lock"), StateDir: state, Log: e.log,
		StopGrace: 300 * time.Millisecond, PTY: ptyhost.Config{NoCgroup: true},
	}
	if o.session != nil {
		o.session(&sc)
	}
	if e.sess, err = session.Start(sc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.sess.Close() })

	e.lockPath = filepath.Join(root, "daemon.lock")
	e.holdDaemonLock(os.Getpid())

	e.cfg = Config{
		Session: e.sess, Dir: e.dir, Lead: "proj-lead", Epoch: testEpoch, Runtime: "fake", RuntimeVersion: "1.2.3",
		Log: e.log, DaemonLock: e.lockPath,
		HandshakeTimeout: 3 * time.Second, FlushWait: 3 * time.Second, Stall: 10 * time.Second,
		PollInterval: 20 * time.Millisecond,
	}
	if o.cfg != nil {
		o.cfg(&e.cfg)
	}
	return e, o
}

// newEnv is newBase and then the service, serving.
func newEnv(t *testing.T, script string, mods ...func(*envOpts)) *env {
	t.Helper()
	e, o := newBase(t, script, mods...)
	e.start(!o.noServe)
	return e
}

// start makes the service; with serve it runs Serve. The cleanup closes it.
func (e *env) start(serve bool) {
	e.t.Helper()
	var err error
	if e.srv, err = New(e.cfg); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { e.srv.Close() })
	if serve {
		go func() { e.served <- e.srv.Serve() }()
	}
}

// holdDaemonLock writes pid in the daemon's lock file and locks it from an open
// file of its own, as the daemon would.
func (e *env) holdDaemonLock(pid int) {
	e.t.Helper()
	if e.daemon != nil {
		e.daemon.Close()
	}
	if err := os.WriteFile(e.lockPath, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	f, err := os.Open(e.lockPath)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		e.t.Fatal(err)
	}
	e.daemon = f
	e.t.Cleanup(func() { f.Close() })
}

// releaseDaemonLock drops the lock and leaves the pid in the file.
func (e *env) releaseDaemonLock() {
	e.t.Helper()
	if e.daemon != nil {
		e.daemon.Close()
		e.daemon = nil
	}
}

// sockDial connects to a socket in the folder through /proc/self/fd, which keeps
// the path short.
func sockDial(dir, name string) (*net.UnixConn, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	addr := &net.UnixAddr{Name: fmt.Sprintf("/proc/self/fd/%d/%s", fd, name), Net: "unix"}
	return net.DialUnix("unix", nil, addr)
}

func (e *env) dialAttach() *net.UnixConn {
	e.t.Helper()
	c, err := sockDial(e.dir, AttachSocket)
	if err != nil {
		e.t.Fatalf("dial attach.sock: %v", err)
	}
	e.t.Cleanup(func() { c.Close() })
	return c
}

// control sends one request and reads the response. A refused peer shows as an
// error: the connection is closed without a response.
func (e *env) control(req wire.Request) (wire.Response, error) {
	e.t.Helper()
	c, err := sockDial(e.dir, ControlSocket)
	if err != nil {
		return wire.Response{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(patience))
	p, err := wire.EncodeRequest(req)
	if err != nil {
		e.t.Fatalf("encode the request: %v", err)
	}
	if err := wire.Write(c, wire.ControlRequests, wire.TypeRequest, p); err != nil {
		return wire.Response{}, err
	}
	f, err := wire.NewReader(c, wire.ControlResponses).Next()
	if err != nil {
		return wire.Response{}, err
	}
	return wire.DecodeResponse(f.Payload)
}

func (e *env) mustControl(req wire.Request) wire.Response {
	e.t.Helper()
	r, err := e.control(req)
	if err != nil {
		e.t.Fatalf("control %s: %v", req.Op, err)
	}
	return r
}

// newTicket makes a ticket and registers it for role.
func (e *env) newTicket(role wire.Role) string {
	e.t.Helper()
	tk, err := wire.NewTicket()
	if err != nil {
		e.t.Fatal(err)
	}
	r := e.mustControl(wire.Request{Op: wire.OpTicket, Epoch: testEpoch, Role: role, Ticket: tk})
	if !r.OK {
		e.t.Fatalf("register a %s ticket: %+v", role, r)
	}
	return tk
}

// eventsNow reads the event log written so far.
func (e *env) eventsNow() []eventlog.Event {
	e.t.Helper()
	evs, err := eventlog.Read(e.logDir)
	if err != nil && !errors.Is(err, eventlog.ErrCorrupt) {
		e.t.Fatalf("read the event log: %v", err)
	}
	return evs
}

// waitEvent waits for an event that match accepts, and returns it. Events are
// written by a goroutine of their own, a moment after what they record.
func (e *env) waitEvent(what string, match func(eventlog.Event) bool) eventlog.Event {
	e.t.Helper()
	var found eventlog.Event
	poll(e.t, func() string { return "an event: " + what }, func() bool {
		for _, ev := range e.eventsNow() {
			if match(ev) {
				found = ev
				return true
			}
		}
		return false
	})
	return found
}

func named(name eventlog.EventName, reason string) func(eventlog.Event) bool {
	return func(ev eventlog.Event) bool { return ev.Event == name && (reason == "" || ev.Reason == reason) }
}

func (e *env) count(name string) uint64 { return e.sess.Counters()[name] }

func (e *env) waitCount(name string, atLeast uint64) {
	e.t.Helper()
	poll(e.t, func() string { return fmt.Sprintf("counter %s >= %d (is %d)", name, atLeast, e.count(name)) }, func() bool {
		return e.count(name) >= atLeast
	})
}

// poll calls f until it returns true or the deadline passes.
func poll(t testing.TB, what func() string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func paneText(p *sessionpane.Pane) string {
	cols, rows := p.Size()
	out, _ := p.Text(uv.Pos(0, 0), uv.Pos(cols-1, rows-1), 1<<20)
	return out
}

func screenText(s *session.Session) string {
	var out string
	s.Screen(func(p *sessionpane.Pane, _ uint64) { out = paneText(p) })
	return out
}

func position(s *session.Session) uint64 {
	var pos uint64
	s.Screen(func(_ *sessionpane.Pane, p uint64) { pos = p })
	return pos
}

func waitScreen(t *testing.T, s *session.Session, want string) {
	t.Helper()
	var last string
	poll(t, func() string { return fmt.Sprintf("the screen to show %q; last screen %q", want, last) }, func() bool {
		last = screenText(s)
		return strings.Contains(last, want)
	})
}

// waitQuiet waits until the session's position has not moved for a moment.
// waitPosition waits until the session has produced n bytes of output.
func waitPosition(t *testing.T, s *session.Session, n uint64) {
	t.Helper()
	poll(t, func() string { return fmt.Sprintf("position %d (at %d)", n, position(s)) }, func() bool { return position(s) >= n })
}

func waitQuiet(t *testing.T, s *session.Session) {
	t.Helper()
	last := position(s)
	stable := 0
	poll(t, func() string { return "the output to go quiet" }, func() bool {
		now := position(s)
		if now == last {
			stable++
		} else {
			stable, last = 0, now
		}
		return stable >= 10
	})
}

func waitDone(t *testing.T, s *session.Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(patience):
		t.Fatalf("the session did not end; facts %+v", s.Facts())
	}
}

// rxBytes decodes what the fake agent's echo step reported on the screen.
func rxBytes(screen string) []byte {
	var all []byte
	for _, line := range strings.Split(screen, "\n") {
		if !strings.HasPrefix(line, "RX:") {
			continue
		}
		var b []byte
		hexs := strings.TrimSpace(strings.TrimPrefix(line, "RX:"))
		for i := 0; i+1 < len(hexs); i += 2 {
			v, err := strconv.ParseUint(hexs[i:i+2], 16, 8)
			if err != nil {
				break
			}
			b = append(b, byte(v))
		}
		all = append(all, b...)
	}
	return all
}

// waitRX waits until the agent has reported exactly want, and no more.
func waitRX(t *testing.T, s *session.Session, want string) {
	t.Helper()
	var got []byte
	poll(t, func() string { return fmt.Sprintf("the agent to receive %q; got %q", want, got) }, func() bool {
		got = rxBytes(screenText(s))
		return len(got) >= len(want)
	})
	if string(got) != want {
		t.Fatalf("the agent received %q, want %q", got, want)
	}
}

// ---- the test client ----

// tclient is an attach client written from wire and sessionpane: it restores a
// snapshot into its own pane, feeds it the Output frames, and compares each
// Check with its own digest. It sends no reply to the agent: its pane's
// replies are only counted.
type tclient struct {
	t    *testing.T
	conn *net.UnixConn
	w    *wire.Writer

	mu        sync.Mutex
	pane      *sessionpane.Pane
	pos       uint64
	haveSnap  bool
	snapshots int
	snapSizes []int
	welcome   *wire.Welcome
	notices   []wire.Notice
	modes     []wire.Modes
	sizes     []wire.Size
	states    []wire.State
	roles     []wire.RoleInfo
	checks    []wire.Check
	checkOK   int
	exit      *wire.Exit
	frames    []byte // the type of every frame, in order
	problems  []string
	outBytes  uint64
	replies   atomic.Int64
	end       error
	closed    chan struct{}

	gateMu           sync.Mutex
	gateCh           chan struct{}
	holdAfterWelcome bool
}

type helloOpts struct {
	name   string
	cols   int
	rows   int
	colors wire.Colors
	// holdAfterWelcome makes the client stop reading after the Welcome, until
	// resume.
	holdAfterWelcome bool
}

func defaultHello() helloOpts { return helloOpts{name: "test-console", cols: 80, rows: 24} }

// attach registers a ticket for role, connects, says Hello and reads the
// Welcome. It does not wait for the snapshot.
func (e *env) attach(role wire.Role, mods ...func(*helloOpts)) *tclient {
	e.t.Helper()
	tk := e.newTicket(role)
	return e.attachWith(tk, mods...)
}

func (e *env) attachWith(ticket string, mods ...func(*helloOpts)) *tclient {
	e.t.Helper()
	c := e.attachWithNoWait(ticket, mods...)
	c.waitFor("the Welcome", func() bool { return c.welcome != nil || c.end != nil })
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.welcome == nil {
		e.t.Fatalf("no Welcome: the connection ended with %v; notices %+v", c.end, c.notices)
	}
	return c
}

// attachWithNoWait says Hello and returns at once, for a Hello that is refused.
func (e *env) attachWithNoWait(ticket string, mods ...func(*helloOpts)) *tclient {
	e.t.Helper()
	h := defaultHello()
	for _, m := range mods {
		m(&h)
	}
	conn := e.dialAttach()
	c := e.newClient(conn)
	c.holdAfterWelcome = h.holdAfterWelcome
	hp, err := wire.EncodeHello(wire.Hello{
		Version: wire.ProtocolVersion, Ticket: ticket, Name: h.name, Columns: h.cols, Rows: h.rows, Colors: h.colors,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := wire.Write(conn, wire.ToHolder, wire.TypeHello, hp); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) newClient(conn *net.UnixConn) *tclient {
	c := &tclient{t: e.t, conn: conn, w: wire.NewWriter(conn, wire.ToHolder), closed: make(chan struct{})}
	// The reply callback runs on the pane's own goroutine while the reader
	// holds c.mu inside Write: it must not take it.
	c.pane = sessionpane.New(80, 24, sessionpane.Options{Replies: func([]byte) { c.replies.Add(1) }})
	go c.run()
	e.t.Cleanup(func() {
		c.resume()
		conn.Close()
		<-c.closed
		c.pane.Close()
	})
	return c
}

func (c *tclient) fail(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
}

func (c *tclient) run() {
	defer close(c.closed)
	rd := wire.NewReader(c.conn, wire.ToClient)
	for {
		if g := c.gate(); g != nil {
			<-g // a paused client does not read
		}
		f, err := rd.Next()
		c.mu.Lock()
		if err != nil {
			c.end = err
			c.mu.Unlock()
			return
		}
		c.frames = append(c.frames, f.Type)
		c.handle(f)
		hold := c.holdAfterWelcome && f.Type == wire.TypeWelcome
		c.mu.Unlock()
		if hold {
			c.pause() // the next turn of the loop waits at the gate until resume
		}
	}
}

// pause stops the client from reading after the frame it is reading; resume
// lets it go on.
func (c *tclient) pause() {
	c.gateMu.Lock()
	defer c.gateMu.Unlock()
	if c.gateCh == nil {
		c.gateCh = make(chan struct{})
	}
}

func (c *tclient) resume() {
	c.gateMu.Lock()
	defer c.gateMu.Unlock()
	if c.gateCh != nil {
		close(c.gateCh)
		c.gateCh = nil
	}
}

func (c *tclient) gate() chan struct{} {
	c.gateMu.Lock()
	defer c.gateMu.Unlock()
	return c.gateCh
}

// handle applies one frame; c.mu is held.
func (c *tclient) handle(f wire.Frame) {
	switch f.Type {
	case wire.TypeWelcome:
		w, err := wire.DecodeWelcome(f.Payload)
		if err != nil {
			c.fail("welcome: %v", err)
			return
		}
		c.welcome = &w
	case wire.TypeSnapshot:
		pos, snap, err := wire.DecodeSnapshot(f.Payload)
		if err != nil {
			c.fail("snapshot frame: %v", err)
			return
		}
		if err := c.pane.Restore(snap); err != nil {
			c.fail("restore: %v", err)
			return
		}
		c.pos, c.haveSnap = pos, true
		c.snapshots++
		c.snapSizes = append(c.snapSizes, len(snap))
	case wire.TypeOutput:
		pos, data, err := wire.DecodeOutput(f.Payload)
		if err != nil {
			c.fail("output frame: %v", err)
			return
		}
		if !c.haveSnap {
			c.fail("output at %d before any snapshot", pos)
			return
		}
		if pos != c.pos {
			c.fail("output at position %d, the client is at %d", pos, c.pos)
			c.pos = pos
		}
		c.pane.Write(data)
		c.pos += uint64(len(data))
		c.outBytes += uint64(len(data))
	case wire.TypeModes:
		m, err := wire.DecodeModes(f.Payload)
		if err != nil {
			c.fail("modes: %v", err)
			return
		}
		c.modes = append(c.modes, m)
	case wire.TypeSize:
		sz, err := wire.DecodeSize(f.Payload)
		if err != nil {
			c.fail("size: %v", err)
			return
		}
		c.sizes = append(c.sizes, sz)
		c.pane.Resize(sz.Columns, sz.Rows)
	case wire.TypeState:
		st, err := wire.DecodeState(f.Payload)
		if err != nil {
			c.fail("state: %v", err)
			return
		}
		c.states = append(c.states, st)
	case wire.TypeRole:
		r, err := wire.DecodeRole(f.Payload)
		if err != nil {
			c.fail("role: %v", err)
			return
		}
		c.roles = append(c.roles, r)
	case wire.TypeCheck:
		ck, err := wire.DecodeCheck(f.Payload)
		if err != nil {
			c.fail("check: %v", err)
			return
		}
		c.checks = append(c.checks, ck)
		if ck.Position != c.pos {
			c.fail("check for position %d at client position %d", ck.Position, c.pos)
			return
		}
		if got := wire.DigestHex(c.pane.Digest()); got != ck.Digest {
			c.fail("check at %d: digest %s, the client has %s", ck.Position, ck.Digest, got)
			return
		}
		c.checkOK++
	case wire.TypeNotice:
		n, err := wire.DecodeNotice(f.Payload)
		if err != nil {
			c.fail("notice: %v", err)
			return
		}
		c.notices = append(c.notices, n)
	case wire.TypeExit:
		x, err := wire.DecodeExit(f.Payload)
		if err != nil {
			c.fail("exit: %v", err)
			return
		}
		c.exit = &x
	default:
		c.fail("frame of type %q", f.Type)
	}
}

func (c *tclient) waitFor(what string, cond func() bool) {
	c.t.Helper()
	poll(c.t, func() string {
		c.mu.Lock()
		defer c.mu.Unlock()
		return fmt.Sprintf("%s (end %v; notices %+v; problems %v; snapshots %d)", what, c.end, c.notices, c.problems, c.snapshots)
	}, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return cond()
	})
}

func (c *tclient) waitSnapshots(n int) {
	c.t.Helper()
	c.waitFor(fmt.Sprintf("snapshot %d", n), func() bool { return c.snapshots >= n || c.end != nil })
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snapshots < n {
		c.t.Fatalf("the connection ended (%v) with %d snapshots, want %d", c.end, c.snapshots, n)
	}
}

// waitChecks waits until n Checks matched this client's own digest.
func (c *tclient) waitChecks(n int) {
	c.t.Helper()
	c.waitFor(fmt.Sprintf("%d matching checks", n), func() bool { return c.checkOK >= n || len(c.problems) > 0 })
	c.noProblems()
}

func (c *tclient) noProblems() {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.problems) > 0 {
		c.t.Fatalf("the client saw: %v", c.problems)
	}
}

func (c *tclient) waitNotice(r wire.Reason) wire.Notice {
	c.t.Helper()
	var found wire.Notice
	c.waitFor("notice "+string(r), func() bool {
		for _, n := range c.notices {
			if n.Reason == r {
				found = n
				return true
			}
		}
		return false
	})
	return found
}

func (c *tclient) noticeCount(r wire.Reason) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, x := range c.notices {
		if x.Reason == r {
			n++
		}
	}
	return n
}

func (c *tclient) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return paneText(c.pane)
}

func (c *tclient) waitText(want string) {
	c.t.Helper()
	var last string
	poll(c.t, func() string { return fmt.Sprintf("the client's screen to show %q; last %q", want, last) }, func() bool {
		last = c.text()
		return strings.Contains(last, want)
	})
}

func (c *tclient) waitEnd() error {
	c.t.Helper()
	select {
	case <-c.closed:
	case <-time.After(patience):
		c.t.Fatal("the connection did not end")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.end
}

func (c *tclient) send(typ byte, payload []byte) {
	c.t.Helper()
	if err := c.w.WriteFrame(typ, payload); err != nil {
		c.t.Fatalf("send %s: %v", wire.TypeName(typ), err)
	}
}

func (c *tclient) input(b string) { c.send(wire.TypeInput, []byte(b)) }
func (c *tclient) paste(s string) { p, _ := wire.EncodePaste(s); c.send(wire.TypePaste, p) }
func (c *tclient) detach()        { c.send(wire.TypeDetach, nil) }
func (c *tclient) resize(w, h int) {
	p, _ := wire.EncodeResize(wire.Size{Columns: w, Rows: h})
	c.send(wire.TypeResize, p)
}

// lastRoleLocked is lastRole for use inside waitFor, where c.mu is held.
func (c *tclient) lastRoleLocked() wire.RoleInfo {
	if len(c.roles) == 0 {
		return wire.RoleInfo{}
	}
	return c.roles[len(c.roles)-1]
}

func (c *tclient) lastRole() wire.RoleInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.roles) == 0 {
		return wire.RoleInfo{}
	}
	return c.roles[len(c.roles)-1]
}

// inSync waits until the client has the session's position and checks that
// its pane and the session's have the same digest there.
func (e *env) inSync(c *tclient) {
	e.t.Helper()
	deadline := time.Now().Add(patience)
	for {
		var spos uint64
		var sd [32]byte
		e.sess.Screen(func(p *sessionpane.Pane, pos uint64) { spos, sd = pos, p.Digest() })
		c.mu.Lock()
		cpos, cd := c.pos, c.pane.Digest()
		probs := append([]string(nil), c.problems...)
		c.mu.Unlock()
		if len(probs) > 0 {
			e.t.Fatalf("the client saw: %v", probs)
		}
		if cpos == spos {
			if cd != sd {
				e.t.Fatalf("at position %d the client's digest differs from the session's", spos)
			}
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("the client is at %d, the session at %d", cpos, spos)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// ---- leaks ----

func countFDs(t testing.TB) int {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

// settled waits for the descriptors and goroutines to fall back to the given
// counts.
func settled(t testing.TB, fds0, gos0 int) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		fds, gos := countFDs(t), runtime.NumGoroutine()
		if fds <= fds0 && gos <= gos0 {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("descriptors %d -> %d, goroutines %d -> %d\n%s", fds0, fds, gos0, gos, buf)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// rawFrame is a frame header and payload written without wire's checks, for the
// frames the holder must refuse.
func rawFrame(typ byte, declared uint32, payload []byte) []byte {
	b := make([]byte, 0, wire.HeaderLen+len(payload))
	b = append(b, typ)
	b = binary.BigEndian.AppendUint32(b, declared)
	return append(b, payload...)
}

func readAll(c net.Conn) []byte {
	_ = c.SetReadDeadline(time.Now().Add(patience))
	b, _ := io.ReadAll(c)
	return b
}

// fakeClock is a clock that tests move by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// close ends the client and its goroutines.
func (c *tclient) close() {
	c.resume()
	c.conn.Close()
	<-c.closed
	c.pane.Close()
}
