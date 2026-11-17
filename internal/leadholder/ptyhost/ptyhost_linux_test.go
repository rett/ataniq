//go:build linux

package ptyhost

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/fakeagent"
)

func TestMain(m *testing.M) {
	fakeagent.RunIfChild()
	os.Exit(m.Run())
}

const patience = 20 * time.Second

// tail reads a host's output in the background and lets a test wait for it.
type tail struct {
	h       *Host
	mu      sync.Mutex
	buf     []byte
	maxRead int
	err     error
	sig     chan struct{}
	eof     chan struct{} // closed when Read returned an error
	exited  chan struct{} // closed when the goroutine is gone
}

func newTail(h *Host) *tail {
	t := &tail{h: h, sig: make(chan struct{}, 1), eof: make(chan struct{}), exited: make(chan struct{})}
	go func() {
		defer close(t.exited)
		buf := make([]byte, ReadBufferSize)
		for {
			n, err := h.Read(buf)
			t.mu.Lock()
			t.buf = append(t.buf, buf[:n]...)
			t.maxRead = max(t.maxRead, n)
			if err != nil {
				t.err = err
			}
			t.mu.Unlock()
			select {
			case t.sig <- struct{}{}:
			default:
			}
			if err != nil {
				close(t.eof)
				return
			}
		}
	}()
	return t
}

func (t *tail) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

func (t *tail) waitFor(tb testing.TB, want string) {
	tb.Helper()
	deadline := time.After(patience)
	for {
		if bytes.Contains(t.bytes(), []byte(want)) {
			return
		}
		select {
		case <-t.sig:
		case <-t.eof:
			if bytes.Contains(t.bytes(), []byte(want)) {
				return
			}
			tb.Fatalf("stream ended (%v) before %q; output %q", t.err, want, t.bytes())
		case <-deadline:
			tb.Fatalf("no %q within %v; output %q", want, patience, t.bytes())
		}
	}
}

func (t *tail) waitEOF(tb testing.TB) {
	tb.Helper()
	select {
	case <-t.eof:
	case <-time.After(patience):
		tb.Fatalf("the stream did not end; output %q", t.bytes())
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != io.EOF {
		tb.Fatalf("stream ended with %v, want io.EOF", t.err)
	}
}

// rx decodes the bytes the fake agent's echo step reported.
func rx(out []byte) []byte {
	var all []byte
	for _, l := range strings.Split(string(out), "\r\n") {
		if h, ok := strings.CutPrefix(l, "RX:"); ok {
			b, err := hex.DecodeString(h)
			if err == nil {
				all = append(all, b...)
			}
		}
	}
	return all
}

type opts struct {
	env []string
	dir string
	mod func(*Config)
}

// start runs a script as the fake agent. Cleanup stops what is left, closes
// the host and waits for the reader.
func start(t *testing.T, script string, o ...opts) (*Host, *tail) {
	t.Helper()
	argv, env := fakeagent.Command(script)
	cfg := Config{Argv: argv, Env: env, Cols: 100, Rows: 30, NoCgroup: true}
	if len(o) > 0 {
		cfg.Env = append(cfg.Env, o[0].env...)
		cfg.Dir = o[0].dir
		if o[0].mod != nil {
			o[0].mod(&cfg)
		}
	}
	h, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	tl := newTail(h)
	t.Cleanup(func() {
		h.Stop(0)
		h.Close()
		<-tl.exited
	})
	return h, tl
}

func waitExit(t *testing.T, h *Host) Exit {
	t.Helper()
	select {
	case <-h.Exited():
	case <-time.After(patience):
		t.Fatal("the child did not exit")
	}
	return h.Wait()
}

func TestChildIsSessionLeaderWithControllingTerminalAndExactEnvironment(t *testing.T) {
	t.Setenv("PTYHOST_TEST_INHERIT", "leaked")
	dir := t.TempDir()
	rep := filepath.Join(dir, "report")
	h, _ := start(t, "report "+rep+"; exit 0", opts{env: []string{"FOO=bar", "EMPTY="}, dir: dir})
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %v", e)
	}
	r, err := fakeagent.ReadReport(rep)
	if err != nil {
		t.Fatal(err)
	}
	if r.Pid != h.Pid() || r.Sid != r.Pid || r.Pgid != r.Pid {
		t.Errorf("pid %d sid %d pgid %d, host pid %d: want a session leader", r.Pid, r.Sid, r.Pgid, h.Pid())
	}
	if !r.Ctty || r.Tpgid != r.Pid {
		t.Errorf("controlling terminal %v, foreground group %d, want true and %d", r.Ctty, r.Tpgid, r.Pid)
	}
	if !r.StdinTTY || !r.StdoutTTY || !r.StderrTTY || !strings.HasPrefix(r.TTYName, "/dev/pts/") {
		t.Errorf("stdio terminals %v %v %v on %q", r.StdinTTY, r.StdoutTTY, r.StderrTTY, r.TTYName)
	}
	if r.Cols != 100 || r.Rows != 30 {
		t.Errorf("size %dx%d, want 100x30", r.Cols, r.Rows)
	}
	for fd := 0; fd <= 2; fd++ {
		if !strings.HasPrefix(r.Fds[fd], "/dev/pts/") {
			t.Errorf("descriptor %d is %q, want the terminal slave", fd, r.Fds[fd])
		}
	}
	t.Logf("the child's descriptors: %v", r.Fds)
	// Above 2 the child may hold only what its own runtime opened (the
	// poller and the pipes of the standard library). Anything that names a
	// file, a terminal or a socket was inherited: the master or the slave of
	// this pair, or a descriptor the test binary holds.
	for fd, target := range r.Fds {
		if fd <= 2 {
			continue
		}
		if !strings.HasPrefix(target, "anon_inode:") && !strings.HasPrefix(target, "pipe:") {
			t.Errorf("descriptor %d is %q: the child inherited it", fd, target)
		}
	}
	want, _ := filepath.EvalSymlinks(dir)
	if r.Cwd != want {
		t.Errorf("cwd %q, want %q", r.Cwd, want)
	}
	_, env := fakeagent.Command("report " + rep + "; exit 0")
	env = append(env, "FOO=bar", "EMPTY=")
	if !reflect.DeepEqual(r.Env, env) {
		t.Errorf("environment %q, want exactly %q", r.Env, env)
	}
}

