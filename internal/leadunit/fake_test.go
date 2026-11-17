package leadunit

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
)

const unitA = "ataniq-lead-a.service"

type rig struct {
	t     *testing.T
	clock *FakeClock
	fake  *Fake
	ctx   context.Context
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clock := NewFakeClock(epoch0)
	return &rig{t: t, clock: clock, fake: NewFake(clock), ctx: context.Background()}
}

func (r *rig) spec(unit string, epoch uint64) Spec {
	s := validSpec()
	s.Unit = unit
	s.Epoch = epoch
	return s
}

func (r *rig) start(unit string, epoch uint64) State {
	r.t.Helper()
	st, err := r.fake.Start(r.ctx, r.spec(unit, epoch))
	if err != nil {
		r.t.Fatalf("Start: %v", err)
	}
	return st
}

func (r *rig) inspect(unit string) State {
	r.t.Helper()
	st, err := r.fake.Inspect(r.ctx, unit)
	if err != nil {
		r.t.Fatalf("Inspect: %v", err)
	}
	return st
}

func (r *rig) empty(st State) bool {
	r.t.Helper()
	e, err := r.fake.Empty(r.ctx, st)
	if err != nil {
		r.t.Fatalf("Empty: %v", err)
	}
	return e
}

func (r *rig) signal(st State, sig syscall.Signal) {
	r.t.Helper()
	if err := r.fake.Signal(r.ctx, st, sig); err != nil {
		r.t.Fatalf("Signal %v: %v", sig, err)
	}
}

func TestFakeStartAndInspect(t *testing.T) {
	r := newRig(t)
	st := r.start(unitA, 4)
	if !st.Loaded || !st.Live() || st.Active != ActiveActive {
		t.Fatalf("state %+v", st)
	}
	if st.Unit != unitA || st.InvocationID == "" || st.ControlGroup == "" || st.ControlGroupID == "" || st.MainPID == 0 || st.BootID == "" {
		t.Fatalf("state misses fields: %+v", st)
	}
	if !st.Ours || st.Epoch != 4 || st.Intent != "intent-0001" || st.Description != "ataniq lead epoch=4 intent=intent-0001" {
		t.Fatalf("description fields: %+v", st)
	}
	if st.BootID != r.fake.BootID() {
		t.Fatal("boot identity differs from the fake's")
	}
	if got := r.inspect(unitA); !reflect.DeepEqual(got, st) {
		t.Fatalf("Inspect %+v differs from Start %+v", got, st)
	}
	specs := r.fake.StartedSpecs()
	if len(specs) != 1 || !reflect.DeepEqual(specs[0], r.spec(unitA, 4)) {
		t.Fatalf("started specs %+v", specs)
	}
	specs[0].Argv[0] = "/changed"
	if r.fake.StartedSpecs()[0].Argv[0] == "/changed" {
		t.Fatal("StartedSpecs shares memory")
	}
}

func TestFakeInspectNotLoaded(t *testing.T) {
	r := newRig(t)
	st := r.inspect(unitA)
	if st.Loaded || st.Live() || st.Unit != unitA || st.InvocationID != "" || st.ControlGroup != "" || st.BootID == "" || st.Ours {
		t.Fatalf("state %+v", st)
	}
	if _, err := r.fake.Inspect(r.ctx, "bad name"); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("invalid name: %v", err)
	}
}

func TestFakeStartInvalidSpec(t *testing.T) {
	r := newRig(t)
	s := r.spec(unitA, 0)
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("got %v", err)
	}
	if r.fake.Loaded(unitA) || len(r.fake.StartedSpecs()) != 0 {
		t.Fatal("invalid spec started a unit")
	}
	if c := r.fake.CallsOf(MethodStart); len(c) != 1 || !errors.Is(c[0].Err, ErrInvalidSpec) {
		t.Fatalf("log %+v", c)
	}
}

