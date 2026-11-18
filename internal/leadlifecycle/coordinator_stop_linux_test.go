package leadlifecycle

import (
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// ownSignals returns the signals the controller sent, without the stops the
// fake applies to bound units by itself.
func (r *rig) ownSignals() []leadunit.Call {
	var out []leadunit.Call
	for _, c := range r.signalCalls() {
		if !c.Implied {
			out = append(out, c)
		}
	}
	return out
}

// twoCoordinators is a running lead and the running coordinators of two
// features, with the processes of each scripted.
func twoCoordinators(t *testing.T, a, b leadunit.ProcScript) (r *rig, lead, ca, cb leadrole.Role) {
	t.Helper()
	r = newRig(t)
	lead = r.define("alpha")
	ca = r.defineCoord("alpha", "aaa")
	cb = r.defineCoord("alpha", "bbb")
	r.fake.SetProcs(unitOf(ca), a)
	r.fake.SetProcs(unitOf(cb), b)
	r.mustStart(lead)
	r.mustStart(ca)
	r.mustStart(cb)
	return r, lead, ca, cb
}

func TestStopLeadStopsItsCoordinatorsFirst(t *testing.T) {
	r, lead, ca, cb := twoCoordinators(t, leadunit.ProcScript{}, leadunit.ProcScript{})
	e, err := r.stop(lead)
	noErr(t, err)
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
	for _, co := range []leadrole.Role{ca, cb} {
		ce := r.expectState(co, leadregistry.StateStopped, "stopped")
		if ce.Desired != leadregistry.DesiredStopped {
			t.Fatalf("%s: desired %s", co, ce.Desired)
		}
	}
	// Each was signalled by the controller, before the lead.
	sig := r.ownSignals()
	want := []string{unitOf(ca), unitOf(cb), unitOf(lead)}
	if len(sig) != len(want) {
		t.Fatalf("signals %+v", sig)
	}
	for i, c := range sig {
		if c.Unit != want[i] || c.Signal != syscall.SIGTERM {
			t.Fatalf("signal %d: %+v, want TERM to %s", i, c, want[i])
		}
	}
	// A coordinator already proven stopped is not signalled again.
	n := len(r.signalCalls())
	_, err = r.stop(lead)
	noErr(t, err)
	if len(r.signalCalls()) != n {
		t.Fatal("a second stop signalled")
	}
	r.auditClean()
}

func TestStopLeadWithATermIgnoringAndAnUnkillableCoordinator(t *testing.T) {
	r, lead, ca, cb := twoCoordinators(t,
		leadunit.ProcScript{Behavior: leadunit.IgnoreTerm},
		leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	begin := r.clock.Now()
	e, err := r.stop(lead)
	isErr(t, err, ErrCoordinatorNotStopped)
	isErr(t, err, ErrStopUnproven)
	var ce *CoordinatorsError
	if !errors.As(err, &ce) || len(ce.Roles) != 1 || ce.Roles[0] != cb {
		t.Fatalf("error %v", err)
	}
	// The lead is stopped all the same.
	if e.Shown.State != leadregistry.StateStopped || r.fake.Loaded(unitOf(lead)) {
		t.Fatalf("lead %+v", e)
	}
	// The TERM-ignoring coordinator was killed at 2 s and is stopped.
	r.expectState(ca, leadregistry.StateStopped, "stopped")
	var aSig []leadunit.Call
	for _, c := range r.ownSignals() {
		if c.Unit == unitOf(ca) {
			aSig = append(aSig, c)
		}
	}
	if len(aSig) != 2 || aSig[0].Signal != syscall.SIGTERM || aSig[1].Signal != syscall.SIGKILL ||
		!aSig[0].At.Equal(begin) || !aSig[1].At.Equal(begin.Add(TermWait)) {
		t.Fatalf("signals to the first coordinator %+v (begun at %v)", aSig, begin)
	}
	// The other one cannot be proven stopped: unknown, with its group still
	// there.
	cbe := r.expectState(cb, leadregistry.StateUnknown, "stop-unproven")
	if !r.fake.Loaded(unitOf(cb)) {
		t.Fatal("the unkillable unit is gone")
	}
	if ok, err := r.reg.CoordinatorStopped(cb, cbe.Epoch); err != nil || ok {
		t.Fatalf("CoordinatorStopped %v %v", ok, err)
	}
	// A later Start of the lead is refused, changing nothing.
	rev := r.revision()
	_, err = r.start(lead)
	isErr(t, err, ErrCoordinatorNotStopped)
	isErr(t, err, leadregistry.ErrLaunchCoordinator)
	if r.revision() != rev {
		t.Fatal("the refused start changed the registry")
	}
	// The owner resolves the coordinator once its group is gone, and the lead
	// starts again with a new epoch.
	r.fake.Crash(unitOf(cb))
	_, err = r.reg.ResolveUnknown(cb, leadregistry.OwnerDecision{Epoch: cbe.Epoch, State: leadregistry.StateStopped,
		Reason: leadregistry.Reason{Code: "owner"}, Decision: "the group was cleaned up by hand"})
	noErr(t, err)
	if e := r.mustStart(lead); e.Epoch != 2 {
		t.Fatalf("lead epoch %d", e.Epoch)
	}
	r.auditClean()
}

func TestStopCoordinatorStopsOnlyThatCoordinator(t *testing.T) {
	r, lead, ca, cb := twoCoordinators(t, leadunit.ProcScript{}, leadunit.ProcScript{})
	e, err := r.stop(ca)
	noErr(t, err)
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("entry %+v", e)
	}
	sig := r.ownSignals()
	if len(sig) != 1 || sig[0].Unit != unitOf(ca) {
		t.Fatalf("signals %+v", sig)
	}
	if !r.fake.Loaded(unitOf(lead)) || !r.fake.Loaded(unitOf(cb)) {
		t.Fatal("another unit was stopped")
	}
	if e := r.get(lead); e.Shown.State != leadregistry.StateStarting || e.Desired != leadregistry.DesiredRunning {
		t.Fatalf("lead %+v", e)
	}
	if e := r.get(cb); e.Shown.State != leadregistry.StateStarting {
		t.Fatalf("other coordinator %+v", e)
	}
	// The coordinator can start again: instance 2 under the same lead epoch.
	if e := r.mustStart(ca); e.Epoch != 2 || e.Intent.Identity != "alpha-lead@e1:coord:aaa:2" {
		t.Fatalf("entry %+v", e)
	}
}

func TestStopCoordinatorThatIsAlreadyStopping(t *testing.T) {
	t.Run("the unit is stopping with its lead", func(t *testing.T) {
		r, lead, ca, _ := twoCoordinators(t, leadunit.ProcScript{Behavior: leadunit.ExitOnTermAfter, Delay: time.Second}, leadunit.ProcScript{})
		// The lead's unit goes: the bound coordinator is told to stop by the
		// manager and is deactivating when the daemon looks.
		r.fake.Crash(unitOf(lead))
		e, err := r.stop(ca)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped {
			t.Fatalf("entry %+v", e)
		}
		r.auditClean()
	})
	for name, replace := range map[string]bool{"not loaded": false, "another invocation": true} {
		t.Run("gone when it is signalled, "+name, func(t *testing.T) {
			r, _, ca, _ := twoCoordinators(t, leadunit.ProcScript{}, leadunit.ProcScript{})
			unit := unitOf(ca)
			r.aud.beforeSignal = func(st leadunit.State, sig syscall.Signal) {
				if st.Unit != unit {
					return
				}
				r.fake.Crash(unit)
				if replace {
					noErr(t, r.fake.AddForeign(unit, "another invocation"))
				}
			}
			e, err := r.stop(ca)
			noErr(t, err)
			if e.Shown.State != leadregistry.StateStopped || e.Shown.Proof == "" {
				t.Fatalf("entry %+v", e)
			}
			sig := r.fake.CallsOf(leadunit.MethodSignal)
			if len(sig) == 0 || sig[0].Err == nil {
				t.Fatalf("signals %+v", sig)
			}
			if replace {
				isErr(t, sig[0].Err, leadunit.ErrInvocationChanged)
			} else {
				isErr(t, sig[0].Err, leadunit.ErrNotLoaded)
			}
		})
	}
}

func TestStopLeadLeavesCoordinatorsNotRunByAtaniqAndNeverLaunchedAlone(t *testing.T) {
	r := newRig(t)
	lead := r.define("alpha")
	never := r.defineCoord("alpha", "never")
	tm, err := leadrole.Coordinator("alpha", "tmux")
	noErr(t, err)
	_, err = r.reg.Define(tm, leadregistry.ManagerTmux, leadregistry.Requested{})
	noErr(t, err)
	r.mustStart(lead)
	rev := r.revision()
	_, err = r.stop(lead)
	noErr(t, err)
	for _, co := range []leadrole.Role{never, tm} {
		if e := r.get(co); e.Epoch != 0 || e.Desired != leadregistry.DesiredStopped {
			t.Fatalf("%s: %+v", co, e)
		}
	}
	// Only the lead's own records were written.
	if r.revision() == rev {
		t.Fatal("nothing was written")
	}
	if got := len(r.ownSignals()); got != 1 {
		t.Fatalf("%d signals", got)
	}
}

func TestStopLeadWhoseCoordinatorHasAnUnsettledLaunch(t *testing.T) {
	r, lead, ca, cb := twoCoordinators(t, leadunit.ProcScript{}, leadunit.ProcScript{})
	// A third coordinator whose start request was dropped: its intent is
	// pending with no unit, so it cannot be stopped yet.
	cc := r.defineCoord("alpha", "ccc")
	r.fake.DropNextStart(unitOf(cc))
	_, err := r.start(cc)
	isErr(t, err, ErrLaunchUncertain)
	e, err := r.stop(lead)
	isErr(t, err, ErrCoordinatorNotStopped)
	isErr(t, err, ErrIntentPending)
	var ce *CoordinatorsError
	if !errors.As(err, &ce) || len(ce.Roles) != 1 || ce.Roles[0] != cc {
		t.Fatalf("error %v", err)
	}
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
	r.expectState(ca, leadregistry.StateStopped, "stopped")
	r.expectState(cb, leadregistry.StateStopped, "stopped")
	// Once the window has passed, Stop of the coordinator settles it.
	r.advance(pastTheWindow)
	ceE, err := r.stop(cc)
	noErr(t, err)
	if ceE.Shown.State != leadregistry.StateStopped {
		t.Fatalf("entry %+v", ceE)
	}
	if e := r.mustStart(lead); e.Epoch != 2 {
		t.Fatalf("epoch %d", e.Epoch)
	}
}

// startInTheGap makes the lead's own stop start the coordinator: the Quiesce
// hook of the lead runs in Stop after the coordinators were listed and before
// the lead is shown stopping, which is the gap a Start of a coordinator (which
// takes only its own lock) can fall into.
func startInTheGap(t *testing.T, r *rig, lead, coord leadrole.Role) {
	t.Helper()
	r.quiet.hook = func(role leadrole.Role, _ uint64) error {
		if role != lead {
			return nil
		}
		e, err := r.ctl.Start(context.Background(), coord)
		if err != nil || e.Intent == nil || e.Intent.Outcome != leadregistry.OutcomeLaunched {
			t.Errorf("Start of the coordinator in the gap: %+v %v", e, err)
		}
		return nil
	}
}

func TestStopLeadWithACoordinatorStartedInTheGapThatSurvivesKill(t *testing.T) {
	r, lead, coord := coordRig(t)
	r.fake.SetProcs(unitOf(coord), leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	startInTheGap(t, r, lead, coord)
	e, err := r.stop(lead)
	// The result names the coordinator, which is shown unknown with its unit
	// loaded; the lead itself is stopped.
	isErr(t, err, ErrCoordinatorNotStopped)
	isErr(t, err, ErrStopUnproven)
	var ce *CoordinatorsError
	if !errors.As(err, &ce) || len(ce.Roles) != 1 || ce.Roles[0] != coord {
		t.Fatalf("error %v", err)
	}
	if e.Shown.State != leadregistry.StateStopped || r.fake.Loaded(unitOf(lead)) {
		t.Fatalf("lead %+v", e)
	}
	ceE := r.expectState(coord, leadregistry.StateUnknown, "stop-unproven")
	if !r.fake.Loaded(unitOf(coord)) {
		t.Fatal("the unkillable unit is gone")
	}
	if ok, err := r.reg.CoordinatorStopped(coord, ceE.Epoch); err != nil || ok {
		t.Fatalf("CoordinatorStopped %v %v", ok, err)
	}
	// Its own stop signalled it in the second listing: TERM and then KILL.
	var sig []syscall.Signal
	for _, c := range r.ownSignals() {
		if c.Unit == unitOf(coord) {
			sig = append(sig, c.Signal)
		}
	}
	if len(sig) != 2 || sig[0] != syscall.SIGTERM || sig[1] != syscall.SIGKILL {
		t.Fatalf("signals to the coordinator %v", sig)
	}
	// A later Start of the lead is refused until the owner resolves it.
	_, err = r.start(lead)
	isErr(t, err, ErrCoordinatorNotStopped)
	r.auditClean()
}

func TestStopLeadWithACoordinatorStartedInTheGapThatExits(t *testing.T) {
	r, lead, coord := coordRig(t)
	startInTheGap(t, r, lead, coord)
	e, err := r.stop(lead)
	noErr(t, err)
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
	ce := r.expectState(coord, leadregistry.StateStopped, "")
	if ok, err := r.reg.CoordinatorStopped(coord, ce.Epoch); err != nil || !ok {
		t.Fatalf("CoordinatorStopped %v %v", ok, err)
	}
	if r.fake.Loaded(unitOf(coord)) {
		t.Fatal("the coordinator's unit is loaded")
	}
	r.auditClean()
}

func TestStopLeadDoesNotStopANamedCoordinatorTwice(t *testing.T) {
	r, lead, ca, cb := twoCoordinators(t,
		leadunit.ProcScript{Behavior: leadunit.SurviveKill}, leadunit.ProcScript{})
	// Another coordinator is started in the gap, so that the second listing
	// runs a stop and has the unproven one to skip.
	cc := r.defineCoord("alpha", "ccc")
	startInTheGap(t, r, lead, cc)
	_, err := r.stop(lead)
	isErr(t, err, ErrCoordinatorNotStopped)
	var ce *CoordinatorsError
	if !errors.As(err, &ce) || len(ce.Roles) != 1 || ce.Roles[0] != ca {
		t.Fatalf("error %v", err)
	}
	r.expectState(cb, leadregistry.StateStopped, "stopped")
	r.expectState(cc, leadregistry.StateStopped, "")
	count := map[string]int{}
	for _, c := range r.ownSignals() {
		if c.Signal == syscall.SIGTERM {
			count[c.Unit]++
		}
	}
	for _, co := range []leadrole.Role{ca, cb, lead} {
		if count[unitOf(co)] != 1 {
			t.Fatalf("%s was sent TERM %d times", co, count[unitOf(co)])
		}
	}
	// The one started in the gap may be stopped by the manager with its
	// parent before the daemon looks (when it does not depend on the
	// moment), and is signalled at most once.
	if count[unitOf(cc)] > 1 {
		t.Fatalf("%s was sent TERM %d times", cc, count[unitOf(cc)])
	}
	r.auditClean()
}

// cancelAfterTheLeadsProof cancels the context the moment the lead's stop has
// its proof: the second Empty that answers true for the lead's unit (the first
// is the wait for SIGTERM, the second is the proof of the stopped invocation).
func cancelAfterTheLeadsProof(r *rig, lead leadrole.Role, cancel context.CancelFunc) {
	var mu sync.Mutex
	empties := 0
	r.aud.afterEmpty = func(st leadunit.State, empty bool) {
		if st.Unit != unitOf(lead) || !empty {
			return
		}
		mu.Lock()
		empties++
		n := empties
		mu.Unlock()
		if n == 2 {
			cancel()
		}
	}
}

// TestStopLeadWhoseContextEndsRightAfterItsProofStillListsTheGap: a coordinator
// is started in the gap (it survives SIGKILL), and the caller's context ends
// right after the proof of the lead's stop. The second listing must still run
// and name the coordinator, shown unknown; Stop must not return nil.
func TestStopLeadWhoseContextEndsRightAfterItsProofStillListsTheGap(t *testing.T) {
	r, lead, coord := coordRig(t)
	r.fake.SetProcs(unitOf(coord), leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	startInTheGap(t, r, lead, coord)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAfterTheLeadsProof(r, lead, cancel)
	var e leadregistry.Entry
	var err error
	r.drive(func() { e, err = r.ctl.Stop(ctx, lead) })
	if ctx.Err() == nil {
		t.Fatal("the context was not cancelled: the test did not reach the lead's proof")
	}
	if err == nil {
		t.Fatalf("Stop(lead) returned nil; coordinator shown %+v, loaded %v", r.get(coord).Shown, r.fake.Loaded(unitOf(coord)))
	}
	isErr(t, err, ErrCoordinatorNotStopped)
	var ce *CoordinatorsError
	if !errors.As(err, &ce) || len(ce.Roles) != 1 || ce.Roles[0] != coord {
		t.Fatalf("error %v", err)
	}
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
	r.expectState(coord, leadregistry.StateUnknown, "stop-unproven")
	if !r.fake.Loaded(unitOf(coord)) {
		t.Fatal("the unkillable unit is gone")
	}
	r.auditClean()
}

// The same sequence with a coordinator that exits when told to: the second
// listing stops it with proof and Stop(lead) returns nil, with the caller's
// context over.
func TestStopLeadWhoseContextEndsRightAfterItsProofStopsTheGapCoordinator(t *testing.T) {
	r, lead, coord := coordRig(t)
	startInTheGap(t, r, lead, coord)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAfterTheLeadsProof(r, lead, cancel)
	var e leadregistry.Entry
	var err error
	r.drive(func() { e, err = r.ctl.Stop(ctx, lead) })
	if ctx.Err() == nil {
		t.Fatal("the context was not cancelled: the test did not reach the lead's proof")
	}
	noErr(t, err)
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
	ce := r.expectState(coord, leadregistry.StateStopped, "")
	if ok, err := r.reg.CoordinatorStopped(coord, ce.Epoch); err != nil || !ok {
		t.Fatalf("CoordinatorStopped %v %v", ok, err)
	}
	r.auditClean()
}

// A context that has ended on entry returns its error before anything is read
// or changed: no listing, no signal, no registry write.
func TestStopLeadWithAnEndedContextOnEntryChangesNothing(t *testing.T) {
	r, lead, _ := coordRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rev := r.revision()
	_, err := r.ctl.Stop(ctx, lead)
	isErr(t, err, context.Canceled)
	if r.revision() != rev || len(r.signalCalls()) != 0 {
		t.Fatal("Stop with an ended context changed something")
	}
}

// When the stop of the lead itself fails and the caller's context is over, the
// result is an error, and a coordinator it could not list is not reported as
// stopped.
func TestStopLeadFailingWithTheContextEndedIsNeverNil(t *testing.T) {
	r, lead, coord := coordRig(t)
	r.mustStart(coord)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The context ends when the lead is first signalled, so the lead's stop
	// does not finish.
	r.aud.afterSignal = func(st leadunit.State, sig syscall.Signal) {
		if st.Unit == unitOf(lead) {
			cancel()
		}
	}
	var e leadregistry.Entry
	var err error
	r.drive(func() { e, err = r.ctl.Stop(ctx, lead) })
	if err == nil {
		t.Fatalf("Stop(lead) returned nil with the lead %+v", e)
	}
	isErr(t, err, context.Canceled)
	if e.Shown.State == leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
}

// TestStopLeadSecondListingReplacesAStaleNamedInstance: the first pass names
// instance 1 of a coordinator (it survives SIGKILL). In the gap instance 1
// dies, the owner's Stop proves it stopped and a Start launches instance 2
// under the lead, which is still live. The second listing must stop, or
// name, instance 2, and the result must carry its error and not instance 1's.
func TestStopLeadSecondListingReplacesAStaleNamedInstance(t *testing.T) {
	r, lead, coord := coordRig(t)
	cunit := unitOf(coord)
	r.fake.SetProcs(cunit, leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	e1 := r.mustStart(coord)
	r.quiet.hook = func(role leadrole.Role, _ uint64) error {
		if role != lead {
			return nil
		}
		if !r.fake.Crash(cunit) {
			t.Error("instance 1 was not loaded in the gap")
		}
		if _, err := r.ctl.Stop(context.Background(), coord); err != nil {
			t.Errorf("Stop of instance 1 in the gap: %v", err)
		}
		r.fake.SetProcs(cunit, leadunit.ProcScript{Behavior: leadunit.SurviveKill})
		e, err := r.ctl.Start(context.Background(), coord)
		if err != nil || e.Epoch != e1.Epoch+1 {
			t.Errorf("Start of instance 2 in the gap: %+v %v", e, err)
		}
		return nil
	}
	e, err := r.stop(lead)
	isErr(t, err, ErrCoordinatorNotStopped)
	var ce *CoordinatorsError
	if !errors.As(err, &ce) || len(ce.Roles) != 1 || ce.Roles[0] != coord || len(ce.Errs) != 1 {
		t.Fatalf("error %v", err)
	}
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
	ce2 := r.expectState(coord, leadregistry.StateUnknown, "stop-unproven")
	if ce2.Epoch != e1.Epoch+1 {
		t.Fatalf("coordinator epoch %d, want %d", ce2.Epoch, e1.Epoch+1)
	}
	if ce.epochs[0] != ce2.Epoch {
		t.Fatalf("the result is about epoch %d, want %d", ce.epochs[0], ce2.Epoch)
	}
	// Instance 2 was signalled by this Stop: TERM and then KILL, after the
	// two signals instance 1 got from the first pass.
	n := 0
	for _, c := range r.ownSignals() {
		if c.Unit == cunit {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("%d signals to the coordinator, want 4: %+v", n, r.ownSignals())
	}
	r.auditClean()
}

// A stale named instance that the later one replaces and that is proven
// stopped leaves no entry: the result is nil.
func TestStopLeadSecondListingDropsAStaleNamedInstanceThatIsReplacedAndStops(t *testing.T) {
	r, lead, coord := coordRig(t)
	cunit := unitOf(coord)
	r.fake.SetProcs(cunit, leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	e1 := r.mustStart(coord)
	r.quiet.hook = func(role leadrole.Role, _ uint64) error {
		if role != lead {
			return nil
		}
		r.fake.Crash(cunit)
		if _, err := r.ctl.Stop(context.Background(), coord); err != nil {
			t.Errorf("Stop of instance 1 in the gap: %v", err)
		}
		r.fake.SetProcs(cunit, leadunit.ProcScript{})
		if e, err := r.ctl.Start(context.Background(), coord); err != nil || e.Epoch != e1.Epoch+1 {
			t.Errorf("Start of instance 2 in the gap: %+v %v", e, err)
		}
		return nil
	}
	_, err := r.stop(lead)
	noErr(t, err)
	r.expectState(coord, leadregistry.StateStopped, "")
	r.auditClean()
}

// TestStopLeadSecondListingDropsANamedInstanceProvenStoppedInTheGap: the
// first pass names instance 1 of a coordinator (it survives SIGKILL). In the
// gap its processes die and the owner's Stop proves that same instance
// stopped; nothing starts the coordinator again. The second listing finds the
// instance the first pass named, which the registry now has as stopped with
// proof, and must not name it: the Stop of the lead returns nil, and the
// coordinator is not signalled again.
func TestStopLeadSecondListingDropsANamedInstanceProvenStoppedInTheGap(t *testing.T) {
	r, lead, coord := coordRig(t)
	cunit := unitOf(coord)
	r.fake.SetProcs(cunit, leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	e1 := r.mustStart(coord)
	gap := false
	r.quiet.hook = func(role leadrole.Role, _ uint64) error {
		if role != lead {
			return nil
		}
		gap = true
		// The first pass named instance 1 and left it unknown.
		if ce := r.get(coord); ce.Epoch != e1.Epoch || ce.Shown.State != leadregistry.StateUnknown {
			t.Errorf("coordinator in the gap: %+v", ce)
		}
		if !r.fake.Crash(cunit) {
			t.Error("instance 1 was not loaded in the gap")
		}
		if _, err := r.ctl.Stop(context.Background(), coord); err != nil {
			t.Errorf("Stop of instance 1 in the gap: %v", err)
		}
		return nil
	}
	e, err := r.stop(lead)
	if !gap {
		t.Fatal("the lead's quiesce did not run")
	}
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
	ce := r.expectState(coord, leadregistry.StateStopped, "")
	if ce.Epoch != e1.Epoch {
		t.Fatalf("coordinator epoch %d, want %d", ce.Epoch, e1.Epoch)
	}
	if ok, serr := r.reg.CoordinatorStopped(coord, e1.Epoch); serr != nil || !ok {
		t.Fatalf("CoordinatorStopped %v %v", ok, serr)
	}
	if err != nil {
		t.Fatalf("Stop(lead) names a coordinator that is proven stopped: %v", err)
	}
	// Only the first pass signalled the coordinator: TERM and then KILL.
	n := 0
	for _, c := range r.ownSignals() {
		if c.Unit == cunit {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d signals to the coordinator, want 2: %+v", n, r.ownSignals())
	}
	r.auditClean()
}

// gapRig is a running lead whose own stop fails (its processes survive
// SIGKILL, so it is not proven stopped) and the two coordinators aaa and bbb
// of its project, which are defined and not running. When the lead is stopped,
// its Quiesce starts both: they are started in the gap, so the first listing
// names none of them and the second one finds both. Their processes survive
// SIGKILL, so each is named when it is stopped. The second listing runs on the
// caller's context, because the lead's own stop failed.
func gapRig(t *testing.T) (r *rig, lead, ca, cb leadrole.Role) {
	t.Helper()
	r = newRig(t)
	lead = r.define("alpha")
	ca = r.defineCoord("alpha", "aaa")
	cb = r.defineCoord("alpha", "bbb")
	for _, u := range []string{unitOf(lead), unitOf(ca), unitOf(cb)} {
		r.fake.SetProcs(u, leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	}
	r.mustStart(lead)
	r.quiet.hook = func(role leadrole.Role, _ uint64) error {
		if role != lead {
			return nil
		}
		for _, co := range []leadrole.Role{ca, cb} {
			if _, err := r.ctl.Start(context.Background(), co); err != nil {
				t.Errorf("Start of %s in the gap: %v", co, err)
			}
		}
		return nil
	}
	return r, lead, ca, cb
}

// namesOnly checks that err holds a *CoordinatorsError that names exactly want.
func namesOnly(t *testing.T, err error, want ...leadrole.Role) {
	t.Helper()
	var ce *CoordinatorsError
	if !errors.As(err, &ce) {
		t.Fatalf("no *CoordinatorsError in %v", err)
	}
	if len(ce.Roles) != len(want) || len(ce.Errs) != len(want) {
		t.Fatalf("the error names %v (%d errors), want %v: %v", ce.Roles, len(ce.Errs), want, err)
	}
	for i, r := range want {
		if ce.Roles[i] != r {
			t.Fatalf("the error names %v, want %v: %v", ce.Roles, want, err)
		}
	}
	isErr(t, err, ErrCoordinatorNotStopped)
}

// TestStopLeadSecondListingEndedByTheContextKeepsWhatItNamed: the lead's own
// stop fails, so the second listing runs on the caller's context. It names the
// coordinator aaa, and the context ends while it handles bbb. The result is an
// error that still names aaa, which is shown unknown.
func TestStopLeadSecondListingEndedByTheContextKeepsWhatItNamed(t *testing.T) {
	r, lead, ca, cb := gapRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.aud.afterSignal = func(st leadunit.State, sig syscall.Signal) {
		if st.Unit == unitOf(cb) {
			cancel()
		}
	}
	var e leadregistry.Entry
	var err error
	r.drive(func() { e, err = r.ctl.Stop(ctx, lead) })
	if ctx.Err() == nil {
		t.Fatal("the context was not cancelled: the second listing did not reach bbb")
	}
	if err == nil {
		t.Fatalf("Stop(lead) returned nil with the lead %+v", e)
	}
	isErr(t, err, context.Canceled)
	isErr(t, err, ErrStopUnproven)
	namesOnly(t, err, ca)
	r.expectState(ca, leadregistry.StateUnknown, "stop-unproven")
}

// TestStopLeadSecondListingEndedByTheRegistryKeepsWhatItNamed: the same, with
// the registry failing (it is closed) while the second listing goes from aaa
// to bbb.
func TestStopLeadSecondListingEndedByTheRegistryKeepsWhatItNamed(t *testing.T) {
	r, lead, ca, cb := gapRig(t)
	r.aud.afterSignal = func(st leadunit.State, sig syscall.Signal) {
		if st.Unit == unitOf(ca) {
			r.reg.Close()
		}
	}
	var err error
	r.drive(func() { _, err = r.ctl.Stop(context.Background(), lead) })
	if err == nil {
		t.Fatal("Stop(lead) returned nil")
	}
	isErr(t, err, leadregistry.ErrClosed)
	namesOnly(t, err, ca)
	for _, c := range r.fake.CallsOf(leadunit.MethodSignal) {
		if c.Unit == unitOf(cb) && !c.Implied {
			t.Fatalf("bbb was signalled after the registry failed: %+v", c)
		}
	}
}

// TestStopLeadFirstListingEndedByTheContextKeepsWhatItNamed: the first
// listing names aaa (it survives SIGKILL) and the context ends while it
// handles bbb. The lead is not stopped, and the result still names aaa.
func TestStopLeadFirstListingEndedByTheContextKeepsWhatItNamed(t *testing.T) {
	r, lead, ca, cb := twoCoordinators(t,
		leadunit.ProcScript{Behavior: leadunit.SurviveKill}, leadunit.ProcScript{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.aud.afterSignal = func(st leadunit.State, sig syscall.Signal) {
		if st.Unit == unitOf(cb) {
			cancel()
		}
	}
	var e leadregistry.Entry
	var err error
	r.drive(func() { e, err = r.ctl.Stop(ctx, lead) })
	if ctx.Err() == nil {
		t.Fatal("the context was not cancelled: the first listing did not reach bbb")
	}
	if err == nil {
		t.Fatalf("Stop(lead) returned nil with the lead %+v", e)
	}
	isErr(t, err, context.Canceled)
	namesOnly(t, err, ca)
	if e.Shown.State == leadregistry.StateStopped {
		t.Fatalf("lead %+v", e)
	}
	r.expectState(ca, leadregistry.StateUnknown, "stop-unproven")
}
