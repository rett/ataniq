package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/wire"
	"golang.org/x/sys/unix"
)

// TestMain lets the test binary stand in for the holder: the direct-mode tests
// start it with the holder's command line, which begins with --scratch, and
// no real holder, agent or unit is involved.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--scratch" {
		os.Exit(fakeHolderMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// lockedBuffer is a bytes.Buffer that a test and the command can both use.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// fakeLauncher records what the command would run and starts nothing. Its
// Start can hand back a fake child, or really start the process (for the
// direct tests that re-execute this test binary).
type fakeLauncher struct {
	mu     sync.Mutex
	runs   [][]string
	starts [][]string
	envs   [][]string

	runFn     func(argv []string) (int, error)
	startFn   func(argv, env []string) (child, error)
	realStart bool
}

func (f *fakeLauncher) Run(argv []string, stdout, stderr io.Writer) (int, error) {
	f.mu.Lock()
	f.runs = append(f.runs, append([]string(nil), argv...))
	fn := f.runFn
	f.mu.Unlock()
	if fn != nil {
		return fn(argv)
	}
	return 0, nil
}

func (f *fakeLauncher) Start(argv, env []string, stdout, stderr io.Writer) (child, error) {
	f.mu.Lock()
	f.starts = append(f.starts, append([]string(nil), argv...))
	f.envs = append(f.envs, append([]string(nil), env...))
	fn := f.startFn
	f.mu.Unlock()
	if f.realStart {
		return execLauncher{}.Start(argv, env, stdout, stderr)
	}
	if fn != nil {
		return fn(argv, env)
	}
	return nil, fmt.Errorf("fake launcher: no start function")
}

func (f *fakeLauncher) runList() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.runs...)
}

// fakeChild is a process that ends when told to.
type fakeChild struct {
	end      chan struct{}
	once     sync.Once
	code     int
	mu       sync.Mutex
	signaled []os.Signal
}

func newFakeChild(code int) *fakeChild { return &fakeChild{end: make(chan struct{}), code: code} }

func (c *fakeChild) finish() { c.once.Do(func() { close(c.end) }) }
func (c *fakeChild) Wait() (int, error) {
	<-c.end
	return c.code, nil
}
func (c *fakeChild) Signal(s os.Signal) error {
	c.mu.Lock()
	c.signaled = append(c.signaled, s)
	c.mu.Unlock()
	c.finish()
	return nil
}

// testEnv is a private runtime folder and state home.
type testEnv struct {
	root    string
	runtime string
	state   string
	home    string
	vars    map[string]string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	// The runtime folder holds unix sockets, whose paths are limited to 107
	// bytes. t.TempDir() carries the test's name and can be too long for that,
	// so this folder has a short name of its own, directly in the temporary
	// folder, and is removed with the test.
	rt, err := os.MkdirTemp("", "as")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	e := &testEnv{root: root, runtime: rt, state: filepath.Join(root, "s"), home: filepath.Join(root, "h")}
	for _, d := range []string{e.state, e.home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.vars = map[string]string{
		"XDG_RUNTIME_DIR": e.runtime,
		"XDG_STATE_HOME":  e.state,
		"HOME":            e.home,
		"LANG":            "de_DE.UTF-8",
		"SECRET_INHERIT":  "must-not-reach-the-agent",
	}
	return e
}

// layout is the layout of name under the test folders.
func (e *testEnv) layout(t *testing.T, name string) layout {
	t.Helper()
	a := &app{getenv: e.getenv}
	l, err := a.layoutFor(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func (e *testEnv) getenv(k string) string { return e.vars[k] }

// holderFile makes an executable stand-in for the holder binary.
func (e *testEnv) holderFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(e.root, "ataniq-holder")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func fastLimits() limits {
	return limits{
		lockWait:  2 * time.Second,
		exchange:  2 * time.Second,
		startWait: 2 * time.Second,
		startPoll: 5 * time.Millisecond,
		stopWait:  60 * time.Millisecond,
		stopPoll:  5 * time.Millisecond,
		termWait:  2 * time.Second,
	}
}

// newApp returns an app on the test environment and its output buffers.
func (e *testEnv) newApp(l launcher) (*app, *lockedBuffer, *lockedBuffer) {
	out, errout := &lockedBuffer{}, &lockedBuffer{}
	return &app{
		out:        out,
		errout:     errout,
		getenv:     e.getenv,
		executable: func() (string, error) { return filepath.Join(e.root, "ataniq-scratch"), nil },
		uid:        os.Getuid(),
		launch:     l,
		lim:        fastLimits(),
		build:      sameBuild(),
	}, out, errout
}

// testBuild is the build the tests' command is and expects.
var testBuild = buildid.Identity{Module: "ataniq", Version: "v0.0.14-0.20261124010203-222222222222",
	Revision: "2222222222222222222222222222222222222222"}

// sameBuild is a build check that finds every holder to be testBuild: the
// stand-in holders of the tests are not builds of this command.
func sameBuild() *buildid.Checker {
	return &buildid.Checker{
		Expected:   testBuild,
		Executable: func(string) (buildid.Identity, error) { return testBuild, nil },
		Record: func(string) (buildid.Record, error) {
			b := testBuild
			return buildid.Record{Build: &b}, nil
		},
	}
}

// fakeHolder is a control server in a unix socket that applies the holder's
// peer rule in a simplified form: the peer is of the same user, the pid in the
// lock file is the peer's pid, and the lock file is held with an exclusive
// flock while the peer is connected.
type fakeHolder struct {
	ln       net.Listener
	sock     string
	lockPath string
	lead     string

	epoch  atomic.Uint64
	denied atomic.Int32

	mu      sync.Mutex
	reqs    []wire.Request
	tickets map[wire.Role]string
	stopped chan struct{}
	wg      sync.WaitGroup
}

func startFakeHolder(sock, lockPath, lead string, epoch uint64) (*fakeHolder, error) {
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	h := &fakeHolder{ln: ln, sock: sock, lockPath: lockPath, lead: lead, tickets: map[wire.Role]string{}, stopped: make(chan struct{})}
	h.epoch.Store(epoch)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			h.wg.Add(1)
			go func() {
				defer h.wg.Done()
				defer c.Close()
				h.serve(c.(*net.UnixConn))
			}()
		}
	}()
	return h, nil
}

func newFakeHolder(t *testing.T, l layout, epoch uint64) *fakeHolder {
	t.Helper()
	if err := os.MkdirAll(l.runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.state, 0o700); err != nil {
		t.Fatal(err)
	}
	h, err := startFakeHolder(l.controlSock, l.daemonLock, "scratch/"+l.name, epoch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

func (h *fakeHolder) Close() {
	h.ln.Close()
	h.wg.Wait()
}

func (h *fakeHolder) requests() []wire.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wire.Request(nil), h.reqs...)
}

func (h *fakeHolder) ticketOf(r wire.Role) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tickets[r]
}