func TestFakeSecondStartIsUnitExists(t *testing.T) {
	r := newRig(t)
	first := r.start(unitA, 1)
	_, err := r.fake.Start(r.ctx, r.spec(unitA, 2))
	if !errors.Is(err, ErrUnitExists) {
		t.Fatalf("got %v", err)
	}
	if got := r.inspect(unitA); got.InvocationID != first.InvocationID || got.Epoch != 1 {
		t.Fatalf("the first unit changed: %+v", got)
	}
	if n := len(r.fake.StartedSpecs()); n != 1 {
		t.Fatalf("%d units started", n)
	}
	// A different name is a different unit.
	r.start("ataniq-lead-b.service", 1)
}

func TestFakeLostReplyAfterStart(t *testing.T) {
	r := newRig(t)
	r.fake.LoseNextStartReply(unitA)
	st, err := r.fake.Start(r.ctx, r.spec(unitA, 1))
	if !errors.Is(err, ErrReplyLost) || !reflect.DeepEqual(st, State{}) {
		t.Fatalf("got %+v, %v", st, err)
	}
	got := r.inspect(unitA)
	if !got.Live() || !got.Ours || got.Epoch != 1 {
		t.Fatalf("the unit is not running: %+v", got)
	}
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 1)); !errors.Is(err, ErrUnitExists) {
		t.Fatalf("second start: %v", err)
	}
	if c := r.fake.CallsOf(MethodStart); c[0].Invocation != got.InvocationID {
		t.Fatalf("the lost start's log entry lacks the invocation: %+v", c[0])
	}
}

func TestFakeLostReplyWithoutStart(t *testing.T) {
	r := newRig(t)
	r.fake.DropNextStart(unitA)
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 1)); !errors.Is(err, ErrReplyLost) {
		t.Fatalf("got %v", err)
	}
	if got := r.inspect(unitA); got.Loaded {
		t.Fatalf("a unit was started: %+v", got)
	}
	if len(r.fake.StartedSpecs()) != 0 {
		t.Fatal("spec recorded for a start that did not happen")
	}
	// The script was used up.
	r.start(unitA, 1)
}

func TestFakeFailedStart(t *testing.T) {
	r := newRig(t)
	boom := errors.New("boom")
	r.fake.FailNextStart(unitA, boom)
	r.fake.FailNextStart(unitA, nil)
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 1)); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if r.fake.Loaded(unitA) {
		t.Fatal("a failed start left a unit")
	}
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 1)); err == nil || errors.Is(err, boom) {
		t.Fatalf("second scripted failure: %v", err)
	}
	r.start(unitA, 1)
}

func TestFakeStartScriptsAreOrderedAndPerUnit(t *testing.T) {
	r := newRig(t)
	r.fake.DropNextStart(unitA)
	r.fake.LoseNextStartReply(unitA)
	other := "ataniq-lead-b.service"
	r.start(other, 1) // not affected by unitA's script
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 1)); !errors.Is(err, ErrReplyLost) || r.fake.Loaded(unitA) {
		t.Fatalf("first: %v", err)
	}
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 1)); !errors.Is(err, ErrReplyLost) || !r.fake.Loaded(unitA) {
		t.Fatalf("second: %v", err)
	}
}

func TestFakeScriptNotUsedByRefusedStart(t *testing.T) {
	r := newRig(t)
	r.start(unitA, 1)
	r.fake.DropNextStart(unitA)
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 2)); !errors.Is(err, ErrUnitExists) {
		t.Fatalf("got %v", err)
	}
	st := r.inspect(unitA)
	r.signal(st, syscall.SIGTERM)
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 2)); !errors.Is(err, ErrReplyLost) {
		t.Fatalf("script lost: %v", err)
	}
}

func TestFakeSigtermExitsAtOnce(t *testing.T) {
	r := newRig(t)
	st := r.start(unitA, 1)
	if r.empty(st) {
		t.Fatal("Empty true for a running unit")
	}
	r.signal(st, syscall.SIGTERM)
	if got := r.inspect(unitA); got.Loaded {
		t.Fatalf("unit still loaded: %+v", got)
	}
	if !r.empty(st) {
		t.Fatal("Empty false after the unit unloaded")
	}
	// CollectMode: the name is free again, with a new invocation.
	again := r.start(unitA, 2)
	if again.InvocationID == st.InvocationID || again.ControlGroupID == st.ControlGroupID {
		t.Fatalf("invocation or control group reused: %+v %+v", st, again)
	}
}

