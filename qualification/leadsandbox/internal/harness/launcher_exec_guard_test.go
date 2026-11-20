//go:build linux && amd64

package harness

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The ExecLauncher tests start the test binary itself as their command (the
// helper, TestExecLauncherHelper). A helper must not outlive the test that
// started it, however that test ends: it passes, it fails, it panics, the test
// binary reaches its time limit or is killed. The launcher puts the helper in a
// session of its own, so no hangup of a terminal and no signal to the test's
// process group reaches it, and a helper that waits to be stopped would
// otherwise wait for good. The cleanup of a test does not run when the test
// binary is killed, so the helper guards itself, in three ways:
//
//   - The parent-death signal. A helper whose parent is the process that must
//     stay asks the kernel, as its first act, to kill it when that parent ends
//     (PR_SET_PDEATHSIG, SIGKILL), and then ends by itself when the parent has
//     gone already. The launcher starts the helper directly, new session or
//     not, so the parent is the test binary.
//   - The owner. The parent-death signal follows the direct parent only. A
//     process that is started through one that ends first by design (the member
//     that the mode leader-with-child leaves in its group, and the process
//     that a stand-in for a version tool leaves behind) can take no signal
//     from that parent: it watches the process it must not outlive through a
//     process descriptor (pidfd) and ends when that process ends.
//   - The deadline. Every helper ends by itself after helperDeadline, whatever
//     it was asked to do. It is the backstop of the other two, and the only one
//     left where the kernel gives no process descriptor.
//
// The parent-death signal is a setting of the thread that asks for it, and it
// is sent when the thread that started the process ends, not the whole parent.
// The guard is armed from an init function, which the runtime runs on the main
// thread, and that thread lives as long as the process. On the other side the
// Go runtime ends a thread only when a goroutine that is locked to it ends, and
// nothing in these tests locks one.
//
// A helper reads its guard from its environment. The launcher gives a version
// tool an empty environment, so the process a tool leaves behind reads its
// guard from files next to the program (toolGuard).

const (
	// helperEnv is the mode of the helper. The other variables are the guard
	// of the helper, and are part of the environment the launcher is given.
	helperEnv = "HARNESS_EXEC_LAUNCHER_HELPER"
	// helperParentEnv is the process id of the parent that must stay.
	helperParentEnv = helperEnv + "_PARENT"
	// helperOwnerEnv is the process id of a process that is not the parent
	// and that the helper must not outlive.
	helperOwnerEnv = helperEnv + "_OWNER"
	// helperDeadlineEnv shortens the deadline, for the test of it.
	helperDeadlineEnv = helperEnv + "_DEADLINE"
	// helperLifelineEnv is the path of a FIFO the helper keeps open for
	// writing until it ends: a test reads it to see that the helper has gone.
	helperLifelineEnv = helperEnv + "_LIFELINE"
)

// helperDeadline is how long a helper may live. No test needs a helper for
// longer than its own waits (30 seconds at most).
const helperDeadline = 60 * time.Second

// helperEndWait is how long a test waits for something it has killed to be
// gone.
const helperEndWait = 10 * time.Second

// The exit codes of the guard.
const (
	// helperExitUnguarded: the helper was not told what it must not outlive,
	// or could not arm its guard. It does nothing then.
	helperExitUnguarded = 95
	// helperExitGone: the parent or the owner has gone.
	helperExitGone = 96
	// helperExitDeadline: the deadline has passed.
	helperExitDeadline = 97
)

// helperGuard is what a helper must not outlive.
type helperGuard struct {
	parent   int
	owner    int
	deadline time.Duration // helperDeadline when zero
	lifeline string
}

// armedGuard is the guard of this process when it is a helper.
var armedGuard helperGuard

func init() {
	if os.Getenv(helperEnv) == "" {
		return
	}
	g, err := helperGuardFromEnv()
	if err != nil {
		unguarded(err)
	}
	g.arm()
}

func unguarded(err error) {
	fmt.Fprintln(os.Stderr, "helper guard:", err)
	os.Exit(helperExitUnguarded)
}

func helperGuardFromEnv() (helperGuard, error) {
	var g helperGuard
	for _, f := range []struct {
		name string
		dst  *int
	}{{helperParentEnv, &g.parent}, {helperOwnerEnv, &g.owner}} {
		v := os.Getenv(f.name)
		if v == "" {
			continue
		}
		pid, err := strconv.Atoi(v)
		if err != nil || pid <= 0 {
			return g, fmt.Errorf("%s=%q is not a process id", f.name, v)
		}
		*f.dst = pid
	}
	if v := os.Getenv(helperDeadlineEnv); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return g, fmt.Errorf("%s=%q is not a duration", helperDeadlineEnv, v)
		}
		g.deadline = d
	}
	g.lifeline = os.Getenv(helperLifelineEnv)
	return g, nil
}

// The files of a guard in the folder of the version tools.
const (
	// toolOwnerFile holds the process id of the owner.
	toolOwnerFile = "owner"
	// toolDeadlineFile shortens the deadline, for the test of it.
	toolDeadlineFile = "deadline"
	// lifelineFile is the FIFO of a lifeline.
	lifelineFile = "lifeline"
)