// peerOK is the simplified peer rule.
func (h *fakeHolder) peerOK(c *net.UnixConn) bool {
	raw, err := c.SyscallConn()
	if err != nil {
		return false
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) { cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || cerr != nil {
		return false
	}
	if int(cred.Uid) != os.Getuid() {
		return false
	}
	b, err := os.ReadFile(h.lockPath)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid != int(cred.Pid) {
		return false
	}
	f, err := os.Open(h.lockPath)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		return false // nobody holds the lock
	}
	return true
}

func (h *fakeHolder) serve(c *net.UnixConn) {
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if !h.peerOK(c) {
		h.denied.Add(1)
		return
	}
	fr, err := wire.NewReader(c, wire.ControlRequests).Next()
	if err != nil {
		return
	}
	req, err := wire.DecodeRequest(fr.Payload)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.reqs = append(h.reqs, req)
	h.mu.Unlock()
	resp := h.answer(req)
	payload, err := wire.EncodeResponse(resp)
	if err != nil {
		return
	}
	wire.NewWriter(c, wire.ControlResponses).WriteFrame(wire.TypeResponse, payload)
}

func (h *fakeHolder) answer(req wire.Request) wire.Response {
	if req.Epoch != h.epoch.Load() {
		return wire.Response{Code: wire.CodeWrongEpoch, Text: "the request is for another epoch"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch req.Op {
	case wire.OpStatus:
		return wire.Response{OK: true, Status: &wire.Status{
			Lead: h.lead, Epoch: req.Epoch, Runtime: "scratch", Idle: wire.IdleUnknown, Title: "a title",
			Size: wire.Size{Columns: 100, Rows: 30}, Caps: wire.DefaultCaps(),
			WriterPresent: h.tickets[wire.RoleWriter] != "", ViewerPresent: h.tickets[wire.RoleViewer] != "",
			Counters: map[string]uint64{"zeta": 2, "alpha": 1},
			Facts:    wire.Facts{StreamEnded: wire.FactNo, TUIExited: wire.FactNo, TreeGone: wire.FactUnknown},
		}}
	case wire.OpTicket:
		if h.tickets[req.Role] != "" {
			return wire.Response{Code: wire.CodeBusy, Text: "the " + string(req.Role) + " role is taken"}
		}
		h.tickets[req.Role] = req.Ticket
		return wire.Response{OK: true}
	case wire.OpReleaseWriter:
		delete(h.tickets, wire.RoleWriter)
		return wire.Response{OK: true}
	case wire.OpStop:
		select {
		case <-h.stopped:
		default:
			close(h.stopped)
		}
		return wire.Response{OK: true}
	}
	return wire.Response{Code: wire.CodeDisabled, Text: "not in this fake"}
}

// fakeHolderMain is the holder when this test binary is started with the
// holder's command line. It records its arguments and environment in
// RUNTIME-DIR/fake-holder.json, serves the control socket like fakeHolder and
// exits on a stop request. The last element of the program path after -- picks a behaviour:
// "exit3" ends at once with status 3, "silent" never answers.
func fakeHolderMain(args []string) int {
	flags := map[string]string{}
	var env []string
	var argv []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			argv = args[i+1:]
			break
		}
		if a == "--scratch" {
			flags[a] = "true"
			continue
		}
		if i+1 >= len(args) {
			fmt.Fprintln(os.Stderr, "fake holder: missing value")
			return 64
		}
		if a == "--env" {
			env = append(env, args[i+1])
		} else {
			flags[a] = args[i+1]
		}
		i++
	}
	rt := flags["--runtime-dir"]
	rec, _ := json.Marshal(map[string]any{"args": args, "env": os.Environ()})
	if err := os.WriteFile(filepath.Join(rt, "fake-holder.json"), rec, 0o600); err != nil {
		return 65
	}
	mode := ""
	if len(argv) > 0 {
		mode = filepath.Base(argv[0])
	}
	switch mode {
	case "exit3":
		return 3
	case "silent":
		time.Sleep(time.Minute)
		return 0
	}
	epoch, err := strconv.ParseUint(flags["--epoch"], 10, 64)
	if err != nil {
		return 66
	}
	h, err := startFakeHolder(filepath.Join(rt, "control.sock"), flags["--daemon-lock"], flags["--role"], epoch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake holder:", err)
		return 67
	}
	<-h.stopped
	h.Close()
	return 0
}
