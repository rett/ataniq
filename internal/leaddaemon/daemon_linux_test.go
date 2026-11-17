package leaddaemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadunit"
)

func TestStartupOrderAndCleanShutdown(t *testing.T) {
	e := newEnv(t)
	e.prepare(func(reg *leadregistry.Registry) {
		role := lead(t, "alpha")
		if _, err := reg.Define(role, leadregistry.ManagerAtaniq, profile()); err != nil {
			t.Fatal(err)
		}
		e.launch(reg, role, leadregistry.StateReady)
	})
	calls := len(e.fake.Calls())
	d := e.startDaemon(e.config())
	if err := d.stop(); err != nil {
		t.Fatalf("Run returned %v after SIGTERM", err)
	}
	want := []Step{StepConfig, StepState, StepLock, StepSocketLock, StepRegistry, StepManager, StepReconcile, StepStaleSocket, StepSocket, StepServe, StepShutdown}
	if got := e.order.get(); !reflect.DeepEqual(got, want) {
		t.Fatalf("steps %v, want %v", got, want)
	}
	// The daemon never started or signalled a unit, and the unit it found is
	// still there.
	noStartNoSignal(t, e.fake, calls)
	if !e.fake.Loaded(lead(t, "alpha").UnitName()) {
		t.Fatal("the lead's unit is gone after the daemon ended")
	}
	// The lock is released and the registry closed; the socket is gone.
	e.lockFree()
	e.prepare(func(*leadregistry.Registry) {})
	if _, err := os.Lstat(e.socket()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket is still there: %v", err)
	}
	fi, err := os.Stat(e.state)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("state folder: %v, %v", fi, err)
	}
}

func TestLockFileHoldsThePid(t *testing.T) {
	e := newEnv(t)
	d := e.startDaemon(e.config())
	defer d.stop()
	b, err := os.ReadFile(leadlock.DaemonPath(e.state))
	if err != nil || string(b) != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("lock file %q, %v", b, err)
	}
}

func TestContextEndsTheDaemon(t *testing.T) {
	e := newEnv(t)
	d := e.startDaemon(e.config())
	d.cancel()
	if err := d.wait(); err != nil {
		t.Fatal(err)
	}
	e.lockFree()
}