func TestFakeSigtermAfterDelay(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: ExitOnTermAfter, Delay: 1500 * time.Millisecond})
	st := r.start(unitA, 1)
	r.signal(st, syscall.SIGTERM)
	if r.empty(st) {
		t.Fatal("Empty true before the delay")
	}
	r.clock.Advance(time.Second)
	// A second SIGTERM does not push the exit back.
	r.signal(st, syscall.SIGTERM)
	if r.empty(st) || !r.inspect(unitA).Live() {
		t.Fatal("exited early")
	}
	r.clock.Advance(500 * time.Millisecond)
	if !r.empty(st) || r.inspect(unitA).Loaded {
		t.Fatal("did not exit after the delay")
	}
}

func TestFakeDelayedExitCutShortByKill(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: ExitOnTermAfter, Delay: time.Hour})
	st := r.start(unitA, 1)
	r.signal(st, syscall.SIGTERM)
	r.signal(st, syscall.SIGKILL)
	if !r.empty(st) {
		t.Fatal("SIGKILL did not end the process")
	}
}

func TestFakeSigtermIgnored(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: IgnoreTerm})
	st := r.start(unitA, 1)
	r.signal(st, syscall.SIGTERM)
	r.clock.Advance(time.Hour)
	if r.empty(st) || !r.inspect(unitA).Live() {
		t.Fatal("process exited on SIGTERM")
	}
	r.signal(st, syscall.SIGHUP)
	if r.empty(st) {
		t.Fatal("process exited on SIGHUP")
	}
	r.signal(st, syscall.SIGKILL)
	if !r.empty(st) {
		t.Fatal("process survived SIGKILL")
	}
}

func TestFakeSurvivesKill(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: ExitOnTerm}, ProcScript{Behavior: SurviveKill})
	st := r.start(unitA, 1)
	r.signal(st, syscall.SIGTERM)
	r.signal(st, syscall.SIGKILL)
	r.clock.Advance(time.Hour)
	if r.empty(st) {
		t.Fatal("control group emptied")
	}
	got := r.inspect(unitA)
	if !got.Live() || got.Active != ActiveDeactivating || got.MainPID != 0 {
		t.Fatalf("main process is gone, others remain: %+v", got)
	}
	if got.InvocationID != st.InvocationID {
		t.Fatal("invocation changed")
	}
	if !r.fake.Crash(unitA) || !r.empty(st) {
		t.Fatal("Crash did not clear the immortal process")
	}
}

func TestFakeProcessesAndMainPID(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{}, ProcScript{Behavior: IgnoreTerm})
	st := r.start(unitA, 1)
	pids := r.fake.Processes(unitA)
	if len(pids) != 2 || st.MainPID != pids[0] {
		t.Fatalf("pids %v main %d", pids, st.MainPID)
	}
	extra, ok := r.fake.AddProcess(unitA, ProcScript{Behavior: SurviveKill})
	if !ok || len(r.fake.Processes(unitA)) != 3 {
		t.Fatal("AddProcess")
	}
	if _, ok := r.fake.AddProcess("ataniq-lead-none.service", ProcScript{}); ok {
		t.Fatal("AddProcess on a missing unit")
	}
	if !r.fake.KillProcess(unitA, st.MainPID) || r.fake.KillProcess(unitA, st.MainPID) {
		t.Fatal("KillProcess")
	}
	if got := r.inspect(unitA); got.Active != ActiveDeactivating {
		t.Fatalf("%+v", got)
	}
	for _, p := range r.fake.Processes(unitA) {
		r.fake.KillProcess(unitA, p)
	}
	if r.fake.Loaded(unitA) || r.fake.KillProcess(unitA, extra) || len(r.fake.Processes(unitA)) != 0 {
		t.Fatal("the unit stayed loaded with no process")
	}
	r.fake.SetProcs(unitA)
	if got := r.start(unitA, 1); len(r.fake.Processes(unitA)) != 1 || got.MainPID == 0 {
		t.Fatal("SetProcs with no argument did not restore the default")
	}
}

