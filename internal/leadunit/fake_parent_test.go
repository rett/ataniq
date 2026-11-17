package leadunit

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	unitP = "ataniq-lead-p.service"
	unitC = "ataniq-coord-p_f.service"
	unitD = "ataniq-coord-p_g.service"
	unitG = "ataniq-supervisor.service"
)

// startBound starts unit bound to parent and returns its state.
func (r *rig) startBound(unit, parent string) State {
	r.t.Helper()
	s := r.spec(unit, 1)
	s.Parent = parent
	st, err := r.fake.Start(r.ctx, s)
	if err != nil {
		r.t.Fatalf("Start %s: %v", unit, err)
	}
	if !st.Loaded {
		r.t.Fatalf("Start %s: not loaded: %+v", unit, st)
	}
	return st
}

func (r *rig) implied() []Call {
	var out []Call
	for _, c := range r.fake.Calls() {
		if c.Implied {
			out = append(out, c)
		}
	}
	return out
}

type impliedWant struct {
	unit string
	sig  syscall.Signal
	at   time.Duration
}

func (r *rig) wantImplied(want ...impliedWant) {
	r.t.Helper()
	got := r.implied()
	if len(got) != len(want) {
		r.t.Fatalf("implied entries %+v, want %+v", got, want)
	}
	for i, w := range want {
		c := got[i]
		if c.Method != MethodSignal || c.Unit != w.unit || c.Signal != w.sig || !c.At.Equal(epoch0.Add(w.at)) || c.Err != nil || c.Invocation == "" {
			r.t.Errorf("implied entry %d is %+v, want %+v", i, c, w)
		}
	}
}

func TestFakeStopTimeoutIsTheFixedTimeoutStopSec(t *testing.T) {
	for _, p := range FixedProperties() {
		if p.Name != "TimeoutStopSec" {
			continue
		}
		d, err := time.ParseDuration(p.Value)
		if err != nil || d != stopTimeout {
			t.Fatalf("TimeoutStopSec %q (%v) is not the fake's %v", p.Value, err, stopTimeout)
		}
		return
	}
	t.Fatal("TimeoutStopSec is not a fixed property")
}

func TestFakeBoundUnitStartsUnderALiveParent(t *testing.T) {
	r := newRig(t)
	r.start(unitP, 1)
	st := r.startBound(unitC, unitP)
	if st.Parent != unitP || !st.Live() || st.Active != ActiveActive {
		t.Fatalf("state %+v", st)
	}
	if got := r.inspect(unitC); got.Parent != unitP {
		t.Fatalf("Inspect did not read the parent back: %+v", got)
	}
	if got := r.inspect(unitP); got.Parent != "" {
		t.Fatalf("the parent has a parent: %+v", got)
	}
	specs := r.fake.StartedSpecs()
	if len(specs) != 2 || specs[1].Parent != unitP {
		t.Fatalf("started specs %+v", specs)
	}

	// A unit that this package did not start can be the parent too.
	if err := r.fake.AddForeign(unitG, "something else"); err != nil {
		t.Fatal(err)
	}
	if st := r.startBound(unitD, unitG); st.Parent != unitG {
		t.Fatalf("%+v", st)
	}
}