func TestFailuresAreDistinctAndFailClosed(t *testing.T) {
	type tc struct {
		name  string
		setup func(e *env, cfg *Config)
		step  Step
		code  int
		text  string
		order []Step
	}
	tests := []tc{
		{"root", func(e *env, cfg *Config) { cfg.EUID = func() int { return 0 } },
			StepConfig, ExitConfig, "root", []Step{StepConfig}},
		{"relative state folder", func(e *env, cfg *Config) { cfg.StateDir = "state" },
			StepConfig, ExitConfig, "absolute", []Step{StepConfig}},
		{"unclean runtime folder", func(e *env, cfg *Config) { cfg.RuntimeDir = e.runtime + "/../run" },
			StepConfig, ExitConfig, "absolute", []Step{StepConfig}},
		{"no manager factory", func(e *env, cfg *Config) { cfg.NewManager = nil },
			StepConfig, ExitConfig, "factory", []Step{StepConfig}},
		{"state folder open to the group", func(e *env, cfg *Config) {
			if err := os.Mkdir(e.state, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(e.state, 0o750); err != nil {
				t.Fatal(err)
			}
		}, StepState, ExitState, "0750", []Step{StepConfig, StepState}},
		{"state folder open to others", func(e *env, cfg *Config) {
			if err := os.Mkdir(e.state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(e.state, 0o705); err != nil {
				t.Fatal(err)
			}
		}, StepState, ExitState, "0705", []Step{StepConfig, StepState}},
		{"state folder is a symbolic link", func(e *env, cfg *Config) {
			target := filepath.Join(e.base, "elsewhere")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, e.state); err != nil {
				t.Fatal(err)
			}
		}, StepState, ExitState, "not a folder", []Step{StepConfig, StepState}},
		{"state folder is a file", func(e *env, cfg *Config) {
			if err := os.WriteFile(e.state, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, StepState, ExitState, "not a folder", []Step{StepConfig, StepState}},
		{"lock is a symbolic link", func(e *env, cfg *Config) {
			if err := os.Mkdir(e.state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/dev/null", leadlock.DaemonPath(e.state)); err != nil {
				t.Fatal(err)
			}
		}, StepLock, ExitLock, "symbolic link", []Step{StepConfig, StepState, StepLock}},
		{"corrupt registry", func(e *env, cfg *Config) {
			e.prepare(func(reg *leadregistry.Registry) {
				if _, err := reg.Define(lead(t, "alpha"), leadregistry.ManagerAtaniq, profile()); err != nil {
					t.Fatal(err)
				}
			})
			path := filepath.Join(e.state, RegistryDirName, leadregistry.FileName)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Damage a byte inside the first line, which is not an unfinished
			// final line.
			b[10] ^= 0x55
			if err := os.WriteFile(path, append(b, []byte("{}\n")...), 0o600); err != nil {
				t.Fatal(err)
			}
		}, StepRegistry, ExitRegistry, "corrupt", []Step{StepConfig, StepState, StepLock, StepSocketLock, StepRegistry}},
		{"registry locked by another writer", func(e *env, cfg *Config) {
			if err := os.MkdirAll(e.state, 0o700); err != nil {
				t.Fatal(err)
			}
			reg, err := leadregistry.Open(filepath.Join(e.state, RegistryDirName), leadregistry.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reg.Close() })
		}, StepRegistry, ExitRegistry, "locked", []Step{StepConfig, StepState, StepLock, StepSocketLock, StepRegistry}},
		{"boot identity unreadable", func(e *env, cfg *Config) {
			cfg.BootID = func() (string, error) { return "", errors.New("no boot id") }
		}, StepManager, ExitManager, "boot identity", []Step{StepConfig, StepState, StepLock, StepSocketLock, StepRegistry, StepManager}},
		{"manager factory fails", func(e *env, cfg *Config) {
			cfg.NewManager = func(context.Context) (leadunit.Manager, error) { return nil, errors.New("no tools") }
		}, StepManager, ExitManager, "no tools", []Step{StepConfig, StepState, StepLock, StepSocketLock, StepRegistry, StepManager}},
		{"runtime folder open to the group", func(e *env, cfg *Config) {
			if err := os.Mkdir(e.runtime, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(e.runtime, 0o770); err != nil {
				t.Fatal(err)
			}
		}, StepSocketLock, ExitSocket, "group or others", []Step{StepConfig, StepState, StepLock, StepSocketLock}},
		{"a file where the socket goes", func(e *env, cfg *Config) {
			if err := os.Mkdir(e.runtime, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(e.socket(), []byte("mine"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, StepStaleSocket, ExitSocket, "not a socket", []Step{StepConfig, StepState, StepLock, StepSocketLock, StepRegistry, StepManager, StepReconcile, StepStaleSocket}},
		{"socket path too long", func(e *env, cfg *Config) {
			cfg.RuntimeDir = filepath.Join(e.base, strings.Repeat("r", 120))
		}, StepSocketLock, ExitSocket, "socket path", []Step{StepConfig, StepState, StepLock, StepSocketLock}},
	}
	seen := map[int]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			cfg := e.config()
			tt.setup(e, &cfg)
			err := e.runToEnd(cfg)
			se := mustFail(t, err, tt.step, tt.code)
			if !strings.Contains(se.Error(), tt.text) {
				t.Fatalf("message %q lacks %q", se.Error(), tt.text)
			}
			if strings.ContainsAny(se.Error(), "\n\r") {
				t.Fatalf("message %q is more than one line", se.Error())
			}
			if got := e.order.get(); !reflect.DeepEqual(got, tt.order) {
				t.Fatalf("steps %v, want %v", got, tt.order)
			}
			// Whatever step failed, the daemon released what it held. The
			// state folder checks need a state folder that is usable.
			if tt.step != StepConfig && tt.step != StepState && tt.name != "lock is a symbolic link" {
				e.lockFree()
			}
			if n := len(e.fake.Calls()); n != 0 {
				t.Fatalf("%d manager calls from a daemon that failed", n)
			}
		})
		seen[tt.code] = true
	}
	// The table reaches six exit codes; TestExitCodesAreDistinct covers all.
	if len(seen) < 6 {
		t.Fatalf("only %d distinct exit codes in the table", len(seen))
	}
}

func TestExitCodesAreDistinct(t *testing.T) {
	steps := map[int]string{}
	for _, c := range []struct {
		name string
		err  error
	}{
		{"config", &StartupError{Step: StepConfig, Err: errors.New("x")}},
		{"state", &StartupError{Step: StepState, Err: errors.New("x")}},
		{"lock held", &StartupError{Step: StepLock, Err: fmt.Errorf("%w: x", errLockHeld)}},
		{"lock", &StartupError{Step: StepLock, Err: errors.New("x")}},
		{"registry", &StartupError{Step: StepRegistry, Err: errors.New("x")}},
		{"manager", &StartupError{Step: StepManager, Err: errors.New("x")}},
		{"reconcile", &StartupError{Step: StepReconcile, Err: errors.New("x")}},
		{"socket", &StartupError{Step: StepSocket, Err: errors.New("x")}},
		{"serve", &StartupError{Step: StepServe, Err: errors.New("x")}},
		{"shutdown", &StartupError{Step: StepShutdown, Err: errors.New("x")}},
	} {
		code := ExitCode(c.err)
		if code == ExitOK || code == ExitFailure || code == ExitUsage {
			t.Fatalf("%s: code %d is not its own", c.name, code)
		}
		if other, dup := steps[code]; dup {
			t.Fatalf("%s and %s share code %d", c.name, other, code)
		}
		steps[code] = c.name
	}
	if ExitCode(nil) != ExitOK || ExitCode(errors.New("plain")) != ExitFailure {
		t.Fatal("nil or a plain error has the wrong code")
	}
}

func TestSecondDaemonInProcessIsRefused(t *testing.T) {
	e := newEnv(t)
	d := e.startDaemon(e.config())
	defer d.stop()
	e2 := newEnv(t)
	e2.state, e2.runtime = e.state, filepath.Join(e2.base, "run2")
	cfg := e2.config()
	err := e2.runToEnd(cfg)
	se := mustFail(t, err, StepLock, ExitLockHeld)
	if !strings.Contains(se.Error(), "held") || !strings.Contains(se.Error(), "pid hint "+strconv.Itoa(os.Getpid())) ||
		!strings.Contains(se.Error(), "not proof") {
		t.Fatalf("message %q", se.Error())
	}
	// It did not touch the first daemon's socket.
	if _, err := os.Lstat(e.socket()); err != nil {
		t.Fatalf("the first daemon's socket: %v", err)
	}
}

// helperEnv names the variable that turns the test binary into a process that
// holds a daemon lock.
const helperEnv = "LEADDAEMON_TEST_HOLD_LOCK"

// TestHelperHoldLock is not a test: re-executed with helperEnv set, it holds
// the daemon lock at that path until its standard input closes.
func TestHelperHoldLock(t *testing.T) {
	path := os.Getenv(helperEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	l, err := leadlock.Acquire(path)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(3)
	}
	defer l.Release()
	fmt.Println("locked")
	io.Copy(io.Discard, os.Stdin)
}

func TestSecondDaemonInAnotherProcessIsRefused(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.state, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$")
	cmd.Env = append(os.Environ(), helperEnv+"="+leadlock.DaemonPath(e.state))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper said %q, %v", line, err)
	}

	// A stale socket is left alone while another daemon holds the lock.
	if err := os.Mkdir(e.runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	makeStaleSocket(t, e.socket())
	err = e.runToEnd(e.config())
	se := mustFail(t, err, StepLock, ExitLockHeld)
	if !strings.Contains(se.Error(), "pid hint "+strconv.Itoa(cmd.Process.Pid)) || !strings.Contains(se.Error(), "not proof") {
		t.Fatalf("message %q lacks the helper's pid as a hint", se.Error())
	}
	if _, err := os.Lstat(e.socket()); err != nil {
		t.Fatalf("the stale socket was touched while the lock was held elsewhere: %v", err)
	}
	if got := e.order.get(); got[len(got)-1] != StepLock {
		t.Fatalf("steps %v", got)
	}

	// When the holder is gone the lock is free and the daemon starts.
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	d := e.startDaemon(e.config())
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
}

// makeStaleSocket leaves a socket file at path with nobody listening, as a
// daemon that was killed does.
func makeStaleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	if fi, err := os.Lstat(path); err != nil || fi.Mode().Type() != os.ModeSocket {
		t.Fatalf("no stale socket: %v", err)
	}
}

func TestStaleSocketIsRemovedOnlyAfterTheLock(t *testing.T) {
	e := newEnv(t)
	if err := os.Mkdir(e.runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	makeStaleSocket(t, e.socket())
	if err := os.MkdirAll(e.state, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := leadlock.Acquire(leadlock.DaemonPath(e.state))
	if err != nil {
		t.Fatal(err)
	}
	mustFail(t, e.runToEnd(e.config()), StepLock, ExitLockHeld)
	if _, err := os.Lstat(e.socket()); err != nil {
		t.Fatalf("the socket was removed without the lock: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	e.order = &orderLog{}
	cfg := e.config()
	d := e.startDaemon(cfg)
	if !strings.Contains(e.log.String(), "removed a stale socket") {
		t.Fatalf("log:\n%s", e.log)
	}
	// It answers: the socket in place is the new one.
	c, err := clientapi.Dial(context.Background(), e.socket())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Hello(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestSignalDuringStartupStopsBeforeServing(t *testing.T) {
	e := newEnv(t)
	cfg := e.config()
	// A signal that arrives while the manager is built: the daemon goes on to
	// nothing else and releases the lock.
	cfg.NewManager = func(ctx context.Context) (leadunit.Manager, error) {
		e.signals <- syscall.SIGINT
		<-ctx.Done()
		return e.fake, nil
	}
	if err := e.runToEnd(cfg); err != nil {
		t.Fatal(err)
	}
	if got := e.order.get(); got[len(got)-1] != StepManager {
		t.Fatalf("steps %v", got)
	}
	e.lockFree()
	e.noSocketMade()
}

// TestStuckServerIsReportedAndNothingIsClosedUnderIt: a server that does not
// end within the grace makes Run return its own error, which says what is
// left, and Run neither closes the registry nor releases the lock.
func TestStuckServerIsReportedAndNothingIsClosedUnderIt(t *testing.T) {
	e := newEnv(t)
	cfg := e.config()
	cfg.ShutdownGrace = 100 * time.Millisecond
	// The server asks for the peer's user before it reads a request; it is
	// stuck there until the test lets it go.
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cfg.Server.PeerUID = func(net.Conn) (uint32, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return 0, errors.New("released by the test")
	}
	d := e.startDaemon(cfg)
	defer close(release)
	c, err := net.Dial("unix", d.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("the server did not reach the peer check")
	}

	journal := filepath.Join(e.state, RegistryDirName, leadregistry.FileName)
	before := openDescriptors(t, journal)
	if before == 0 {
		t.Fatalf("the daemon holds no descriptor to %s while it runs; the check below would prove nothing", journal)
	}

	err = d.stop()
	se := mustFail(t, err, StepShutdown, ExitShutdown)
	for _, want := range []string{"did not stop within 100ms", "registry is left open", "daemon lock held", "end with this process"} {
		if !strings.Contains(se.Error(), want) {
			t.Errorf("error %q does not say %q", se, want)
		}
	}
	// The message is in the returned error, which the command prints; the
	// daemon does not log it as well, so that standard error has it once.
	if strings.Contains(e.log.String(), "did not stop within") {
		t.Errorf("the message is also in the log:\n%s", e.log)
	}
	// The registry is still open: no descriptor to its journal was closed.
	if after := openDescriptors(t, journal); after != before {
		t.Errorf("descriptors to the registry journal: %d before the stop, %d after; the registry was closed under a server that may still run", before, after)
	}
	// The daemon lock and the socket lock are still held.
	for _, path := range []string{leadlock.DaemonPath(e.state), SocketLockPath(e.runtime)} {
		if l, err := leadlock.Acquire(path); err == nil {
			l.Release()
			t.Fatalf("%s was released under a server that may still run", path)
		} else if !errors.Is(err, leadlock.ErrHeld{}) {
			t.Fatal(err)
		}
	}
}

// openDescriptors counts the descriptors of this process whose target is path.
func openDescriptors(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		// An entry can be gone by now (the directory read holds one itself).
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && target == path {
			n++
		}
	}
	return n
}

// holdLockInAnotherProcess starts a process that holds the lock at path and
// returns its command, once it said it holds it. Its standard input closing
// ends it; the test may kill it instead.
func holdLockInAnotherProcess(t *testing.T, path string) (*exec.Cmd, io.Closer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$")
	cmd.Env = append(os.Environ(), helperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper said %q, %v", line, err)
	}
	return cmd, stdin
}

// TestTwoDaemonsOneRuntimeFolder: a daemon with another state folder and the
// same runtime folder is refused at the socket lock, and the first daemon's
// socket is neither removed nor replaced; it still answers, and its shutdown
// leaves nothing of either.
func TestTwoDaemonsOneRuntimeFolder(t *testing.T) {
	a := newEnv(t)
	da := a.startDaemon(a.config())
	if _, err := os.Lstat(a.socket()); err != nil {
		t.Fatal(err)
	}

	// B has a role shown ready whose unit is gone, which a reconcile would
	// write to its journal. B is refused before its registry is opened, so
	// nothing is written and the manager is not asked.
	b := newEnv(t)
	b.runtime = a.runtime
	aReadyRoleWhoseUnitIsGone(t, b)
	journal := filepath.Join(b.state, RegistryDirName, leadregistry.FileName)
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(b.fake.Calls())
	err = b.runToEnd(b.config())
	se := mustFail(t, err, StepSocketLock, ExitSocket)
	if got, want := b.order.get(), []Step{StepConfig, StepState, StepLock, StepSocketLock}; !reflect.DeepEqual(got, want) {
		t.Errorf("steps %v, want %v", got, want)
	}
	if after, err := os.ReadFile(journal); err != nil || !bytes.Equal(after, before) {
		t.Errorf("the refused daemon wrote to its journal: %v", err)
	}
	if n := len(b.fake.Calls()); n != calls {
		t.Errorf("the refused daemon made %d manager calls", n-calls)
	}
	for _, want := range []string{"another daemon serves the runtime folder", "pid hint " + strconv.Itoa(os.Getpid()), "not proof"} {
		if !strings.Contains(se.Error(), want) {
			t.Errorf("message %q lacks %q", se.Error(), want)
		}
	}
	if strings.ContainsAny(se.Error(), "\n\r") {
		t.Errorf("message %q is more than one line", se.Error())
	}
	if strings.Contains(b.log.String(), "removed a stale socket") {
		t.Errorf("B removed a socket:\n%s", b.log)
	}
	// B released what it held, and A's socket is the one in place and answers.
	b.lockFree()
	c, err := clientapi.Dial(context.Background(), a.socket())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h, err := c.Hello(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != "9.9.9-test" {
		t.Fatalf("hello %+v", h)
	}
	if got := status(t, a.socket()); got.Daemon.PID != os.Getpid() {
		t.Fatalf("daemon block %+v", got.Daemon)
	}
	c.Close()

	if err := da.stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(a.socket()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("A's socket is still there: %v", err)
	}
	// With A gone, B starts on the same runtime folder.
	b.order = &orderLog{}
	db := b.startDaemon(b.config())
	if err := db.stop(); err != nil {
		t.Fatal(err)
	}
}

// TestTooLongSocketPathLeavesNothingBehind: a runtime folder whose socket path
// does not fit in a Unix socket address is refused before the folder or the
// socket lock file is made, whether the folder is missing or already there.
func TestTooLongSocketPathLeavesNothingBehind(t *testing.T) {
	for _, tt := range []struct {
		name  string
		exist bool
	}{{"folder missing", false}, {"folder there", true}} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			cfg := e.config()
			cfg.RuntimeDir = filepath.Join(e.base, strings.Repeat("r", 120))
			if tt.exist {
				if err := os.Mkdir(cfg.RuntimeDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			mustFail(t, e.runToEnd(cfg), StepSocketLock, ExitSocket)
			if !tt.exist {
				if _, err := os.Lstat(cfg.RuntimeDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the runtime folder was made: %v", err)
				}
				return
			}
			if left, err := os.ReadDir(cfg.RuntimeDir); err != nil || len(left) != 0 {
				t.Fatalf("the folder holds %d entries after the refusal: %v", len(left), err)
			}
		})
	}
}

// TestShutdownReleasesTheSocketLock: after a clean shutdown the socket is gone
// and the socket lock can be taken; the lock file itself stays.
func TestShutdownReleasesTheSocketLock(t *testing.T) {
	e := newEnv(t)
	d := e.startDaemon(e.config())
	held, err := leadlock.Acquire(SocketLockPath(e.runtime))
	if err == nil {
		held.Release()
		t.Fatal("the socket lock is free while the daemon serves")
	} else if !errors.Is(err, leadlock.ErrHeld{}) {
		t.Fatal(err)
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(e.socket()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket is still there: %v", err)
	}
	held, err = leadlock.Acquire(SocketLockPath(e.runtime))
	if err != nil {
		t.Fatalf("the socket lock is not free after the shutdown: %v", err)
	}
	held.Release()
	if fi, err := os.Lstat(SocketLockPath(e.runtime)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file: %v, %v", fi, err)
	}
	e.lockFree()
}

// TestSocketLockHeldElsewhereLeavesTheSocketAlone: while a process that is not
// a daemon of this state folder holds the socket lock, a socket in the
// runtime folder is not removed as stale.
func TestSocketLockHeldElsewhereLeavesTheSocketAlone(t *testing.T) {
	e := newEnv(t)
	if err := os.Mkdir(e.runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd, _ := holdLockInAnotherProcess(t, SocketLockPath(e.runtime))
	makeStaleSocket(t, e.socket())
	err := e.runToEnd(e.config())
	se := mustFail(t, err, StepSocketLock, ExitSocket)
	if !strings.Contains(se.Error(), "pid hint "+strconv.Itoa(cmd.Process.Pid)) || !strings.Contains(se.Error(), "not proof") {
		t.Fatalf("message %q lacks the holder's pid as a hint", se.Error())
	}
	if _, err := os.Lstat(e.socket()); err != nil {
		t.Fatalf("the socket was removed while the socket lock was held elsewhere: %v", err)
	}
	e.lockFree()
}

// TestCrashedDaemonLeftoversDoNotBlock: a daemon that was killed leaves its
// socket and both lock files, the second with a pid in it; the next daemon
// starts, removes the socket and takes the locks.
func TestCrashedDaemonLeftoversDoNotBlock(t *testing.T) {
	e := newEnv(t)
	if err := os.Mkdir(e.runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd, _ := holdLockInAnotherProcess(t, SocketLockPath(e.runtime))
	makeStaleSocket(t, e.socket())
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	pid, err := leadlock.ReadHolder(SocketLockPath(e.runtime))
	if err != nil || pid != cmd.Process.Pid {
		t.Fatalf("the lock file the crash left: pid %d, %v", pid, err)
	}
	d := e.startDaemon(e.config())
	if !strings.Contains(e.log.String(), "removed a stale socket") {
		t.Fatalf("log:\n%s", e.log)
	}
	if got := status(t, d.socket); got.Daemon.PID != os.Getpid() {
		t.Fatalf("daemon block %+v", got.Daemon)
	}
	if pid, err := leadlock.ReadHolder(SocketLockPath(e.runtime)); err != nil || pid != os.Getpid() {
		t.Fatalf("the socket lock file holds pid %d, %v", pid, err)
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
}
