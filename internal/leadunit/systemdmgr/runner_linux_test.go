package systemdmgr

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadunit"
)

// None of these tests runs a tool: they resolve files in temporary
// directories and check what is refused before anything would run.

func writeTool(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolveToolFixedDirectoriesInOrder(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	pb := writeTool(t, b, "busctl", 0o755)
	writeTool(t, b, "systemctl", 0o755)
	pa := writeTool(t, a, "busctl", 0o755)
	r, err := NewExecRunner([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if r.Path("busctl") != pa || r.Path("systemctl") != filepath.Join(b, "systemctl") || pb == pa {
		t.Fatalf("%q %q", r.Path("busctl"), r.Path("systemctl"))
	}
	if r.Path("systemd-run") != "" || r.Path("sh") != "" {
		t.Fatal("a tool outside the two resolved")
	}
}

func TestResolveToolSkipsWhatItDoesNotTrust(t *testing.T) {
	dir, good := t.TempDir(), t.TempDir()
	writeTool(t, dir, "busctl", 0o666)    // writable by others, not executable
	writeTool(t, dir, "systemctl", 0o775) // writable by the group
	if err := os.Mkdir(filepath.Join(dir, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecRunner([]string{dir}); err == nil {
		t.Fatal("untrusted tools resolved")
	}
	if _, err := resolveTool([]string{"relative", ""}, "busctl"); err == nil {
		t.Fatal("relative directory used")
	}
	if err := os.Mkdir(filepath.Join(dir, "busctl2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTool([]string{dir}, "busctl2"); err == nil {
		t.Fatal("a directory resolved as a tool")
	}
	writeTool(t, good, "busctl", 0o644) // not executable
	if _, err := resolveTool([]string{good}, "busctl"); err == nil {
		t.Fatal("a file without an execute bit resolved")
	}
	// An untrusted candidate does not hide a trusted one further on.
	writeTool(t, good, "busctl", 0o755)
	if p, err := resolveTool([]string{dir, good}, "busctl"); err != nil || p != filepath.Join(good, "busctl") {
		t.Fatal(p, err)
	}
}

func TestDefaultToolDirsAreFixedAndAbsolute(t *testing.T) {
	for _, d := range DefaultToolDirs {
		if !filepath.IsAbs(d) {
			t.Errorf("%q", d)
		}
	}
}

func TestExecRunnerRefusesWhatItWouldNotRun(t *testing.T) {
	dir := t.TempDir()
	writeTool(t, dir, "busctl", 0o755)
	writeTool(t, dir, "systemctl", 0o755)
	r, err := NewExecRunner([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{nil, {}, {"sh", "-c", "true"}, {"/usr/bin/busctl"}, {"systemd-run"}} {
		if _, err := r.Run(context.Background(), argv); !errors.Is(err, ErrNotLaunched) {
			t.Errorf("%q: %v", argv, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, []string{"busctl", "--user"}); !errors.Is(err, ErrNotLaunched) || !errors.Is(err, context.Canceled) {
		t.Errorf("done context: %v", err)
	}
}

func TestNotLaunchedClassification(t *testing.T) {
	if !notLaunched(&fs.PathError{Op: "fork/exec", Path: "/x", Err: fs.ErrPermission}) {
		t.Error("fork/exec failure")
	}
	if !notLaunched(&exec.Error{Name: "x", Err: exec.ErrNotFound}) {
		t.Error("exec error")
	}
	if notLaunched(&exec.ExitError{}) {
		t.Error("an exit status means the tool ran")
	}
	if notLaunched(errors.New("signal: killed")) || notLaunched(context.DeadlineExceeded) {
		t.Error("a kill or a timeout means the tool ran")
	}
}

func TestSnippetIsBoundedAndPrintable(t *testing.T) {
	got := snippet("  a\x00b\n\x1b[31mé  ")
	if got != "a?b??[31m?" {
		t.Fatalf("%q", got)
	}
	long := snippet(string(make([]byte, 5000)))
	if len(long) > 210 {
		t.Fatal(len(long))
	}
}

// toolScript is a stand-in for a tool that the test itself writes under
// t.TempDir(): a shell script, named neither busctl nor systemctl, that waits
// for a line on a named pipe and then creates a marker file, so that "the tool
// delivered its request" is a fact the test can read. It is run through an
// ExecRunner built with the script as the path of its "busctl", which is the
// only way the real tools could be named here; nothing from the host is run
// but /bin/sh.
type toolScript struct {
	runner   *ExecRunner
	marker   string
	released atomic.Bool
	fifo     *os.File
}

func newToolScript(t *testing.T) *toolScript {
	t.Helper()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "gate")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Read and write, so that opening it never blocks and the bytes written
	// before the script opens it are kept.
	f, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	script := filepath.Join(dir, "fake-tool.sh")
	body := "#!/bin/sh\nread line < \"$GATE\"\n: > \"$MARKER\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ts := &toolScript{marker: filepath.Join(dir, "delivered"), fifo: f}
	ts.runner = &ExecRunner{
		paths: map[string]string{toolBusctl: script},
		env:   []string{"LANG=C", "GATE=" + fifo, "MARKER=" + ts.marker},
	}
	return ts
}

// releaseAfter lets the script go on after d.
func (ts *toolScript) releaseAfter(d time.Duration) {
	time.AfterFunc(d, func() {
		ts.released.Store(true)
		_, _ = ts.fifo.WriteString("go\n")
	})
}

func (ts *toolScript) delivered() bool {
	_, err := os.Stat(ts.marker)
	return err == nil
}

// A context that is cancelled while the tool runs must not let Run return
// before the tool has ended: the tool could still deliver its request after
// the caller was told the reply was lost.
func TestExecRunnerWaitsForTheToolWhenTheContextIsCancelled(t *testing.T) {
	ts := newToolScript(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	ts.releaseAfter(400 * time.Millisecond)
	_, err := ts.runner.Run(ctx, []string{toolBusctl, "--user"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v", err)
	}
	if errors.Is(err, ErrNotLaunched) {
		t.Fatalf("a context that ends mid-call is classified as nothing started: %v", err)
	}
	if !ts.released.Load() || !ts.delivered() {
		t.Fatalf("Run returned before the tool ended: released=%v delivered=%v", ts.released.Load(), ts.delivered())
	}
}

// The wait is bounded by the deadline the tool was given, not by the tool.
func TestExecRunnerWaitIsBoundedByTheDeadline(t *testing.T) {
	ts := newToolScript(t) // never released
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ts.runner.Run(ctx, []string{toolBusctl, "--user"})
	if err == nil || errors.Is(err, ErrNotLaunched) {
		t.Fatalf("error %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("Run took %s for a deadline of 300ms", d)
	}
	if ts.delivered() {
		t.Fatal("the tool was not cut off")
	}
}

// Start over a context that ends while the tool runs is a lost reply, never
// "nothing was started", and it returns only after the tool has ended.
func TestStartContextEndsMidCallIsALostReplyAfterTheToolEnded(t *testing.T) {
	ts := newToolScript(t)
	m := newManager(ts.runner, newFakeKernel(), 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	ts.releaseAfter(400 * time.Millisecond)
	st, err := m.Start(ctx, validSpec())
	if !errors.Is(err, leadunit.ErrReplyLost) || st != (leadunit.State{}) {
		t.Fatalf("%+v %v", st, err)
	}
	if errors.Is(err, leadunit.ErrUnavailable) || errors.Is(err, leadunit.ErrUnitExists) || errors.Is(err, ErrNotLaunched) {
		t.Fatalf("wrong class: %v", err)
	}
	if !ts.released.Load() || !ts.delivered() {
		t.Fatalf("Start returned before the tool ended: released=%v delivered=%v", ts.released.Load(), ts.delivered())
	}
}
