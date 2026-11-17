//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helperEnv makes the test binary act as the harness itself, so that the
// child helper can be run as a separate process. Nothing here talks to
// systemd, and no test starts a unit.
const helperEnv = "LEADUNIT_QUALIFY_TEST_HELPER"

// refuseManager is the manager factory of every test: it builds nothing. A test
// reaches the real manager only through run, which no test calls, so no test
// can start a unit by construction and not only because an earlier check
// happens to fail.
func refuseManager() (lifecycle, error) {
	return nil, errors.New("a test must not build a manager")
}

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(runWith(os.Args[1:], os.Stdout, os.Stderr, refuseManager))
	}
	os.Exit(m.Run())
}

func TestParseArgs(t *testing.T) {
	good := []struct {
		args []string
		want options
	}{
		{nil, options{}},
		{[]string{"--state-dir", "/var/q"}, options{stateDir: "/var/q"}},
		{[]string{"--run", "--state-dir", "/var/q"}, options{run: true, stateDir: "/var/q"}},
		{[]string{"--state-dir=/var/q", "--run=true"}, options{run: true, stateDir: "/var/q"}},
	}
	for _, c := range good {
		got, err := parseArgs(c.args)
		if err != nil || got != c.want {
			t.Errorf("%q: %+v %v", c.args, got, err)
		}
	}
	bad := [][]string{
		{"--run"},
		{"--run", "--state-dir", ""},
		{"--state-dir", "relative"},
		{"--state-dir", "/var/q/"},
		{"--state-dir", "/var/../q"},
		{"--nope"},
		{"extra"},
		{"--run", "--state-dir", "/var/q", "extra"},
		{"child"}, // only runWith() routes the child subcommand
	}
	for _, args := range bad {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
	if _, err := parseArgs([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("-h: %v", err)
	}
}

func TestDryRunPrintsThePlanAndTouchesNothing(t *testing.T) {
	for _, args := range [][]string{nil, {"--state-dir", "/var/q"}} {
		var out, errb bytes.Buffer
		if code := runWith(args, &out, &errb, refuseManager); code != exitOK {
			t.Fatalf("%q: exit %d: %s", args, code, errb.String())
		}
		if errb.Len() != 0 {
			t.Fatalf("stderr: %s", errb.String())
		}
		text := out.String()
		for _, want := range []string{
			"dry run. Nothing is started and systemd is not touched.",
			"ataniq-qualify-<random>.service",
			"child --ignore-term",
			"a second start with the same name is refused",
			"one with $HOME and one with %h, reach the child unchanged",
			"Empty is true for a fresh, never-used unit name",
			"never a unit whose description is not this run's",
			"SIGTERM to the first unit, whose process ignores it, leaves the control group populated",
			"ataniq-qualify-<random>-child.service, bound to the first (BindsTo, PartOf",
			"Inspect reads the",
			"first unit back as its parent",
			"the bound child unit stops with its parent (it is not loaded, with no signal sent to it)",
			"Empty is true for its control group",
			"wait 2 s, send SIGKILL",
			"Empty becomes true and the unit is not loaded",
			"always clean up",
			"Not attempted: a lost start reply.",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%q: dry run lacks %q", args, want)
			}
		}
		if len(args) > 0 && !strings.Contains(text, "--state-dir /var/q") {
			t.Errorf("the state folder is not named:\n%s", text)
		}
	}
}

func TestDryRunIsIdenticalForTheSameArguments(t *testing.T) {
	var a, b bytes.Buffer
	runWith(nil, &a, new(bytes.Buffer), refuseManager)
	runWith(nil, &b, new(bytes.Buffer), refuseManager)
	if a.String() != b.String() || a.Len() == 0 {
		t.Fatal("the dry run is not deterministic")
	}
}

func TestRunUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"--run"}, {"--bogus"}, {"extra"}, {"--state-dir", "rel"}} {
		var out, errb bytes.Buffer
		if code := runWith(args, &out, &errb, refuseManager); code != exitUsage {
			t.Errorf("%q: exit %d", args, code)
		}
		if out.Len() != 0 || !strings.Contains(errb.String(), "usage: leadunit-qualify") {
			t.Errorf("%q: stdout %q stderr %q", args, out.String(), errb.String())
		}
	}
	var out bytes.Buffer
	if code := runWith([]string{"-h"}, &out, new(bytes.Buffer), refuseManager); code != exitOK || !strings.Contains(out.String(), "usage:") {
		t.Errorf("-h: %d %q", code, out.String())
	}
}

// --run with a state folder that is not acceptable stops before any manager
// is built, so this test cannot reach systemd.
func TestRunRefusesAnUnacceptableStateFolder(t *testing.T) {
	root := t.TempDir()
	open := filepath.Join(root, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"missing": filepath.Join(root, "none"), "open": open, "file": file, "symlink": link} {
		var out, errb bytes.Buffer
		if code := runWith([]string{"--run", "--state-dir", dir}, &out, &errb, refuseManager); code != exitUsage || out.Len() != 0 {
			t.Errorf("%s: exit %d stdout %q stderr %q", name, code, out.String(), errb.String())
		}
	}
}

