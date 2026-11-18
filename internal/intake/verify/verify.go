package verify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/sandboxplan"
)

const (
	defaultTimeout   = 30 * time.Minute
	defaultMaxOutput = 4 << 20
	// maxMaxOutput bounds what a caller may ask for: the output is held in
	// memory.
	maxMaxOutput = 256 << 20
)

// Stopped values of a [Result].
const (
	// StoppedTimeout means the run passed Limits.Timeout and was killed.
	StoppedTimeout = "timeout"
	// StoppedOutputLimit means the output passed Limits.MaxOutput and the
	// run was killed.
	StoppedOutputLimit = "output-limit"
	// StoppedError means the run ended without an exit status, or its
	// output may be incomplete: a signal, a context that ended, a pipe held
	// by a process that outlived the run, or a runner that failed.
	StoppedError = "error"
)

// Limits bounds one run.
type Limits struct {
	// Timeout is how long the whole run may take, including the clone. Zero
	// means the default.
	Timeout time.Duration
	// MaxOutput is how many bytes of output are kept, standard output and
	// standard error together. Zero means the default.
	MaxOutput int64
}

// DefaultLimits returns the limits used for a zero field: 30 minutes and
// 4 MiB.
func DefaultLimits() Limits {
	return Limits{Timeout: defaultTimeout, MaxOutput: defaultMaxOutput}
}

func (l Limits) resolved() (Limits, error) {
	d := DefaultLimits()
	if l.Timeout < 0 || l.MaxOutput < 0 {
		return Limits{}, errors.New("verify: limits must not be negative")
	}
	if l.Timeout == 0 {
		l.Timeout = d.Timeout
	}
	if l.MaxOutput == 0 {
		l.MaxOutput = d.MaxOutput
	}
	if l.MaxOutput > maxMaxOutput {
		return Limits{}, fmt.Errorf("verify: MaxOutput is more than %d bytes", int64(maxMaxOutput))
	}
	return l, nil
}

// Runner starts a bounded command. [ExecRunner] is the real one; tests use a
// fake.
type Runner interface {
	Run(ctx context.Context, c boundedexec.Cmd) (boundedexec.Result, error)
}

// ExecRunner runs the command with [boundedexec.Run].
type ExecRunner struct{}

// Run calls [boundedexec.Run].
func (ExecRunner) Run(ctx context.Context, c boundedexec.Cmd) (boundedexec.Result, error) {
	return boundedexec.Run(ctx, c)
}

// Request is one verification run.
type Request struct {
	// Project is a name that passes [sandboxplan.ValidName].
	Project string
	// Mirror is the absolute, clean host path of the bare exchange mirror.
	Mirror string
	// TipRef is the lead ref under refs/leads/ whose tip is verified.
	TipRef string
	// Tip is the full object id the review was made for. The run fails with
	// exit 90 when TipRef no longer resolves to it.
	Tip string
	// Gates are run in order, in the scratch clone's root. At least one.
	Gates []Gate
	// Planner supplies the sandbox. It is required. A planner that has no
	// sandbox returns [sandboxplan.ErrNotConfigured] and nothing runs.
	Planner sandboxplan.Planner
	// Runner starts the planned command. Nil means [ExecRunner].
	Runner Runner
	// WorkRoot is an owner-owned folder: a real folder (not a symlink),
	// owned by the caller, mode 0700. The run makes its scratch and home
	// folders in it and removes them again.
	WorkRoot string
	// Limits bound the run. A zero field means its default.
	Limits Limits
}

// Result is what a run found. It says nothing about the code beyond what the
// package comment lists.
type Result struct {
	// Tip and TipRef are the request's.
	Tip, TipRef string
	// PlanDigest is [PlanDigest] of the gates that were run.
	PlanDigest string
	// Passed is true only when the command exited 0 by itself, was not
	// stopped, the output cap was not hit and both folders were removed.
	Passed bool
	// Exit is the command's exit status, or -1 when it did not exit by
	// itself. See the package comment about statuses 90 to 99.
	Exit int
	// Stopped is "", "timeout", "output-limit" or "error".
	Stopped string
	// Output is standard output and standard error together, at most
	// Limits.MaxOutput bytes. It is raw text the lead influenced: a caller
	// shows it only through leadtext.
	Output []byte
	// OutputCut is true when output was dropped.
	OutputCut bool
	// Started is when the command was started.
	Started time.Time
	// Duration is how long the command ran.
	Duration time.Duration
}

func checkHostPath(what, p string) error {
	switch {
	case p == "" || !filepath.IsAbs(p):
		return fmt.Errorf("verify: %s %q is not an absolute path", what, p)
	case strings.ContainsAny(p, "\x00\n"):
		return fmt.Errorf("verify: %s holds a NUL byte or a newline", what)
	case filepath.Clean(p) != p || p == "/":
		return fmt.Errorf("verify: %s %q is not a clean path below /", what, p)
	}
	return nil
}

func within(parent, p string) bool {
	return p == parent || strings.HasPrefix(p, parent+"/")
}

