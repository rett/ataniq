//go:build linux && amd64

package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

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
	p := &execProcess{
		cmd: cmd, master: master, buf: newBoundedBuffer(MaxTerminalBytes),
		done: make(chan struct{}), readerDone: make(chan struct{}),
	}
	go p.read()
	go p.wait()
	return p, nil
}

type execProcess struct {
	cmd        *exec.Cmd
	master     *os.File
	buf        *boundedBuffer
	done       chan struct{}
	readerDone chan struct{}
	exit       Exit
	closeOnce  sync.Once
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

func (p *execProcess) wait() {
	err := p.cmd.Wait()
	p.exit = exitOf(p.cmd.ProcessState, err)
	close(p.done)
}

// exitOf describes how a process ended. A state without a wait status (never
// started) is an error; an exit by signal has Code -1.
func exitOf(ps *os.ProcessState, waitErr error) Exit {
	e := Exit{Code: -1}
	if ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
			switch {
			case ws.Signaled():
				e.Signal = unix.SignalName(ws.Signal())
				if e.Signal == "" {
					e.Signal = fmt.Sprintf("signal %d", int(ws.Signal()))
				}
			case ws.Exited():
				e.Code = ws.ExitStatus()
			}
		}
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		e.Err = assert.EscapeDetail(waitErr.Error())
	}
	return e
}

func (p *execProcess) PID() int              { return p.cmd.Process.Pid }
func (p *execProcess) Done() <-chan struct{} { return p.done }

func (p *execProcess) Exit() Exit {
	<-p.done
	return p.exit
}

// Signal sends the signal to the process group. ESRCH means no process is
// left in it.
func (p *execProcess) Signal(sig syscall.Signal) (bool, error) {
	err := syscall.Kill(-p.cmd.Process.Pid, sig)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	}
	return false, err
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

func (p *execProcess) Close() error {
	var err error
	p.closeOnce.Do(func() { err = p.master.Close() })
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
		if err != nil {
			v.Errors = append(v.Errors, t.name+" --version: "+assert.EscapeDetail(err.Error()))
			continue
		}
		*t.dst = out
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
	buf := newBoundedBuffer(maxVersionBytes)
	cmd.Stdout, cmd.Stderr = buf, buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return firstLine(buf.snapshot().Data), nil
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