// A program that is not written in Go lists its own descriptors: the Go
// runtime opens some of its own, a program like ls opens only the directory it
// lists, so anything else above 2 would be inherited.
func TestChildOfAnotherLanguageInheritsOnlyTheTerminal(t *testing.T) {
	ls, err := exec.LookPath("ls")
	if err != nil {
		t.Skip("no ls binary")
	}
	h, tl := start(t, "", opts{mod: func(c *Config) { c.Argv = []string{ls, "-1", "/proc/self/fd"}; c.Env = nil }})
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %v", e)
	}
	tl.waitEOF(t)
	var fds []string
	for _, l := range strings.Split(strings.TrimSpace(string(tl.bytes())), "\n") {
		fds = append(fds, strings.TrimSpace(l))
	}
	// 0, 1 and 2 are the slave; 3 is the directory ls itself opened.
	if want := []string{"0", "1", "2", "3"}; !reflect.DeepEqual(fds, want) {
		t.Errorf("ls saw descriptors %q, want %q", fds, want)
	}
}

func TestEmptyEnvironmentIsEmpty(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env binary")
	}
	h, tl := start(t, "", opts{mod: func(c *Config) { c.Argv = []string{envBin}; c.Env = nil }})
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %v", e)
	}
	tl.waitEOF(t)
	if out := tl.bytes(); len(out) != 0 {
		t.Errorf("the child saw an environment: %q", out)
	}
}

func TestOutputArrivesAndStreamEnds(t *testing.T) {
	h, tl := start(t, "say HELLO; say WORLD")
	tl.waitFor(t, "HELLO\r\r\nWORLD\r\r\n")
	tl.waitEOF(t)
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %v", e)
	}
}

func TestReadIsBoundedAndKeepsEveryByte(t *testing.T) {
	const total = 300000
	_, tl := start(t, "raw; flood 0 300000 7")
	tl.waitEOF(t)
	got := tl.bytes()
	if !bytes.Equal(got, fakeagent.FloodBytes(7, total)) {
		t.Errorf("got %d bytes, not the flood of seed 7", len(got))
	}
	if tl.maxRead > ReadBufferSize || tl.maxRead == 0 {
		t.Errorf("largest read %d, want 1..%d", tl.maxRead, ReadBufferSize)
	}
}

