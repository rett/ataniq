package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/instanceid"
	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadunit"
)

// asBinaryEnv makes the test binary behave as the command: TestMain runs main
// and exits when it is set.
const asBinaryEnv = "ATANIQD_TEST_AS_BINARY"

// TestMain, with asBinaryEnv set, runs the command as main does (newApp, so
// the signal wiring is the real one) but with a manager that cannot reach
// systemd: an ordinary test never builds the real one.
func TestMain(m *testing.M) {
	if os.Getenv(asBinaryEnv) == "1" {
		args := os.Args[1:]
		a := newApp(args)
		a.newManager = func(context.Context) (leadunit.Manager, error) {
			return leaddaemon.UnavailableManager(errors.New("systemd is out of reach in a test")), nil
		}
		os.Exit(a.run(args))
	}
	os.Exit(m.Run())
}

// TestMainWiringCompiles checks the wiring of main without running it: main
// and the real manager factory exist with the types the command needs, and
// newApp, for a command that is not run, takes no signal and leaves the
// factory set. Nothing here calls the factory, which would build the real
// systemd manager.
func TestMainWiringCompiles(t *testing.T) {
	var _ func() = main
	var _ func(context.Context) (leadunit.Manager, error) = (&app{}).systemdManager
	a := newApp([]string{"version"})
	if a.newManager == nil || a.signals != nil || a.getenv == nil || a.stdout != os.Stdout || a.stderr != os.Stderr {
		t.Fatalf("newApp: %+v", a)
	}
}

// TestStatusEndsOnASignal: SIGTERM and SIGINT end status at once, and
// SIGTERM still ends the daemon cleanly.
func TestStatusEndsOnASignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			if sig == syscall.SIGINT && signal.Ignored(sig) {
				t.Skip("SIGINT is ignored in this process, and so in the child")
			}
			// A listener that accepts and never answers.
			socket := shortDir(t) + "/silent.sock"
			l, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				if c, err := l.Accept(); err == nil {
					accepted <- c
				}
			}()
			cmd := binary(t, "status", "--socket", socket)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() { cmd.Process.Kill() })
			select {
			case c := <-accepted:
				defer c.Close()
			case err := <-exited:
				t.Fatalf("status ended before it connected: %v", err)
			case <-time.After(20 * time.Second):
				t.Fatal("status did not connect in 20 s")
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			// The client's own timeout is 10 s; the signal must be faster.
			select {
			case err := <-exited:
				var ee *exec.ExitError
				if !asExit(err, &ee) || ee.ExitCode() == 0 {
					t.Fatalf("status ended with %v after %s", err, sig)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("status did not end within 5 s of %s", sig)
			}
		})
	}
}

type testApp struct {
	*app
	out, err bytes.Buffer
	signals  chan os.Signal
	fake     *leadunit.Fake
	env      map[string]string
}

func newTestApp(t *testing.T, env map[string]string) *testApp {
	t.Helper()
	ta := &testApp{signals: make(chan os.Signal, 2), env: env,
		fake: leadunit.NewFake(leadunit.NewFakeClock(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)))}
	ta.app = &app{
		getenv:  func(k string) string { return env[k] },
		signals: ta.signals,
		newManager: func(context.Context) (leadunit.Manager, error) {
			return ta.fake, nil
		},
	}
	ta.stdout, ta.stderr = &ta.out, &ta.err
	return ta
}

// shortDir returns a short private folder: a Unix socket path is short.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ac")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		nil, {"start"}, {"stop"}, {"define"}, {"help"}, {"--state-dir", "x"}, {"version", "extra"},
		{"run", "extra"}, {"run", "--no-such-flag"}, {"run", "--state-dir"}, {"status", "extra"}, {"status", "--no-such"},
	} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitUsage {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if !strings.Contains(ta.err.String(), "usage: ataniqd run") || ta.out.Len() != 0 {
			t.Errorf("%v: stdout %q stderr %q", args, ta.out.String(), ta.err.String())
		}
	}
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--help"}); code != 0 || !strings.Contains(ta.out.String(), "This build is read-only") {
		t.Fatalf("--help: %d %q", code, ta.out.String())
	}
	ta = newTestApp(t, nil)
	if code := ta.run([]string{"run", "-h"}); code != 0 {
		t.Fatalf("run -h: %d", code)
	}
}

