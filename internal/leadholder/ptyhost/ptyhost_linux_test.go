//go:build linux

package ptyhost

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

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

// leakedFd opens /dev/null without close-on-exec, as a process started by a
// service manager may hold a descriptor it was handed.
func leakedFd(t *testing.T) int {
	t.Helper()
	fd, err := unix.Open("/dev/null", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return fd
}

func closeOnExec(t *testing.T, fd int) bool {
	t.Helper()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	return flags&unix.FD_CLOEXEC != 0
}

func TestCloseOnExecFromMarksOnlyTheDescriptorsFromTheBound(t *testing.T) {
	low, mid, high := leakedFd(t), leakedFd(t), leakedFd(t)
	if closeOnExec(t, low) || closeOnExec(t, mid) || closeOnExec(t, high) {
		t.Fatal("a plain open is close-on-exec")
	}
	if err := CloseOnExecFrom(mid); err != nil {
		t.Fatal(err)
	}
	if closeOnExec(t, low) || !closeOnExec(t, mid) || !closeOnExec(t, high) {
		t.Errorf("after CloseOnExecFrom(%d): low %v, mid %v, high %v, want false, true, true", mid, closeOnExec(t, low), closeOnExec(t, mid), closeOnExec(t, high))
	}
	// The fallback for kernels without CLOSE_RANGE_CLOEXEC does the same.
	a, b := leakedFd(t), leakedFd(t)
	if err := closeOnExecFromProc(b, "/proc"); err != nil {
		t.Fatal(err)
	}
	if closeOnExec(t, a) || !closeOnExec(t, b) {
		t.Errorf("fallback from %d: a %v, b %v, want false, true", b, closeOnExec(t, a), closeOnExec(t, b))
	}
	if err := closeOnExecFromProc(0, filepath.Join(t.TempDir(), "no-proc")); err == nil {
		t.Error("the fallback without a proc filesystem succeeded")
	}
	if err := CloseOnExecFrom(-1); err == nil {
		t.Error("CloseOnExecFrom(-1) succeeded")
	}
	if err := CloseOnExecFrom(low); err != nil {
		t.Fatal(err)
	}
	if !closeOnExec(t, low) || !closeOnExec(t, a) {
		t.Error("CloseOnExecFrom(low) left a descriptor above it open to children")
	}
}

// What the helper is for: a descriptor the holder was handed reaches the
// agent, until CloseOnExecFrom(3) is called.
func TestInheritedDescriptorReachesTheChildUnlessMarkedCloseOnExec(t *testing.T) {
	fd := leakedFd(t)
	fds := func() map[int]string {
		t.Helper()
		rep := filepath.Join(t.TempDir(), "report")
		h, _ := start(t, "report "+rep+"; exit 0")
		if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
			t.Fatalf("exit %v", e)
		}
		r, err := fakeagent.ReadReport(rep)
		if err != nil {
			t.Fatal(err)
		}
		return r.Fds
	}
	if got := fds(); got[fd] != "/dev/null" {
		t.Fatalf("the child's descriptors %v: want %d to be inherited, or the test shows nothing", got, fd)
	}
	if err := CloseOnExecFrom(3); err != nil {
		t.Fatal(err)
	}
	for n, target := range fds() {
		if n > 2 && target == "/dev/null" {
			t.Errorf("descriptor %d (%s) was inherited after CloseOnExecFrom(3)", n, target)
		}
	}
}

func TestParseSigIgn(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []syscall.Signal
	}{
		{"Name:\tx\nSigIgn:\t0000000000001001\nSigCgt:\t0\n", []syscall.Signal{syscall.SIGHUP, syscall.SIGPIPE}},
		{"SigIgn:\t0000000000000000\n", nil},
		{"SigIgn:\t8000000000000000\n", []syscall.Signal{64}},
	} {
		got, err := parseSigIgn([]byte(c.in))
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseSigIgn(%q) = %v, %v, want %v", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "Name:\tx\n", "SigIgn:\tzz\n", "SigIgn:\t\n"} {
		if got, err := parseSigIgn([]byte(in)); err == nil {
			t.Errorf("parseSigIgn(%q) = %v, want an error", in, got)
		}
	}
	if _, err := ignoredSignals(filepath.Join(t.TempDir(), "no-proc")); err == nil {
		t.Error("ignoredSignals without a proc filesystem succeeded")
	}
}

