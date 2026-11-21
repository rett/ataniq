//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"ataniq/internal/exchange/safefs"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/intake/verify"
)

// VerifySuite is the real [Suite]: it runs [verify.Run] in merge mode with the
// sandbox planner the intake uses for verification (the sandbox template) and
// a runner, in a work folder of the host's.
//
// Making one starts nothing and reads nothing: the planner is loaded by each
// run, from the template file, so the file that is read is the file that is
// there then. A run that cannot be set up (no template, a template that is not
// valid, a work folder that is not private) is an error, and nothing runs.
type VerifySuite struct {
	// Template is the clean absolute path of the sandbox template file, the
	// owner's. It is read with [LoadTemplate].
	Template string
	// WorkRoot is the clean absolute path of an owner-owned, private
	// (mode 0700) folder that holds the scratch and home folders of a run
	// and nowhere near the records or the mirror, as [verify.Request] asks.
	WorkRoot string
	// Runner starts the planned command; nil means [verify.ExecRunner].
	Runner verify.Runner
	// Limits bound one run; zero fields take the defaults of package verify.
	Limits verify.Limits
	// Load reads the planner from the template; nil means [LoadTemplate].
	// A test sets it.
	Load func(path string) (sandboxplan.Planner, error)
}

var _ Suite = (*VerifySuite)(nil)

// Check reports why the settings cannot be used, in words that name no value.
// It looks at the form of the paths only.
func (s *VerifySuite) Check() error {
	switch {
	case s == nil:
		return errors.New("hostadapter: no suite")
	case s.Template == "" || !filepath.IsAbs(s.Template) || filepath.Clean(s.Template) != s.Template || strings.ContainsAny(s.Template, "\x00\n"):
		return errors.New("hostadapter: the sandbox template is not a clean absolute path")
	case s.WorkRoot == "" || !filepath.IsAbs(s.WorkRoot) || filepath.Clean(s.WorkRoot) != s.WorkRoot || strings.ContainsAny(s.WorkRoot, "\x00\n"):
		return errors.New("hostadapter: the work folder is not a clean absolute path")
	}
	return nil
}

// Run implements [Suite]. It loads the planner, then runs [verify.Run] in merge
// mode on the pin and returns what the run decided. A run that did not start
// is an error; one that started is a result, a failure of its cleanup
// included (its Passed is then false).
func (s *VerifySuite) Run(ctx context.Context, req SuiteRequest) (SuiteResult, error) {
	if err := s.Check(); err != nil {
		return SuiteResult{}, err
	}
	load := s.Load
	if load == nil {
		load = LoadTemplate
	}
	planner, err := load(s.Template)
	if err != nil {
		return SuiteResult{}, err
	}
	if planner == nil {
		return SuiteResult{}, errors.New("hostadapter: no usable sandbox template, nothing was run")
	}
	runner := s.Runner
	if runner == nil {
		runner = verify.ExecRunner{}
	}
	res, runErr := verify.Run(ctx, verify.Request{
		Project: req.Project, Mirror: req.Mirror, TipRef: req.TipRef, Tip: req.Merge, Merge: true, Base: req.Base,
		Gates: req.Gates, Planner: planner, Runner: runner, WorkRoot: s.WorkRoot, Limits: s.Limits,
	})
	if res.Started.IsZero() {
		if runErr == nil {
			runErr = errors.New("the run did not start")
		}
		return SuiteResult{}, fmt.Errorf("hostadapter: the suite did not run: %w", runErr)
	}
	return SuiteResult{
		Passed: res.Passed, Exit: res.Exit, Stopped: res.Stopped, Output: res.Output, OutputCut: res.OutputCut,
		Started: res.Started, Duration: res.Duration,
	}, nil
}

// LoadTemplate reads the sandbox template at path and returns it as a planner,
// as the intake command does: the file is opened through safefs, so a symlink
// as the file is refused, and at most [sandboxplan.MaxTemplateBytes] are read.
// The template says which commands run and the host trusts it: it is not a
// statement that the commands confine anything.
func LoadTemplate(path string) (sandboxplan.Planner, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sandbox template: %w", err)
	}
	root, err := safefs.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("sandbox template: cannot open its folder: %w", err)
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.Base(abs), sandboxplan.MaxTemplateBytes)
	if err != nil {
		return nil, fmt.Errorf("sandbox template: cannot read it: %w", err)
	}
	t, err := sandboxplan.ParseTemplate(data)
	if err != nil {
		return nil, err
	}
	return t, nil
}