func TestFakeBoundUnitNeverBecomesActiveWithoutALiveParent(t *testing.T) {
	r := newRig(t)
	s := r.spec(unitC, 1)
	s.Parent = unitP
	st, err := r.fake.Start(r.ctx, s)
	if err != nil || st.Loaded || st.Active != ActiveUnknown || st.InvocationID != "" || st.Unit != unitC || st.BootID == "" {
		t.Fatalf("the default outcome: %+v %v", st, err)
	}
	if r.fake.Loaded(unitC) || len(r.fake.StartedSpecs()) != 0 {
		t.Fatal("a unit was started")
	}
	calls := r.fake.CallsOf(MethodStart)
	if len(calls) != 1 || calls[0].Err != nil || calls[0].Invocation != "" {
		t.Fatalf("%+v", calls)
	}
	if got := r.inspect(unitC); got.Loaded {
		t.Fatalf("%+v", got)
	}

	// The same outcome in the other mode is an error that wraps ErrParentNotLive
	// inside ErrReplyLost, as the real manager reports a refusal it does not
	// recognise.
	r.fake.SetMissingParentOutcome(MissingParentRefused)
	st, err = r.fake.Start(r.ctx, s)
	if !errors.Is(err, ErrParentNotLive) || !errors.Is(err, ErrReplyLost) || errors.Is(err, ErrUnitExists) || st != (State{}) || !strings.Contains(err.Error(), unitP) {
		t.Fatalf("%+v %v", st, err)
	}
	if r.fake.Loaded(unitC) || len(r.fake.StartedSpecs()) != 0 {
		t.Fatal("a unit was started")
	}

	// And back.
	r.fake.SetMissingParentOutcome(MissingParentNeverActive)
	if st, err := r.fake.Start(r.ctx, s); err != nil || st.Loaded {
		t.Fatalf("%+v %v", st, err)
	}

	// A parent that is not live is the same as one that is not loaded: a
	// parent that is itself being stopped.
	r.start(unitG, 1)
	r.fake.SetProcs(unitP, ProcScript{Behavior: IgnoreTerm})
	sp := r.spec(unitP, 1)
	sp.Parent = unitG
	if _, err := r.fake.Start(r.ctx, sp); err != nil {
		t.Fatal(err)
	}
	r.signal(r.inspect(unitG), syscall.SIGKILL)
	if got := r.inspect(unitP); !got.Loaded || got.Active != ActiveDeactivating {
		t.Fatalf("the middle unit is not stopping: %+v", got)
	}
	if st, err := r.fake.Start(r.ctx, s); err != nil || st.Loaded {
		t.Fatalf("a start under a stopping parent: %+v %v", st, err)
	}
	r.fake.SetMissingParentOutcome(MissingParentRefused)
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrParentNotLive) {
		t.Fatalf("a start under a stopping parent: %v", err)
	}
}

func TestFakeBoundStartKeepsItsScriptedOutcomes(t *testing.T) {
	r := newRig(t)
	s := r.spec(unitC, 1)
	s.Parent = unitP

	// A lost reply is lost whether or not anything started; nothing does
	// without a parent.
	r.fake.LoseNextStartReply(unitC)
	if st, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrReplyLost) || st != (State{}) || r.fake.Loaded(unitC) {
		t.Fatalf("%+v %v", st, err)
	}
	r.fake.ExpireNextStart(unitC)
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrReplyLost) || !errors.Is(err, context.DeadlineExceeded) || r.fake.Loaded(unitC) {
		t.Fatalf("%v", err)
	}
	r.fake.DelayNextStart(unitC, time.Hour) // not applied: nothing started
	if st, err := r.fake.Start(r.ctx, s); err != nil || st.Loaded {
		t.Fatalf("%+v %v", st, err)
	}
	// The scripts were each used once.
	if st, err := r.fake.Start(r.ctx, s); err != nil || st.Loaded {
		t.Fatalf("%+v %v", st, err)
	}

	// With the parent live the same scripts start the unit, bound.
	r.start(unitP, 1)
	r.fake.LoseNextStartReply(unitC)
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrReplyLost) {
		t.Fatal(err)
	}
	if got := r.inspect(unitC); !got.Loaded || got.Parent != unitP {
		t.Fatalf("%+v", got)
	}
}

