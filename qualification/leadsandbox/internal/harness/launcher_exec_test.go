//go:build linux && amd64

package harness

import (
	"context"
	"fmt"
	"os"
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
		fmt.Printf("tty=%t rows=%d cols=%d pgid=%t env=%d\n", termErr == nil, ws.Row, ws.Col, syscall.Getpgrp() == os.Getpid(), len(os.Environ()))
		os.Exit(7)
	case "wait":
		fmt.Println("waiting")
		select {}
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
	if !strings.Contains(out, "tty=true rows=40 cols=100 pgid=true env=1") {
		t.Errorf("terminal output %q", out)
	}
	if p.PID() <= 0 {
		t.Errorf("pid %d", p.PID())
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

func TestExecLauncherToolVersions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("tool-a", "#!/bin/sh\necho \"tool 1.2.3\"\necho second line\n")
	bad := write("tool-b", "#!/bin/sh\nexit 3\n")
	v := ExecLauncher{}.ToolVersions(context.Background(), ToolPaths{Bwrap: good, Pasta: bad})
	if v.Bwrap != "tool 1.2.3" || v.Pasta != "" || v.Kernel == "" {
		t.Errorf("versions: %+v", v)
	}
	if len(v.Errors) != 1 || !strings.Contains(v.Errors[0], "pasta --version") {
		t.Errorf("errors: %v", v.Errors)
	}
	v = ExecLauncher{}.ToolVersions(context.Background(), ToolPaths{Bwrap: "relative", Pasta: dir + "/missing"})
	if len(v.Errors) != 2 {
		t.Errorf("errors: %v", v.Errors)
	}
}
