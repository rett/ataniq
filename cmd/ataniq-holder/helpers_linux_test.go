//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// holderMainEnv makes the test binary run the real command instead of the
// tests, for the tests that start the holder as a process.
const holderMainEnv = "ATANIQ_HOLDER_TEST_AS_HOLDER"

func TestMain(m *testing.M) {
	fakeagent.RunIfChild()
	if os.Getenv(holderMainEnv) != "" {
		os.Exit(run(os.Args[1:], realDeps()))
	}
	os.Exit(m.Run())
}

const patience = 20 * time.Second

const testEpoch = 7

// syncBuf is a buffer that the holder and the test can use at once.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
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

// argSpec is the command line of a holder, as the launcher builds it: the
// flags in their order, the --env list, and the agent.
type argSpec struct {
	flags   [][2]string
	scratch bool
	env     []string
	agent   []string
	// extraBefore are arguments put before the "--" that no flag takes.
	extraBefore []string
}

func (a *argSpec) flagValue(name string) string {
	for _, f := range a.flags {
		if f[0] == name {
			return f[1]
		}
	}
	return ""
}

func (a *argSpec) set(name, v string) *argSpec {
	for i := range a.flags {
		if a.flags[i][0] == name {
			a.flags[i][1] = v
			return a
		}
	}
	a.flags = append(a.flags, [2]string{name, v})
	return a
}

func (a *argSpec) drop(name string) *argSpec {
	var out [][2]string
	for _, f := range a.flags {
		if f[0] != name {
			out = append(out, f)
		}
	}
	a.flags = out
	return a
}

func (a *argSpec) build() []string {
	var out []string
	if a.scratch {
		out = append(out, "--scratch")
	}
	for _, f := range a.flags {
		out = append(out, "--"+f[0], f[1])
	}
	for _, e := range a.env {
		out = append(out, "--env", e)
	}
	out = append(out, a.extraBefore...)
	out = append(out, "--")
	return append(out, a.agent...)
}

// rig is the folders and the stand-ins of one holder under test.
type rig struct {
	t                                          *testing.T
	root, state, runtime, tree                 string
	roleLock, daemonLock, fakeProc, agentFiles string

	daemon *os.File
	errbuf *syncBuf
	sigc   chan os.Signal

	// ignored is what the fake ignoredSignals reports.
	ignored []syscall.Signal
	// stopGrace, when set, replaces the session's two seconds.
	stopGrace time.Duration

	done chan int
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t, root: root, errbuf: &syncBuf{}, sigc: make(chan os.Signal, 8), done: make(chan int, 1)}
	for _, d := range []struct {
		p    *string
		n    string
		mode os.FileMode
	}{{&r.state, "state", 0o700}, {&r.runtime, "rt", 0o700}, {&r.tree, "tree", 0o755}, {&r.agentFiles, "files", 0o755}} {
		*d.p = filepath.Join(root, d.n)
		if err := os.Mkdir(*d.p, d.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(*d.p, d.mode); err != nil {
			t.Fatal(err)
		}
	}
	r.roleLock = filepath.Join(r.state, "role.lock")
	r.daemonLock = filepath.Join(r.state, "daemon.lock")
	r.fakeProc = filepath.Join(root, "proc")
	r.setTIOCSTI("0\n")
	r.holdDaemonLock()
	return r
}

