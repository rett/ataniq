package systemdmgr

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"time"

	"ataniq/internal/systemd"
)

// The two tools the manager runs, as the first element of a [Runner] argv.
const (
	toolBusctl    = "busctl"
	toolSystemctl = "systemctl"
)

// maxOutput caps what a tool may write. A "show" of the fixed properties and
// a job reply are far smaller.
const maxOutput = 64 << 10

// DefaultToolDirs are the directories [NewExecRunner] searches for the tools,
// in order. They are fixed and root-owned on a normal host; PATH is never
// consulted.
var DefaultToolDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// ErrNotLaunched is wrapped by a [Runner] error when the tool could not be
// started at all, so that nothing was sent to the manager.
var ErrNotLaunched = errors.New("systemdmgr: tool not launched")

// Runner runs one of the two tools. argv[0] is the tool's name, "busctl" or
// "systemctl", never a path: a runner maps it to the executable it trusts.
// It returns the tool's combined output; on an error it may still return the
// text the tool printed, which the manager reads only to tell a refusal from
// a failure. A runner returns an error wrapping [ErrNotLaunched] only when the
// tool did not start, and it honours ctx.
//
// Run returns only after the tool has ended, also when ctx ends first: it then
// waits for the tool, which is cut off at the deadline it was given so that the
// wait is bounded, and returns the context's error. The manager relies on this.
// A caller that sees a lost reply from Start inspects the unit next; a runner
// that had returned at the end of ctx while busctl was still running would let
// the request reach systemd after that inspection, a window the caller can
// neither see nor close. What the tool had already written to the bus before it
// ended can still be processed afterwards: that window belongs to the bus, not
// to the runner (see [ExecRunner.Run]). A runner that cannot promise this does
// not satisfy the contract. A Runner is safe for concurrent use.
type Runner interface {
	Run(ctx context.Context, argv []string) (stdout string, err error)
}

// ExecRunner is the default [Runner]: it runs the tools it resolved at
// construction with the bounded call of internal/systemd and the environment
// of this user's own manager.
type ExecRunner struct {
	paths map[string]string
	env   []string
}

var _ Runner = (*ExecRunner)(nil)

// NewExecRunner resolves busctl and systemctl in dirs (in order, the first
// match of each) and fixes the user-manager environment. A match is a regular
// file with an execute bit that group and others cannot write. It fails when
// a tool is not found. dirs must be absolute; pass [DefaultToolDirs] normally.
func NewExecRunner(dirs []string) (*ExecRunner, error) {
	r := &ExecRunner{paths: map[string]string{}, env: systemd.UserManagerEnvironment()}
	for _, tool := range []string{toolBusctl, toolSystemctl} {
		p, err := resolveTool(dirs, tool)
		if err != nil {
			return nil, err
		}
		r.paths[tool] = p
	}
	return r, nil
}

func resolveTool(dirs []string, tool string) (string, error) {
	for _, d := range dirs {
		if d == "" || d[0] != '/' {
			continue
		}
		p := d + "/" + tool
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 || fi.Mode().Perm()&0o022 != 0 {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("systemdmgr: %s not found in the fixed directories", tool)
}

// Path returns the resolved absolute path of a tool, or "" for a name that is
// not one of the two.
func (r *ExecRunner) Path(tool string) string { return r.paths[tool] }

// Run implements [Runner]. It refuses any tool but the two and returns the
// context's error at once when ctx is done before the tool starts. Once the
// tool runs, Run does not return before the tool has ended, even when ctx
// ends first: the tool is cut off at the deadline it was given (ctx's
// deadline, at most [systemd.MaxCall] after the start) plus one second for
// its output pipes, so the wait is bounded by that. A caller that sees the
// context's error from a tool that had started therefore knows that the tool
// is gone and cannot send anything any more. A message that the tool had
// already written to the bus can still be processed by the manager after the
// tool has ended; that window belongs to the bus and is not closed here.
func (r *ExecRunner) Run(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 || r.paths[argv[0]] == "" {
		return "", fmt.Errorf("%w: unknown tool", ErrNotLaunched)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotLaunched, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(systemd.MaxCall)
	}
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := systemd.Run(r.paths[argv[0]], argv[1:], deadline, r.env, maxOutput)
		done <- result{out, err}
	}()
	select {
	case res := <-done:
		if res.err != nil && notLaunched(res.err) {
			return res.out, fmt.Errorf("%w: %w", ErrNotLaunched, res.err)
		}
		return res.out, res.err
	case <-ctx.Done():
		// The tool may still deliver its request. Wait until it has ended,
		// bounded by the deadline that systemd.Run was given, so that a
		// caller that inspects after the lost reply sees the outcome and not
		// a request that lands afterwards. The error stays the context's, and
		// never wraps ErrNotLaunched: the request may have been sent.
		<-done
		return "", ctx.Err()
	}
}

// notLaunched reports whether err is the failure of starting the executable
// (as opposed to an exit status, a signal or an output error).
func notLaunched(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return false
	}
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Op == "fork/exec" {
		return true
	}
	var xe *exec.Error
	return errors.As(err, &xe)
}
