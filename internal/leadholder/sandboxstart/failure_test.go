//go:build linux && ataniq_canary

package sandboxstart

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/mountpoints"
)

// problemReport is a report of the mount points with one problem in it.
func problemReport(path string) mountpoints.Report {
	return mountpoints.Report{Problems: []*mountpoints.Problem{{Kind: mountpoints.ErrSymlink, Path: path, Detail: "a symbolic link where a folder belongs"}}}
}

// TestEachFailedStepRefusesTheStartWithItsName makes each step fail in turn and
// checks the refusal names it, nothing after it ran, a child that was launched
// was stopped and reaped, and nothing the sequence opened is left.
func TestEachFailedStepRefusesTheStartWithItsName(t *testing.T) {
	boom := errors.New("injected failure")
	cases := []struct {
		step string
		// launched says a child exists when the step fails.
		launched bool
		set      func(w *world)
	}{
		{StepValidate, false, func(w *world) { w.sys.fail["CheckHost"] = boom }},
		{StepValidate, false, func(w *world) { w.in.TimeLimitSeconds = 0 }},
		{StepValidate, false, func(w *world) { w.in.AgentArgv = nil }},
		{StepVersions, false, func(w *world) { w.sys.versions.Errors = []string{"bwrap --version: boom"} }},
		{StepVersions, false, func(w *world) { w.sys.versions.Bwrap = "bubblewrap 0.4.1" }},
		{StepOwnClone, false, func(w *world) { w.sys.fail["CheckOwnClone"] = boom }},
		{StepOwnClone, false, func(w *world) { w.sys.ownClone = problemReport("/tree") }},
		{StepPrepare, false, func(w *world) { w.sys.fail["PrepareMountPoints"] = boom }},
		{StepCheck, false, func(w *world) { w.sys.fail["CheckMountPoints"] = boom }},
		{StepCheck, false, func(w *world) { w.sys.checks = []mountpoints.Report{problemReport("/tree/.git")} }},
		{StepResolv, false, func(w *world) { w.sys.fail["DeriveResolv"] = boom }},
		{StepResolv, false, func(w *world) { w.sys.fail["MakeBound"] = boom }},
		{StepResolv, false, func(w *world) { w.sys.fail["WriteResolv"] = boom }},
		{StepResolve, false, func(w *world) { w.sys.fail["ResolveSources"] = boom }},
		{StepChannel, false, func(w *world) { w.sys.fail["OpenReport"] = boom }},
		{StepCanary, false, func(w *world) { w.sys.fail["NewCanary"] = boom }},
		{StepBuild, false, func(w *world) { w.in.AgentArgv = []string{"relative/agent"} }},
		{StepVerify, false, func(w *world) { w.in.ExpectedDigest = strings.Repeat("f", 64) }},
		{StepSpec, false, func(w *world) { w.sys.fail["WriteSpec"] = boom }},
		{StepRecheck, false, func(w *world) { w.sys.failN["CheckMountPoints#2"] = boom }},
		{StepRecheck, false, func(w *world) { w.sys.checks = []mountpoints.Report{{}, problemReport("/tree/.git/hooks")} }},
		{StepRecheck, false, func(w *world) { w.sys.failN["ResolveSources#2"] = boom }},
		{StepRecheck, false, func(w *world) { w.sys.resolveShift = true }},
		{StepBindSources, false, func(w *world) { w.sys.bindRep = problemReport("/ataniq/agent") }},
		{StepLaunch, false, func(w *world) { w.lch.err = boom }},
		{StepReadReport, true, func(w *world) { w.sys.fail["ReadLine"] = boom }},
		{StepOutside, true, func(w *world) { w.sys.lock.held = false }},
		{StepTrailing, true, func(w *world) { w.sys.fail["Trailing"] = boom }},
		{StepCleanup, true, func(w *world) { w.sys.fail["Bound.Close"] = boom }},
	}
	for _, c := range cases {
		t.Run(c.step, func(t *testing.T) {
			w := newWorld(t)
			c.set(w)
			st, err := w.run_()
			var se *StepError
			if !errors.As(err, &se) {
				t.Fatalf("error %v is no step refusal", err)
			}
			if se.Step != c.step {
				t.Fatalf("refused at %s, want %s: %v", se.Step, c.step, err)
			}
			if !strings.Contains(err.Error(), c.step) {
				t.Errorf("the message does not name the step: %v", err)
			}
			if st.Child != nil || st.Waived || st.Plan != nil {
				t.Errorf("a refused start returned %+v", st)
			}
			// The failed step is the last of the steps that ran, but for the stop
			// and the clean-up that follow a failure.
			var ran []string
			for _, r := range se.Steps {
				ran = append(ran, r.Name)
			}
			failed := -1
			for i, r := range se.Steps {
				if r.Status == StepFailed && r.Name == c.step {
					failed = i
				}
			}
			if failed < 0 {
				t.Fatalf("the steps %v do not hold the failed step %s", ran, c.step)
			}
			for _, r := range se.Steps[failed+1:] {
				if r.Name != StepStop && r.Name != StepCleanup {
					t.Errorf("step %s ran after the failure of %s", r.Name, c.step)
				}
			}
			// What was launched is stopped and reaped, through the handle.
			if got := len(w.lch.kids); c.launched != (got == 1) {
				t.Fatalf("%d children, launched=%v", got, c.launched)
			}
			if c.launched {
				k := w.lch.kids[0]
				if len(k.stops) != 1 || k.stops[0] != assert.StopTermWait || k.closes != 1 {
					t.Errorf("child stops %v, closes %d", k.stops, k.closes)
				}
			}
			// And nothing is left open.
			if c.step == StepCleanup {
				// The clean-up that failed was the first; the folder is closed once
				// and the second attempt has nothing to do.
				for _, b := range w.sys.bounds {
					if b.closes != 1 {
						t.Errorf("bound folder closed %d times", b.closes)
					}
				}
				return
			}
			assertNothingOpen(t, w)
			if _, ok := stepIndex(se.Steps, StepCleanup); !ok {
				t.Errorf("no clean-up was recorded: %v", ran)
			}
		})
	}
}

