package switcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Command is one program the switch runs: a fixed argument vector, no shell.
type Command struct {
	Argv    []string
	Stdin   []byte // nil reads from /dev/null
	Timeout time.Duration
}

// Output is what a command wrote, each stream kept up to a bound.
type Output struct {
	Stdout []byte
	Stderr []byte
}

// Runner runs a command. The error is nil only when the command ran and exited
// with status 0; otherwise it says why (could not start, exit status, killed,
// time limit).
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
// folder /, their own process group (killed whole at the time limit) and
// standard input from /dev/null unless Stdin is given.
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

func (ExecRunner) Run(ctx context.Context, c Command) (Output, error) {
	if len(c.Argv) == 0 {
		return Output{}, errors.New("empty command")
	}
	prog, err := LookPath(c.Argv[0])
	if err != nil {
		return Output{}, err
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	var stdout, stderr boundedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	out := Output{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.Bytes()}
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("%s: no exit within %s, killed", c.Argv[0], c.Timeout)
	}
	return out, fmt.Errorf("%s: %w", c.Argv[0], err)
}

// boundedBuffer keeps the first maxOutput bytes written and drops the rest,
// so a chatty command cannot fill memory.
type boundedBuffer struct{ buf bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

// tail returns the last few hundred bytes of a command's output, for a
// message.
func tail(out Output) string {
	s := strings.TrimSpace(string(out.Stderr))
	if s == "" {
		s = strings.TrimSpace(string(out.Stdout))
	}
	const n = 400
	if len(s) > n {
		s = "..." + s[len(s)-n:]
	}
	return s
}