func TestUsageSaysReadOnlyAndOffersNothingToStart(t *testing.T) {
	if !strings.Contains(usage, "read-only") || !strings.Contains(usage, "no flag") {
		t.Fatalf("usage:\n%s", usage)
	}
	for _, word := range []string{"--start", "--stop", "--define", "--launch"} {
		if strings.Contains(usage, word) {
			t.Errorf("usage mentions %s", word)
		}
	}
}

func TestVersion(t *testing.T) {
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"version"}); code != 0 || ta.out.String() != "ataniqd "+leaddaemon.DefaultVersion+"\n" {
		t.Fatalf("%d %q", code, ta.out.String())
	}
}

func TestNoDefaultWithoutRuntimeDir(t *testing.T) {
	base := shortDir(t)
	env := map[string]string{"HOME": base}
	ta := newTestApp(t, env)
	code := ta.run([]string{"run"})
	if code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "XDG_RUNTIME_DIR") || !strings.Contains(ta.err.String(), "no shared folder") {
		t.Fatalf("run: %d %q", code, ta.err.String())
	}
	// Nothing was made under the home folder, and nothing in /tmp.
	if _, err := os.Lstat(filepath.Join(base, ".local")); err == nil {
		t.Fatal("a state folder was made before the refusal")
	}
	ta = newTestApp(t, env)
	if code := ta.run([]string{"status"}); code != leaddaemon.ExitUsage || !strings.Contains(ta.err.String(), "XDG_RUNTIME_DIR") {
		t.Fatalf("status: %d %q", code, ta.err.String())
	}
	// A relative XDG_RUNTIME_DIR is not a folder to use either.
	ta = newTestApp(t, map[string]string{"HOME": base, "XDG_RUNTIME_DIR": "run"})
	if code := ta.run([]string{"run"}); code != leaddaemon.ExitConfig {
		t.Fatalf("relative XDG_RUNTIME_DIR: %d", code)
	}
}

func TestNoStateFolderWithoutHome(t *testing.T) {
	ta := newTestApp(t, map[string]string{"XDG_RUNTIME_DIR": shortDir(t)})
	if code := ta.run([]string{"run"}); code != leaddaemon.ExitConfig || !strings.Contains(ta.err.String(), "state folder") {
		t.Fatalf("%d %q", code, ta.err.String())
	}
}

func initTestState(t *testing.T, state string) {
	t.Helper()
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"init", "--state-dir", state}); code != 0 {
		t.Fatalf("init: exit %d: %s", code, ta.err.String())
	}
}

func enrollTestProject(t *testing.T, state, home, project string) {
	t.Helper()
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"project-home", "--state-dir", state, "--home", home, project}); code != 0 {
		t.Fatalf("enroll %s: exit %d: %s", project, code, ta.err.String())
	}
}