func stepIndex(recs []StepRecord, name string) (int, bool) {
	for i, r := range recs {
		if r.Name == name {
			return i, true
		}
	}
	return 0, false
}

// TestFailedStartOpensNothingAfterTheFailure checks the later opens are never
// made: a failure at the report channel makes no canary, no spec, no launch.
func TestFailedStartOpensNothingAfterTheFailure(t *testing.T) {
	w := newWorld(t)
	w.sys.fail["OpenReport"] = errors.New("no folder")
	if _, err := w.run_(); err == nil {
		t.Fatal("no error")
	}
	if len(w.sys.canaries) != 0 || len(w.sys.channels) != 0 || w.lch.launches() != 0 {
		t.Errorf("canaries %d, channels %d, launches %d", len(w.sys.canaries), len(w.sys.channels), w.lch.launches())
	}
	// The bound folder was made before the channel and is removed.
	if len(w.sys.bounds) != 1 || w.sys.bounds[0].closes != 1 {
		t.Errorf("bound folders: %d", len(w.sys.bounds))
	}
}

// TestFailedStartReportsAChildThatWillNotStop shows a tree that is still there
// after the stop is told, not hidden: the state is unknown.
func TestFailedStartReportsAChildThatWillNotStop(t *testing.T) {
	w := newWorld(t)
	w.sys.lock.held = false
	w.lch.onLaunch = nil
	// A child whose tree never goes.
	stubborn := &fakeLauncher2{base: w.lch}
	_, err := Run(t.Context(), w.in, w.sys, stubborn)
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepOutside {
		t.Fatalf("error %v", err)
	}
	if !strings.Contains(err.Error(), "state is unknown") {
		t.Errorf("the refusal does not say the state is unknown: %v", err)
	}
	if i, ok := stepIndex(se.Steps, StepStop); !ok || se.Steps[i].Status != StepFailed {
		t.Errorf("the stop is not recorded as failed: %v", se.Steps)
	}
	if stubborn.kid.closes != 1 {
		t.Errorf("the child was closed %d times", stubborn.kid.closes)
	}
}

// fakeLauncher2 launches a child whose tree does not go.
type fakeLauncher2 struct {
	base *fakeLauncher
	kid  *fakeChild
}

func (l *fakeLauncher2) Launch(argv []string) (Child, error) {
	c, err := l.base.Launch(argv)
	if err != nil {
		return nil, err
	}
	l.kid = c.(*fakeChild)
	l.kid.gone = false
	return c, nil
}

// TestReadReportTimeLimit shows the wait for the report line is bounded by the
// time limit of the inputs and says so.
func TestReadReportTimeLimit(t *testing.T) {
	w := newWorld(t)
	w.in.TimeLimitSeconds = 1
	slow := &slowSystem{fakeSystem: w.sys}
	start := time.Now()
	_, err := Run(t.Context(), w.in, slow, w.lch)
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepReadReport || !strings.Contains(err.Error(), "within 1s") {
		t.Fatalf("error %v, want a refusal at %s naming the limit", err, StepReadReport)
	}
	if d := time.Since(start); d < 900*time.Millisecond || d > 5*time.Second {
		t.Errorf("the wait took %v", d)
	}
	if k := w.lch.kids[0]; len(k.stops) != 1 || k.closes != 1 {
		t.Errorf("child stops %v closes %d", k.stops, k.closes)
	}
}

// slowSystem gives a report channel whose line never comes.
type slowSystem struct{ *fakeSystem }

func (s *slowSystem) OpenReport(runtimeRoot, layoutRole string) (ReportChannel, error) {
	c, err := s.fakeSystem.OpenReport(runtimeRoot, layoutRole)
	if err != nil {
		return nil, err
	}
	return &silentChannel{ReportChannel: c}, nil
}

type silentChannel struct{ ReportChannel }

func (c *silentChannel) ReadLine(ctx context.Context, exited <-chan struct{}) (assert.Report, error) {
	<-ctx.Done()
	return assert.Report{}, ctx.Err()
}
