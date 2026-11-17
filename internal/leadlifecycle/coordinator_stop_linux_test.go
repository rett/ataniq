package leadlifecycle

import (
	"context"
	"errors"
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