// toolGuard is the guard of the process a version tool leaves behind, read
// from the folder of the tools. The process has no parent that stays: the
// tool that started it ends at once, which is the point of it.
func toolGuard(dir string) (helperGuard, error) {
	var g helperGuard
	data, err := os.ReadFile(filepath.Join(dir, toolOwnerFile))
	if err != nil {
		return g, err
	}
	if g.owner, err = strconv.Atoi(strings.TrimSpace(string(data))); err != nil || g.owner <= 0 {
		return g, fmt.Errorf("the file %s does not hold a process id", toolOwnerFile)
	}
	if data, err := os.ReadFile(filepath.Join(dir, toolDeadlineFile)); err == nil {
		if g.deadline, err = time.ParseDuration(strings.TrimSpace(string(data))); err != nil || g.deadline <= 0 {
			return g, fmt.Errorf("the file %s does not hold a duration", toolDeadlineFile)
		}
	}
	if path := filepath.Join(dir, lifelineFile); isThere(path) {
		g.lifeline = path
	}
	return g, nil
}

func isThere(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// ownTools makes the calling process the owner of what the tools of the
// folder leave behind.
func ownTools(dir string) error {
	return os.WriteFile(filepath.Join(dir, toolOwnerFile), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
}

// keeper is the process the helper must not outlive.
func (g helperGuard) keeper() int {
	if g.parent > 0 {
		return g.parent
	}
	return g.owner
}

// arm makes this process end with its parent or owner, and at its deadline.
// It must be called from an init function (see above).
func (g helperGuard) arm() {
	if g.parent > 0 {
		if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
			unguarded(fmt.Errorf("the parent-death signal: %w", err))
		}
		if unix.Gettid() != unix.Getpid() {
			unguarded(fmt.Errorf("the guard is not armed on the main thread"))
		}
		// The parent may have ended before the signal was asked for: the
		// process has another parent then, and no signal will come.
		if os.Getppid() != g.parent {
			os.Exit(helperExitGone)
		}
	}
	if g.keeper() <= 0 {
		unguarded(fmt.Errorf("neither %s nor %s is set", helperParentEnv, helperOwnerEnv))
	}
	d := g.deadline
	if d <= 0 {
		d = helperDeadline
	}
	time.AfterFunc(d, func() { os.Exit(helperExitDeadline) })
	if g.owner > 0 {
		endWith(g.owner)
	}
	if g.lifeline != "" {
		// Without O_NONBLOCK the open would wait for a reader; with it, no
		// reader is an error, and nobody is there to see this process go.
		fd, err := unix.Open(g.lifeline, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			unguarded(fmt.Errorf("the lifeline: %w", err))
		}
		// The descriptor is never closed: the kernel closes it when the
		// process ends. The line says the guard is armed.
		_, _ = unix.Write(fd, []byte(strconv.Itoa(os.Getpid())+"\n"))
	}
	armedGuard = g
}

// endWith ends this process when the process pid ends. A process descriptor
// names that process and no later one with the same id, and it is readable
// from the moment the process has ended, reaped or not. Where the kernel gives
// none, the deadline is what is left.
func endWith(pid int) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err == unix.ESRCH {
		os.Exit(helperExitGone)
	}
	if err != nil {
		return
	}
	go func() {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			if _, err := unix.Poll(fds, -1); err == unix.EINTR {
				continue
			}
			os.Exit(helperExitGone)
		}
	}()
}

// helperArgv is the command line of a helper: the test binary itself.
func helperArgv() ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return []string{exe, "-test.run=^TestExecLauncherHelper$"}, nil
}

// helperEnviron is the whole environment of a helper that the calling process
// starts and stays the parent of.
func helperEnviron(mode string, more ...string) []string {
	return append(helperEnvironFor(mode, helperParentEnv, os.Getpid()), more...)
}

// helperEnvironFor is the environment of a helper in the mode, whose parent
// (who is helperParentEnv) or owner (helperOwnerEnv) is the process pid. A
// helper hands its own deadline and lifeline on to what it starts.
func helperEnvironFor(mode, who string, pid int) []string {
	env := []string{helperEnv + "=" + mode, who + "=" + strconv.Itoa(pid)}
	for _, k := range []string{helperDeadlineEnv, helperLifelineEnv} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// lifeline is a FIFO that every process of a test keeps open for writing
// until it ends. Each writes its process id on a line when its guard is armed,
// and the read reaches end-of-file when the last of them has gone: that does
// not depend on a process id still naming the same process.
type lifeline struct {
	path string
	r    *os.File
	br   *bufio.Reader
	// w is the test's own writer. While it is open a read with no other
	// writer waits, instead of ending before the first process has opened the
	// FIFO.
	w *os.File
}

// newLifeline makes the FIFO in the folder and opens it.
func newLifeline(t *testing.T, dir string) *lifeline {
	t.Helper()
	l := &lifeline{path: filepath.Join(dir, lifelineFile)}
	if err := unix.Mkfifo(l.path, 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	// The reader first, without waiting for a writer.
	if l.r, err = os.OpenFile(l.path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.r.Close() })
	if l.w, err = os.OpenFile(l.path, os.O_WRONLY, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.w.Close() })
	l.br = bufio.NewReader(l.r)
	return l
}

// announced waits until n more processes have written their line, and returns
// their process ids.
func (l *lifeline) announced(n int, within time.Duration) ([]int, error) {
	if err := l.r.SetReadDeadline(time.Now().Add(within)); err != nil {
		return nil, err
	}
	var pids []int
	for len(pids) < n {
		line, err := l.br.ReadString('\n')
		if err != nil {
			return pids, fmt.Errorf("%d of %d processes announced themselves (%v): %w", len(pids), n, pids, err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			return pids, fmt.Errorf("the line %q is not a process id", line)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// gone waits until no process holds the FIFO open. An error says one still
// did when the time was up.
func (l *lifeline) gone(within time.Duration) error {
	l.w.Close()
	if err := l.r.SetReadDeadline(time.Now().Add(within)); err != nil {
		return err
	}
	_, err := io.Copy(io.Discard, l.br)
	return err
}
