//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/exchange/sandboxplan/plantest"
)

// recRunner is a runner that starts nothing: it records the command it is
// given and answers as it was told.
type recRunner struct {
	mu   sync.Mutex
	cmds []boundedexec.Cmd
	res  boundedexec.Result
	err  error
}

func (r *recRunner) Run(_ context.Context, c boundedexec.Cmd) (boundedexec.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, c)
	return r.res, r.err
}

// A realSuite is a VerifySuite over a fake planner and a runner that starts
// nothing, and the planner loads are counted.
func realSuite(t *testing.T, run *recRunner) (*VerifySuite, *int, *plantest.Fake) {
	t.Helper()
	loads := 0
	fake := &plantest.Fake{VerifyPrefix: []string{"/usr/bin/env"}}
	return &VerifySuite{
		Template: "/scratch/sandbox.json", WorkRoot: privateDir(t), Runner: run,
		Load: func(string) (sandboxplan.Planner, error) { loads++; return fake, nil },
	}, &loads, fake
}

// The real suite runs package verify in merge mode on the pin of the merge: the
// request it makes is accepted by verify's own checks (the pin under the
// pending prefix, the merge as the tip, the base), and the script it hands the
// sandbox fetches the pin and checks the merge against the base before a gate.
func TestTheRealSuiteRunsVerifyInMergeModeOnThePin(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	merge, pin := p.Binding.Merge.Commit, r.pinOfMerge(p.Binding.Merge.Commit)
	run := &recRunner{res: boundedexec.Result{Stdout: []byte("ok\n")}}
	s, loads, fake := realSuite(t, run)
	if *loads != 0 || len(fake.VerifyRequests()) != 0 {
		t.Fatalf("making the suite loaded a template")
	}
	got, err := s.Run(context.Background(), SuiteRequest{
		Project: "project-a", Mirror: r.lab.dir, Merge: merge, Base: r.scene.target, TipRef: pin, Gates: r.proj.Gates,
	})
	if err != nil || !got.Passed || got.Exit != 0 || got.Stopped != "" || string(got.Output) != "ok\n" || got.Started.IsZero() {
		t.Fatalf("Run = %+v, %v", got, err)
	}
	if *loads != 1 || len(run.cmds) != 1 {
		t.Fatalf("loads %d, runs %d", *loads, len(run.cmds))
	}
	script := strings.Join(run.cmds[0].Argv, "\n")
	for _, want := range []string{pin, merge, r.scene.target} {
		if !strings.Contains(script, want) {
			t.Errorf("the sandbox script does not name %s", want)
		}
	}
	// Each run loads the template again.
	if _, err := s.Run(context.Background(), SuiteRequest{
		Project: "project-a", Mirror: r.lab.dir, Merge: merge, Base: r.scene.target, TipRef: pin, Gates: r.proj.Gates,
	}); err != nil || *loads != 2 {
		t.Errorf("second run: %v, loads %d", err, *loads)
	}
}

// A lead ref as the tip ref is refused by verify in merge mode, and nothing runs.
func TestTheRealSuiteRefusesALeadRefAsThePin(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	run := &recRunner{}
	s, _, _ := realSuite(t, run)
	_, err := s.Run(context.Background(), SuiteRequest{
		Project: "project-a", Mirror: r.lab.dir, Merge: p.Binding.Merge.Commit, Base: r.scene.target,
		TipRef: "refs/leads/role-a/heads/feature/x", Gates: r.proj.Gates,
	})
	if err == nil || len(run.cmds) != 0 {
		t.Fatalf("Run = %v, runs %d, want a refusal and no run", err, len(run.cmds))
	}
}

// A failed gate, a time-out and a runner that fails are results, not passes; a
// planner or a template that cannot be used is an error and nothing runs.
func TestTheRealSuiteSaysHowARunEndedAndRefusesWhatCannotBeSetUp(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	merge, pin := p.Binding.Merge.Commit, r.pinOfMerge(p.Binding.Merge.Commit)
	in := SuiteRequest{Project: "project-a", Mirror: r.lab.dir, Merge: merge, Base: r.scene.target, TipRef: pin, Gates: r.proj.Gates}
	ctx := context.Background()

	run := &recRunner{err: &boundedexec.ExitError{Exit: 3}}
	s, _, _ := realSuite(t, run)
	if got, err := s.Run(ctx, in); err != nil || got.Passed || got.Exit != 3 {
		t.Errorf("a failed gate: %+v, %v", got, err)
	}
	run.err = boundedexec.ErrTimeout
	if got, err := s.Run(ctx, in); err != nil || got.Passed || got.Stopped != "timeout" {
		t.Errorf("a time-out: %+v, %v", got, err)
	}

	run = &recRunner{}
	s, _, fake := realSuite(t, run)
	fake.VerifyErr = errors.New("no sandbox")
	if _, err := s.Run(ctx, in); err == nil || len(run.cmds) != 0 {
		t.Errorf("a planner that refuses: %v, runs %d", err, len(run.cmds))
	}
	s.Load = func(string) (sandboxplan.Planner, error) { return nil, errors.New("no template") }
	if _, err := s.Run(ctx, in); err == nil || len(run.cmds) != 0 {
		t.Errorf("a template that cannot be read: %v, runs %d", err, len(run.cmds))
	}
	s.Load = func(string) (sandboxplan.Planner, error) { return nil, nil }
	if _, err := s.Run(ctx, in); err == nil || len(run.cmds) != 0 {
		t.Errorf("no planner: %v, runs %d", err, len(run.cmds))
	}
	// The real loader refuses a template that is not there.
	s.Load = nil
	s.Template = "/nonexistent-scratch/sandbox.json"
	if _, err := s.Run(ctx, in); err == nil || len(run.cmds) != 0 {
		t.Errorf("a missing template: %v, runs %d", err, len(run.cmds))
	}
	// A work folder that anyone else can reach is refused by verify.
	s2, _, _ := realSuite(t, run)
	s2.WorkRoot = "/"
	if _, err := s2.Run(ctx, in); err == nil || len(run.cmds) != 0 {
		t.Errorf("a bad work folder: %v, runs %d", err, len(run.cmds))
	}
}

// The settings make a suite only when they name the template and the work
// folder, and making it starts nothing.
func TestTheScratchSettingsMakeTheRealSuiteOnlyWhenTheyNameItsParts(t *testing.T) {
	sc := Scratch{}
	if _, err := sc.VerifySuiteOf(); err == nil {
		t.Errorf("settings with no template and no work folder made a suite")
	}
	sc.Sandbox = "/scratch/sandbox.json"
	if _, err := sc.VerifySuiteOf(); err == nil {
		t.Errorf("settings with no work folder made a suite")
	}
	sc.Work = "/scratch/work"
	s, err := sc.VerifySuiteOf()
	if err != nil || s.Template != sc.Sandbox || s.WorkRoot != sc.Work || s.Runner != nil {
		t.Errorf("VerifySuiteOf = %+v, %v", s, err)
	}
	for _, bad := range []string{"relative/x", "/a/../b", "/a//b", "/a/b\n"} {
		sc.Sandbox = bad
		if err := (&VerifySuite{Template: bad, WorkRoot: "/w"}).Check(); err == nil {
			t.Errorf("Check accepted the template %q", bad)
		}
	}
}
