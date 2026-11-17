package boundedexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const sh = "/bin/sh"

func tool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("the test needs %s: %v", name, err)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func script(body string) Cmd {
	return Cmd{
		Argv:      []string{sh, "-c", body},
		Env:       []string{"PATH=/usr/bin:/bin"},
		Timeout:   10 * time.Second,
		MaxStdout: 1 << 20,
		MaxStderr: 1 << 20,
	}
}

func gone(pid int) bool {
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func pids(t *testing.T, out []byte) []int {
	t.Helper()
	var ps []int
	for _, f := range strings.Fields(string(out)) {
		n, err := strconv.Atoi(f)
		if err != nil {
			t.Fatalf("bad pid %q in %q", f, out)
		}
		ps = append(ps, n)
	}
	return ps
}

func TestSuccessWithOutput(t *testing.T) {
	res, err := Run(context.Background(), script("echo out; echo err >&2"))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" {
		t.Errorf("stdout %q stderr %q", res.Stdout, res.Stderr)
	}
	if res.Exit != 0 || res.StderrCut {
		t.Errorf("Exit %d StderrCut %v", res.Exit, res.StderrCut)
	}
}

func TestNonZeroExit(t *testing.T) {
	res, err := Run(context.Background(), script("echo partial; echo oops >&2; exit 3"))
	var xe *ExitError
	if !errors.As(err, &xe) {
		t.Fatalf("error %v is not an *ExitError", err)
	}
	if xe.Exit != 3 || string(xe.Stderr) != "oops\n" {
		t.Errorf("ExitError = %+v", xe)
	}
	if res.Exit != 3 || string(res.Stdout) != "partial\n" {
		t.Errorf("Result = %+v", res)
	}
}