func TestFakeLateBoundStartLandsOnlyUnderALiveParent(t *testing.T) {
	r := newRig(t)
	s := r.spec(unitC, 1)
	s.Parent = unitP

	// The parent is gone by the time the request lands: the unit never
	// becomes active, and the entry says it was a late one.
	r.fake.LateNextStart(unitC)
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrReplyLost) {
		t.Fatal(err)
	}
	st, err := r.fake.LandLateStart(unitC)
	if err != nil || st.Loaded || r.fake.Loaded(unitC) || len(r.fake.StartedSpecs()) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	last := r.fake.Calls()[len(r.fake.Calls())-1]
	if !last.Late || last.Method != MethodStart || last.Err != nil || last.Invocation != "" {
		t.Fatalf("%+v", last)
	}

	r.fake.SetMissingParentOutcome(MissingParentRefused)
	r.fake.LateNextStart(unitC)
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrReplyLost) {
		t.Fatal(err)
	}
	if _, err := r.fake.LandLateStart(unitC); !errors.Is(err, ErrParentNotLive) {
		t.Fatalf("%v", err)
	}
	if last := r.fake.Calls()[len(r.fake.Calls())-1]; !last.Late || !errors.Is(last.Err, ErrParentNotLive) {
		t.Fatalf("%+v", last)
	}

	// The parent came up meanwhile: the request lands and the unit is bound.
	r.start(unitP, 1)
	r.fake.LateNextStart(unitC)
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrReplyLost) {
		t.Fatal(err)
	}
	st, err = r.fake.LandLateStart(unitC)
	if err != nil || !st.Loaded || st.Parent != unitP {
		t.Fatalf("%+v %v", st, err)
	}
	if err := r.fake.Signal(r.ctx, r.inspect(unitP), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if r.fake.Loaded(unitC) {
		t.Fatal("the late unit was not stopped with its parent")
	}
}

func TestFakeParentSigtermStopsTheBoundUnit(t *testing.T) {
	r := newRig(t)
	parent := r.start(unitP, 1)
	child := r.startBound(unitC, unitP)
	r.start(unitD, 1) // bound to nothing
	r.signal(parent, syscall.SIGTERM)

	if r.fake.Loaded(unitP) || r.fake.Loaded(unitC) {
		t.Fatal("a unit is still loaded")
	}
	if !r.fake.Loaded(unitD) {
		t.Fatal("a unit that is bound to nothing was stopped")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 0})
	if got := r.implied()[0]; got.Invocation != child.InvocationID {
		t.Fatalf("the entry names invocation %q, not %q", got.Invocation, child.InvocationID)
	}
	// The explicit signal is logged before the stop it caused.
	var order []string
	for _, c := range r.fake.Calls() {
		if c.Method == MethodSignal {
			order = append(order, c.Unit)
		}
	}
	if strings.Join(order, " ") != unitP+" "+unitC {
		t.Fatalf("order %v", order)
	}
	if !r.empty(child) || !r.empty(parent) {
		t.Fatal("the groups are not proven empty")
	}
}

func TestFakeParentKilledStopsTheBoundUnit(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitP, ProcScript{Behavior: IgnoreTerm})
	parent := r.start(unitP, 1)
	r.startBound(unitC, unitP)
	r.signal(parent, syscall.SIGTERM)
	if !r.fake.Loaded(unitP) || !r.fake.Loaded(unitC) || len(r.implied()) != 0 {
		t.Fatal("a signal that did not kill the parent stopped the bound unit")
	}
	r.signal(parent, syscall.SIGKILL)
	if r.fake.Loaded(unitC) {
		t.Fatal("the bound unit outlived the parent")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 0})
}

func TestFakeParentCrashStopsTheBoundUnit(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitP, ProcScript{Behavior: SurviveKill})
	r.start(unitP, 1)
	r.fake.SetProcs(unitC, ProcScript{Behavior: IgnoreTerm})
	r.startBound(unitC, unitP)
	r.clock.Advance(time.Second)
	if !r.fake.Crash(unitP) {
		t.Fatal("not loaded")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, time.Second})
	if got := r.inspect(unitC); !got.Loaded || got.Active != ActiveDeactivating {
		t.Fatalf("the child ignores SIGTERM and is stopping: %+v", got)
	}
	r.clock.Advance(2 * time.Second)
	if r.fake.Loaded(unitC) {
		t.Fatal("the stop timeout did not end the child")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, time.Second}, impliedWant{unitC, syscall.SIGKILL, 3 * time.Second})
}

// The timing the fake had first, chosen with SetBoundStop: the bound unit is
// stopped only when the parent is collected.
func TestFakeParentCollectedStopsTheBoundUnit(t *testing.T) {
	r := newRig(t)
	r.fake.SetBoundStop(BoundStopOnUnload)
	r.fake.SetProcs(unitP, ProcScript{}, ProcScript{Behavior: SurviveKill})
	r.start(unitP, 1)
	r.startBound(unitC, unitP)
	pids := r.fake.Processes(unitP)
	if len(pids) != 2 {
		t.Fatal(pids)
	}
	if !r.fake.KillProcess(unitP, pids[0]) || !r.fake.Loaded(unitC) {
		t.Fatal("the bound unit stopped while its parent still holds a process")
	}
	if !r.fake.KillProcess(unitP, pids[1]) || r.fake.Loaded(unitP) || r.fake.Loaded(unitC) {
		t.Fatal("the parent was collected and the bound unit was not stopped")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 0})
}