func TestFakeUnavailable(t *testing.T) {
	r := newRig(t)
	st := r.start(unitA, 1)
	r.fake.MakeUnavailable(MethodInspect, 2)
	r.fake.MakeUnavailable(MethodEmpty, 1)
	for i := 0; i < 2; i++ {
		if got, err := r.fake.Inspect(r.ctx, unitA); !errors.Is(err, ErrUnavailable) || got.Loaded {
			t.Fatalf("Inspect %d: %+v %v", i, got, err)
		}
	}
	if got := r.inspect(unitA); !got.Live() {
		t.Fatal("Inspect after the scripted failures")
	}
	if e, err := r.fake.Empty(r.ctx, st); e || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Empty: %v %v", e, err)
	}
	r.signal(st, syscall.SIGTERM)
	if !r.empty(st) {
		t.Fatal("Empty after the scripted failure")
	}
	// Unavailability has no effect on the unit and covers Start and Signal too.
	r.fake.MakeUnavailable(MethodStart, 1)
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 2)); !errors.Is(err, ErrUnavailable) || r.fake.Loaded(unitA) {
		t.Fatalf("Start: %v", err)
	}
	st = r.start(unitA, 2)
	r.fake.MakeUnavailable(MethodSignal, 1)
	if err := r.fake.Signal(r.ctx, st, syscall.SIGTERM); !errors.Is(err, ErrUnavailable) || !r.fake.Loaded(unitA) {
		t.Fatalf("Signal: %v", err)
	}
	r.fake.MakeUnavailable(MethodSignal, 5)
	r.fake.MakeUnavailable(MethodSignal, 0)
	r.signal(st, syscall.SIGTERM)
}

func TestFakeCrash(t *testing.T) {
	r := newRig(t)
	if r.fake.Crash(unitA) {
		t.Fatal("Crash reported a unit that is not there")
	}
	r.fake.SetProcs(unitA, ProcScript{Behavior: SurviveKill}, ProcScript{Behavior: IgnoreTerm})
	st := r.start(unitA, 1)
	if !r.fake.Crash(unitA) {
		t.Fatal("Crash")
	}
	if got := r.inspect(unitA); got.Loaded {
		t.Fatalf("unit survived the crash: %+v", got)
	}
	if !r.empty(st) {
		t.Fatal("Empty false after the crash")
	}
	if err := r.fake.Signal(r.ctx, st, syscall.SIGTERM); !errors.Is(err, ErrNotLoaded) {
		t.Fatalf("Signal: %v", err)
	}
	r.start(unitA, 2)
}

func TestFakeReboot(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: SurviveKill})
	old := r.start(unitA, 1)
	if err := r.fake.AddForeign("ataniq-lead-f.service", "foreign"); err != nil {
		t.Fatal(err)
	}
	boot := r.fake.Reboot()
	if boot == old.BootID || boot != r.fake.BootID() {
		t.Fatalf("boot identity %q, was %q", boot, old.BootID)
	}
	got := r.inspect(unitA)
	if got.Loaded || got.BootID != boot {
		t.Fatalf("after reboot: %+v", got)
	}
	if r.fake.Loaded("ataniq-lead-f.service") {
		t.Fatal("foreign unit survived the reboot")
	}
	if !r.empty(old) {
		t.Fatal("Empty false for a unit of a previous boot")
	}
	if err := r.fake.Signal(r.ctx, old, syscall.SIGKILL); !errors.Is(err, ErrNotLoaded) {
		t.Fatalf("Signal: %v", err)
	}
	if again := r.start(unitA, 2); again.BootID != boot || again.InvocationID == old.InvocationID {
		t.Fatalf("restart after reboot: %+v", again)
	}
}

