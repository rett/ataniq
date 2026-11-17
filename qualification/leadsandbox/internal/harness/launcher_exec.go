//go:build linux && amd64

package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

// This file is the only non-test file of the module's qualification harness
// that imports os/exec. A test guards that.

const (
	// terminalDrain is how long Terminal waits, after the first process has
	// exited, for the last bytes the pseudo-terminal holds.
	terminalDrain = time.Second
	// versionTimeout bounds one "--version" question.
	versionTimeout = 10 * time.Second
	// maxVersionBytes bounds what a version question keeps.
	maxVersionBytes = 4096
	// versionWaitDelay is how long a version question waits for its output
	// after the tool has ended or been killed: a process the tool left
	// behind may hold the pipe open for good.
	versionWaitDelay = time.Second
)

// ExecLauncher is the real [Launcher]. It starts the plan's command line with
// a pseudo-terminal as its standard streams, in a session and process group of
// its own with no controlling terminal, and keeps
// the first MaxTerminalBytes of the output. It creates no systemd unit, no
// cgroup and no namespace of its own: the namespaces are what the plan's
// programs make. It exists to be run by a coordinator, on a host the
// coordinator chose; nothing in the repository's tests runs it against pasta
// or bubblewrap.
type ExecLauncher struct{}

var _ Launcher = ExecLauncher{}

// Launch implements Launcher.
func (ExecLauncher) Launch(req LaunchRequest) (Process, error) {
	if req.Plan == nil || len(req.Plan.Argv) == 0 {
		return nil, errors.New("harness: no command line to start")
	}
	argv := req.Plan.Argv
	if !filepath.IsAbs(argv[0]) {
		return nil, fmt.Errorf("harness: the first element of the command line is not an absolute path")
	}
	if req.Rows == 0 || req.Cols == 0 {
		return nil, errors.New("harness: the terminal size is empty")
	}
	master, slave, err := openPTY(req.Rows, req.Cols)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	// A nil Env would mean the caller's environment.
	cmd.Env = append([]string{}, req.Env...)
	cmd.Dir = "/"
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// A session of its own, and with it a process group of its own (the group
	// id is the process id, which the stop rule signals), and no controlling
	// terminal: the pseudo-terminal is only the standard streams. Nothing in
	// the tree keeps the coordinator's session or its terminal, which is how
	// a holder under a unit runs it. The probe inside makes its own session
	// and takes the pseudo-terminal; it is not a group leader, so it can.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		slave.Close()
		master.Close()
		return nil, fmt.Errorf("harness: start %s: %w", argv[0], err)
	}
	// The parent keeps only the master: when every process of the group has
	// gone, a read of the master ends.
	slave.Close()
	p := newExecProcess(cmd, master)
	go p.read()
	go p.wait()
	return p, nil
}

// execProcess is a started process group. The first process is kept as an
// unreaped zombie from its exit until Close: while it exists the kernel does
// not give its process id to another process or group, so the stop rule's
// signal to the group cannot reach a stranger's. Its exit is read with waitid
// and WNOWAIT, and the group is "empty" when no other member is alive.
type execProcess struct {
	cmd        *exec.Cmd
	master     *os.File
	buf        *boundedBuffer
	done       chan struct{}
	readerDone chan struct{}
	exit       Exit
	closeOnce  sync.Once

	// release is closed by Close: the wait goroutine reaps the first process
	// then (at once when it has exited, when it exits otherwise).
	release chan struct{}
	// reapMu guards reaped: no signal is sent to the group once the first
	// process has been reaped, because its id may be someone else's by then.
	reapMu sync.RWMutex
	reaped bool
	// reapDone is closed when the first process has been reaped.
	reapDone chan struct{}
}

func newExecProcess(cmd *exec.Cmd, master *os.File) *execProcess {
	return &execProcess{
		cmd: cmd, master: master, buf: newBoundedBuffer(MaxTerminalBytes),
		done: make(chan struct{}), readerDone: make(chan struct{}),
		release: make(chan struct{}), reapDone: make(chan struct{}),
	}
}

func (p *execProcess) read() {
	defer close(p.readerDone)
	chunk := make([]byte, 32<<10)
	for {
		n, err := p.master.Read(chunk)
		if n > 0 {
			p.buf.Write(chunk[:n])
		}
		if err != nil {
			// EIO: every holder of the slave has gone. Any other error ends
			// the capture too; the count says how much was kept.
			return
		}
	}
}

// The codes of si_code that waitid gives for a child that has ended.
const (
	cldExited  = 1
	cldKilled  = 2
	cldDumped  = 3
	siStatusAt = 24 // offset of si_status in siginfo_t on linux/amd64
)

// wait waits for the first process to end without reaping it, records how it
// ended and closes done; then it waits for Close and reaps it.
func (p *execProcess) wait() {
	defer close(p.reapDone)
	var info unix.Siginfo
	var err error
	for {
		err = unix.Waitid(unix.P_PID, p.cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			break
		}
	}
	p.exit = exitOfInfo(&info, err)
	close(p.done)
	<-p.release
	p.reapMu.Lock()
	p.reaped = true
	p.reapMu.Unlock()
	// The process has ended: this reaps it and returns at once. The streams
	// of the command are files, so there is no copying to wait for.
	_ = p.cmd.Wait()
}