// The stop of a bound unit takes place when the parent unloaded, however long
// after that the fake is looked at, and the stop timeout counts from there.
func TestFakeBoundStopHappensAtTheParentsTime(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitP, ProcScript{Behavior: ExitOnTermAfter, Delay: 3 * time.Second})
	r.fake.SetProcs(unitC, ProcScript{Behavior: IgnoreTerm})
	parent := r.start(unitP, 1)
	r.startBound(unitC, unitP)
	r.signal(parent, syscall.SIGTERM)
	if len(r.implied()) != 0 {
		t.Fatal("stopped before the parent is gone")
	}

	r.clock.Advance(4 * time.Second) // nothing has looked since the signal
	got := r.inspect(unitC)
	if !got.Loaded || got.Active != ActiveDeactivating || got.MainPID == 0 || len(r.fake.Processes(unitC)) != 1 {
		t.Fatalf("a stopping child at 4 s: %+v", got)
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 3 * time.Second})

	r.clock.Advance(500 * time.Millisecond)
	if !r.fake.Loaded(unitC) {
		t.Fatal("killed before its stop timeout")
	}
	r.clock.Advance(500 * time.Millisecond)
	if r.fake.Loaded(unitC) {
		t.Fatal("not killed at its stop timeout")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 3 * time.Second}, impliedWant{unitC, syscall.SIGKILL, 5 * time.Second})

	// The log is numbered without a gap and without a repeat.
	for i, c := range r.fake.Calls() {
		if c.Seq != i+1 {
			t.Fatalf("call %d has Seq %d", i, c.Seq)
		}
	}
}

func TestFakeBoundStopFollowsEachProcessScript(t *testing.T) {
	r := newRig(t)
	r.start(unitP, 1)
	r.fake.SetProcs(unitC,
		ProcScript{Behavior: ExitOnTerm},
		ProcScript{Behavior: ExitOnTermAfter, Delay: time.Second},
		ProcScript{Behavior: ExitOnTermAfter, Delay: 5 * time.Second},
		ProcScript{Behavior: IgnoreTerm},
		ProcScript{Behavior: SurviveKill})
	r.startBound(unitC, unitP)
	r.fake.Crash(unitP)
	if n := len(r.fake.Processes(unitC)); n != 4 {
		t.Fatalf("after SIGTERM: %v", r.fake.Processes(unitC))
	}
	r.clock.Advance(time.Second)
	if n := len(r.fake.Processes(unitC)); n != 3 {
		t.Fatalf("after the delayed exit: %d", n)
	}
	r.clock.Advance(time.Second)
	// SIGKILL at 2 s ends the long delay and the one that ignores SIGTERM.
	if got := r.fake.Processes(unitC); len(got) != 1 {
		t.Fatalf("after the stop timeout: %v", got)
	}
	got := r.inspect(unitC)
	if !got.Loaded || got.Active != ActiveDeactivating {
		t.Fatalf("a process that survives SIGKILL keeps the unit stopping: %+v", got)
	}
	r.clock.Advance(time.Hour)
	if !r.fake.Loaded(unitC) {
		t.Fatal("a process that survives SIGKILL went away")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 0}, impliedWant{unitC, syscall.SIGKILL, 2 * time.Second})
	// Only a crash of the unit itself removes it.
	if !r.fake.Crash(unitC) || r.fake.Loaded(unitC) {
		t.Fatal("crash")
	}
}

