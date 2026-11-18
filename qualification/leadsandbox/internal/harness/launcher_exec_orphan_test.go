//go:build linux && amd64

package harness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// These tests show that no helper outlives the test that started it (see
// launcher_exec_guard_test.go). Like the other ExecLauncher tests they start
// only the test binary itself.

// startGuarded starts the test binary as a helper, directly and with the
// environment given. The process is killed and waited for when the test ends.
// The buffer is what it printed.
func startGuarded(t *testing.T, env []string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	argv, err := helperArgv()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	out := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Both do nothing once the test has waited for the process.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd, out
}

// exitCode waits for the process to end by itself and returns its exit code.
// A process that is still there when the time is up is killed, and the test
// fails.
func exitCode(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var err error
	select {
	case err = <-waited:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-waited
		t.Fatalf("the process %d did not end by itself", cmd.Process.Pid)
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	t.Fatalf("wait: %v", err)
	return -1
}

func skipWithoutPidfd(t *testing.T) {
	t.Helper()
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Skipf("no process descriptor here (%v): only the deadline ends a process that is not a child", err)
	}
	unix.Close(fd)
}

// A test that is killed runs no cleanup. A process plays such a test: it
// starts a helper through the launcher as the tests do, and is killed with
// SIGKILL while the helper waits. Nothing it started is left a short time
// later, in each mode that waits: the helper itself, whose parent is the
// killed process, and the member that leader-with-child leaves in its group,
// whose parent ended before.
//
// The time allowed is far below the deadline of a helper, so the deadline is
// not what ends them here. Should the test fail, the deadline ends within a
// minute what the failure left.
func TestExecLauncherHelpersEndWhenTheirParentIsKilled(t *testing.T) {
	for _, tc := range []struct {
		mode string
		// holders are the processes that announce themselves: the parent,
		// the helper and what the helper starts.
		holders int
		// needsPidfd: a process of the mode is not a child of the parent.
		needsPidfd bool
	}{
		{mode: "wait", holders: 2},
		{mode: "leader-with-child", holders: 3, needsPidfd: true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			skipWithoutPTY(t)
			if tc.needsPidfd {
				skipWithoutPidfd(t)
			}
			life := newLifeline(t, t.TempDir())
			parent, out := startGuarded(t, helperEnviron("parent-of-"+tc.mode, helperLifelineEnv+"="+life.path))
			pids, err := life.announced(tc.holders, 30*time.Second)
			if err != nil {
				t.Fatalf("%v; the parent printed %q", err, out)
			}
			if err := parent.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = parent.Wait()
			if err := life.gone(helperEndWait); err != nil {
				t.Errorf("a process of %v is still there %v after its parent %d was killed: %v", pids, helperEndWait, parent.Process.Pid, err)
			}
		})
	}
}

// A helper ends at its deadline while its parent, or its owner, lives and
// nothing stops it. The deadline is shortened through the environment.
func TestExecLauncherHelperEndsAtItsDeadline(t *testing.T) {
	const deadline = 500 * time.Millisecond
	for name, who := range map[string]string{"with a parent": helperParentEnv, "with an owner": helperOwnerEnv} {
		t.Run(name, func(t *testing.T) {
			life := newLifeline(t, t.TempDir())
			start := time.Now()
			env := append(helperEnvironFor("wait", who, os.Getpid()),
				helperDeadlineEnv+"="+deadline.String(), helperLifelineEnv+"="+life.path)
			cmd, out := startGuarded(t, env)
			if code := exitCode(t, cmd); code != helperExitDeadline {
				t.Fatalf("exit code %d, want %d (the deadline); it printed %q", code, helperExitDeadline, out)
			}
			if lived := time.Since(start); lived < deadline {
				t.Errorf("the helper ended after %v, before its deadline of %v", lived, deadline)
			}
			if _, err := life.announced(1, helperEndWait); err != nil {
				t.Error(err)
			}
			if err := life.gone(helperEndWait); err != nil {
				t.Errorf("the lifeline is still held: %v", err)
			}
		})
	}
}