func TestFakeForeignUnit(t *testing.T) {
	r := newRig(t)
	if err := r.fake.AddForeign("not a unit", "x"); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("got %v", err)
	}
	if err := r.fake.AddForeign(unitA, "ataniq lead epoch=0 intent=x", ProcScript{Behavior: IgnoreTerm}); err != nil {
		t.Fatal(err)
	}
	if err := r.fake.AddForeign(unitA, "x"); !errors.Is(err, ErrUnitExists) {
		t.Fatalf("second foreign: %v", err)
	}
	st := r.inspect(unitA)
	if !st.Loaded || !st.Live() || st.Ours || st.Epoch != 0 || st.Intent != "" || st.Description != "ataniq lead epoch=0 intent=x" {
		t.Fatalf("foreign state %+v", st)
	}
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 1)); !errors.Is(err, ErrUnitExists) {
		t.Fatalf("Start over a foreign unit: %v", err)
	}
	if r.empty(st) {
		t.Fatal("Empty true for a foreign unit that runs")
	}
	r.signal(st, syscall.SIGKILL)
	if !r.empty(st) {
		t.Fatal("foreign unit survived SIGKILL")
	}
	// A foreign unit with a description that looks ours is ours by the rule.
	if err := r.fake.AddForeign(unitA, "ataniq lead epoch=9 intent=zz"); err != nil {
		t.Fatal(err)
	}
	if st := r.inspect(unitA); !st.Ours || st.Epoch != 9 || st.Intent != "zz" {
		t.Fatalf("%+v", st)
	}
}

func TestFakeSignalRefusals(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: IgnoreTerm})
	first := r.start(unitA, 1)
	r.signal(first, syscall.SIGKILL)
	if err := r.fake.Signal(r.ctx, first, syscall.SIGTERM); !errors.Is(err, ErrNotLoaded) {
		t.Fatalf("unit not loaded: %v", err)
	}
	second := r.start(unitA, 2)
	if err := r.fake.Signal(r.ctx, first, syscall.SIGKILL); !errors.Is(err, ErrInvocationChanged) {
		t.Fatalf("stale invocation: %v", err)
	}
	if !r.inspect(unitA).Live() {
		t.Fatal("a refused signal reached the new invocation")
	}
	noInv := second
	noInv.InvocationID = ""
	if err := r.fake.Signal(r.ctx, noInv, syscall.SIGKILL); !errors.Is(err, ErrInvocationChanged) {
		t.Fatalf("state without invocation: %v", err)
	}
	if !r.inspect(unitA).Live() {
		t.Fatal("a refused signal reached the unit")
	}
	if err := r.fake.Signal(r.ctx, State{Unit: "bad"}, syscall.SIGTERM); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("bad unit: %v", err)
	}
	r.signal(second, syscall.SIGKILL)
}

func TestFakeSignalReachesEveryProcess(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{}, ProcScript{}, ProcScript{Behavior: ExitOnTermAfter, Delay: time.Second})
	st := r.start(unitA, 1)
	r.signal(st, syscall.SIGTERM)
	if n := len(r.fake.Processes(unitA)); n != 1 {
		t.Fatalf("%d processes left", n)
	}
	r.clock.Advance(time.Second)
	if !r.empty(st) {
		t.Fatal("not empty")
	}
}

