//go:build linux && amd64

package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/plan"
)

// These tests exercise the real launcher's pseudo-terminal, process group,
// environment and signal handling with the test binary itself as the command,
// run once per test. They start no sandbox tool, no namespace and no unit:
// nothing here names pasta, bubblewrap or the probe.

const helperEnv = "HARNESS_EXEC_LAUNCHER_HELPER"

// TestExecLauncherHelper is the command the launcher starts. It does nothing
// in a normal test run.
func TestExecLauncherHelper(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	switch mode {
	case "report":
		_, termErr := unix.IoctlGetTermios(0, unix.TCGETS)
		ws, _ := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
		// A controlling terminal is what /dev/tty opens: a new session has
		// none until a session leader takes one.
		tty, ctty := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if ctty == nil {
			tty.Close()
		}
		sid, _ := unix.Getsid(0)
		fmt.Printf("tty=%t rows=%d cols=%d pgid=%t env=%d sid=%d pid=%d ctty=%t\n",
			termErr == nil, ws.Row, ws.Col, syscall.Getpgrp() == os.Getpid(), len(os.Environ()), sid, os.Getpid(), ctty == nil)
		os.Exit(7)
	case "wait":
		fmt.Println("waiting")
		select {}
	case "leader-with-child":
		// The first process starts a member of its own group that waits, and
		// ends: what is left is a group with a dead leader.
		child := exec.Command(os.Args[0], "-test.run=^TestExecLauncherHelper$")
		child.Env = []string{helperEnv + "=wait"}
		if err := child.Start(); err != nil {
			fmt.Println("start:", err)
			os.Exit(98)
		}
		fmt.Printf("child=%d\n", child.Process.Pid)
		os.Exit(0)
	}
	os.Exit(99)
}

func helperPlan(t *testing.T) *plan.Plan {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &plan.Plan{Argv: []string{exe, "-test.run=^TestExecLauncherHelper$"}}
}

func launchHelper(t *testing.T, mode string) Process {
	t.Helper()
	if m, s, err := openPTY(1, 1); err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	} else {
		m.Close()
		s.Close()
	}
	p, err := ExecLauncher{}.Launch(LaunchRequest{Plan: helperPlan(t), Env: []string{helperEnv + "=" + mode}, Rows: 40, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.Signal(syscall.SIGKILL)
		p.Close()
	})
	return p
}

func TestExecLauncherRunsTheCommandOnAPseudoTerminalInItsOwnGroup(t *testing.T) {
	p := launchHelper(t, "report")
	select {
	case <-p.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the helper did not exit")
	}
	if e := p.Exit(); e.Code != 7 || e.Signal != "" || e.Err != "" {
		t.Errorf("exit %+v", e)
	}
	out := string(p.Terminal().Data)
	// The terminal is a terminal of the size given, the first process is its
	// own group leader, and only the environment the request gave is there.
	if !strings.Contains(out, "tty=true rows=40 cols=100 pgid=true env=1 ") {
		t.Errorf("terminal output %q", out)
	}
	if p.PID() <= 0 {
		t.Errorf("pid %d", p.PID())
	}
}

// The command starts in a session of its own: not the coordinator's, so that
// nothing in the tree keeps the coordinator's controlling terminal, and not
// one with the pseudo-terminal as its controlling terminal either.
func TestExecLauncherStartsTheCommandInANewSessionWithoutAControllingTerminal(t *testing.T) {
	p := launchHelper(t, "report")
	select {
	case <-p.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the helper did not exit")
	}
	var sid, pid int
	var ctty bool
	out := string(p.Terminal().Data)
	i := strings.Index(out, "sid=")
	if i < 0 {
		t.Fatalf("terminal output %q", out)
	}
	if _, err := fmt.Sscanf(out[i:], "sid=%d pid=%d ctty=%t", &sid, &pid, &ctty); err != nil {
		t.Fatalf("terminal output %q: %v", out, err)
	}
	parent, err := unix.Getsid(0)
	if err != nil {
		t.Fatal(err)
	}
	if sid == parent {
		t.Errorf("the command is in the coordinator's session %d", sid)
	}
	if pid != p.PID() || sid != pid {
		t.Errorf("session %d, process %d, launcher says %d: the command is not the leader of a session of its own", sid, pid, p.PID())
	}
	if ctty {
		t.Error("the command has a controlling terminal")
	}
}