func (r *Request) validate() (Limits, error) {
	var zero Limits
	if !sandboxplan.ValidName(r.Project) {
		return zero, fmt.Errorf("verify: project name %q is not [a-z0-9][a-z0-9-]{0,62}", r.Project)
	}
	if err := checkHostPath("mirror", r.Mirror); err != nil {
		return zero, err
	}
	if err := checkHostPath("work root", r.WorkRoot); err != nil {
		return zero, err
	}
	if within(r.WorkRoot, r.Mirror) || within(r.Mirror, r.WorkRoot) {
		return zero, errors.New("verify: the mirror and the work root must not contain each other")
	}
	if err := checkTipRef(r.TipRef); err != nil {
		return zero, err
	}
	if err := checkTip(r.Tip); err != nil {
		return zero, err
	}
	if err := validateGates(r.Gates); err != nil {
		return zero, err
	}
	if r.Planner == nil {
		return zero, errors.New("verify: no planner")
	}
	return r.Limits.resolved()
}

// Run verifies the tip: it makes a scratch folder and a home folder in
// WorkRoot, asks the planner for the sandbox, runs the script of [Script]
// through the runner, and removes both folders.
//
// A failed gate, a time-out and a tip that moved are results, not errors:
// Run returns a [Result] with Passed false and a nil error. The error is
// non-nil when the run could not be attempted (a refused request, a planner
// that refused, folders that could not be made) and when a folder could not
// be removed afterwards; the message names the folder. When folders were
// left behind the result is still returned, with Passed false.
func Run(ctx context.Context, req Request) (res Result, err error) {
	lim, err := req.validate()
	if err != nil {
		return Result{}, err
	}
	res = Result{Tip: req.Tip, TipRef: req.TipRef, PlanDigest: PlanDigest(req.Gates), Exit: -1}

	wr, err := openWorkRoot(req.WorkRoot)
	if err != nil {
		return res, err
	}
	defer wr.close()

	var made []string
	defer func() {
		// Runs also when the runner panics. The panic goes on afterwards.
		for _, name := range made {
			if rerr := wr.removeDir(name); rerr != nil {
				res.Passed = false
				err = errors.Join(err, fmt.Errorf("verify: could not remove %s: %w", filepath.Join(req.WorkRoot, name), rerr))
			}
		}
	}()
	scratchName, err := wr.makeDir("scratch")
	if scratchName != "" {
		made = append(made, scratchName)
	}
	if err != nil {
		return res, err
	}
	homeName, err := wr.makeDir("home")
	if homeName != "" {
		made = append(made, homeName)
	}
	if err != nil {
		return res, err
	}

	plan, err := req.Planner.Verify(sandboxplan.VerifyRequest{
		Project: req.Project,
		Mirror:  req.Mirror,
		Scratch: filepath.Join(req.WorkRoot, scratchName),
		Home:    filepath.Join(req.WorkRoot, homeName),
	})
	if err != nil {
		return res, fmt.Errorf("verify: sandbox plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return res, fmt.Errorf("verify: sandbox plan: %w", err)
	}
	script, err := Script(req.TipRef, req.Tip, plan.Mirror, plan.Scratch, req.Gates)
	if err != nil {
		return res, err
	}
	cmd, err := plan.Command([]string{"/bin/sh", "-c", script})
	if err != nil {
		return res, fmt.Errorf("verify: sandbox plan: %w", err)
	}

	runner := req.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	res.Started = time.Now()
	r, rerr := runner.Run(ctx, boundedexec.Cmd{
		Argv:      cmd.Argv,
		Env:       cmd.Env,
		Timeout:   lim.Timeout,
		MaxStdout: lim.MaxOutput,
		MaxStderr: lim.MaxOutput,
	})
	res.Duration = time.Since(res.Started)
	finish(&res, r, rerr, lim)
	return res, nil
}

// finish turns what the runner returned into the result's fields.
func finish(res *Result, r boundedexec.Result, rerr error, lim Limits) {
	res.Exit = r.Exit
	var exitErr *boundedexec.ExitError
	switch {
	case rerr == nil:
	case errors.As(rerr, &exitErr):
		res.Exit = exitErr.Exit
		if exitErr.Exit < 0 {
			res.Stopped = StoppedError
		}
	case errors.Is(rerr, boundedexec.ErrTimeout):
		res.Stopped, res.Exit = StoppedTimeout, -1
	case errors.Is(rerr, boundedexec.ErrOutputLimit):
		res.Stopped, res.Exit, res.OutputCut = StoppedOutputLimit, -1, true
	default:
		res.Stopped = StoppedError
	}

	// Standard output first, then whatever the launcher itself wrote to
	// standard error (the script joins its own).
	out := r.Stdout
	if int64(len(out)) > lim.MaxOutput {
		out, res.OutputCut = out[:lim.MaxOutput], true
	}
	res.Output = append([]byte(nil), out...)
	if room := lim.MaxOutput - int64(len(res.Output)); len(r.Stderr) > 0 {
		e := r.Stderr
		if int64(len(e)) > room {
			e, res.OutputCut = e[:room], true
		}
		res.Output = append(res.Output, e...)
	}
	if r.StderrCut {
		res.OutputCut = true
	}
	res.Passed = res.Stopped == "" && res.Exit == 0 && !res.OutputCut
}