func TestFakeEmpty(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: IgnoreTerm})
	if e, err := r.fake.Empty(r.ctx, State{Unit: unitA}); !e || err != nil {
		t.Fatalf("not loaded: %v %v", e, err)
	}
	if e, err := r.fake.Empty(r.ctx, State{Unit: "bad"}); e || !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("bad unit: %v %v", e, err)
	}
	st := r.start(unitA, 1)
	if e, err := r.fake.Empty(r.ctx, st); e || err != nil {
		t.Fatalf("populated: %v %v", e, err)
	}
	noInv := st
	noInv.InvocationID = ""
	if e, err := r.fake.Empty(r.ctx, noInv); e || !errors.Is(err, ErrInvocationChanged) {
		t.Fatalf("no invocation on a loaded unit: %v %v", e, err)
	}
	wrongCG := st
	wrongCG.ControlGroupID = "cg-other"
	if e, err := r.fake.Empty(r.ctx, wrongCG); e || !errors.Is(err, ErrControlGroupMismatch) {
		t.Fatalf("control group identity: %v %v", e, err)
	}
	wrongPath := st
	wrongPath.ControlGroup = "/elsewhere"
	if e, err := r.fake.Empty(r.ctx, wrongPath); e || !errors.Is(err, ErrControlGroupMismatch) {
		t.Fatalf("control group path: %v %v", e, err)
	}
	// Another invocation is running: the old control group is gone.
	r.signal(st, syscall.SIGKILL)
	r.start(unitA, 2)
	if e, err := r.fake.Empty(r.ctx, st); !e || err != nil {
		t.Fatalf("other invocation: %v %v", e, err)
	}
}

func TestFakeDoneContext(t *testing.T) {
	r := newRig(t)
	st := r.start(unitA, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.fake.MakeUnavailable(MethodInspect, 1)
	if _, err := r.fake.Start(ctx, r.spec("ataniq-lead-b.service", 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start: %v", err)
	}
	if _, err := r.fake.Inspect(ctx, unitA); !errors.Is(err, context.Canceled) {
		t.Fatalf("Inspect: %v", err)
	}
	if err := r.fake.Signal(ctx, st, syscall.SIGKILL); !errors.Is(err, context.Canceled) {
		t.Fatalf("Signal: %v", err)
	}
	if e, err := r.fake.Empty(ctx, st); e || !errors.Is(err, context.Canceled) {
		t.Fatalf("Empty: %v %v", e, err)
	}
	if !r.fake.Loaded(unitA) || r.fake.Loaded("ataniq-lead-b.service") {
		t.Fatal("a call with a done context had an effect")
	}
	// The scripted unavailability was not consumed by the refused Inspect.
	if _, err := r.fake.Inspect(r.ctx, unitA); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("script consumed: %v", err)
	}
}

func TestFakeCallLog(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: IgnoreTerm})
	if len(r.fake.Calls()) != 0 {
		t.Fatal("log not empty")
	}
	st := r.start(unitA, 1)
	r.clock.Advance(time.Second)
	r.signal(st, syscall.SIGTERM)
	r.clock.Advance(2 * time.Second)
	r.signal(st, syscall.SIGKILL)
	e := r.empty(st)
	r.inspect(unitA)
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 3)); !errors.Is(err, ErrUnitExists) {
		t.Fatal(err)
	}
	if !e {
		t.Fatal("empty")
	}

	calls := r.fake.Calls()
	type row struct {
		m   Method
		sig syscall.Signal
		at  time.Duration
		err error
		e   bool
	}
	want := []row{
		{MethodStart, 0, 0, nil, false},
		{MethodSignal, syscall.SIGTERM, time.Second, nil, false},
		{MethodSignal, syscall.SIGKILL, 3 * time.Second, nil, false},
		{MethodEmpty, 0, 3 * time.Second, nil, true},
		{MethodInspect, 0, 3 * time.Second, nil, false},
		{MethodStart, 0, 3 * time.Second, nil, false},
		{MethodStart, 0, 3 * time.Second, ErrUnitExists, false},
	}
	if len(calls) != len(want) {
		t.Fatalf("%d calls: %+v", len(calls), calls)
	}
	for i, c := range calls {
		w := want[i]
		if c.Seq != i+1 || c.Method != w.m || c.Signal != w.sig || !c.At.Equal(epoch0.Add(w.at)) || !errors.Is(c.Err, w.err) || c.Empty != w.e || c.Unit != unitA {
			t.Errorf("call %d: %+v, want %+v", i, c, w)
		}
	}
	if calls[1].Invocation != st.InvocationID || calls[3].Invocation != st.InvocationID || calls[4].Invocation != "" {
		t.Errorf("invocations in the log: %+v", calls)
	}
	if calls[0].Invocation != st.InvocationID {
		t.Errorf("start entry: %+v", calls[0])
	}
	if n := len(r.fake.CallsOf(MethodStart)); n != 3 {
		t.Errorf("%d start entries", n)
	}
	// The log is a copy.
	calls[0].Unit = "changed"
	if r.fake.Calls()[0].Unit != unitA {
		t.Fatal("Calls shares memory")
	}
}