func TestInputReachesTheChild(t *testing.T) {
	h, tl := start(t, "raw; say READY; echo 5")
	tl.waitFor(t, "READY")
	if n, err := h.Write([]byte("hello")); n != 5 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	tl.waitEOF(t)
	if got := rx(tl.bytes()); string(got) != "hello" {
		t.Errorf("the child read %q (output %q)", got, tl.bytes())
	}
	if n, err := h.Write(nil); n != 0 || err != nil {
		t.Errorf("empty Write = %d, %v", n, err)
	}
}

func TestLargeWriteIsWrittenWhole(t *testing.T) {
	const size = 100 << 10
	in := fakeagent.FloodBytes(3, size)
	h, tl := start(t, "raw; say READY; echo "+strconv.Itoa(size))
	tl.waitFor(t, "READY")
	if n, err := h.Write(in); n != size || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	tl.waitEOF(t)
	if got := rx(tl.bytes()); !bytes.Equal(got, in) {
		t.Errorf("the child read %d bytes, not the %d written", len(got), size)
	}
}

func TestWriteDoesNotBlockForeverWhenTheChildDoesNotRead(t *testing.T) {
	h, tl := start(t, "raw; say READY; wait", opts{mod: func(c *Config) { c.WriteTimeout = 300 * time.Millisecond }})
	tl.waitFor(t, "READY")
	in := make([]byte, 1<<20)
	begin := time.Now()
	n, err := h.Write(in)
	took := time.Since(begin)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Write = %d, %v, want a deadline error", n, err)
	}
	if n >= len(in) || n < 0 {
		t.Errorf("n = %d of %d", n, len(in))
	}
	if took < 250*time.Millisecond || took > 10*time.Second {
		t.Errorf("Write gave up after %v", took)
	}
}

func TestResizeIsSeenByTheChild(t *testing.T) {
	h, tl := start(t, "raw; watchwinch; size; say READY; wait")
	tl.waitFor(t, "READY")
	if !bytes.Contains(tl.bytes(), []byte("SIZE 100x30\r\n")) {
		t.Errorf("initial size not seen: %q", tl.bytes())
	}
	if err := h.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	tl.waitFor(t, "WINCH 120x40\r\n")
	if err := h.Resize(MinCols, MinRows); err != nil {
		t.Fatal(err)
	}
	tl.waitFor(t, "WINCH 2x1\r\n")
}

func TestSizeIsRefusedOutsideTheLimits(t *testing.T) {
	argv, env := fakeagent.Command("wait")
	for _, s := range [][2]int{{1, 24}, {501, 24}, {80, 0}, {80, 201}, {0, 0}, {-1, 5}} {
		if h, err := Start(Config{Argv: argv, Env: env, Cols: s[0], Rows: s[1]}); err == nil {
			h.Stop(0)
			h.Close()
			t.Errorf("Start accepted %dx%d", s[0], s[1])
		}
	}
	h, _ := start(t, "wait")
	for _, s := range [][2]int{{1, 24}, {501, 24}, {80, 0}, {80, 201}} {
		if err := h.Resize(s[0], s[1]); err == nil {
			t.Errorf("Resize accepted %dx%d", s[0], s[1])
		}
	}
	if err := h.Resize(MaxCols, MaxRows); err != nil {
		t.Errorf("Resize to the maximum: %v", err)
	}
}

func TestStartRefusesWhatItCannotRun(t *testing.T) {
	for _, argv := range [][]string{nil, {""}, {"sh"}, {"/nonexistent/agent"}} {
		if h, err := Start(Config{Argv: argv, Cols: 80, Rows: 24}); err == nil {
			h.Stop(0)
			h.Close()
			t.Errorf("Start accepted argv %q", argv)
		}
	}
}

