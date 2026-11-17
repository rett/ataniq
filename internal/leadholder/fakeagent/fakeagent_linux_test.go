//go:build linux

package fakeagent_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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

type result struct {
	stdout, stderr string
	code           int
	signal         syscall.Signal
}

// command builds the agent as a child process on pipes, in a session of its
// own so that it has no controlling terminal.
func command(ctx context.Context, script string, extraEnv ...string) *exec.Cmd {
	argv, env := fakeagent.Command(script)
	cmd := exec.CommandContext(ctx, argv[0])
	cmd.Env = append(env, extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

func run(t *testing.T, script string, extraEnv ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	cmd := command(ctx, script, extraEnv...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.code = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			r.signal = ws.Signal()
		}
	} else if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCommandNamesThisExecutableAndCarriesTheScript(t *testing.T) {
	argv, env := fakeagent.Command("say HI")
	self, _ := os.Executable()
	if len(argv) != 1 || argv[0] != self || !filepath.IsAbs(argv[0]) {
		t.Errorf("argv = %q, want [%q]", argv, self)
	}
	if want := []string{fakeagent.EnvScript + "=say HI"}; !reflect.DeepEqual(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
}

func TestOutputVerbs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bytes")
	if err := os.WriteFile(file, []byte("from a file\x00\xff"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := run(t, "print 1b5b324a; print 0x4142; say word; printfile "+file)
	if want := "\x1b[2JAB" + "word\r\n" + "from a file\x00\xff"; r.stdout != want || r.code != 0 {
		t.Errorf("stdout %q code %d, want %q and 0 (stderr %q)", r.stdout, r.code, want, r.stderr)
	}
}

func TestModeVerbs(t *testing.T) {
	r := run(t, "mode mouse1000 mouse1006 paste focus alt sync 7727; unmode alt 1000; frame 41; frameopen 4243; frame")
	want := "\x1b[?1000h\x1b[?1006h\x1b[?2004h\x1b[?1004h\x1b[?1049h\x1b[?2026h\x1b[?7727h" +
		"\x1b[?1049l\x1b[?1000l" +
		"\x1b[?2026hA\x1b[?2026l" + "\x1b[?2026hBC" + "\x1b[?2026h\x1b[?2026l"
	if r.stdout != want || r.code != 0 {
		t.Errorf("stdout %q code %d, want %q (stderr %q)", r.stdout, r.code, want, r.stderr)
	}
}

func TestExitStatusAndSignals(t *testing.T) {
	if r := run(t, "say a; exit 3; say b"); r.code != 3 || r.stdout != "a\r\n" {
		t.Errorf("exit 3: %+v", r)
	}
	if r := run(t, "die TERM"); r.signal != syscall.SIGTERM {
		t.Errorf("die TERM: %+v", r)
	}
	if r := run(t, "die sigkill"); r.signal != syscall.SIGKILL {
		t.Errorf("die sigkill: %+v", r)
	}
	if r := run(t, "ignore TERM; die HUP"); r.signal != syscall.SIGHUP {
		t.Errorf("die HUP: %+v", r)
	}
	if r := run(t, ""); r.code != 0 {
		t.Errorf("empty script: %+v", r)
	}
}

func TestScriptErrorsAreFoundBeforeAnythingRuns(t *testing.T) {
	for _, s := range []string{"say a; nosuch", "print", "say a b", "exit", "ignore KILL", "sleep"} {
		r := run(t, s)
		if r.stdout != "" && strings.HasPrefix(s, "say a;") {
			t.Errorf("%q wrote %q before the error", s, r.stdout)
		}
		if r.code != 64 && r.code != 70 {
			t.Errorf("%q: code %d, stderr %q, want 64 or 70", s, r.code, r.stderr)
		}
		if r.stderr == "" {
			t.Errorf("%q: no reason on stderr", s)
		}
	}
	if r := run(t, "say a; nosuch"); r.code != 64 {
		t.Errorf("an unknown verb exits %d, want 64", r.code)
	}
	if r := run(t, "print zz"); r.code != 70 || !strings.Contains(r.stderr, "print") {
		t.Errorf("a failing step: code %d stderr %q, want 70", r.code, r.stderr)
	}
}

func TestRunRefusesABadScript(t *testing.T) {
	if code := fakeagent.Run("not-a-verb"); code != 64 {
		t.Errorf("Run of a bad script = %d, want 64", code)
	}
}

func TestFloodIsDeterministicPacedAndChunkIndependent(t *testing.T) {
	const n = 100000
	r := run(t, "flood 0 100000 5")
	if want := fakeagent.FloodBytes(5, n); r.stdout != string(want) {
		t.Errorf("flood of seed 5 differs from FloodBytes (%d bytes)", len(r.stdout))
	}
	if r2 := run(t, "flood 0 100000 6"); r2.stdout == r.stdout || len(r2.stdout) != n {
		t.Errorf("another seed gave the same bytes, or %d bytes", len(r2.stdout))
	}
	if a, b := fakeagent.FloodBytes(5, 50), fakeagent.FloodBytes(5, 100); !bytes.Equal(a, b[:50]) {
		t.Error("a shorter flood is not a prefix of a longer one")
	}
	if got := fakeagent.FloodBytes(1, 64*3); !bytes.Equal(got[62:64], []byte("\r\n")) || !bytes.Equal(got[190:192], []byte("\r\n")) {
		t.Errorf("lines are not 62 bytes and CR LF: %q", got[:70])
	}
	begin := time.Now()
	r = run(t, "flood 20000 5000 9")
	if took := time.Since(begin); took < 150*time.Millisecond {
		t.Errorf("5000 bytes at 20000 a second took %v", took)
	}
	if r.stdout != string(fakeagent.FloodBytes(9, 5000)) {
		t.Errorf("a paced flood wrote other bytes: %d", len(r.stdout))
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	// Kill through a pidfd opened now: by the time the cleanup runs the pid
	// may have been reaped and reused.
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if err != unix.ESRCH {
			t.Errorf("pidfd_open(%d): %v", pid, err)
		}
		return pid
	}
	t.Cleanup(func() {
		_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		_ = unix.Close(fd)
	})
	return pid
}

// sessionAndGroup reads them from /proc.
func sessionAndGroup(t *testing.T, pid int) (sid, pgrp int) {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
	pgrp, _ = strconv.Atoi(f[2])
	sid, _ = strconv.Atoi(f[3])
	return sid, pgrp
}

func TestSpawnKinds(t *testing.T) {
	for _, kind := range []string{"same", "group", "session"} {
		t.Run(kind, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child")
			// The leader of its own session, so "same" has something to share.
			r := run(t, "spawn "+kind+" "+pidFile+" detach ttl=60s")
			if r.code != 0 {
				t.Fatalf("%+v", r)
			}
			child := readPid(t, pidFile) // spawn returned only once the file existed
			sid, pgrp := sessionAndGroup(t, child)
			switch kind {
			case "session":
				if sid != child || pgrp != child {
					t.Errorf("session %d group %d, want a session of its own (%d)", sid, pgrp, child)
				}
			case "group":
				if pgrp != child || sid == child {
					t.Errorf("session %d group %d: want a group of its own in a shared session", sid, pgrp)
				}
			case "same":
				if pgrp == child || sid == child {
					t.Errorf("session %d group %d: want the spawner's", sid, pgrp)
				}
			}
		})
	}
	if r := run(t, "spawn nowhere "+filepath.Join(t.TempDir(), "x")); r.code != 70 {
		t.Errorf("an unknown kind: %+v", r)
	}
}

func TestIgnoreKeepsTheAgentAliveThroughSIGTERM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	cmd := command(ctx, "ignore TERM; pidfile "+pidFile+"; wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the agent ended before it was ready: %v", err)
		case <-time.After(2 * time.Millisecond):
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-done:
		t.Fatalf("the agent died of SIGTERM: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	<-done
}

func TestReportOnPipes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "report")
	argv, env := fakeagent.Command("report " + file)
	env = append(env, "A=1", "B=two words")
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0])
	cmd.Env = env
	cmd.Dir = t.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	r, err := fakeagent.ReadReport(file)
	if err != nil {
		t.Fatal(err)
	}
	if r.StdinTTY || r.StdoutTTY || r.StderrTTY || r.Ctty || r.Tpgid != -1 {
		t.Errorf("on pipes, no terminal: %+v", r)
	}
	if r.Sid != r.Pid || r.Ppid != os.Getpid() {
		t.Errorf("sid %d pid %d ppid %d", r.Sid, r.Pid, r.Ppid)
	}
	if !reflect.DeepEqual(r.Env, env) {
		t.Errorf("env %q, want %q", r.Env, env)
	}
	if len(r.Args) != 1 || r.Cwd == "" {
		t.Errorf("args %q cwd %q", r.Args, r.Cwd)
	}
	if _, err := fakeagent.ReadReport(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("ReadReport of a missing file succeeded")
	}
}