// startApp runs `ataniqd run` with args in process and waits until status
// answers.
func startApp(t *testing.T, ta *testApp, args []string, socket string, localProjects ...string) <-chan int {
	t.Helper()
	cfg, _, code := ta.runConfig(args)
	if code >= 0 {
		t.Fatalf("run config: exit %d: %s", code, ta.err.String())
	}
	initTestState(t, cfg.StateDir)
	for _, project := range localProjects {
		enrollTestProject(t, cfg.StateDir, "self", project)
	}
	done := make(chan int, 1)
	go func() { done <- ta.run(append([]string{"run"}, args...)) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case code := <-done:
			t.Fatalf("the daemon ended with %d before it answered: %s", code, ta.err.String())
		default:
		}
		c, err := clientapi.Dial(context.Background(), socket)
		if err == nil {
			_, err = c.Hello(context.Background())
			c.Close()
			if err == nil {
				return done
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon did not answer in 20 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDefaultFoldersFromTheEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name      string
		env       func(base string) map[string]string
		stateWant func(base string) string
	}{
		{"XDG_STATE_HOME", func(b string) map[string]string {
			return map[string]string{"XDG_STATE_HOME": b + "/xs", "XDG_RUNTIME_DIR": b + "/xr", "HOME": b + "/home"}
		}, func(b string) string { return b + "/xs/ataniq" }},
		{"HOME", func(b string) map[string]string {
			return map[string]string{"XDG_RUNTIME_DIR": b + "/xr", "HOME": b + "/home"}
		}, func(b string) string { return b + "/home/.local/state/ataniq" }},
		{"a relative XDG_STATE_HOME is ignored", func(b string) map[string]string {
			return map[string]string{"XDG_STATE_HOME": "rel", "XDG_RUNTIME_DIR": b + "/xr", "HOME": b + "/home"}
		}, func(b string) string { return b + "/home/.local/state/ataniq" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base := shortDir(t)
			if err := os.Mkdir(base+"/xr", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(base+"/home", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(base+"/xs", 0o700); err != nil {
				t.Fatal(err)
			}
			ta := newTestApp(t, tt.env(base))
			socket := base + "/xr/ataniq/daemon.sock"
			done := startApp(t, ta, nil, socket)

			// status finds the socket by the same rule.
			st := newTestApp(t, tt.env(base))
			if code := st.run([]string{"status"}); code != 0 || !strings.HasPrefix(st.out.String(), "daemon version=") {
				t.Fatalf("status: %d %q %q", code, st.out.String(), st.err.String())
			}
			if _, err := os.Stat(leadlock.DaemonPath(tt.stateWant(base))); err != nil {
				t.Fatalf("no lock in the default state folder: %v", err)
			}
			ta.signals <- syscall.SIGTERM
			if code := <-done; code != 0 {
				t.Fatalf("exit %d: %s", code, ta.err.String())
			}
			if n := len(ta.fake.Calls()); n != 0 {
				t.Fatalf("%d unit calls from an empty registry", n)
			}
		})
	}
}

func TestStatusJSONFlag(t *testing.T) {
	base := shortDir(t)
	args := []string{"--state-dir", base + "/s", "--runtime-dir", base + "/r"}
	if err := os.Mkdir(base+"/r", 0o700); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp(t, nil)
	done := startApp(t, ta, args, base+"/r/daemon.sock")
	st := newTestApp(t, nil)
	if code := st.run([]string{"status", "--socket", base + "/r/daemon.sock", "--json"}); code != 0 {
		t.Fatalf("status: %d %q", code, st.err.String())
	}
	var res clientapi.StatusResult
	if err := json.Unmarshal(st.out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, st.out.String())
	}
	if res.Daemon.PID != os.Getpid() || !res.Daemon.LastReconcileCompleted || res.Leads == nil {
		t.Fatalf("%+v", res)
	}
	ta.signals <- syscall.SIGINT
	if code := <-done; code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// TestUnavailableLineIsTrueForAnEmptyRegistry: the line the command prints
// when the systemd tools are missing says the reconcile will be shown as not
// completed, and it is, also when the registry holds no role to ask about.
func TestUnavailableLineIsTrueForAnEmptyRegistry(t *testing.T) {
	base := shortDir(t)
	args := []string{"--state-dir", base + "/s", "--runtime-dir", base + "/r"}
	ta := newTestApp(t, nil)
	ta.newManager = func(context.Context) (leadunit.Manager, error) {
		return ta.unavailable(errors.New("no busctl")), nil
	}
	done := startApp(t, ta, args, base+"/r/daemon.sock")
	if got := ta.err.String(); !strings.Contains(got, "the systemd tools are unavailable (no busctl); the reconcile will be shown as not completed\n") {
		t.Fatalf("standard error:\n%s", got)
	}
	st := newTestApp(t, nil)
	if code := st.run([]string{"status", "--socket", base + "/r/daemon.sock", "--json"}); code != 0 {
		t.Fatalf("status: %d %q", code, st.err.String())
	}
	var res clientapi.StatusResult
	if err := json.Unmarshal(st.out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, st.out.String())
	}
	if len(res.Leads) != 0 || res.Daemon.LastReconcileCompleted {
		t.Fatalf("the registry is empty and the reconcile is shown as completed: %+v", res)
	}
	ta.signals <- syscall.SIGINT
	if code := <-done; code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestStatusWithNoDaemonExits20(t *testing.T) {
	base := shortDir(t)
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"status", "--socket", base + "/none.sock"}); code != leaddaemon.ExitNoDaemon || !strings.Contains(ta.err.String(), "no daemon") {
		t.Fatalf("%d %q", code, ta.err.String())
	}
}

func TestRefusedRunHasItsExitCode(t *testing.T) {
	base := shortDir(t)
	if err := os.Mkdir(base+"/s", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base+"/s", 0o750); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp(t, nil)
	code := ta.run([]string{"run", "--state-dir", base + "/s", "--runtime-dir", base + "/r"})
	if code != leaddaemon.ExitState || strings.Count(ta.err.String(), "\n") != 1 || !strings.Contains(ta.err.String(), "state: ") {
		t.Fatalf("%d %q", code, ta.err.String())
	}
}

// binary starts the test binary as the real command.
func binary(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	home := shortDir(t)
	cmd.Env = []string{asBinaryEnv + "=1", "HOME=" + home, "PATH=" + os.Getenv("PATH")}
	return cmd
}

func TestSecondDaemonAsASeparateProcess(t *testing.T) {
	base := shortDir(t)
	args := []string{"run", "--state-dir", base + "/s", "--runtime-dir", base + "/r"}
	socket := base + "/r/daemon.sock"

	initTestState(t, base+"/s")
	first := binary(t, args...)
	var firstErr bytes.Buffer
	first.Stderr = &firstErr
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- first.Wait() }()
	t.Cleanup(func() { first.Process.Kill() })

	// Wait for it to answer.
	deadline := time.Now().Add(20 * time.Second)
	for {
		ta := newTestApp(t, nil)
		if ta.run([]string{"status", "--socket", socket}) == 0 {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("the daemon ended: %v\n%s", err, firstErr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon did not answer in 20 s\n%s", firstErr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A second daemon on the same state folder is refused with its own code.
	second := binary(t, "run", "--state-dir", base+"/s", "--runtime-dir", base+"/r2")
	var secondErr bytes.Buffer
	second.Stderr = &secondErr
	err := second.Run()
	var ee *exec.ExitError
	if !asExit(err, &ee) || ee.ExitCode() != leaddaemon.ExitLockHeld {
		t.Fatalf("second daemon: %v\n%s", err, secondErr.String())
	}
	msg := secondErr.String()
	if strings.Count(msg, "\n") != 1 || !strings.Contains(msg, "held") ||
		!strings.Contains(msg, "pid hint "+strconv.Itoa(first.Process.Pid)) || !strings.Contains(msg, "not proof") {
		t.Fatalf("second daemon said %q", msg)
	}
	// It did not make a socket or take the first one's.
	if _, err := os.Lstat(base + "/r2"); err == nil {
		t.Fatal("the second daemon made a runtime folder")
	}

	// SIGTERM ends the first cleanly: exit 0, socket gone, lock free.
	if err := first.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the first daemon: %v\n%s", err, firstErr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the first daemon did not stop")
	}
	if _, err := os.Lstat(socket); err == nil {
		t.Fatal("the socket is still there")
	}
	l, err := leadlock.Acquire(leadlock.DaemonPath(base + "/s"))
	if err != nil {
		t.Fatalf("the lock is not free: %v", err)
	}
	l.Release()
}

func asExit(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

func TestRunRefusesForeignInstanceJournal(t *testing.T) {
	base := shortDir(t)
	state, other := base+"/s", base+"/other"
	initTestState(t, state)
	initTestState(t, other)
	journal := filepath.Join(leaddaemon.RegistryDirName, leadregistry.FileName)
	data, err := os.ReadFile(filepath.Join(other, journal))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, journal), data, 0600); err != nil {
		t.Fatal(err)
	}
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", base + "/runtime"}); code != leaddaemon.ExitRegistry || !strings.Contains(ta.err.String(), leadregistry.ErrForeignInstance.Error()) {
		t.Fatalf("foreign journal: exit %d: %s", code, ta.err.String())
	}
}