func TestKilledBySignalIsExitErrorMinusOne(t *testing.T) {
	res, err := Run(context.Background(), script("kill -9 $$"))
	var xe *ExitError
	if !errors.As(err, &xe) || xe.Exit != -1 || res.Exit != -1 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestStartFailure(t *testing.T) {
	c := script("")
	c.Argv = []string{"/nonexistent/binary"}
	res, err := Run(context.Background(), c)
	if err == nil || res.Exit != -1 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestArgv0IsNotSearched(t *testing.T) {
	// "sh" is refused as relative even though PATH would find it.
	c := script("")
	c.Argv = []string{"sh", "-c", "true"}
	if _, err := Run(context.Background(), c); err == nil {
		t.Fatal("relative Argv[0] ran")
	}
}

func TestEnvironmentIsExactlyWhatWasGiven(t *testing.T) {
	t.Setenv("BOUNDEDEXEC_LEAK", "yes")
	env := tool(t, "env")
	c := script("")
	c.Argv = []string{env}
	c.Env = []string{"B=two words", "A=1", "EMPTY="}
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(res.Stdout), "B=two words\nA=1\nEMPTY=\n"; got != want {
		t.Errorf("env = %q, want %q", got, want)
	}
}

func TestNilEnvironmentIsEmpty(t *testing.T) {
	t.Setenv("BOUNDEDEXEC_LEAK", "yes")
	c := script("")
	c.Argv = []string{tool(t, "env")}
	c.Env = nil
	c.Dir = t.TempDir()
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 0 {
		t.Errorf("a nil Env gave %q", res.Stdout)
	}
}

func TestWorkingFolder(t *testing.T) {
	res, err := Run(context.Background(), script("pwd -P"))
	if err != nil || string(res.Stdout) != "/\n" {
		t.Fatalf("default dir: %q, %v", res.Stdout, err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := script("pwd -P")
	c.Dir = dir
	res, err = Run(context.Background(), c)
	if err != nil || string(res.Stdout) != dir+"\n" {
		t.Fatalf("given dir: %q, %v", res.Stdout, err)
	}
}

func TestStdin(t *testing.T) {
	c := script("read a; read b; echo \"$b $a\"")
	c.Stdin = strings.NewReader("one\ntwo\n")
	res, err := Run(context.Background(), c)
	if err != nil || string(res.Stdout) != "two one\n" {
		t.Fatalf("%q, %v", res.Stdout, err)
	}
}

func TestNilStdinIsEmpty(t *testing.T) {
	res, err := Run(context.Background(), script("read x; echo status=$?"))
	if err != nil || string(res.Stdout) != "status=1\n" {
		t.Fatalf("%q, %v", res.Stdout, err)
	}
}

func TestOnlyStandardDescriptorsArePassedOn(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	res, err := Run(context.Background(), script(`i=0; while [ $i -lt 64 ]; do [ -e /proc/$$/fd/$i ] && echo $i; i=$((i+1)); done`))
	if err != nil {
		t.Fatal(err)
	}
	// Test the open descriptors without opening one: a glob would open a
	// directory of its own.
	if got := strings.Fields(string(res.Stdout)); strings.Join(got, " ") != "0 1 2" {
		t.Errorf("open descriptors in the command: %q", res.Stdout)
	}
}

func TestTimeoutKillsCommandAndGrandchild(t *testing.T) {
	late := filepath.Join(t.TempDir(), "late")
	// The background subshell keeps stdout open and would write the file
	// after one second. The leader sleeps on.
	body := fmt.Sprintf("(sleep 1; echo x > %s) & echo $! ; echo $$; sleep 30", late)
	c := script(body)
	c.Timeout = 300 * time.Millisecond
	start := time.Now()
	res, err := Run(context.Background(), c)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("Run took %v after a 300ms time-out", elapsed)
	}
	if res.Exit != -1 {
		t.Errorf("Exit = %d, want -1", res.Exit)
	}
	ps := pids(t, res.Stdout)
	if len(ps) != 2 {
		t.Fatalf("pids %q", res.Stdout)
	}
	for _, p := range ps {
		if !gone(p) {
			t.Errorf("process %d is still there", p)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(late); err == nil {
		t.Error("the grandchild survived and wrote its file")
	}
}

func TestTimeoutKillsSleep(t *testing.T) {
	c := script("")
	c.Argv = []string{tool(t, "sleep"), "30"}
	c.Timeout = 200 * time.Millisecond
	start := time.Now()
	_, err := Run(context.Background(), c)
	if !errors.Is(err, ErrTimeout) || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestBackgroundProcessDoesNotOutliveAnOrdinaryExit(t *testing.T) {
	late := filepath.Join(t.TempDir(), "late")
	body := fmt.Sprintf("(sleep 1; echo x > %s) & echo done", late)
	start := time.Now()
	res, err := Run(context.Background(), script(body))
	if err != nil || string(res.Stdout) != "done\n" {
		t.Fatalf("%q, %v", res.Stdout, err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("Run waited %v for the background process", time.Since(start))
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(late); err == nil {
		t.Error("the background process survived the run")
	}
}

func TestOutputOverTheCap(t *testing.T) {
	c := script("while :; do echo yyyyyyyyy; done")
	c.MaxStdout = 1000
	start := time.Now()
	res, err := Run(context.Background(), c)
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("err = %v, want ErrOutputLimit", err)
	}
	if len(res.Stdout) != 1000 {
		t.Errorf("kept %d bytes, want 1000", len(res.Stdout))
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Run took %v", time.Since(start))
	}
}

func TestOutputOverTheCapAfterTheCommandEnded(t *testing.T) {
	c := script("echo 0123456789")
	c.MaxStdout = 5
	res, err := Run(context.Background(), c)
	if !errors.Is(err, ErrOutputLimit) || string(res.Stdout) != "01234" {
		t.Fatalf("%q, %v", res.Stdout, err)
	}
}

func TestOutputExactlyAtTheCap(t *testing.T) {
	c := script("printf 01234")
	c.MaxStdout = 5
	res, err := Run(context.Background(), c)
	if err != nil || string(res.Stdout) != "01234" {
		t.Fatalf("%q, %v", res.Stdout, err)
	}
}

func TestStderrIsCutAndDrained(t *testing.T) {
	// Far more than a pipe holds: the command would block if it were not
	// drained.
	c := script("head -c 1000000 /dev/zero | tr '\\0' e >&2; echo ok")
	c.MaxStderr = 100
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stderr) != 100 || !res.StderrCut {
		t.Errorf("kept %d bytes, cut %v", len(res.Stderr), res.StderrCut)
	}
	if string(res.Stdout) != "ok\n" {
		t.Errorf("stdout %q", res.Stdout)
	}
}

func TestStderrAtTheCapIsNotCut(t *testing.T) {
	c := script("printf 01234 >&2")
	c.MaxStderr = 5
	res, err := Run(context.Background(), c)
	if err != nil || string(res.Stderr) != "01234" || res.StderrCut {
		t.Fatalf("%q cut %v err %v", res.Stderr, res.StderrCut, err)
	}
}

func TestWatchdogStopsTheCommand(t *testing.T) {
	stop := errors.New("lease lost")
	var calls atomic.Int32
	c := script("echo started; sleep 30")
	c.WatchInterval = 20 * time.Millisecond
	c.Watch = func() error {
		if calls.Add(1) == 3 {
			return stop
		}
		return nil
	}
	start := time.Now()
	res, err := Run(context.Background(), c)
	var we *WatchError
	if !errors.As(err, &we) || !errors.Is(err, stop) {
		t.Fatalf("err = %v, want a *WatchError around %v", err, stop)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Run took %v", time.Since(start))
	}
	if string(res.Stdout) != "started\n" || res.Exit != -1 {
		t.Errorf("Result = %+v", res)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("watchdog called %d times, want 3", n)
	}
}

func TestWatchdogIsQuietWhenFine(t *testing.T) {
	var calls atomic.Int32
	c := script("sleep 0.3")
	c.WatchInterval = 20 * time.Millisecond
	c.Watch = func() error { calls.Add(1); return nil }
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 {
		t.Error("the watchdog never ran")
	}
}

func TestWatchdogDefaultInterval(t *testing.T) {
	calls := make(chan struct{}, 8)
	c := script("sleep 0.7")
	c.Watch = func() error { calls <- struct{}{}; return nil }
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if n := len(calls); n < 1 || n > 3 {
		t.Errorf("%d calls in 0.7s at the default interval", n)
	}
}

func TestWatchdogStopsAfterTheRun(t *testing.T) {
	var calls atomic.Int32
	c := script("sleep 0.2")
	c.WatchInterval = 10 * time.Millisecond
	c.Watch = func() error { calls.Add(1); return nil }
	if _, err := Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// Allow a call that was already running when Run returned.
	time.Sleep(50 * time.Millisecond)
	seen := calls.Load()
	time.Sleep(150 * time.Millisecond)
	if calls.Load() != seen {
		t.Error("the watchdog kept running after Run returned")
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	c := script("echo up; sleep 30")
	start := time.Now()
	res, err := Run(ctx, c)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Run took %v", time.Since(start))
	}
	if string(res.Stdout) != "up\n" {
		t.Errorf("stdout %q", res.Stdout)
	}
}

func TestContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := Run(ctx, script("sleep 30"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestContextAlreadyDone(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Run(ctx, script("echo x > "+marker))
	if !errors.Is(err, context.Canceled) || res.Exit != -1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran with a finished context")
	}
}

func TestStreamedStdout(t *testing.T) {
	var sink bytes.Buffer
	c := script("echo one; echo two; echo err >&2")
	c.Stdout = &sink
	res, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if sink.String() != "one\ntwo\n" || len(res.Stdout) != 0 {
		t.Errorf("sink %q, Result.Stdout %q", sink.String(), res.Stdout)
	}
	if string(res.Stderr) != "err\n" {
		t.Errorf("stderr %q", res.Stderr)
	}
}

func TestStreamedStdoutStillHasTheCap(t *testing.T) {
	var sink bytes.Buffer
	c := script("while :; do echo yyyyyyyyy; done")
	c.Stdout = &sink
	c.MaxStdout = 500
	res, err := Run(context.Background(), c)
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("err = %v", err)
	}
	if sink.Len() != 500 || len(res.Stdout) != 0 {
		t.Errorf("sink %d bytes, Result.Stdout %d bytes", sink.Len(), len(res.Stdout))
	}
}

type failWriter struct{ err error }

func (f failWriter) Write([]byte) (int, error) { return 0, f.err }

func TestStreamedStdoutSinkFailureStopsTheCommand(t *testing.T) {
	boom := errors.New("disk full")
	c := script("echo x; sleep 30")
	c.Stdout = failWriter{boom}
	start := time.Now()
	_, err := Run(context.Background(), c)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Run took %v", time.Since(start))
	}
}

func TestFirstStopWins(t *testing.T) {
	// A time-out and a cancel at nearly the same moment still give one
	// definite answer.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := script("sleep 30")
	c.Timeout = 100 * time.Millisecond
	_, err := Run(ctx, c)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}