// A helper does nothing when it is not told what it must not outlive, and
// ends at once when the parent it is told of is not its parent: the parent
// ended before the helper asked for the parent-death signal, and the signal
// will not come.
func TestExecLauncherHelperRefusesToRunUnguarded(t *testing.T) {
	// Process 1 is there, and is not the parent of what this test starts.
	for name, tc := range map[string]struct {
		env  []string
		want int
	}{
		"no parent and no owner":   {[]string{helperEnv + "=wait"}, helperExitUnguarded},
		"a parent that is not its": {helperEnvironFor("wait", helperParentEnv, 1), helperExitGone},
		"a parent that is no id":   {[]string{helperEnv + "=wait", helperParentEnv + "=x"}, helperExitUnguarded},
		"an owner that has ended":  {nil, helperExitGone},
	} {
		t.Run(name, func(t *testing.T) {
			env := tc.env
			if env == nil {
				// The helper has an owner and no parent: it needs a process
				// descriptor to notice that the owner has ended.
				skipWithoutPidfd(t)
				// A child that has ended and is not reaped yet keeps its id:
				// no other process has it.
				ended, _ := startGuarded(t, helperEnviron("exit-0"))
				var info unix.Siginfo
				for {
					err := unix.Waitid(unix.P_PID, ended.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
					if err == nil {
						break
					}
					if err != unix.EINTR {
						t.Fatal(err)
					}
				}
				env = helperEnvironFor("wait", helperOwnerEnv, ended.Process.Pid)
			}
			cmd, out := startGuarded(t, env)
			if code := exitCode(t, cmd); code != tc.want {
				t.Errorf("exit code %d, want %d; it printed %q", code, tc.want, out)
			}
		})
	}
}

// The process that a version tool leaves behind holds the tool's output open
// until its test releases it. A test that is killed releases nothing: a
// process plays such a test, asks the tool for its version and is killed, and
// the process left behind is gone a short time later.
func TestExecLauncherToolProcessEndsWhenItsTestIsKilled(t *testing.T) {
	skipWithoutPidfd(t)
	dir, _ := toolLinks(t, "good", "leaves-child")
	life := newLifeline(t, dir)
	parent, out := startGuarded(t, helperEnviron("tool-parent="+dir, helperLifelineEnv+"="+life.path))
	// The parent, and the process its tool left behind.
	pids, err := life.announced(2, 30*time.Second)
	if err != nil {
		t.Fatalf("%v; the parent printed %q", err, out)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	if err := life.gone(helperEndWait); err != nil {
		t.Errorf("a process of %v is still there %v after the parent %d was killed: %v", pids, helperEndWait, parent.Process.Pid, err)
	}
}

// With its owner alive and no release, the process that a version tool leaves
// behind ends at its deadline, shortened through a file of the folder.
func TestExecLauncherToolProcessEndsAtItsDeadline(t *testing.T) {
	const deadline = 500 * time.Millisecond
	dir, tools := toolLinks(t, "good", "leaves-child")
	if err := os.WriteFile(filepath.Join(dir, toolDeadlineFile), []byte(deadline.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	life := newLifeline(t, dir)
	start := time.Now()
	v := ExecLauncher{}.ToolVersions(context.Background(), ToolPaths{Bwrap: tools["leaves-child"], Pasta: tools["good"]})
	if v.Bwrap != "tool 4.5.6" {
		t.Fatalf("versions: %+v", v)
	}
	if _, err := life.announced(1, helperEndWait); err != nil {
		t.Fatal(err)
	}
	if err := life.gone(helperEndWait); err != nil {
		t.Fatalf("the process is still there: %v", err)
	}
	if lived := time.Since(start); lived < deadline {
		t.Errorf("the process ended after %v, before its deadline of %v", lived, deadline)
	}
}

// The process that a version tool leaves behind does nothing when the folder
// does not say who owns it.
func TestExecLauncherToolProcessRefusesToRunUnguarded(t *testing.T) {
	dir, tools := toolLinks(t, "leaves-child")
	if err := os.Remove(filepath.Join(dir, toolOwnerFile)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(tools["leaves-child"], "--hold")
	cmd.Env = []string{}
	out := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if code := exitCode(t, cmd); code != helperExitUnguarded {
		t.Errorf("exit code %d, want %d; it printed %q", code, helperExitUnguarded, out)
	}
}