func TestRestoredInstanceRefusesOldJournal(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	initTestState(t, state)
	before, err := instanceid.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(state, instanceid.FileName)
	backup, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise both a full-root restore and a key-only restore.
	for _, target := range []string{state, t.TempDir()} {
		if err := os.Chmod(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, instanceid.FileName), backup, 0600); err != nil {
			t.Fatal(err)
		}
		ta := newTestApp(t, nil)
		if code := ta.run([]string{"init", "--restored", "--state-dir", target}); code != 0 {
			t.Fatalf("restore: %d: %s", code, ta.err.String())
		}
		after, err := instanceid.Load(target)
		if err != nil || after.Authority != before.Authority || after.Incarnation == before.Incarnation || len(after.Incarnation) != 32 {
			t.Fatalf("restored identity %+v, %v", after, err)
		}
		if key, err := os.ReadFile(filepath.Join(target, instanceid.FileName)); err != nil || !bytes.Equal(key, backup) {
			t.Fatal("restore changed the key")
		}
		if target != state {
			initTestState(t, target)
		}
	}
	if _, err := leadregistry.Open(filepath.Join(state, leaddaemon.RegistryDirName), leadregistry.Options{}); !errors.Is(err, leadregistry.ErrForeignInstance) {
		t.Fatalf("old journal after restore: %v", err)
	}
}