// The limit the package comment states: an ignored signal is inherited by the
// agent. A program that is not written in Go (grep stands for it; a Go program
// puts its own handler over most inherited ignores) lists its own mask.
func TestIgnoredSignalsAreInheritedByTheChild(t *testing.T) {
	grep, err := exec.LookPath("grep")
	if err != nil {
		t.Skip("no grep binary")
	}
	signal.Ignore(syscall.SIGUSR2)
	t.Cleanup(func() {
		// Reset does not undo Ignore for a signal the runtime handles; handling
		// it again does.
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGUSR2)
		signal.Stop(c)
	})
	ours, err := IgnoredSignals()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ours, syscall.SIGUSR2) || slices.Contains(ours, syscall.SIGUSR1) {
		t.Fatalf("IgnoredSignals = %v, want USR2 and not USR1", ours)
	}
	h, tl := start(t, "", opts{mod: func(c *Config) {
		c.Argv = []string{grep, "SigIgn:", "/proc/self/status"}
		c.Env = nil
	}})
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %v", e)
	}
	tl.waitEOF(t)
	theirs, err := parseSigIgn(bytes.ReplaceAll(tl.bytes(), []byte("\r"), nil))
	if err != nil {
		t.Fatalf("the child's status %q: %v", tl.bytes(), err)
	}
	if !slices.Contains(theirs, syscall.SIGUSR2) || slices.Contains(theirs, syscall.SIGUSR1) {
		t.Errorf("the child ignores %v, want USR2 inherited and not USR1: the package comment is wrong", theirs)
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

// staysRunning reports whether the process behind a pidfd is still running
// after d: the pidfd becomes readable when the process exits (a zombie has).
func staysRunning(t *testing.T, pid int, d time.Duration) bool {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	end := time.Now().Add(d)
	for {
		// The runtime's preemption signals interrupt a blocking poll.
		n, err := unix.Poll(fds, int(max(time.Until(end), 0).Milliseconds()))
		switch {
		case err == unix.EINTR:
			continue
		case err != nil:
			t.Fatalf("poll: %v", err)
		}
		return n == 0
	}
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
			killAtCleanup(t, pid)
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

// stateOf returns the state letter of a process from /proc, or 0 when there is
// no such process.
func stateOf(pid int) byte {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	st, ok := parseStat(b)
	if !ok {
		return '?'
	}
	return st.state
}

// waitGoneFromProc waits until /proc no longer lists pid.
func waitGoneFromProc(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for stateOf(pid) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%s: process %d is still in /proc (state %c)", what, pid, stateOf(pid))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The child is a zombie from its exit until the session is proven empty: the
// zombie keeps the pid, and so the session id, from being given to a stranger
// (the review of the package, finding F2). Wait does not wait for the proof.
func TestLeaderStaysAZombieUntilTheSessionIsEmpty(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	h, tl := start(t, "spawn same "+pidFile+" ttl=60s; say READY; exit 0")
	tl.waitFor(t, "READY")
	grandchild := pidFromFile(t, pidFile)
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %+v", e)
	}
	leader := h.Pid()
	if st := stateOf(leader); st != 'Z' || h.isReaped() {
		t.Fatalf("after Wait the leader's state is %c, reaped %v: want a zombie holding the pid", st, h.isReaped())
	}
	if gone, proof := h.TreeGone(); gone {
		t.Fatalf("TreeGone = true, %q, with a live grandchild in the session", proof)
	}
	if st := stateOf(leader); st != 'Z' {
		t.Fatalf("a failed proof released the zombie: state %c", st)
	}
	// The group signal reaches the grandchild through the held group id.
	h.Stop(5 * time.Second)
	if alive(grandchild) {
		t.Error("the grandchild survived Stop")
	}
	if gone, proof := h.TreeGone(); !gone || !strings.HasPrefix(proof, "session") {
		t.Errorf("TreeGone after Stop = %v, %q", gone, proof)
	}
	waitGoneFromProc(t, leader, "the proof lets the zombie go")
	if !h.isReaped() {
		t.Error("the zombie is gone from /proc but the host does not know it")
	}
}

// Close is the final release: the zombie is reaped whether or not the session
// was proven empty, and from then on nothing is signalled by its ids.
func TestCloseLetsTheZombieGoAndTheHostStopsActingOnItsIds(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild")
	h, tl := start(t, "spawn same "+pidFile+" ttl=60s; say READY; exit 0")
	tl.waitFor(t, "READY")
	grandchild := pidFromFile(t, pidFile)
	waitExit(t, h)
	leader := h.Pid()
	if st := stateOf(leader); st != 'Z' {
		t.Fatalf("the leader's state is %c, want a zombie", st)
	}
	h.killFor = 100 * time.Millisecond
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	waitGoneFromProc(t, leader, "Close lets the zombie go")
	gone, proof := h.TreeGone()
	if gone || !strings.Contains(proof, "no longer reserved") {
		t.Errorf("TreeGone = %v, %q: want not gone, the id no longer reserved", gone, proof)
	}
	h.Stop(0)
	if !staysRunning(t, grandchild, 300*time.Millisecond) {
		t.Error("Stop signalled a member by an id that is no longer reserved")
	}
}

// The reviewer's probe: the host believes its child was reaped without the
// session having been proven empty, and a stranger that called setsid holds
// the number. Neither the proof nor Stop may treat it as a member.
func TestAStrangerThatReusedTheSessionIdIsNeverSignalled(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	cmd := exec.Command(sleep, "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stranger := cmd.Process.Pid
	t.Cleanup(func() { cmd.Wait() }) // runs after the kill below: cleanups run last in, first out
	killAtCleanup(t, stranger)
	h := &Host{
		pid: stranger, procRoot: "/proc", scanMax: DefaultProcScanLimit, walkMax: 10,
		cgroupNote: "fake", done: closedChan(), release: make(chan struct{}),
		reaped: true, killFor: 100 * time.Millisecond,
	}
	if gone, proof := h.TreeGone(); gone || !strings.Contains(proof, "no longer reserved") {
		t.Errorf("TreeGone = %v, %q", gone, proof)
	}
	h.Stop(0)
	if !staysRunning(t, stranger, 300*time.Millisecond) {
		t.Fatalf("Stop killed the stranger (state %c)", stateOf(stranger))
	}
	// With the leader unreaped the same number is the session, and is signalled.
	h = &Host{
		pid: stranger, procRoot: "/proc", scanMax: DefaultProcScanLimit, walkMax: 10,
		cgroupNote: "fake", done: closedChan(), release: make(chan struct{}),
		killFor: 2 * time.Second,
	}
	h.Stop(0)
	if staysRunning(t, stranger, patience) {
		t.Error("a member of the reserved session was not signalled")
	}
}

// A zombie that is still listed in cgroup.procs is not a process of the tree,
// and a proven group lets the zombie go; once it is reaped its number is
// nobody's, so a listing of it counts again. The host is built by hand, with
// a number that stands for the unreaped leader.
func TestCgroupProofIgnoresTheZombieAndReleasesIt(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	_, _, procsFile := fakeCgroup(t, self+"\n4242\n")
	h := &Host{pid: 4242, cgroupProcs: procsFile, walkMax: 10, done: closedChan(), release: make(chan struct{})}
	if h.TreeMethod() != "cgroup" {
		t.Fatalf("method %q", h.TreeMethod())
	}
	if gone, proof := h.TreeGone(); !gone {
		t.Fatalf("the unreaped leader counted as a process of the tree: %q", proof)
	}
	select {
	case <-h.release:
	default:
		t.Error("a proven group did not let the zombie go")
	}
	h.reaped = true
	gone, proof := h.TreeGone()
	if gone || !strings.Contains(proof, "4242") {
		t.Errorf("after the reap TreeGone = %v, %q: want the listed number to count", gone, proof)
	}
}

// sleeper starts a process of its own session that stands for a member of the
// tree, and kills it at the end of the test through a pidfd.
func sleeper(t *testing.T) int {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	cmd := exec.Command(sleep, "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Wait() }) // runs after the kill below: cleanups run last in, first out
	killAtCleanup(t, cmd.Process.Pid)
	return cmd.Process.Pid
}

// With the cgroup method Stop reaches the processes that left the child's
// session and process group but stayed in the unit, also in a child group, and
// leaves alone a process the group does not list (the review of the package,
// finding F4).
func TestStopSignalsEveryProcessOfTheControlGroup(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	procRoot, cgroupRoot, procsFile := fakeCgroup(t, self+"\n")
	dir := filepath.Dir(procsFile)
	files := t.TempDir()
	fileA, fileB := filepath.Join(files, "a"), filepath.Join(files, "b")
	script := "spawn session " + fileA + " detach ttl=60s; spawn session " + fileB + " detach ttl=60s; say READY; wait"

	for _, method := range []string{"session", "cgroup"} {
		t.Run(method, func(t *testing.T) {
			os.Remove(fileA)
			os.Remove(fileB)
			h, tl := start(t, script, opts{mod: func(c *Config) {
				if method == "cgroup" {
					c.NoCgroup, c.ProcRoot, c.CgroupRoot = false, procRoot, cgroupRoot
				}
			}})
			if h.TreeMethod() != method {
				t.Fatalf("method %q (note %q)", h.TreeMethod(), h.cgroupNote)
			}
			h.killFor = 300 * time.Millisecond
			tl.waitFor(t, "READY")
			a, b := pidFromFile(t, fileA), pidFromFile(t, fileB)
			bystander := sleeper(t)
			t.Cleanup(func() {
				os.RemoveAll(filepath.Join(dir, "sub"))
				writeFile(t, procsFile, self+"\n")
			})
			writeFile(t, procsFile, self+"\n"+strconv.Itoa(a)+"\n")
			subGroup(t, dir, "sub", strconv.Itoa(b)+"\n")

			h.Stop(0)
			if method == "session" {
				// Both left the session: out of reach of the weaker method.
				if !staysRunning(t, a, 300*time.Millisecond) || !staysRunning(t, b, time.Millisecond) {
					t.Error("the session method reached a process that left the session")
				}
				return
			}
			for name, pid := range map[string]int{"the one in the group": a, "the one in the child group": b} {
				if staysRunning(t, pid, patience) {
					t.Errorf("%s was not signalled", name)
				}
			}
			if !staysRunning(t, bystander, 300*time.Millisecond) {
				t.Error("a process the group does not list was signalled")
			}
			if c := h.Counters(); c.PidfdFailures != 0 {
				t.Errorf("counters %+v", c)
			}
			// The fake group still lists them, so the proof says not gone.
			if gone, proof := h.TreeGone(); gone {
				t.Errorf("TreeGone = true, %q, with processes listed", proof)
			}
		})
	}
}

// The membership is read again after the pidfd is open, from the file the pid
// came from; a pid that is not listed any more, or from a file that cannot be
// read again, is not signalled. A failing pidfd call is counted, a process
// that is gone is not a failure.
func TestSignalListedChecksMembershipAndCountsFailures(t *testing.T) {
	listed, unlisted, unreadable, fatal := sleeper(t), sleeper(t), sleeper(t), sleeper(t)
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Skip("no true binary")
	}
	h := &Host{}
	relist := func(procs string) map[int]bool {
		switch procs {
		case "ok":
			return map[int]bool{listed: true, fatal: true}
		case "unreadable":
			return nil
		}
		return map[int]bool{}
	}
	h.signalListed([]cgroupMember{
		{listed, "ok"}, {unlisted, "ok-but-not-in-it"}, {unreadable, "unreadable"},
		{exited.Process.Pid, "ok"}, // gone: not a failure
	}, syscall.SIGKILL, relist)
	if staysRunning(t, listed, patience) {
		t.Error("a listed process was not signalled")
	}
	for name, pid := range map[string]int{"an unlisted process": unlisted, "one from an unreadable list": unreadable} {
		if !staysRunning(t, pid, 100*time.Millisecond) {
			t.Errorf("%s was signalled", name)
		}
	}
	if c := h.Counters(); c.PidfdFailures != 0 || c.CgroupErrors != 1 {
		t.Errorf("counters %+v, want no pidfd failure and one unreadable list", c)
	}
	// pidfd_open(0) and a signal number the kernel refuses fail for other
	// reasons than a missing process: both are counted, and nothing is killed.
	h.signalListed([]cgroupMember{{0, "ok"}}, syscall.SIGKILL, relist)
	if c := h.Counters(); c.PidfdFailures != 1 {
		t.Errorf("pidfd_open failure: counters %+v, want 1", c)
	}
	h.signalListed([]cgroupMember{{fatal, "ok"}}, syscall.Signal(999), relist)
	if c := h.Counters(); c.PidfdFailures != 2 {
		t.Errorf("pidfd_send_signal failure: counters %+v, want 2", c)
	}
	if !staysRunning(t, fatal, 100*time.Millisecond) {
		t.Error("a refused signal killed the process")
	}
}

func fakeSiginfo(signo, code, pid, status int32) *unix.Siginfo {
	info := &unix.Siginfo{Signo: signo, Code: code}
	b := unsafe.Slice((*byte)(unsafe.Pointer(info)), unsafe.Sizeof(*info))
	off := 16
	if unsafe.Sizeof(uintptr(0)) == 4 {
		off = 12
	}
	binary.NativeEndian.PutUint32(b[off:], uint32(pid))
	binary.NativeEndian.PutUint32(b[off+8:], uint32(status))
	return info
}

func TestExitFromSiginfo(t *testing.T) {
	chld := int32(syscall.SIGCHLD)
	for _, c := range []struct {
		name string
		info *unix.Siginfo
		want Exit
	}{
		{"exited", fakeSiginfo(chld, cldExited, 77, 3), Exit{Pid: 77, How: "exit", Code: 3}},
		{"status above 255 is kept as the kernel reports it", fakeSiginfo(chld, cldExited, 77, 255), Exit{Pid: 77, How: "exit", Code: 255}},
		{"killed", fakeSiginfo(chld, cldKilled, 77, 9), Exit{Pid: 77, How: "signal", Code: -1, Signal: syscall.SIGKILL}},
		{"dumped", fakeSiginfo(chld, cldDumped, 77, 11), Exit{Pid: 77, How: "signal", Code: -1, Signal: syscall.SIGSEGV, CoreDump: true}},
	} {
		if got := exitFromSiginfo(77, c.info); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	for name, info := range map[string]*unix.Siginfo{
		"another signal":  fakeSiginfo(int32(syscall.SIGUSR1), cldExited, 77, 0),
		"another pid":     fakeSiginfo(chld, cldExited, 78, 0),
		"stopped (CLD_4)": fakeSiginfo(chld, 5, 77, 0),
		"empty":           {},
	} {
		if got := exitFromSiginfo(77, info); got.How != "unknown" || got.Err == nil || got.Code != -1 {
			t.Errorf("%s: %+v, want an unknown exit with a reason", name, got)
		}
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

// groupHost starts an agent that exits at once in a fake control group that
// lists only the test process, waits for the exit, and returns the host and
// the group directory. The group is restored to that state before the
// cleanup's Stop, which would otherwise wait for a tree that is not gone.
func groupHost(t *testing.T, mod func(*Config)) (*Host, string) {
	t.Helper()
	self := strconv.Itoa(os.Getpid())
	procRoot, cgroupRoot, procsFile := fakeCgroup(t, self+"\n")
	h, _ := start(t, "exit 0", opts{mod: func(c *Config) {
		c.NoCgroup, c.ProcRoot, c.CgroupRoot = false, procRoot, cgroupRoot
		if mod != nil {
			mod(c)
		}
	}})
	if h.TreeMethod() != "cgroup" {
		t.Fatalf("method %q, want cgroup (note %q)", h.TreeMethod(), h.cgroupNote)
	}
	dir := filepath.Dir(procsFile)
	t.Cleanup(func() {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
		writeFile(t, procsFile, self+"\n")
	})
	waitExit(t, h)
	return h, dir
}

// subGroup makes the group dir/rel, and every group between, with an empty
// cgroup.procs, and puts content in the last one's.
func subGroup(t *testing.T, dir, rel, content string) {
	t.Helper()
	cur := dir
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		if _, err := os.Stat(filepath.Join(cur, "cgroup.procs")); err != nil {
			writeFile(t, filepath.Join(cur, "cgroup.procs"), "")
		}
	}
	writeFile(t, filepath.Join(cur, "cgroup.procs"), content)
}

// A process that moved into a child group is not listed in the parent's
// cgroup.procs: the proof has to read every group below (the review of the
// package, finding F1).
func TestTreeGoneSeesProcessesInDescendantGroups(t *testing.T) {
	h, dir := groupHost(t, nil)
	if gone, proof := h.TreeGone(); !gone || !strings.HasPrefix(proof, "cgroup") {
		t.Fatalf("TreeGone = %v, %q: want gone", gone, proof)
	}
	for _, rel := range []string{"escape", "a/b/c/d"} {
		subGroup(t, dir, rel, "4194000\n")
		gone, proof := h.TreeGone()
		if gone || !strings.HasPrefix(proof, "cgroup") || !strings.Contains(proof, "4194000") || !strings.Contains(proof, filepath.Join(rel, "cgroup.procs")) {
			t.Errorf("a process in %s: TreeGone = %v, %q, want not gone naming the pid and the group", rel, gone, proof)
		}
		if err := os.RemoveAll(filepath.Join(dir, strings.SplitN(rel, "/", 2)[0])); err != nil {
			t.Fatal(err)
		}
	}
	// Empty child groups hold nothing.
	subGroup(t, dir, "x/y", "\n")
	gone, proof := h.TreeGone()
	if !gone || !strings.HasPrefix(proof, "cgroup") || !strings.Contains(proof, "3 group(s)") {
		t.Errorf("empty sub-groups: TreeGone = %v, %q, want gone over 3 groups", gone, proof)
	}
	// The holder itself in a child group is not a process of the tree.
	writeFile(t, filepath.Join(dir, "x", "cgroup.procs"), strconv.Itoa(os.Getpid())+"\n")
	if gone, proof := h.TreeGone(); !gone {
		t.Errorf("the holder in a sub-group: TreeGone = false, %q", proof)
	}
	if c := h.Counters(); c.CgroupErrors != 0 || c.CgroupTruncated != 0 {
		t.Errorf("counters %+v, want no errors and no truncation", c)
	}
}

func TestTreeGoneProvesNothingWhenAGroupCannotBeRead(t *testing.T) {
	h, dir := groupHost(t, nil)
	for i, bad := range []func(){
		func() { writeFile(t, filepath.Join(dir, "k", "cgroup.procs"), "not a pid\n") },
		func() { // the file is a directory: unreadable
			if err := os.MkdirAll(filepath.Join(dir, "k", "cgroup.procs"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			writeFile(t, filepath.Join(dir, "k", "cgroup.procs"), strings.Repeat("1\n", maxProcsBytes/2+1))
		},
	} {
		os.RemoveAll(filepath.Join(dir, "k"))
		bad()
		before := h.Counters()
		gone, proof := h.TreeGone()
		if gone || !strings.HasPrefix(proof, "cgroup") {
			t.Errorf("case %d: TreeGone = %v, %q: an unreadable group proved the tree gone", i, gone, proof)
		}
		after := h.Counters()
		if after.CgroupErrors+after.CgroupTruncated == before.CgroupErrors+before.CgroupTruncated {
			t.Errorf("case %d: the failure was not counted: %+v -> %+v", i, before, after)
		}
	}
	// A group that vanished during the walk (a directory with no cgroup.procs
	// any more) was empty.
	os.RemoveAll(filepath.Join(dir, "k"))
	if err := os.MkdirAll(filepath.Join(dir, "gone"), 0o755); err != nil {
		t.Fatal(err)
	}
	if gone, proof := h.TreeGone(); !gone {
		t.Errorf("a vanished group blocked the proof: %q", proof)
	}
}

func TestCgroupWalkIsBoundedAndCounted(t *testing.T) {
	h, dir := groupHost(t, func(c *Config) { c.CgroupWalkLimit = 6 })
	for i := 0; i < 20; i++ {
		subGroup(t, dir, "g"+strconv.Itoa(i), "")
	}
	gone, proof := h.TreeGone()
	if gone || !strings.Contains(proof, "limit") {
		t.Errorf("a truncated walk proved the tree gone: %v, %q", gone, proof)
	}
	if got := h.Counters().CgroupTruncated; got != 1 {
		t.Errorf("CgroupTruncated = %d, want 1", got)
	}
	// A deep chain is bounded by the same limit.
	subGroup(t, dir, "chain/n/n/n/n/n/n/n/n/n/n", "")
	if gone, _ := h.TreeGone(); gone {
		t.Error("a truncated walk of a deep tree proved the tree gone")
	}
	if got := h.Counters().CgroupTruncated; got != 2 {
		t.Errorf("CgroupTruncated = %d, want 2", got)
	}
	h.walkMax = 1000
	if gone, proof := h.TreeGone(); !gone {
		t.Errorf("with room for every group: TreeGone = false, %q", proof)
	}
}

func TestWalkCgroupCountsGroupsAndIgnores(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cgroup.procs"), "10\n11\n")
	writeFile(t, filepath.Join(root, "a", "cgroup.procs"), "12\n")
	subGroup(t, root, "b/c", "13\n")
	if err := os.Symlink(root, filepath.Join(root, "link")); err != nil { // not followed
		t.Fatal(err)
	}
	s := walkCgroup(root, 100, 10, func(pid int) bool { return pid == 13 })
	var got []int
	for _, m := range s.members {
		got = append(got, m.pid)
	}
	if !s.selfSeen || s.groups != 4 || !s.complete() || !reflect.DeepEqual(got, []int{11, 12}) {
		t.Errorf("walk = %+v (pids %v), want self seen, 4 groups, members 11 and 12", s, got)
	}
	// The top group must be readable.
	if s := walkCgroup(filepath.Join(root, "missing"), 100, 10, nil); s.complete() || s.err == nil {
		t.Errorf("walk of a missing top group = %+v, want an error", s)
	}
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
	t.Run("child group", func(t *testing.T) {
		p, c, f := fakeCgroup(t, self+"\n")
		writeFile(t, filepath.Join(filepath.Dir(f), "kid", "cgroup.procs"), "")
		fall(t, p, c, "child groups")
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
	h := &Host{pid: 4242, procRoot: root, scanMax: 1000, done: closedChan(), release: make(chan struct{})}
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
	h := &Host{pid: 4242, procRoot: root, scanMax: 25, cgroupNote: "fake", done: closedChan(), release: make(chan struct{})}
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
	h := &Host{pid: 4242, procRoot: filepath.Join(t.TempDir(), "missing"), scanMax: 10, cgroupNote: "fake", done: closedChan(), release: make(chan struct{})}
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