func TestFakeBoundStopCascades(t *testing.T) {
	r := newRig(t)
	r.fake.SetBoundStop(BoundStopOnUnload)
	r.fake.SetProcs(unitP, ProcScript{Behavior: IgnoreTerm})
	r.fake.SetProcs(unitC, ProcScript{Behavior: IgnoreTerm})
	g := r.start(unitG, 1)
	r.startBound(unitP, unitG)
	r.startBound(unitC, unitP)
	r.startBound(unitD, unitG) // a second unit bound to the same parent
	r.signal(g, syscall.SIGTERM)

	// The grandparent is gone at once; its units are stopping, the grandchild
	// is not stopped before its parent is gone.
	r.wantImplied(impliedWant{unitD, syscall.SIGTERM, 0}, impliedWant{unitP, syscall.SIGTERM, 0})
	if r.fake.Loaded(unitD) {
		t.Fatal("the unit that exits on SIGTERM is still loaded")
	}
	r.clock.Advance(2 * time.Second)
	if r.fake.Loaded(unitP) || !r.fake.Loaded(unitC) {
		t.Fatal("the middle unit should be killed at 2 s and its child stopping")
	}
	r.wantImplied(
		impliedWant{unitD, syscall.SIGTERM, 0},
		impliedWant{unitP, syscall.SIGTERM, 0},
		impliedWant{unitP, syscall.SIGKILL, 2 * time.Second},
		impliedWant{unitC, syscall.SIGTERM, 2 * time.Second},
	)
	r.clock.Advance(2 * time.Second)
	if r.fake.Loaded(unitC) {
		t.Fatal("the grandchild was not killed 2 s after its own stop")
	}
	if n := len(r.implied()); n != 5 {
		t.Fatalf("%d implied entries: %+v", n, r.implied())
	}
}

func TestFakeBoundStopIsNotRepeatedByANewParent(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitC, ProcScript{Behavior: IgnoreTerm})
	r.start(unitP, 1)
	r.startBound(unitC, unitP)
	r.fake.Crash(unitP)
	r.clock.Advance(time.Second)

	// The name is taken again by a new unit while the child is still stopping,
	// and that one goes too: the stop under way is not started again, and the
	// timeout of the first one stands.
	r.start(unitP, 2)
	if st := r.inspect(unitC); !st.Loaded || st.Active != ActiveDeactivating {
		t.Fatalf("%+v", st)
	}
	r.fake.Crash(unitP)
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 0})
	r.clock.Advance(time.Second)
	if r.fake.Loaded(unitC) {
		t.Fatal("the first stop timeout did not end the child")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 0}, impliedWant{unitC, syscall.SIGKILL, 2 * time.Second})

	// A child started under the new parent is bound to it too, and gets its
	// own stop.
	r.start(unitP, 3)
	r.startBound(unitC, unitP)
	r.fake.Crash(unitP)
	if st := r.inspect(unitC); !st.Loaded || st.Active != ActiveDeactivating || len(r.implied()) != 3 {
		t.Fatalf("%+v %v", st, r.implied())
	}
}

func TestFakeUnitsBoundElsewhereAreNotStopped(t *testing.T) {
	r := newRig(t)
	r.start(unitP, 1)
	r.start(unitG, 1)
	r.startBound(unitC, unitG)
	r.fake.Crash(unitP)
	if !r.fake.Loaded(unitC) || len(r.implied()) != 0 {
		t.Fatal("a unit bound to another unit was stopped")
	}
}