func TestInitLabelOnlyOnFirstInitialization(t *testing.T) {
	state := filepath.Join(t.TempDir(), "a state")
	cmd := func(flags ...string) (int, string) {
		ta := newTestApp(t, nil)
		code := ta.run(append([]string{"init", "--state-dir", state}, flags...))
		return code, ta.err.String()
	}
	if code, msg := cmd("--label", "first"); code != 0 {
		t.Fatalf("init: %d %s", code, msg)
	}
	before, err := instanceid.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"first", "second", ""} {
		code, msg := cmd("--label", label)
		if code != leaddaemon.ExitState || !strings.Contains(msg, filepath.Join(state, instanceid.FileName)) || !strings.Contains(msg, "--label is accepted only on first init") {
			t.Errorf("second --label %q: %d %s", label, code, msg)
		}
	}
	if code, msg := cmd(); code != 0 {
		t.Fatalf("ordinary retry: %d %s", code, msg)
	}
	if got, err := instanceid.Load(state); err != nil || got != before {
		t.Fatalf("label retry: %+v, %v", got, err)
	}
}

func TestInstanceRefusalsNameTheirFiles(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	key := filepath.Join(state, instanceid.FileName)
	run := func() (int, string) {
		ta := newTestApp(t, nil)
		code := ta.run([]string{"run", "--state-dir", state, "--runtime-dir", filepath.Join(dir, "runtime")})
		return code, ta.err.String()
	}
	if code, msg := run(); code != leaddaemon.ExitState || !strings.Contains(msg, key) || !strings.Contains(msg, initHint(state)) {
		t.Fatalf("missing key: %d %s", code, msg)
	}
	initTestState(t, state)
	journal := journalPath(state)
	v1, err := os.ReadFile("../../internal/leadregistry/testdata/v1-base/registry.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, v1, 0600); err != nil {
		t.Fatal(err)
	}
	if code, msg := run(); code != leaddaemon.ExitRegistry || !strings.Contains(msg, journal) || !strings.Contains(msg, initHint(state)) {
		t.Fatalf("unbound journal: %d %s", code, msg)
	}
	for _, change := range []func() error{
		func() error { return os.Truncate(key, 1) },
		func() error { return os.Chmod(key, 0640) },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
		if code, msg := run(); code != leaddaemon.ExitState || !strings.Contains(msg, key) {
			t.Fatalf("refused key: %d %s", code, msg)
		}
	}
}