// TestFakeCallLogOrdering shows the assertions the log is for: a replacement
// starts only after Empty returned true, and exactly one Start succeeds.
func TestFakeCallLogOrdering(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: ExitOnTermAfter, Delay: 2 * time.Second})
	old := r.start(unitA, 1)
	r.signal(old, syscall.SIGTERM)
	for !r.empty(old) {
		r.clock.Advance(time.Second)
	}
	r.start(unitA, 2)

	emptyTrue, secondStart, starts := -1, -1, 0
	for i, c := range r.fake.Calls() {
		switch {
		case c.Method == MethodEmpty && c.Empty && emptyTrue < 0:
			emptyTrue = i
		case c.Method == MethodStart && c.Err == nil:
			starts++
			if starts == 2 {
				secondStart = i
			}
		}
	}
	if starts != 2 || emptyTrue < 0 || emptyTrue > secondStart {
		t.Fatalf("starts %d, first Empty true at %d, second Start at %d", starts, emptyTrue, secondStart)
	}
	for _, c := range r.fake.CallsOf(MethodEmpty)[:len(r.fake.CallsOf(MethodEmpty))-1] {
		if c.Empty {
			t.Fatal("an Empty before the last returned true")
		}
	}
}

func TestFakeConcurrentStartsOneWins(t *testing.T) {
	r := newRig(t)
	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.fake.Start(r.ctx, r.spec(unitA, uint64(i+1)))
		}()
	}
	wg.Wait()
	ok, exists := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrUnitExists):
			exists++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || exists != n-1 || len(r.fake.StartedSpecs()) != 1 {
		t.Fatalf("%d started, %d refused", ok, exists)
	}
}

func TestFakeConcurrentUse(t *testing.T) {
	r := newRig(t)
	r.fake.SetProcs(unitA, ProcScript{Behavior: ExitOnTermAfter, Delay: time.Second})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				st, err := r.fake.Start(r.ctx, r.spec(unitA, uint64(g*1000+i+1)))
				if err != nil {
					st, err = r.fake.Inspect(r.ctx, unitA)
					if errors.Is(err, ErrUnavailable) {
						continue
					}
					if err != nil {
						t.Error(err)
						return
					}
				}
				if st.Loaded {
					err = r.fake.Signal(r.ctx, st, syscall.SIGTERM)
					if err != nil && !errors.Is(err, ErrNotLoaded) && !errors.Is(err, ErrInvocationChanged) {
						t.Error(err)
						return
					}
				}
				if _, err := r.fake.Empty(r.ctx, st); err != nil && !errors.Is(err, ErrInvocationChanged) {
					t.Error(err)
					return
				}
				r.clock.Advance(300 * time.Millisecond)
				r.fake.Processes(unitA)
				if i%25 == 0 {
					r.fake.Calls()
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			r.fake.MakeUnavailable(MethodInspect, 1)
			r.fake.Crash(unitA)
			r.fake.Reboot()
			_ = r.fake.AddForeign("ataniq-lead-f.service", "x")
			r.fake.AddProcess(unitA, ProcScript{})
		}
	}()
	wg.Wait()
	for i, c := range r.fake.Calls() {
		if c.Seq != i+1 {
			t.Fatalf("call %d has Seq %d", i, c.Seq)
		}
	}
}