func TestFakeRebootForgetsBoundUnits(t *testing.T) {
	r := newRig(t)
	r.start(unitP, 1)
	r.startBound(unitC, unitP)
	r.fake.Reboot()
	if r.fake.Loaded(unitP) || r.fake.Loaded(unitC) || len(r.implied()) != 0 {
		t.Fatal("a reboot stops units one by one")
	}
	s := r.spec(unitC, 1)
	s.Parent = unitP
	if st, err := r.fake.Start(r.ctx, s); err != nil || st.Loaded {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestFakeImpliedStopsAreMarkedAndExplicitCallsAreNot(t *testing.T) {
	r := newRig(t)
	parent := r.start(unitP, 1)
	child := r.startBound(unitC, unitP)
	r.signal(parent, syscall.SIGTERM)
	// A signal for the stale invocation of the child is refused as any other.
	if err := r.fake.Signal(r.ctx, child, syscall.SIGKILL); !errors.Is(err, ErrNotLoaded) {
		t.Fatal(err)
	}
	var marked, plain int
	for _, c := range r.fake.Calls() {
		if c.Implied {
			marked++
		} else {
			plain++
		}
	}
	if marked != 1 || plain != 4 {
		t.Fatalf("%d implied, %d explicit: %+v", marked, plain, r.fake.Calls())
	}
}

// A parent whose main process is gone while another process of its group
// ignores SIGTERM is deactivating, and its bound unit is stopped from then on.
func TestFakeBoundStopWhenTheParentTurnsDeactivating(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitP, ProcScript{}, ProcScript{Behavior: IgnoreTerm})
	r.fake.SetProcs(unitC, ProcScript{Behavior: IgnoreTerm})
	parent := r.start(unitP, 1)
	r.startBound(unitC, unitP)
	r.clock.Advance(time.Second)
	r.signal(parent, syscall.SIGTERM)
	if got := r.inspect(unitP); !got.Loaded || got.Active != ActiveDeactivating {
		t.Fatalf("the parent should be deactivating: %+v", got)
	}
	if got := r.inspect(unitC); !got.Loaded || got.Active != ActiveDeactivating {
		t.Fatalf("the bound unit should be stopping with its parent: %+v", got)
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, time.Second})

	// The stop timeout counts from the moment the parent turned deactivating,
	// and the parent stays loaded.
	r.clock.Advance(2 * time.Second)
	if r.fake.Loaded(unitC) || !r.fake.Loaded(unitP) {
		t.Fatalf("child loaded %v, parent loaded %v", r.fake.Loaded(unitC), r.fake.Loaded(unitP))
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, time.Second}, impliedWant{unitC, syscall.SIGKILL, 3 * time.Second})
}

// The same holds for a main process that exits by itself, and for the
// process list changing by KillProcess.
func TestFakeBoundStopWhenTheParentsMainProcessGoes(t *testing.T) {
	for _, how := range []string{"delayed exit", "kill process"} {
		t.Run(how, func(t *testing.T) {
			r := newRig(t)
			if how == "delayed exit" {
				r.fake.SetProcs(unitP, ProcScript{Behavior: ExitOnTermAfter, Delay: 3 * time.Second}, ProcScript{Behavior: SurviveKill})
			} else {
				r.fake.SetProcs(unitP, ProcScript{}, ProcScript{Behavior: SurviveKill})
			}
			parent := r.start(unitP, 1)
			r.startBound(unitC, unitP)
			switch how {
			case "delayed exit":
				r.signal(parent, syscall.SIGTERM)
				if len(r.implied()) != 0 {
					t.Fatal("stopped before the main process is gone")
				}
				r.clock.Advance(4 * time.Second) // nothing looks until now
				if got := r.inspect(unitC); got.Loaded {
					t.Fatalf("the child should be gone (it exits on SIGTERM): %+v", got)
				}
				r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 3 * time.Second})
			case "kill process":
				r.clock.Advance(time.Second)
				if !r.fake.KillProcess(unitP, r.fake.Processes(unitP)[0]) {
					t.Fatal("no such process")
				}
				if r.fake.Loaded(unitC) {
					t.Fatal("the bound unit outlived the main process of its parent")
				}
				r.wantImplied(impliedWant{unitC, syscall.SIGTERM, time.Second})
			}
			if !r.fake.Loaded(unitP) {
				t.Fatal("the parent was collected: it still holds a process")
			}
		})
	}
}