// exitOfInfo describes how a process ended from the waitid record. An exit by
// signal has Code -1.
func exitOfInfo(info *unix.Siginfo, waitErr error) Exit {
	e := Exit{Code: -1}
	if waitErr != nil {
		e.Err = assert.EscapeDetail(waitErr.Error())
		return e
	}
	status := int(*(*int32)(unsafe.Add(unsafe.Pointer(info), siStatusAt)))
	switch info.Code {
	case cldExited:
		e.Code = status
	case cldKilled, cldDumped:
		e.Signal = unix.SignalName(syscall.Signal(status))
		if e.Signal == "" {
			e.Signal = fmt.Sprintf("signal %d", status)
		}
	default:
		e.Err = fmt.Sprintf("the process ended with the unexpected wait code %d", info.Code)
	}
	return e
}

func (p *execProcess) PID() int              { return p.cmd.Process.Pid }
func (p *execProcess) Done() <-chan struct{} { return p.done }

func (p *execProcess) Exit() Exit {
	<-p.done
	return p.exit
}

// Signal sends the signal to the process group. No process left in it is
// (false, nil). Once the first process has ended it is a zombie that still
// belongs to the group, and kill(2) would count it: the members that are not
// zombies are counted from /proc instead, and nothing is sent when there are
// none. After Close the group id may belong to someone else, and nothing is
// ever sent.
func (p *execProcess) Signal(sig syscall.Signal) (bool, error) {
	p.reapMu.RLock()
	defer p.reapMu.RUnlock()
	if p.reaped {
		return false, nil
	}
	pid := p.cmd.Process.Pid
	select {
	case <-p.done:
		if n, err := liveGroupMembers(pid); err == nil && n == 0 {
			return false, nil
		}
	default:
	}
	err := syscall.Kill(-pid, sig)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	}
	return false, err
}

// liveGroupMembers counts the processes of the group that are not zombies, from
// the stat file of each process: the group id is the fifth field and the state
// the first after the command name, which may hold spaces and parentheses.
func liveGroupMembers(pgid int) (int, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		data, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil {
			continue // gone while the folder was read
		}
		i := bytes.LastIndexByte(data, ')')
		if i < 0 {
			return 0, errors.New("a stat file has no command name")
		}
		f := bytes.Fields(data[i+1:])
		if len(f) < 3 {
			return 0, errors.New("a stat file is short")
		}
		if g, err := strconv.Atoi(string(f[2])); err != nil || g != pgid {
			continue
		}
		if st := f[0][0]; st != 'Z' && st != 'X' {
			n++
		}
	}
	return n, nil
}

func (p *execProcess) Terminal() TerminalOutput {
	select {
	case <-p.done:
		t := time.NewTimer(terminalDrain)
		defer t.Stop()
		select {
		case <-p.readerDone:
		case <-t.C:
		}
	default:
	}
	return p.buf.snapshot()
}

// Close releases the pseudo-terminal and lets the first process be reaped,
// which is the last point at which its id is kept from reuse. It waits for the
// reaping when the process has already ended.
func (p *execProcess) Close() error {
	var err error
	p.closeOnce.Do(func() {
		err = p.master.Close()
		close(p.release)
	})
	select {
	case <-p.done:
		<-p.reapDone
	default:
	}
	return err
}

// ToolVersions implements Launcher. The kernel release comes from the
// uname(2) call, not from a program found in PATH; each tool is run once with
// "--version" by its absolute path, with an empty environment, no input and a
// time limit.
func (ExecLauncher) ToolVersions(ctx context.Context, tools ToolPaths) ToolVersions {
	var v ToolVersions
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		v.Errors = append(v.Errors, "kernel release: "+err.Error())
	} else {
		v.Kernel = assert.EscapeDetail(unix.ByteSliceToString(u.Release[:]))
	}
	for _, t := range []struct {
		name, path string
		dst        *string
	}{{"bwrap", tools.Bwrap, &v.Bwrap}, {"pasta", tools.Pasta, &v.Pasta}} {
		out, err := runVersion(ctx, t.path)
		*t.dst = out
		if err != nil {
			v.Errors = append(v.Errors, t.name+" --version: "+assert.EscapeDetail(err.Error()))
		}
	}
	return v
}

func runVersion(ctx context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("not an absolute path")
	}
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{}
	cmd.Dir = "/"
	// The output is read through a pipe, which a child of the tool can keep
	// open: without a delay Run would wait for it for good, past the time
	// limit.
	cmd.WaitDelay = versionWaitDelay
	buf := newBoundedBuffer(maxVersionBytes)
	cmd.Stdout, cmd.Stderr = buf, buf
	err := cmd.Run()
	switch {
	case err == nil:
		return firstLine(buf.snapshot().Data), nil
	case errors.Is(err, exec.ErrWaitDelay):
		// The tool itself ended well and printed its version, but something it
		// started kept the output open: the version is a fact, and so is this.
		return firstLine(buf.snapshot().Data), err
	}
	return "", err
}

// firstLine is the first line of the text, escaped and bounded.
func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			b = b[:i]
			break
		}
	}
	return assert.EscapeDetail(string(b))
}