func TestInitValidatesLegacyJournalBeforeCreatingIdentity(t *testing.T) {
	base, err := os.ReadFile("../../internal/leadregistry/testdata/v1-base/registry.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	dir := filepath.Join(state, leaddaemon.RegistryDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := journalPath(state)
	bad := append(bytes.Clone(base), "damaged complete record\nunfinished"...)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		ta := newTestApp(t, nil)
		if code := ta.run([]string{"init", "--state-dir", state}); code != leaddaemon.ExitState || !strings.Contains(ta.err.String(), path) || !strings.Contains(ta.err.String(), "corrupt journal") {
			t.Fatalf("init: %d %s", code, ta.err.String())
		}
		entries, err := os.ReadDir(state)
		if err != nil || len(entries) != 1 || entries[0].Name() != leaddaemon.RegistryDirName {
			t.Fatalf("refused init left files in state: %v, %v", entries, err)
		}
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, bad) {
			t.Fatalf("refused init changed journal: %v", err)
		}
	}
	// A valid journal with an uncommitted tail must reach normal migration
	// intact. The first real open owns the recovery record.
	if err := os.WriteFile(path, append(base, "unfinished"...), 0600); err != nil {
		t.Fatal(err)
	}
	initTestState(t, state)
	got, err := os.ReadFile(path)
	if err != nil || !bytes.HasSuffix(got, []byte("unfinished")) {
		t.Fatalf("lost torn tail: %v", err)
	}
}

func TestInvalidIdentityInitLeavesOnlyOriginalFiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{instanceid.FileName, []string{"--restored"}},
		{instanceid.IncarnationFile, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(state, tc.name)
			if err := os.WriteFile(path, []byte("bad"), 0600); err != nil {
				t.Fatal(err)
			}
			ta := newTestApp(t, nil)
			if code := ta.run(append([]string{"init", "--state-dir", state}, tc.flags...)); code != leaddaemon.ExitState || !strings.Contains(ta.err.String(), path) {
				t.Fatalf("init: %d %s", code, ta.err.String())
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 1 || entries[0].Name() != tc.name {
				t.Fatalf("refused init left %v, %v", entries, err)
			}
		})
	}
}

// A full daemon start holds both the daemon and journal locks. Neither form
// of init can change the binding while the daemon answers clients.
func TestInitRefusedWhileDaemonServes(t *testing.T) {
	dir := shortDir(t)
	state, runtime := filepath.Join(dir, "state"), filepath.Join(dir, "run")
	daemon := newTestApp(t, nil)
	done := startApp(t, daemon, []string{"--state-dir", state, "--runtime-dir", runtime}, filepath.Join(runtime, "daemon.sock"))
	defer func() {
		daemon.signals <- syscall.SIGTERM
		if code := <-done; code != 0 {
			t.Errorf("daemon exit: %d", code)
		}
	}()
	before, err := instanceid.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--state-dir", state}, {"init", "--restored", "--state-dir", state}} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitState || !strings.Contains(ta.err.String(), "lock") {
			t.Fatalf("%v: %d %s", args, code, ta.err.String())
		}
	}
	if after, err := instanceid.Load(state); err != nil || after != before {
		t.Fatalf("binding after refused init: %+v, %v", after, err)
	}
}