// With an acceptable folder --run reaches the manager factory and nowhere
// else: the test factory refuses, and the run ends before any check.
func TestRunBuildsTheManagerOnlyAfterTheFolderIsAccepted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var out, errb bytes.Buffer
	code := runWith([]string{"--run", "--state-dir", dir}, &out, &errb, func() (lifecycle, error) {
		calls++
		return nil, errors.New("no manager in a test")
	})
	if code != exitRuntime || calls != 1 || out.Len() != 0 || !strings.Contains(errb.String(), "no manager in a test") {
		t.Fatalf("exit %d calls %d stdout %q stderr %q", code, calls, out.String(), errb.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files written before any check: %v", entries)
	}
	// A dry run and a bad argument never ask for it.
	for _, args := range [][]string{nil, {"--bogus"}, {"--run"}, {"-h"}} {
		runWith(args, new(bytes.Buffer), new(bytes.Buffer), func() (lifecycle, error) {
			t.Errorf("%q: the manager was built", args)
			return nil, errors.New("no")
		})
	}
}

func TestCheckStateDirAcceptsAPrivateFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkStateDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestParseChild(t *testing.T) {
	o, err := parseChild([]string{"--ignore-term", "--ready-file", "/x/ready", "--max-life", "3s"})
	if err != nil || !o.ignoreTerm || o.readyFile != "/x/ready" || o.maxLife != 3*time.Second {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseChild([]string{"--args-file", "/x/args", "--literal", "$HOME", "--literal", "%h", "--literal=a%%b"})
	if err != nil || o.argsFile != "/x/args" || !reflect.DeepEqual(o.literals, []string{"$HOME", "%h", "a%%b"}) {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseChild(nil)
	if err != nil || o.ignoreTerm || o.maxLife != defaultMaxLife {
		t.Fatalf("%+v %v", o, err)
	}
	for _, args := range [][]string{{"--max-life", "0s"}, {"--max-life", "2h"}, {"--max-life", "x"}, {"extra"}, {"--bogus"}} {
		if _, err := parseChild(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
}

// helper starts the test binary as "leadunit-qualify child ...".
func helper(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"child"}, args...)...)
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	return cmd
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the child never wrote its ready file")
}

// startHelper starts the child, waits for its ready file and returns a
// channel that closes with its wait status.
func startHelper(t *testing.T, args ...string) (*exec.Cmd, <-chan *os.ProcessState) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := helper(t, append(args, "--ready-file", ready)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	done := make(chan *os.ProcessState, 1)
	go func() {
		st, _ := cmd.Process.Wait()
		done <- st
	}()
	waitFile(t, ready)
	return cmd, done
}

func TestChildIgnoringTermSurvivesItAndDiesOfKill(t *testing.T) {
	cmd, done := startHelper(t, "--ignore-term")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case st := <-done:
		t.Fatalf("the child ended after SIGTERM: %v", st)
	case <-time.After(500 * time.Millisecond):
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case st := <-done:
		ws, ok := st.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
			t.Fatalf("status %v", st)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the child survived SIGKILL")
	}
}

func TestChildWithoutIgnoreTermDiesOfTerm(t *testing.T) {
	cmd, done := startHelper(t)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case st := <-done:
		ws, ok := st.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Fatalf("status %v", st)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the child ignored SIGTERM without being asked to")
	}
}

// The child records the arguments it was started with, as a process sees them,
// before it writes its ready file, so that the harness can compare them with
// what it sent.
func TestChildRecordsItsArgumentsBeforeItIsReady(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	ready := filepath.Join(dir, "ready")
	sent := []string{"--ignore-term", "--ready-file", ready, "--args-file", argsFile,
		"--literal", literalDollar, "--literal", literalPercent, "--max-life", "30s"}
	cmd := helper(t, sent...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitFile(t, ready)
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal("the ready file exists before the argument record:", err)
	}
	var got []string
	if err := json.Unmarshal(b, &got); err != nil || !reflect.DeepEqual(got, sent) {
		t.Fatalf("%q %v", got, err)
	}
	fi, err := os.Stat(argsFile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
}

func TestChildEndsByItselfAtItsMaximumLife(t *testing.T) {
	cmd := helper(t, "--ignore-term", "--max-life", "200ms")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestChildFailsOnAnExistingReadyFileAndOnBadArguments(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	if err := os.WriteFile(ready, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	if code := runChild([]string{"--ready-file", ready, "--max-life", "10ms"}, &errb); code != exitRuntime {
		t.Fatalf("exit %d", code)
	}
	args := filepath.Join(t.TempDir(), "args")
	if err := os.WriteFile(args, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runChild([]string{"--args-file", args, "--max-life", "10ms"}, &errb); code != exitRuntime {
		t.Fatalf("an existing argument record: exit %d", code)
	}
	if code := runChild([]string{"--bogus"}, &errb); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}
