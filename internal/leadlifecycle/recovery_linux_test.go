package leadlifecycle

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

func TestL3SecondStartRefusedByUnitExists(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	// A foreign unit takes the name after our proof and before our Start.
	r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
		if err := r.fake.AddForeign(unitOf(rl), "someone else's unit"); err != nil {
			t.Error(err)
		}
	}
	e, err := r.start(role)
	isErr(t, err, ErrStartFailed)
	isErr(t, err, leadunit.ErrUnitExists)
	isErr(t, err, ErrForeignUnit)
	if e.Intent.Outcome != leadregistry.OutcomeFailed || e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "unit-exists" {
		t.Fatalf("entry %+v", e)
	}
	if len(r.fake.StartedSpecs()) != 0 || r.callsOf(leadunit.MethodStart) != 1 {
		t.Fatalf("starts %d calls %d", len(r.fake.StartedSpecs()), r.callsOf(leadunit.MethodStart))
	}
	// Nothing starts while the role is unknown.
	_, err = r.start(role)
	isErr(t, err, leadregistry.ErrLaunchRefused)
	// The foreign unit goes; Reconcile proves the name free and a start works.
	r.fake.Crash(unitOf(role))
	rep := r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Action != ActionStopped || rr.Reason != "unit-absent" {
		t.Fatalf("report %+v", rr)
	}
	r.holder.hook = nil
	e = r.mustStart(role)
	if e.Epoch != 2 {
		t.Fatalf("epoch %d", e.Epoch)
	}
	r.auditClean()
}

func TestStartReplacementRule(t *testing.T) {
	// A role that ended: its previous control group must be proven empty.
	setup := func(t *testing.T) (*rig, leadrole.Role) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		if !r.fake.Crash(unitOf(role)) {
			t.Fatal("no unit to crash")
		}
		rep := r.reconcile()
		if rr, _ := rep.Find(role); rr.Action != ActionEnded {
			t.Fatalf("report %+v", rr)
		}
		return r, role
	}

	t.Run("same boot, control group proven empty", func(t *testing.T) {
		r, role := setup(t)
		before := r.callsOf(leadunit.MethodEmpty)
		r.mustStart(role)
		if r.callsOf(leadunit.MethodEmpty) <= before {
			t.Fatal("no Empty call before the start")
		}
		r.auditClean()
	})
	t.Run("same boot, control group left behind", func(t *testing.T) {
		r, role := setup(t)
		r.fake.LeaveControlGroup(unitOf(role), true)
		rev := r.revision()
		_, err := r.start(role)
		isErr(t, err, ErrNoProof)
		if r.revision() != rev || r.callsOf(leadunit.MethodStart) != 1 {
			t.Fatalf("revision %d->%d, Start calls %d", rev, r.revision(), r.callsOf(leadunit.MethodStart))
		}
		if e := r.get(role); e.Shown.State != leadregistry.StateEnded || e.Epoch != 1 {
			t.Fatalf("entry %+v", e)
		}
		r.fake.LeaveControlGroup(unitOf(role), false)
		r.mustStart(role)
	})
	t.Run("another boot is the proof", func(t *testing.T) {
		r, role := setup(t)
		r.fake.LeaveControlGroup(unitOf(role), true)
		r.fake.Reboot()
		before := r.callsOf(leadunit.MethodEmpty)
		r.mustStart(role)
		if r.callsOf(leadunit.MethodEmpty) != before {
			t.Fatal("Empty was needed although the boot identity differs")
		}
		r.auditClean()
	})
}