// TestFakeExpiredStartIsReplyLost: a deadline that passes after the request was
// sent starts the unit and returns an error that is ErrReplyLost and also the
// context error, never a bare context error.
func TestFakeExpiredStartIsReplyLost(t *testing.T) {
	r := newRig(t)
	r.fake.ExpireNextStart(unitA)
	st, err := r.fake.Start(r.ctx, r.spec(unitA, 1))
	if !errors.Is(err, ErrReplyLost) || !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(st, State{}) {
		t.Fatalf("got %+v, %v", st, err)
	}
	got := r.inspect(unitA)
	if !got.Live() || !got.Ours || got.Epoch != 1 {
		t.Fatalf("the unit is not running: %+v", got)
	}
	if n := len(r.fake.StartedSpecs()); n != 1 {
		t.Fatalf("%d specs", n)
	}
	if c := r.fake.CallsOf(MethodStart); len(c) != 1 || c[0].Invocation != got.InvocationID || !errors.Is(c[0].Err, ErrReplyLost) {
		t.Fatalf("log: %+v", c)
	}
	// The caller must not start again for that intent.
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 1)); !errors.Is(err, ErrUnitExists) {
		t.Fatalf("second start: %v", err)
	}
}

// TestFakeContextEndsDuringStartDelay: a context that ends while the reply of a
// started unit is delayed loses the reply and not the unit.
func TestFakeContextEndsDuringStartDelay(t *testing.T) {
	r := newRig(t)
	r.fake.DelayNextStart(unitA, 5*time.Second)
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	type result struct {
		st  State
		err error
	}
	done := make(chan result, 1)
	go func() {
		st, err := r.fake.Start(ctx, r.spec(unitA, 1))
		done <- result{st, err}
	}()
	bctx, bcancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer bcancel()
	if err := r.clock.BlockUntil(bctx, 1); err != nil {
		t.Fatal(err)
	}
	// The unit is running and other calls are served during the delay.
	if got := r.inspect(unitA); !got.Live() || got.Epoch != 1 {
		t.Fatalf("during the delay: %+v", got)
	}
	select {
	case res := <-done:
		t.Fatalf("Start returned early: %+v", res)
	default:
	}
	cancel()
	res := <-done
	if !errors.Is(res.err, ErrReplyLost) || !errors.Is(res.err, context.Canceled) || !reflect.DeepEqual(res.st, State{}) {
		t.Fatalf("got %+v, %v", res.st, res.err)
	}
	if !r.fake.Loaded(unitA) {
		t.Fatal("the unit is gone")
	}
	// The log has the Start before the Inspect that saw its unit, with the
	// error the call returned.
	calls := r.fake.Calls()
	if len(calls) != 2 || calls[0].Method != MethodStart || calls[1].Method != MethodInspect || !errors.Is(calls[0].Err, ErrReplyLost) || calls[0].Invocation == "" {
		t.Fatalf("log: %+v", calls)
	}
	if r.clock.Waiters() != 0 {
		t.Fatalf("waiters: %d", r.clock.Waiters())
	}
}

// TestFakeStartDelayWithinContext: without a context that ends, the delayed
// reply is the ordinary one.
func TestFakeStartDelayWithinContext(t *testing.T) {
	r := newRig(t)
	r.fake.DelayNextStart(unitA, 5*time.Second)
	type result struct {
		st  State
		err error
	}
	done := make(chan result, 1)
	go func() {
		st, err := r.fake.Start(r.ctx, r.spec(unitA, 1))
		done <- result{st, err}
	}()
	bctx, bcancel := context.WithTimeout(r.ctx, 10*time.Second)
	defer bcancel()
	if err := r.clock.BlockUntil(bctx, 1); err != nil {
		t.Fatal(err)
	}
	r.clock.Advance(5 * time.Second)
	res := <-done
	if res.err != nil || !res.st.Live() || res.st != r.inspect(unitA) {
		t.Fatalf("got %+v, %v", res.st, res.err)
	}
	// A delay of 0 is no delay, and a failed start is not delayed.
	r.fake.DelayNextStart("ataniq-lead-b.service", 0)
	r.start("ataniq-lead-b.service", 1)
	r.fake.DelayNextStart(unitA, time.Hour)
	if _, err := r.fake.Start(r.ctx, r.spec(unitA, 2)); !errors.Is(err, ErrUnitExists) {
		t.Fatalf("got %v", err)
	}
}