func TestExecLauncherStopsAGroupWithTheStopRule(t *testing.T) {
	p := launchHelper(t, "wait")
	ev := stopProcess(p, 5*time.Second, 5*time.Second, 5*time.Millisecond)
	if ev.State != StopByTerm {
		t.Fatalf("stop %+v", ev)
	}
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the process is not reaped")
	}
	if e := p.Exit(); e.Signal != "SIGTERM" || e.Code != -1 {
		t.Errorf("exit %+v", e)
	}
	if there, err := p.Signal(0); there || err != nil {
		t.Errorf("the group is still there: %v %v", there, err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestExecLauncherRefusesWhatItCannotStart(t *testing.T) {
	for name, req := range map[string]LaunchRequest{
		"no plan":            {Rows: 1, Cols: 1},
		"empty argv":         {Plan: &plan.Plan{}, Rows: 1, Cols: 1},
		"relative argv":      {Plan: &plan.Plan{Argv: []string{"pasta"}}, Rows: 1, Cols: 1},
		"no terminal size":   {Plan: &plan.Plan{Argv: []string{"/bin/true"}}},
		"missing executable": {Plan: &plan.Plan{Argv: []string{"/nonexistent/for/the/test"}}, Rows: 1, Cols: 1},
	} {
		if p, err := (ExecLauncher{}).Launch(req); err == nil {
			p.Signal(syscall.SIGKILL)
			t.Errorf("%s: started", name)
		}
	}
}

// The tools of the version tests are the test binary itself, started under a
// name: a link in a temporary folder whose base name selects what the program
// does (no script, no shell, and nothing that needs the folder to allow
// execution). They run before the test framework does anything.
const toolPrefix = "harness-tool-"

func init() {
	name := filepath.Base(os.Args[0])
	if !strings.HasPrefix(name, toolPrefix) {
		return
	}
	switch {
	case len(os.Args) == 2 && os.Args[1] == "--hold":
		holdUntilReleased()
	case name == toolPrefix+"good":
		fmt.Println("tool 1.2.3")
		fmt.Println("second line")
	case name == toolPrefix+"bad":
		os.Exit(3)
	case name == toolPrefix+"leaves-child":
		// Prints its version and ends, leaving a process behind that holds
		// the same output open until the test releases it.
		fmt.Println("tool 4.5.6")
		child := exec.Command(os.Args[0], "--hold")
		child.Env = []string{}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(98)
		}
	default:
		os.Exit(99)
	}
	os.Exit(0)
}

// holdUntilReleased waits for the file "release" next to the program, for at
// most a minute, then ends. The test makes the file; the wait only keeps a
// process alive and is not a synchronisation of the test.
func holdUntilReleased() {
	release := filepath.Join(filepath.Dir(os.Args[0]), "release")
	for i := 0; i < 600; i++ {
		if _, err := os.Lstat(release); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Exit(0)
}

// toolLinks makes the tools in a temporary folder and returns the folder and
// the path of each by name.
func toolLinks(t *testing.T, names ...string) (string, map[string]string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out := map[string]string{}
	for _, n := range names {
		p := filepath.Join(dir, toolPrefix+n)
		if err := os.Symlink(self, p); err != nil {
			t.Skipf("cannot link the test binary: %v", err)
		}
		out[n] = p
	}
	return dir, out
}

func TestExecLauncherToolVersions(t *testing.T) {
	_, tools := toolLinks(t, "good", "bad")
	v := ExecLauncher{}.ToolVersions(context.Background(), ToolPaths{Bwrap: tools["good"], Pasta: tools["bad"]})
	if v.Bwrap != "tool 1.2.3" || v.Pasta != "" || v.Kernel == "" {
		t.Errorf("versions: %+v", v)
	}
	if len(v.Errors) != 1 || !strings.Contains(v.Errors[0], "pasta --version") {
		t.Errorf("errors: %v", v.Errors)
	}
	v = ExecLauncher{}.ToolVersions(context.Background(), ToolPaths{Bwrap: "relative", Pasta: filepath.Dir(tools["good"]) + "/missing"})
	if len(v.Errors) != 2 {
		t.Errorf("errors: %v", v.Errors)
	}
}

// A tool that leaves a process behind holding its output open does not hold
// the question up: it returns after the delay, with the version the tool
// printed and the fact that the output was not closed.
func TestExecLauncherVersionQuestionDoesNotWaitForAChildOfTheTool(t *testing.T) {
	dir, tools := toolLinks(t, "good", "leaves-child")
	release := func() {
		if err := os.WriteFile(dir+"/release", nil, 0o600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(release)
	got := make(chan ToolVersions, 1)
	go func() {
		got <- ExecLauncher{}.ToolVersions(context.Background(), ToolPaths{Bwrap: tools["leaves-child"], Pasta: tools["good"]})
	}()
	select {
	case v := <-got:
		if v.Bwrap != "tool 4.5.6" || v.Pasta != "tool 1.2.3" {
			t.Errorf("versions: %+v", v)
		}
		if len(v.Errors) != 1 || !strings.Contains(v.Errors[0], "bwrap --version") || !strings.Contains(v.Errors[0], "WaitDelay") {
			t.Errorf("errors: %v", v.Errors)
		}
	case <-time.After(30 * time.Second):
		release()
		t.Fatal("the version question waited for the output of a process the tool left behind")
	}
}

// procState is the state letter of a process in /proc, or "" when there is no
// such process.
func procState(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	i := strings.LastIndexByte(string(data), ')')
	f := strings.Fields(string(data[i+1:]))
	if len(f) == 0 {
		t.Fatalf("stat of %d: %q", pid, data)
	}
	return f[0]
}

// The first process stays unreaped until Close, so its id (the group's) cannot
// be reused while the stop rule signals the group; and a group whose leader
// has ended is not empty while another member lives.
func TestExecLauncherKeepsTheLeaderUnreapedUntilTheGroupIsKnownEmpty(t *testing.T) {
	p := launchHelper(t, "leader-with-child")
	select {
	case <-p.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the helper did not exit")
	}
	var child int
	if _, err := fmt.Sscanf(string(p.Terminal().Data), "child=%d", &child); err != nil {
		t.Fatalf("terminal output %q: %v", p.Terminal().Data, err)
	}
	if e := p.Exit(); e.Code != 0 || e.Signal != "" || e.Err != "" {
		t.Fatalf("exit %+v", e)
	}
	// The leader has ended and is still there, as a zombie that holds the id.
	if st := procState(t, p.PID()); st != "Z" {
		t.Fatalf("the leader's state is %q, want a zombie (Z): it was reaped", st)
	}
	// The member of its group is alive, so the group is not empty, whatever
	// kill(2) says about the zombie.
	if there, err := p.Signal(0); !there || err != nil {
		t.Fatalf("Signal(0) = %v, %v with a live member", there, err)
	}
	ev := stopProcess(p, 5*time.Second, 5*time.Second, 5*time.Millisecond)
	if ev.State != StopByTerm || len(ev.Signals) != 1 {
		t.Fatalf("stop %+v", ev)
	}
	// The member is gone, and the leader is still unreaped.
	if st := procState(t, p.PID()); st != "Z" {
		t.Errorf("the leader's state is %q after the stop, want Z", st)
	}
	if there, err := p.Signal(0); there || err != nil {
		t.Errorf("Signal(0) = %v, %v after the member ended", there, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if st := procState(t, p.PID()); st != "" {
		t.Errorf("the leader's state is %q after Close, want it reaped", st)
	}
	// Nothing is sent once the id may be someone else's.
	for _, sig := range []syscall.Signal{0, syscall.SIGTERM, syscall.SIGKILL} {
		if there, err := p.Signal(sig); there || err != nil {
			t.Errorf("Signal(%d) = %v, %v after Close", sig, there, err)
		}
	}
}

// With no other member, the stop of a group whose leader has already ended
// finds nothing left and sends nothing.
func TestExecLauncherStopOfAFinishedGroupSendsNothing(t *testing.T) {
	p := launchHelper(t, "report")
	<-p.Done()
	ev := stopProcess(p, time.Second, time.Second, time.Millisecond)
	if ev.State != StopNothingLeft || len(ev.Signals) != 0 {
		t.Errorf("stop %+v", ev)
	}
	if e := p.Exit(); e.Code != 7 {
		t.Errorf("exit %+v", e)
	}
}

// TestExitOfInfoReadsTheStatusFromARealWaitid pins the offset of si_status
// (siStatusAt) against the kernel: a child that exits with a known code, and
// one that is ended by a known signal, are waited for with waitid and decoded
// by exitOfInfo. It starts /bin/sh, with no pseudo-terminal, no namespace and
// no sandbox tool.
func TestExitOfInfoReadsTheStatusFromARealWaitid(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   Exit
	}{
		{"exit 5", Exit{Code: 5}},
		{"exit 0", Exit{Code: 0}},
		{"exit 201", Exit{Code: 201}},
		{"kill -TERM $$", Exit{Code: -1, Signal: "SIGTERM"}},
		{"kill -KILL $$", Exit{Code: -1, Signal: "SIGKILL"}},
	} {
		t.Run(tc.script, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", tc.script)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var info unix.Siginfo
			var err error
			for {
				err = unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED, nil)
				if err != unix.EINTR {
					break
				}
			}
			if got := exitOfInfo(&info, err); got != tc.want {
				t.Errorf("exitOfInfo = %+v, want %+v", got, tc.want)
			}
			_ = cmd.Wait() // already reaped by waitid: only releases the process record
		})
	}
}