func TestStartReplyLostAndNeverStarted(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
		r.fake.DropNextStart(unitOf(rl))
	}
	e, err := r.start(role)
	isErr(t, err, ErrLaunchUncertain)
	// The registry's table leaves one way to keep the role from being resolved
	// early: the intent stays pending and the role is unknown.
	if e.Intent.Outcome != leadregistry.OutcomePending || e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "launch-reply-lost" {
		t.Fatalf("entry %+v", e)
	}
	if r.callsOf(leadunit.MethodStart) != 1 {
		t.Fatal("Start retried")
	}
	r.holder.hook = nil

	// Nothing may start while it is pending.
	_, err = r.start(role)
	isErr(t, err, leadregistry.ErrLaunchRefused)

	// Before the end of the window and its margin: nothing is recorded as
	// ended, the role stays unknown.
	r.advance(DefaultLateStartWindow + DefaultLateStartMargin - time.Second)
	rev := r.revision()
	rep := r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Action != ActionPending || rr.After != leadregistry.StateUnknown || r.revision() != rev {
		t.Fatalf("report %+v revision %d->%d", rr, rev, r.revision())
	}
	// The owner cannot resolve it either while the intent is pending.
	_, err = r.reg.ResolveUnknown(role, leadregistry.OwnerDecision{Epoch: 1, State: leadregistry.StateStopped,
		Reason: leadregistry.Reason{Code: "owner"}, Decision: "stop it"})
	isErr(t, err, leadregistry.ErrOutcomePending)

	// After the window and the margin with the unit not loaded and the group
	// empty: abandoned, stopped.
	r.advance(2 * time.Second)
	rep = r.reconcile()
	rr, _ = rep.Find(role)
	if rr.Action != ActionStopped || rr.After != leadregistry.StateStopped {
		t.Fatalf("report %+v", rr)
	}
	e = r.expectState(role, leadregistry.StateStopped, "launch-abandoned")
	if e.Intent.Outcome != leadregistry.OutcomeAbandoned || !strings.Contains(e.Shown.Proof, "late-start window") || !strings.Contains(e.Shown.Proof, e.Intent.ID) {
		t.Fatalf("entry %+v", e)
	}
	// And the role can be started again with a new epoch.
	if e := r.mustStart(role); e.Epoch != 2 {
		t.Fatalf("epoch %d", e.Epoch)
	}
	r.auditClean()
}

func TestStartReplyLostLandingLate(t *testing.T) {
	t.Run("lands inside the window and is adopted", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.LateNextStart(unitOf(rl))
		}
		_, err := r.start(role)
		isErr(t, err, ErrLaunchUncertain)
		r.advance(5 * time.Second)
		_, err = r.fake.LandLateStart(unitOf(role))
		noErr(t, err)
		rep := r.reconcile()
		rr, _ := rep.Find(role)
		if rr.Action != ActionAdopted {
			t.Fatalf("report %+v", rr)
		}
		e := r.get(role)
		// The registry cannot take unknown back to starting: adopted, and the
		// role stays unknown until it is stopped or decided.
		if e.Intent.Outcome != leadregistry.OutcomeAdopted || e.Shown.State != leadregistry.StateUnknown || e.Unit.InvocationID == "" {
			t.Fatalf("entry %+v", e)
		}
		if r.callsOf(leadunit.MethodStart) != 2 { // the controller's one and the late landing's log entry
			t.Fatalf("Start log entries %d", r.callsOf(leadunit.MethodStart))
		}
		// Stop settles it: our unit, our epoch and intent.
		e, err = r.stop(role)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped {
			t.Fatalf("entry %+v", e)
		}
		r.auditClean()
	})
	t.Run("lands after the window and is stale", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.LateNextStart(unitOf(rl))
		}
		_, err := r.start(role)
		isErr(t, err, ErrLaunchUncertain)
		r.advance(pastTheWindow)
		rep := r.reconcile()
		if rr, _ := rep.Find(role); rr.Action != ActionStopped {
			t.Fatalf("report %+v", rr)
		}
		_, err = r.fake.LandLateStart(unitOf(role))
		noErr(t, err)
		rep = r.reconcile()
		rr, _ := rep.Find(role)
		if rr.Action != ActionUnknown || rr.Reason != "stale-unit" || rr.After != leadregistry.StateUnknown {
			t.Fatalf("report %+v", rr)
		}
		// Reconcile never signals it.
		r.noSignals()
		// A start is refused while it is there.
		_, err = r.start(role)
		isErr(t, err, leadregistry.ErrLaunchRefused)
		// The owner's order: Stop signals a unit with exactly this epoch and intent.
		e, err := r.stop(role)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped {
			t.Fatalf("entry %+v", e)
		}
		r.auditClean()
	})
}