func TestTermiosFollowsRawAndCooked(t *testing.T) {
	h, tl := start(t, "raw; say RAW; echo 1; cooked; say COOKED; echo 1; cooked; isig off; say NOSIG; wait")
	tl.waitFor(t, "RAW\r\n")
	got, err := h.Termios()
	if err != nil {
		t.Fatal(err)
	}
	if got.ISIG || got.ICANON || got.ECHO || got.HeldBack() != nil {
		t.Errorf("raw mode reads as %+v", got)
	}
	if _, err := h.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	tl.waitFor(t, "COOKED")
	got, err = h.Termios()
	if err != nil {
		t.Fatal(err)
	}
	want := Termios{ISIG: true, ICANON: true, ECHO: true, VINTR: 0x03, VQUIT: 0x1c, VSUSP: 0x1a}
	if got != want {
		t.Errorf("cooked mode reads as %+v, want %+v", got, want)
	}
	if hb := got.HeldBack(); !bytes.Equal(hb, []byte{0x03, 0x1c, 0x1a}) {
		t.Errorf("held back %x", hb)
	}
	// The agent stays in cooked mode (echo 1 reads a line, so send one).
	if _, err := h.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	tl.waitFor(t, "NOSIG")
	got, err = h.Termios()
	if err != nil {
		t.Fatal(err)
	}
	if got.ISIG || !got.ICANON || got.HeldBack() != nil {
		t.Errorf("cooked without ISIG reads as %+v", got)
	}
}

func TestHeldBack(t *testing.T) {
	cases := []struct {
		t    Termios
		want []byte
	}{
		{Termios{ISIG: true, VINTR: 3, VQUIT: 0x1c, VSUSP: 0x1a}, []byte{3, 0x1c, 0x1a}},
		{Termios{ISIG: true, VINTR: 3, VQUIT: 3, VSUSP: 0}, []byte{3}},
		{Termios{ISIG: true}, nil},
		{Termios{ISIG: false, VINTR: 3, VQUIT: 0x1c, VSUSP: 0x1a}, nil},
		{Termios{ISIG: true, VINTR: 0x07, VQUIT: 0x1c, VSUSP: 0x1a}, []byte{7, 0x1c, 0x1a}},
	}
	for i, c := range cases {
		if got := c.t.HeldBack(); !bytes.Equal(got, c.want) {
			t.Errorf("case %d: %x, want %x", i, got, c.want)
		}
	}
}

func TestExitStatusAndSignalAreReported(t *testing.T) {
	h, _ := start(t, "exit 3")
	e := waitExit(t, h)
	if e.How != "exit" || e.Code != 3 || e.Signal != 0 || e.Pid != h.Pid() {
		t.Errorf("exit = %+v", e)
	}
	if !strings.Contains(e.String(), "status 3") {
		t.Errorf("String = %q", e.String())
	}
	if again := h.Wait(); again != e {
		t.Errorf("a second Wait differs: %+v", again)
	}

	h, _ = start(t, "die TERM")
	e = waitExit(t, h)
	if e.How != "signal" || e.Signal != syscall.SIGTERM || e.Code != -1 {
		t.Errorf("exit = %+v", e)
	}
	if !strings.Contains(e.String(), "signal 15") {
		t.Errorf("String = %q", e.String())
	}
}

func TestStopEndsAChildThatIgnoresSIGTERM(t *testing.T) {
	h, tl := start(t, "ignore TERM,HUP; say READY; wait")
	tl.waitFor(t, "READY")
	const grace = 300 * time.Millisecond
	begin := time.Now()
	h.Stop(grace)
	took := time.Since(begin)
	if took < grace-50*time.Millisecond {
		t.Errorf("Stop returned after %v: SIGTERM was not given the grace period", took)
	}
	if took > grace+3*time.Second {
		t.Errorf("Stop took %v", took)
	}
	e := h.Wait()
	if e.How != "signal" || e.Signal != syscall.SIGKILL {
		t.Errorf("exit = %+v, want SIGKILL", e)
	}
	if gone, proof := h.TreeGone(); !gone {
		t.Errorf("tree not gone after Stop: %s", proof)
	}
}

func TestStopUsesSIGTERMFirst(t *testing.T) {
	h, tl := start(t, "say READY; wait")
	tl.waitFor(t, "READY")
	begin := time.Now()
	h.Stop(10 * time.Second)
	if took := time.Since(begin); took > 5*time.Second {
		t.Errorf("Stop took %v: the child dies on SIGTERM", took)
	}
	if e := h.Wait(); e.How != "signal" || e.Signal != syscall.SIGTERM {
		t.Errorf("exit = %+v, want SIGTERM", e)
	}
	h.Stop(time.Second) // after the reap: nothing to signal, no panic, no wait
}