// setTIOCSTI writes the fake proc root's dev.tty.legacy_tiocsti; an empty
// string removes it.
func (r *rig) setTIOCSTI(content string) {
	r.t.Helper()
	dir := filepath.Join(r.fakeProc, "sys", "dev", "tty")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	p := filepath.Join(dir, "legacy_tiocsti")
	if content == "" {
		os.Remove(p)
		return
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// holdDaemonLock stands in for the daemon: an exclusive flock on the daemon-lock
// file and the pid in it.
func (r *rig) holdDaemonLock() {
	r.t.Helper()
	f, err := os.OpenFile(r.daemonLock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		r.t.Fatal(err)
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		r.t.Fatal(err)
	}
	r.daemon = f
	r.t.Cleanup(func() { f.Close() })
}

// spec is a valid command line for a fake agent running script. The agent's
// environment is the script's marker variable and extra.
func (r *rig) spec(script string, extra ...string) *argSpec {
	argv, env := fakeagent.Command(script)
	a := &argSpec{scratch: true, agent: argv, env: append(env, extra...)}
	for _, f := range [][2]string{
		{"role", "scratch/t"}, {"epoch", strconv.Itoa(testEpoch)}, {"launch-intent", "intent-0123456789abcdef"},
		{"runtime", "scratch"}, {"runtime-version", "0"}, {"profile-digest", "none"},
		{"tree", r.tree}, {"state-dir", r.state}, {"runtime-dir", r.runtime},
		{"role-lock", r.roleLock}, {"daemon-lock", r.daemonLock}, {"cols", "100"}, {"rows", "30"},
	} {
		a.flags = append(a.flags, f)
	}
	return a
}

func (r *rig) deps() deps {
	return deps{
		stderr:   r.errbuf,
		procRoot: r.fakeProc,
		now:      time.Now,
		signals:  func() (<-chan os.Signal, func()) { return r.sigc, func() {} },
		ignoredSignals: func() ([]syscall.Signal, error) {
			return r.ignored, nil
		},
		tweakSession: func(c *session.Config) {
			c.PTY.NoCgroup = true
			if r.stopGrace > 0 {
				c.StopGrace = r.stopGrace
			}
		},
		tweakServe: func(c *serve.Config) {
			c.PollInterval = 20 * time.Millisecond
		},
	}
}

// start runs the holder in a goroutine; wait gets its exit status.
func (r *rig) start(args []string) {
	r.t.Helper()
	go func() { r.done <- run(args, r.deps()) }()
}

// runNow runs the holder and returns its exit status.
func (r *rig) runNow(args []string) int { return run(args, r.deps()) }

// waitReady waits for the holder's ready line.
func (r *rig) waitReady() {
	r.t.Helper()
	poll(r.t, func() string { return "the ready line; stderr: " + r.errbuf.String() }, func() bool {
		select {
		case code := <-r.done:
			r.t.Fatalf("the holder exited with %d before it was ready; stderr: %s", code, r.errbuf.String())
		default:
		}
		return strings.Contains(r.errbuf.String(), "ready:")
	})
}

// wait waits for the holder to exit and returns its status.
func (r *rig) wait() int {
	r.t.Helper()
	select {
	case code := <-r.done:
		return code
	case <-time.After(patience):
		r.t.Fatalf("the holder did not exit; stderr: %s", r.errbuf.String())
	}
	return -1
}

// sockDial connects to a socket in the runtime folder through /proc/self/fd,
// which keeps the path short.
func (r *rig) sockDial(name string) (*net.UnixConn, error) {
	fd, err := unix.Open(r.runtime, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	return net.DialUnix("unix", nil, &net.UnixAddr{Name: fmt.Sprintf("/proc/self/fd/%d/%s", fd, name), Net: "unix"})
}

// control sends one request on control.sock and reads the response.
func (r *rig) control(req wire.Request) (wire.Response, error) {
	r.t.Helper()
	c, err := r.sockDial(serve.ControlSocket)
	if err != nil {
		return wire.Response{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(patience))
	p, err := wire.EncodeRequest(req)
	if err != nil {
		r.t.Fatalf("encode the request: %v", err)
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

func (r *rig) mustControl(req wire.Request) wire.Response {
	r.t.Helper()
	resp, err := r.control(req)
	if err != nil {
		r.t.Fatalf("control %s: %v", req.Op, err)
	}
	return resp
}

func (r *rig) status() wire.Status {
	r.t.Helper()
	resp := r.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch})
	if !resp.OK || resp.Status == nil {
		r.t.Fatalf("status: %+v", resp)
	}
	return *resp.Status
}

func (r *rig) newTicket(role wire.Role) string {
	r.t.Helper()
	tk, err := wire.NewTicket()
	if err != nil {
		r.t.Fatal(err)
	}
	resp := r.mustControl(wire.Request{Op: wire.OpTicket, Epoch: testEpoch, Role: role, Ticket: tk})
	if !resp.OK {
		r.t.Fatalf("register a %s ticket: %+v", role, resp)
	}
	return tk
}

// tclient is a small attach client: it keeps a pane of its own in step with
// the holder's frames.
type tclient struct {
	t    *testing.T
	conn *net.UnixConn
	w    *wire.Writer

	mu        sync.Mutex
	pane      *sessionpane.Pane
	pos       uint64
	haveSnap  bool
	snapshots int
	welcome   *wire.Welcome
	notices   []wire.Notice
	exit      *wire.Exit
	problems  []string
	end       error
	closed    chan struct{}
}

// attach registers a ticket for role, connects and says Hello.
func (r *rig) attach(role wire.Role) *tclient {
	r.t.Helper()
	tk := r.newTicket(role)
	conn, err := r.sockDial(serve.AttachSocket)
	if err != nil {
		r.t.Fatalf("dial attach.sock: %v", err)
	}
	c := &tclient{t: r.t, conn: conn, w: wire.NewWriter(conn, wire.ToHolder), closed: make(chan struct{})}
	c.pane = sessionpane.New(80, 24, sessionpane.Options{Replies: func([]byte) {}})
	hp, err := wire.EncodeHello(wire.Hello{Version: wire.ProtocolVersion, Ticket: tk, Name: "test-console", Columns: 100, Rows: 30})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := wire.Write(conn, wire.ToHolder, wire.TypeHello, hp); err != nil {
		r.t.Fatal(err)
	}
	go c.run()
	r.t.Cleanup(func() {
		conn.Close()
		<-c.closed
		c.pane.Close()
	})
	return c
}

func (c *tclient) run() {
	defer close(c.closed)
	rd := wire.NewReader(c.conn, wire.ToClient)
	for {
		f, err := rd.Next()
		c.mu.Lock()
		if err != nil {
			c.end = err
			c.mu.Unlock()
			return
		}
		c.handle(f)
		c.mu.Unlock()
	}
}

func (c *tclient) fail(format string, args ...any) {
	c.problems = append(c.problems, fmt.Sprintf(format, args...))
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
	case wire.TypeOutput:
		pos, data, err := wire.DecodeOutput(f.Payload)
		if err != nil {
			c.fail("output frame: %v", err)
			return
		}
		if !c.haveSnap || pos != c.pos {
			c.fail("output at position %d, the client is at %d (snapshot %v)", pos, c.pos, c.haveSnap)
			c.pos = pos
		}
		c.pane.Write(data)
		c.pos += uint64(len(data))
	case wire.TypeSize:
		sz, err := wire.DecodeSize(f.Payload)
		if err != nil {
			c.fail("size: %v", err)
			return
		}
		c.pane.Resize(sz.Columns, sz.Rows)
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

func (c *tclient) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	cols, rows := c.pane.Size()
	out, _ := c.pane.Text(uv.Pos(0, 0), uv.Pos(cols-1, rows-1), 1<<20)
	return out
}

func (c *tclient) waitText(want string) {
	c.t.Helper()
	var last string
	poll(c.t, func() string { return fmt.Sprintf("the client's screen to show %q; last %q", want, last) }, func() bool {
		last = c.text()
		return strings.Contains(last, want)
	})
}

func (c *tclient) waitExit() wire.Exit {
	c.t.Helper()
	c.waitFor("the Exit frame", func() bool { return c.exit != nil })
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.exit
}

func (c *tclient) noProblems() {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.problems) > 0 {
		c.t.Fatalf("the client saw: %v", c.problems)
	}
}

func (c *tclient) input(b string) {
	c.t.Helper()
	if err := c.w.WriteFrame(wire.TypeInput, []byte(b)); err != nil {
		c.t.Fatalf("send input: %v", err)
	}
}

// readFinal reads final.json of the state folder.
func (r *rig) readFinal() session.FinalRecord {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.state, session.FinalName))
	if err != nil {
		r.t.Fatalf("final record: %v", err)
	}
	var rec session.FinalRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		r.t.Fatalf("final record: %v", err)
	}
	return rec
}

// readState reads holder.json of the state folder.
func (r *rig) readState() stateFile {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.state, StateFileName))
	if err != nil {
		r.t.Fatalf("state file: %v", err)
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		r.t.Fatalf("state file: %v", err)
	}
	return st
}

// logBytes is every byte of the event log, both files.
func (r *rig) logBytes() []byte {
	var all []byte
	for _, n := range []string{eventlog.FileName, eventlog.RotatedName} {
		b, err := os.ReadFile(filepath.Join(r.state, n))
		if err == nil {
			all = append(all, b...)
		}
	}
	return all
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return fi.Mode()
}