// A stop of the whole chain: the grandchild is stopped as soon as its parent
// is stopping, without waiting for it to be collected.
func TestFakeBoundStopCascadesWhenDeactivating(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitP, ProcScript{Behavior: IgnoreTerm})
	r.fake.SetProcs(unitC, ProcScript{Behavior: IgnoreTerm})
	g := r.start(unitG, 1)
	r.startBound(unitP, unitG)
	r.startBound(unitC, unitP)
	r.startBound(unitD, unitG) // a second unit bound to the same parent
	r.signal(g, syscall.SIGTERM)
	r.wantImplied(
		impliedWant{unitD, syscall.SIGTERM, 0},
		impliedWant{unitP, syscall.SIGTERM, 0},
		impliedWant{unitC, syscall.SIGTERM, 0},
	)
	if r.fake.Loaded(unitD) {
		t.Fatal("the unit that exits on SIGTERM is still loaded")
	}
	if !r.fake.Loaded(unitP) || !r.fake.Loaded(unitC) {
		t.Fatal("the units that ignore SIGTERM went away before their stop timeout")
	}
	r.clock.Advance(2 * time.Second)
	if r.fake.Loaded(unitP) || r.fake.Loaded(unitC) {
		t.Fatal("both units should be killed at their stop timeout")
	}
	r.wantImplied(
		impliedWant{unitD, syscall.SIGTERM, 0},
		impliedWant{unitP, syscall.SIGTERM, 0},
		impliedWant{unitC, syscall.SIGTERM, 0},
		impliedWant{unitC, syscall.SIGKILL, 2 * time.Second},
		impliedWant{unitP, syscall.SIGKILL, 2 * time.Second},
	)
}

// A start under a parent that is deactivating is treated as one under a
// parent that is not live, in both outcomes; under BoundStopOnUnload it is
// accepted and the unit is stopped when the parent unloads.
func TestFakeStartUnderADeactivatingParent(t *testing.T) {
	setup := func(t *testing.T) (*rig, State) {
		r := newRig(t)
		r.fake.SetProcs(unitP, ProcScript{}, ProcScript{Behavior: IgnoreTerm})
		parent := r.start(unitP, 1)
		r.signal(parent, syscall.SIGTERM)
		if got := r.inspect(unitP); got.Active != ActiveDeactivating {
			t.Fatalf("%+v", got)
		}
		return r, parent
	}
	s := func(r *rig) Spec {
		sp := r.spec(unitC, 1)
		sp.Parent = unitP
		return sp
	}

	r, _ := setup(t)
	if st, err := r.fake.Start(r.ctx, s(r)); err != nil || st.Loaded || r.fake.Loaded(unitC) {
		t.Fatalf("default outcome: %+v %v", st, err)
	}
	r.fake.SetMissingParentOutcome(MissingParentRefused)
	if _, err := r.fake.Start(r.ctx, s(r)); !errors.Is(err, ErrParentNotLive) || !errors.Is(err, ErrReplyLost) {
		t.Fatalf("refused outcome: %v", err)
	}

	r, _ = setup(t)
	r.fake.SetBoundStop(BoundStopOnUnload)
	if st, err := r.fake.Start(r.ctx, s(r)); err != nil || !st.Loaded || st.Active != ActiveActive {
		t.Fatalf("the earlier timing accepts the start: %+v %v", st, err)
	}
	if len(r.implied()) != 0 {
		t.Fatal("stopped before the parent unloaded")
	}
	r.fake.Crash(unitP)
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 0})
}

// Under BoundStopOnUnload a deactivating parent keeps its bound unit running.
func TestFakeBoundUnitKeepsRunningUnderADeactivatingParentOnUnload(t *testing.T) {
	r := newRig(t)
	r.fake.SetBoundStop(BoundStopOnUnload)
	r.fake.SetProcs(unitP, ProcScript{}, ProcScript{Behavior: IgnoreTerm})
	parent := r.start(unitP, 1)
	r.startBound(unitC, unitP)
	r.signal(parent, syscall.SIGTERM)
	if got := r.inspect(unitC); !got.Loaded || got.Active != ActiveActive || len(r.implied()) != 0 {
		t.Fatalf("%+v %v", got, r.implied())
	}
	// Back to the default: the next look does not stop it retroactively, but
	// the next change of the parent does.
	r.fake.SetBoundStop(BoundStopOnDeactivating)
	if got := r.inspect(unitC); !got.Loaded || got.Active != ActiveActive || len(r.implied()) != 0 {
		t.Fatalf("a look stopped the unit retroactively: %+v %v", got, r.implied())
	}
	r.signal(parent, syscall.SIGKILL)
	if r.fake.Loaded(unitC) || r.fake.Loaded(unitP) {
		t.Fatal("the parent was killed and the child did not follow")
	}
	r.wantImplied(impliedWant{unitC, syscall.SIGTERM, 0})
}