func TestClosingTheMasterHangsUpTheChild(t *testing.T) {
	h, tl := start(t, "say READY; wait")
	tl.waitFor(t, "READY")
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if e := waitExit(t, h); e.How != "signal" || e.Signal != syscall.SIGHUP {
		t.Errorf("exit = %+v, want SIGHUP", e)
	}
	<-tl.exited
	if _, err := h.Read(make([]byte, 8)); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Read after Close = %v", err)
	}
	if _, err := h.Write([]byte("x")); err == nil {
		t.Error("Write after Close succeeded")
	}
	if err := h.Resize(80, 24); err == nil {
		t.Error("Resize after Close succeeded")
	}
	if _, err := h.Termios(); err == nil {
		t.Error("Termios after Close succeeded")
	}
}

// killAtCleanup opens a pidfd for pid now and kills the process through it
// when the test ends, so a pid that was reaped and reused in between is not
// hit. A process that is already gone needs nothing.
func killAtCleanup(t *testing.T, pid int) {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if err != unix.ESRCH {
			t.Errorf("pidfd_open(%d): %v", pid, err)
		}
		return
	}
	t.Cleanup(func() {
		_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		_ = unix.Close(fd)
	})
}

// pidFromFile waits for a file with a pid in it and returns the pid; the
// process is killed (through a pidfd opened now) when the test ends.
func pidFromFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		if b, err := os.ReadFile(path); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatalf("pid file: %v", err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pid file %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func alive(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	st, ok := parseStat(b)
	return ok && !st.dead()
}

func TestStreamAndChildAreSeparateFacts(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	h, tl := start(t, "spawn same "+pidFile+" ttl=60s; say READY; exit 0")
	tl.waitFor(t, "READY")
	grandchild := pidFromFile(t, pidFile)
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %+v", e)
	}
	// The child is gone, the grandchild holds the slave: the stream goes on.
	select {
	case <-tl.eof:
		t.Fatalf("the stream ended while a grandchild holds the terminal: %v", tl.err)
	case <-time.After(200 * time.Millisecond):
	}
	gone, proof := h.TreeGone()
	if gone || !strings.HasPrefix(proof, "session") {
		t.Errorf("TreeGone = %v, %q: want not gone, by the session method", gone, proof)
	}
	if !alive(grandchild) {
		t.Fatal("the grandchild is not running")
	}
	// Stop after the reap still ends the session's processes.
	h.Stop(0)
	tl.waitEOF(t)
	if gone, proof := h.TreeGone(); !gone || !strings.HasPrefix(proof, "session") {
		t.Errorf("TreeGone after Stop = %v, %q", gone, proof)
	}
	if h.TreeMethod() != "session" {
		t.Errorf("TreeMethod = %q", h.TreeMethod())
	}
}

func TestSessionProofMissesAChildInItsOwnSession(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "escaped")
	h, tl := start(t, "spawn session "+pidFile+" detach ttl=60s; say READY; exit 0")
	tl.waitFor(t, "READY")
	escaped := pidFromFile(t, pidFile)
	waitExit(t, h)
	tl.waitEOF(t) // it holds no terminal, so the stream ended
	gone, proof := h.TreeGone()
	if !gone || !strings.HasPrefix(proof, "session") || !strings.Contains(proof, "weaker") {
		t.Errorf("TreeGone = %v, %q: want gone, labelled weaker, by the session method", gone, proof)
	}
	if !alive(escaped) {
		t.Error("the escaped process is gone; the test shows nothing")
	}
}

func TestTreeGoneNeedsTheChildToHaveExited(t *testing.T) {
	h, tl := start(t, "say READY; wait")
	tl.waitFor(t, "READY")
	if gone, proof := h.TreeGone(); gone || !strings.Contains(proof, "not exited") {
		t.Errorf("TreeGone = %v, %q", gone, proof)
	}
}