// A start request that lands after a newer epoch has run leaves a stale unit
// of ours with an older epoch. Reconcile reports it and never signals it; the
// owner's Stop ends it, and the role has a way forward.
func TestStopEndsAStaleUnitOfAnOlderEpoch(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	unit := unitOf(role)
	// Epoch 1: the start request is held on its way, the reply is lost.
	r.fake.LateNextStart(unit)
	_, err := r.start(role)
	isErr(t, err, ErrLaunchUncertain)
	first := r.get(role).Intent.ID
	// The window passes with nothing loaded: the intent is abandoned.
	r.advance(pastTheWindow)
	rep := r.reconcile()
	if rr, _ := rep.Find(role); rr.Action != ActionStopped {
		t.Fatalf("report %+v", rr)
	}
	// Epoch 2 runs and its holder dies.
	if e := r.mustStart(role); e.Epoch != 2 {
		t.Fatalf("epoch %d", e.Epoch)
	}
	r.fake.Crash(unit)
	rep = r.reconcile()
	if rr, _ := rep.Find(role); rr.Action != ActionEnded {
		t.Fatalf("report %+v", rr)
	}
	// The epoch 1 request lands now.
	st, err := r.fake.LandLateStart(unit)
	noErr(t, err)
	if st.Epoch != 1 || st.Intent != first {
		t.Fatalf("landed %+v", st)
	}
	rep = r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Action != ActionUnknown || rr.Reason != "stale-unit" || rr.After != leadregistry.StateUnknown {
		t.Fatalf("report %+v", rr)
	}
	r.noSignals()
	// A start is refused while the stale unit is loaded.
	_, err = r.start(role)
	isErr(t, err, leadregistry.ErrLaunchRefused)
	// Stop is the owner's order and ends it, with proof.
	e, err := r.stop(role)
	noErr(t, err)
	if e.Shown.State != leadregistry.StateStopped || e.Epoch != 2 {
		t.Fatalf("entry %+v", e)
	}
	if r.fake.Loaded(unit) || len(r.fake.Processes(unit)) != 0 {
		t.Fatal("the stale unit is still there")
	}
	if got := signalsOf(r.signalCalls()); len(got) == 0 || got[0] != syscall.SIGTERM {
		t.Fatalf("signals %v", got)
	}
	for _, c := range r.signalCalls() {
		if c.Invocation != st.InvocationID {
			t.Fatalf("signal to invocation %s, want the stale unit's %s", c.Invocation, st.InvocationID)
		}
	}
	// And the role moves on: a new epoch starts.
	if e := r.mustStart(role); e.Epoch != 3 {
		t.Fatalf("epoch %d", e.Epoch)
	}
	r.auditClean()
}

func TestStartManagerUnavailableThenReconcile(t *testing.T) {
	t.Run("start, then inspect", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.MakeUnavailable(leadunit.MethodStart, 1)
			r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
		}
		e, err := r.start(role)
		isErr(t, err, ErrStartFailed)
		isErr(t, err, leadunit.ErrUnavailable)
		if r.callsOf(leadunit.MethodStart) != 1 {
			t.Fatalf("Start called %d times", r.callsOf(leadunit.MethodStart))
		}
		if e.Intent.Outcome != leadregistry.OutcomeFailed || e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("entry %+v", e)
		}
		// Reconcile later proves the name free.
		rep := r.reconcile()
		if rr, _ := rep.Find(role); rr.Action != ActionStopped {
			t.Fatalf("report %+v", rr)
		}
	})
	t.Run("reply lost, then inspect", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.LoseNextStartReply(unitOf(rl))
			r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
		}
		e, err := r.start(role)
		isErr(t, err, ErrLaunchUncertain)
		isErr(t, err, leadunit.ErrReplyLost)
		if e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "launch-reply-lost" || e.Intent.Outcome != leadregistry.OutcomePending {
			t.Fatalf("entry %+v", e)
		}
		// The unit runs; Reconcile adopts it.
		rep := r.reconcile()
		rr, _ := rep.Find(role)
		if rr.Action != ActionAdopted {
			t.Fatalf("report %+v", rr)
		}
		e = r.get(role)
		if e.Intent.Outcome != leadregistry.OutcomeAdopted || e.Unit.InvocationID == "" || len(r.fake.StartedSpecs()) != 1 {
			t.Fatalf("entry %+v", e)
		}
	})
}
