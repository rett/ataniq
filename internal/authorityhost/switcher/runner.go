package switcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Command is one program the switch runs: a fixed argument vector, no shell.
type Command struct {
	Argv      []string
	Stdin     []byte // nil reads from /dev/null
	Timeout   time.Duration
	MaxOutput int // bound of each stream kept; 0: maxOutput
	// ExtraFiles are given to the command as its fd 3 and up (the lock,
	// for the package step).
	ExtraFiles []*os.File
	// OnStart, when set, is called with the process id once the command
	// runs, before it is waited for.
	OnStart func(pid int)
}

// Output is what a command wrote, each stream kept up to a bound.
type Output struct {
	Stdout    []byte
	Stderr    []byte
	Truncated bool // a stream wrote more than the bound
}

// Runner runs a command. The error is nil only when the command ran and exited
// with status 0; otherwise it says why (could not start, exit status, killed,
// time limit), as a *RunError for ExecRunner. The output is for the caller to
// use as data (the firewall listing); the switch never prints it or records
// it, since a diagnostic can hold a secret.
type Runner interface {
	Run(ctx context.Context, c Command) (Output, error)
}

// SearchPath is where ExecRunner looks up a bare program name. It is fixed:
// neither the caller's PATH nor anything else in its environment chooses which
// program runs.
var SearchPath = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// CleanEnv is the whole environment of every command the switch runs.
var CleanEnv = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}

// maxOutput bounds what is kept of each stream of a command.
const maxOutput = 64 << 10

// ExecRunner runs commands as child processes with CleanEnv, the working
// folder /, their own process group (killed whole at the time limit),
// standard input from /dev/null unless Stdin is given, and, where the
// system has it, SIGKILL when the switch dies (so a command never outlives
// the switch that ran it).
type ExecRunner struct{}

// LookPath resolves a program: an absolute path is used as it is, a bare name
// is looked up in SearchPath, and anything else is refused.
func LookPath(prog string) (string, error) {
	if filepath.IsAbs(prog) {
		return prog, nil
	}
	if prog == "" || strings.Contains(prog, "/") {
		return "", fmt.Errorf("program %q is neither absolute nor a bare name", prog)
	}
	for _, dir := range SearchPath {
		p := filepath.Join(dir, prog)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("program %q not found in %s", prog, strings.Join(SearchPath, ":"))
}

// RunError is why a command did not exit 0. Its text holds no output of the
// command and no word of its arguments.
type RunError struct {
	NotStarted bool          // the program was not found or could not start
	Timeout    time.Duration // non-zero: killed at this time limit
	Status     int           // the exit status; -1 when killed by a signal
}

func (e *RunError) Error() string {
	switch {
	case e.NotStarted:
		return "did not start"
	case e.Timeout > 0:
		return fmt.Sprintf("no exit within %s, killed", e.Timeout)
	case e.Status < 0:
		return "killed by a signal"
	}
	return fmt.Sprintf("exit status %d", e.Status)
}

// fixedError is an error whose text is fixed words of the switch itself.
type fixedError string

func (e fixedError) Error() string { return string(e) }

// summary says why a command failed in fixed words: the text of a *RunError
// or of a fixedError, or "failed" for any other error.
func summary(err error) string {
	var re *RunError
	if errors.As(err, &re) {
		return re.Error()
	}
	var fe fixedError
	if errors.As(err, &fe) {
		return fe.Error()
	}
	return "failed"
}

func (ExecRunner) Run(ctx context.Context, c Command) (Output, error) {
	if len(c.Argv) == 0 {
		return Output{}, &RunError{NotStarted: true}
	}
	prog, err := LookPath(c.Argv[0])
	if err != nil {
		return Output{}, &RunError{NotStarted: true}
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, prog, c.Argv[1:]...)
	cmd.Args[0] = c.Argv[0]
	cmd.Env = append([]string(nil), CleanEnv...)
	cmd.Dir = "/"
	cmd.SysProcAttr = procAttr()
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.ExtraFiles = c.ExtraFiles
	cmd.WaitDelay = 2 * time.Second
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	limit := c.MaxOutput
	if limit <= 0 {
		limit = maxOutput
	}
	stdout, stderr := boundedBuffer{max: limit}, boundedBuffer{max: limit}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// The parent-death signal goes with the thread that started the child:
	// keep this goroutine on it until the child is waited for.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err = cmd.Start()
	if err == nil {
		if c.OnStart != nil {
			c.OnStart(cmd.Process.Pid)
		}
		err = cmd.Wait()
	}
	out := Output{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes(), Truncated: stdout.cut || stderr.cut}
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, &RunError{Timeout: c.Timeout}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, &RunError{Status: ee.ExitCode()}
	}
	return out, &RunError{NotStarted: true}
}

// boundedBuffer keeps the first max bytes written and drops the rest, so a
// chatty command cannot fill memory; cut says that it dropped some.
type boundedBuffer struct {
	buf bytes.Buffer
	max int
	cut bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := b.max - b.buf.Len()
	if len(p) > room {
		b.cut = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
	} else {
		b.buf.Write(p)
	}
	return len(p), nil
}