func TestNothingLeaksAfterCloseAndReap(t *testing.T) {
	cycle := func() {
		argv, env := fakeagent.Command("say HI; exit 0")
		h, err := Start(Config{Argv: argv, Env: env, Cols: 80, Rows: 24, NoCgroup: true})
		if err != nil {
			t.Fatal(err)
		}
		tl := newTail(h)
		<-tl.eof
		h.Wait()
		h.Stop(0)
		if err := h.Close(); err != nil {
			t.Fatal(err)
		}
		<-tl.exited
		if _, err := Start(Config{Argv: []string{"/nonexistent/agent"}, Cols: 80, Rows: 24}); err == nil {
			t.Fatal("a missing program started")
		}
	}
	cycle() // the runtime opens its poller once
	fds0, gos0 := countFDs(t), runtime.NumGoroutine()
	for range 3 {
		cycle()
	}
	deadline := time.Now().Add(patience)
	for {
		fds, gos := countFDs(t), runtime.NumGoroutine()
		if fds <= fds0 && gos <= gos0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descriptors %d -> %d, goroutines %d -> %d", fds0, fds, gos0, gos)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countFDs(t *testing.T) int {
	t.Helper()
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeCgroup builds a proc root whose self/cgroup names a group in a fake
// cgroup mount, and returns the roots and the group's cgroup.procs path.
func fakeCgroup(t *testing.T, procs string) (procRoot, cgroupRoot, procsFile string) {
	t.Helper()
	dir := t.TempDir()
	procRoot, cgroupRoot = filepath.Join(dir, "proc"), filepath.Join(dir, "cgroup")
	writeFile(t, filepath.Join(procRoot, "self", "cgroup"), "0::/user.slice/lead.service\n")
	procsFile = filepath.Join(cgroupRoot, "user.slice", "lead.service", "cgroup.procs")
	writeFile(t, procsFile, procs)
	return
}

func TestTreeGoneWithAFakeControlGroup(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	procRoot, cgroupRoot, procsFile := fakeCgroup(t, self+"\n")
	h, tl := start(t, "say READY; wait", opts{mod: func(c *Config) {
		c.NoCgroup, c.ProcRoot, c.CgroupRoot = false, procRoot, cgroupRoot
	}})
	if h.TreeMethod() != "cgroup" {
		t.Fatalf("method %q, want cgroup (note %q)", h.TreeMethod(), h.cgroupNote)
	}
	tl.waitFor(t, "READY")
	if gone, proof := h.TreeGone(); gone || !strings.HasPrefix(proof, "cgroup") {
		t.Errorf("a running child: TreeGone = %v, %q", gone, proof)
	}
	h.Stop(5 * time.Second) // returns when the child is reaped and the group holds only the holder
	if e := h.Wait(); e.How != "signal" {
		t.Fatalf("exit %+v", e)
	}
	// Still one process of the old tree: not gone, and the proof names it.
	writeFile(t, procsFile, self+"\n4194000\n")
	gone, proof := h.TreeGone()
	if gone || !strings.HasPrefix(proof, "cgroup") || !strings.Contains(proof, "4194000") {
		t.Errorf("TreeGone = %v, %q: want not gone naming 4194000", gone, proof)
	}
	writeFile(t, procsFile, self+"\n")
	gone, proof = h.TreeGone()
	if !gone || !strings.HasPrefix(proof, "cgroup") || strings.Contains(proof, "session") {
		t.Errorf("TreeGone = %v, %q: want gone by cgroup", gone, proof)
	}
	// The group file becoming unreadable proves nothing.
	if err := os.Remove(procsFile); err != nil {
		t.Fatal(err)
	}
	if gone, proof := h.TreeGone(); gone {
		t.Errorf("TreeGone = true, %q, for an unreadable group", proof)
	}
	writeFile(t, procsFile, self+"\n") // so that the cleanup's Stop sees a gone tree
}

func TestSharedOrMissingControlGroupFallsBackToTheSession(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	fall := func(t *testing.T, procRoot, cgroupRoot, wantNote string) {
		t.Helper()
		h, _ := start(t, "exit 0", opts{mod: func(c *Config) {
			c.NoCgroup, c.ProcRoot, c.CgroupRoot = false, procRoot, cgroupRoot
		}})
		if h.TreeMethod() != "session" || !strings.Contains(h.cgroupNote, wantNote) {
			t.Errorf("method %q, note %q, want session with %q", h.TreeMethod(), h.cgroupNote, wantNote)
		}
		waitExit(t, h)
		// The fake proc root lists no process, so the session is empty.
		if gone, proof := h.TreeGone(); !gone || !strings.HasPrefix(proof, "session") {
			t.Errorf("TreeGone = %v, %q", gone, proof)
		}
	}
	t.Run("shared", func(t *testing.T) {
		p, c, _ := fakeCgroup(t, self+"\n1\n")
		fall(t, p, c, "shared")
	})
	t.Run("holder not listed", func(t *testing.T) {
		p, c, _ := fakeCgroup(t, "")
		fall(t, p, c, "not listed")
	})
	t.Run("no cgroup file", func(t *testing.T) {
		p, c, _ := fakeCgroup(t, self+"\n")
		os.Remove(filepath.Join(p, "self", "cgroup"))
		fall(t, p, c, "no control group information")
	})
	t.Run("cgroup v1 only", func(t *testing.T) {
		p, c, _ := fakeCgroup(t, self+"\n")
		writeFile(t, filepath.Join(p, "self", "cgroup"), "12:cpu:/x\n1:name=systemd:/x\n")
		fall(t, p, c, "no cgroup v2")
	})
	t.Run("group missing", func(t *testing.T) {
		p, c, f := fakeCgroup(t, self+"\n")
		os.Remove(f)
		fall(t, p, c, "not readable")
	})
	t.Run("path with dots", func(t *testing.T) {
		p, c, _ := fakeCgroup(t, self+"\n")
		writeFile(t, filepath.Join(p, "self", "cgroup"), "0::/../../etc\n")
		fall(t, p, c, "not readable")
	})
}

// fakeProc writes stat files for a fake proc root.
func fakeProc(t *testing.T, stats map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, stat := range stats {
		writeFile(t, filepath.Join(root, name, "stat"), stat)
	}
	return root
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func TestSessionScanReadsStatCarefully(t *testing.T) {
	root := fakeProc(t, map[string]string{
		"10": "10 (leader) S 1 4242 4242 0 -1 0\n",           // member
		"11": "11 (a b) c) R 10 4242 4242 0 -1 0\n",          // member, awkward command name
		"12": "12 (dead) Z 10 4242 4242 0 -1 0\n",            // zombie: not present
		"13": "13 (other) S 1 77 77 0 -1 0\n",                // another session
		"14": "14 (odd) S 1 99 4242 0 -1 0\n",                // member in another group
		"15": "not a stat line\n",                            // unreadable
		"16": "16 (short) S 1\n",                             // unreadable
		"17": "17 (gone-state) X 1 4242 4242 0 -1 0\n",       // dead
		"18": "18 (non-numeric) S 1 4242 abc 0 -1 0\n",       // unreadable
		"19": "19 (member of group) S 1 4242 4242 0 -1 0\n",  // member
		"20": "20 (" + strings.Repeat("x", 9000) + ") S 1\n", // longer than the read bound: unreadable
	})
	writeFile(t, filepath.Join(root, "self", "other"), "x")               // not a pid
	if err := os.MkdirAll(filepath.Join(root, "21"), 0o755); err != nil { // vanished: no stat
		t.Fatal(err)
	}
	h := &Host{pid: 4242, procRoot: root, scanMax: 1000, done: closedChan()}
	members, trunc, nerr, err := h.scanSession()
	if err != nil || trunc {
		t.Fatalf("scan: truncated %v, err %v", trunc, err)
	}
	got := map[int]int{}
	for _, m := range members {
		got[m.pid] = m.pgrp
	}
	want := map[int]int{10: 4242, 11: 4242, 14: 99, 19: 4242}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("members %v, want %v", got, want)
	}
	if nerr != 4 || h.Counters().ScanErrors != 4 {
		t.Errorf("unreadable entries %d (counter %d), want 4", nerr, h.Counters().ScanErrors)
	}
}

func TestSessionScanIsBoundedAndCounted(t *testing.T) {
	stats := map[string]string{}
	for i := 100; i < 140; i++ {
		stats[strconv.Itoa(i)] = strconv.Itoa(i) + " (x) S 1 77 77 0\n"
	}
	root := fakeProc(t, stats)
	h := &Host{pid: 4242, procRoot: root, scanMax: 25, cgroupNote: "fake", done: closedChan()}
	_, trunc, _, err := h.scanSession()
	if err != nil || !trunc {
		t.Fatalf("truncated %v, err %v, want a truncated scan", trunc, err)
	}
	if h.Counters().ScanTruncated != 1 {
		t.Errorf("counter %d", h.Counters().ScanTruncated)
	}
	if gone, proof := h.TreeGone(); gone || !strings.Contains(proof, "limit") {
		t.Errorf("a truncated scan proved the tree gone: %v, %q", gone, proof)
	}
	if h.Counters().ScanTruncated != 2 {
		t.Errorf("counter %d, want 2", h.Counters().ScanTruncated)
	}
	// With room for every entry the same tree is gone, and stays gone.
	h.scanMax = 1000
	if gone, proof := h.TreeGone(); !gone || !strings.HasPrefix(proof, "session") {
		t.Errorf("TreeGone = %v, %q", gone, proof)
	}
	writeFile(t, filepath.Join(root, "200", "stat"), "200 (late) S 1 4242 4242 0\n")
	if gone, _ := h.TreeGone(); !gone {
		t.Error("an empty session came back to life; the fact must stay latched")
	}
}

func TestSessionScanFailureProvesNothing(t *testing.T) {
	h := &Host{pid: 4242, procRoot: filepath.Join(t.TempDir(), "missing"), scanMax: 10, cgroupNote: "fake", done: closedChan()}
	if gone, proof := h.TreeGone(); gone || !strings.HasPrefix(proof, "session") {
		t.Errorf("TreeGone = %v, %q", gone, proof)
	}
	h.procRoot = fakeProc(t, map[string]string{"10": "garbage"})
	if gone, proof := h.TreeGone(); gone || !strings.Contains(proof, "could not be read") {
		t.Errorf("TreeGone = %v, %q", gone, proof)
	}
}

func TestLegacyTIOCSTI(t *testing.T) {
	for _, c := range []struct {
		name, content          string
		missing                bool
		wantEnabled, wantKnown bool
	}{
		{"off", "0\n", false, false, true},
		{"on", "1\n", false, true, true},
		{"missing", "", true, true, false},
		{"garbage", "yes\n", false, true, false},
		{"empty", "", false, true, false},
		{"huge", strings.Repeat("0", 1<<16), false, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if !c.missing {
				writeFile(t, filepath.Join(root, "sys", "dev", "tty", "legacy_tiocsti"), c.content)
			}
			en, known := LegacyTIOCSTI(root)
			if en != c.wantEnabled || known != c.wantKnown {
				t.Errorf("LegacyTIOCSTI = %v, %v, want %v, %v", en, known, c.wantEnabled, c.wantKnown)
			}
		})
	}
	en, known := LegacyTIOCSTI("")
	t.Logf("this host: enabled %v, known %v", en, known)
}

func TestParseStat(t *testing.T) {
	good := map[string]procStat{
		"1 (init) S 0 1 1 0 -1":             {'S', 1, 1},
		"5 (a) b) c) R 4 40 50 0":           {'R', 40, 50},
		"5 () Z 4 40 50 0":                  {'Z', 40, 50},
		"5 (x) S 4 40 50 0 0 0 0 0 0 0 0 0": {'S', 40, 50},
	}
	for in, want := range good {
		if got, ok := parseStat([]byte(in)); !ok || got != want {
			t.Errorf("parseStat(%q) = %+v, %v, want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "1 init S 0 1 1", "1 (x) S 0", "1 (x) SS 0 1 1", "1 (x) S 0 a 1", "1 (x"} {
		if _, ok := parseStat([]byte(in)); ok {
			t.Errorf("parseStat(%q) accepted", in)
		}
	}
}

// The terminal queries (case A11): the test plays the terminal, answers the
// background colour and the device attributes, and sees what the agent read.
func TestQueryRecordsTheReplyTheAgentRead(t *testing.T) {
	file := filepath.Join(t.TempDir(), "reply")
	h, tl := start(t, "query osc11,da1 "+file+"; say DONE")
	tl.waitFor(t, "\x1b]11;?\x1b\\\x1b[c")
	reply := "\x1b]11;rgb:1111/2222/3333\x1b\\\x1b[?62;22c"
	if _, err := h.Write([]byte(reply)); err != nil {
		t.Fatal(err)
	}
	tl.waitFor(t, "DONE")
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %v", e)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != reply {
		t.Errorf("the agent recorded %q, %v, want %q", got, err, reply)
	}
	tl.waitEOF(t) // everything the agent wrote has been read
	if seen := tl.bytes(); bytes.Contains(seen, []byte("rgb:1111")) {
		t.Errorf("the reply was echoed back to the terminal: %q", seen)
	}
}
