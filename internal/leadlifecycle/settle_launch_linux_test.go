package leadlifecycle

import (
	"context"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// settle calls SettleLaunch the way the tests call Reconcile.
func (r *rig) settle(role leadrole.Role) (RoleReport, error) {
	r.t.Helper()
	var rr RoleReport
	var err error
	r.drive(func() { rr, err = r.ctl.SettleLaunch(context.Background(), role) })
	return rr, err
}

func (r *rig) mustSettle(role leadrole.Role) RoleReport {
	r.t.Helper()
	rr, err := r.settle(role)
	noErr(r.t, err)
	return rr
}

// A daemon that died between the intent and the outcome leaves the role
// unknown inside the window; SettleLaunch does what Reconcile does for it, one
// role at a time: nothing inside the window, then the stop with the proof.
func TestSettleLaunchClosesAnIntentOfADeadDaemonAfterTheWindow(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	in, err := r.reg.BeginLaunchWith(role, leadregistry.LaunchOptions{BootIdentity: r.fake.BootID()})
	noErr(t, err)
	r.restart()
	rr, _ := r.reconcile().Find(role)
	if rr.Action != ActionPending || rr.Reason != codeUnsettled {
		t.Fatalf("report %+v", rr)
	}
	r.expectState(role, leadregistry.StateUnknown, codeUnsettled)

	if got, want := r.ctl.LaunchDeadline(in), in.At.Add(DefaultLateStartWindow+DefaultLateStartMargin); !got.Equal(want) {
		t.Fatalf("deadline %v, want %v", got, want)
	}

	// Inside the window nothing changes.
	rev := r.revision()
	r.advance(DefaultLateStartWindow)
	rr = r.mustSettle(role)
	if rr.Action != ActionPending || rr.Reason != codeUnsettled || r.revision() != rev {
		t.Fatalf("inside the window: report %+v", rr)
	}
	// A new start is refused: the role is unknown.
	_, err = r.start(role)
	isErr(t, err, leadregistry.ErrLaunchRefused)

	// The window and the margin have passed: the intent is abandoned and the
	// role stopped with the proof.
	r.advance(DefaultLateStartMargin + time.Second)
	rr = r.mustSettle(role)
	if rr.Action != ActionStopped || rr.Reason != "launch-abandoned" || rr.After != leadregistry.StateStopped {
		t.Fatalf("after the window: report %+v", rr)
	}
	e := r.expectState(role, leadregistry.StateStopped, "launch-abandoned")
	if e.Intent.Outcome != leadregistry.OutcomeAbandoned {
		t.Fatalf("entry %+v", e)
	}
	r.startsNothing(0)

	// The role is free: a start takes the next epoch, and the lifecycle's own
	// rules decide it.
	if e := r.mustStart(role); e.Epoch != in.Epoch+1 {
		t.Fatalf("epoch %d", e.Epoch)
	}
	r.auditClean()
}

// The role settled by SettleLaunch is the role Reconcile would have settled
// (same report, same entry).
func TestSettleLaunchIsWhatReconcileDoesForTheRole(t *testing.T) {
	for _, step := range []time.Duration{0, DefaultLateStartWindow, pastTheWindow} {
		a, b := newRig(t), newRig(t)
		ra := replyLostRole(t, a)
		rb := replyLostRole(t, b)
		a.advance(step)
		b.advance(step)
		got := a.mustSettle(ra)
		want, _ := b.reconcile().Find(rb)
		if got.Action != want.Action || got.Reason != want.Reason || got.Before != want.Before || got.After != want.After {
			t.Fatalf("after %v: SettleLaunch %+v, Reconcile %+v", step, got, want)
		}
		ea, eb := a.get(ra), b.get(rb)
		if ea.Shown.State != eb.Shown.State || ea.Shown.Reason.Code != eb.Shown.Reason.Code || ea.Intent.Outcome != eb.Intent.Outcome {
			t.Fatalf("after %v: entries %+v and %+v", step, ea, eb)
		}
	}
}

// A lost reply whose unit is not loaded is settled after the window too.
func TestSettleLaunchClosesALostReplyAfterTheWindow(t *testing.T) {
	r := newRig(t)
	role := replyLostRole(t, r)
	rr := r.mustSettle(role)
	if rr.Action != ActionPending {
		t.Fatalf("inside the window: report %+v", rr)
	}
	r.expectState(role, leadregistry.StateUnknown, "launch-reply-lost")
	r.advance(pastTheWindow)
	rr = r.mustSettle(role)
	if rr.Action != ActionStopped {
		t.Fatalf("report %+v", rr)
	}
	r.expectState(role, leadregistry.StateStopped, "launch-abandoned")
	r.startsNothing(1) // the lost start's own call, and no other
}

// A unit that is loaded at the deadline is never a reason to stop the role:
// a unit that is this launch's is adopted (the role stays unknown, and is
// stopped by an explicit Stop), one that is not is reported and the intent
// abandoned. Nothing is signalled and nothing is started.
func TestSettleLaunchNeverSettlesARoleWhoseUnitIsLoaded(t *testing.T) {
	t.Run("the unit of the launch landed", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.LateNextStart(unitOf(rl))
		}
		_, err := r.start(role)
		isErr(t, err, ErrLaunchUncertain)
		r.holder.hook = nil
		_, err = r.fake.LandLateStart(unitOf(role))
		noErr(t, err)
		r.advance(pastTheWindow)
		rr := r.mustSettle(role)
		if rr.Action != ActionAdopted {
			t.Fatalf("report %+v", rr)
		}
		e := r.expectState(role, leadregistry.StateUnknown, "launch-reply-lost")
		if e.Intent.Outcome != leadregistry.OutcomeAdopted {
			t.Fatalf("entry %+v", e)
		}
		r.startsNothing(2) // the lost start and the landing of its request
		_, err = r.start(role)
		isErr(t, err, leadregistry.ErrLaunchRefused)
		r.auditClean()
	})
	t.Run("a unit that is not the launch's", func(t *testing.T) {
		r := newRig(t)
		role := replyLostRole(t, r)
		noErr(t, r.fake.AddForeign(unitOf(role), "someone else's unit"))
		r.advance(pastTheWindow)
		rr := r.mustSettle(role)
		if rr.Action != ActionUnknown || rr.Reason != "foreign-unit" {
			t.Fatalf("report %+v", rr)
		}
		e := r.get(role)
		if e.Shown.State != leadregistry.StateUnknown || e.Intent.Outcome != leadregistry.OutcomeAbandoned {
			t.Fatalf("entry %+v", e)
		}
		r.startsNothing(1)
		_, err := r.start(role)
		isErr(t, err, leadregistry.ErrLaunchRefused)
		r.auditClean()
	})
	t.Run("a unit loaded inside the window", func(t *testing.T) {
		r := newRig(t)
		role := replyLostRole(t, r)
		noErr(t, r.fake.AddForeign(unitOf(role), "someone else's unit"))
		rr := r.mustSettle(role)
		if rr.Action != ActionUnknown || r.get(role).Intent.Outcome != leadregistry.OutcomePending {
			t.Fatalf("report %+v", rr)
		}
		r.startsNothing(1)
	})
}

// A control group that is not proven empty keeps the intent pending, past the
// window.
func TestSettleLaunchKeepsTheIntentWhileTheControlGroupIsNotEmpty(t *testing.T) {
	r := newRig(t)
	role := replyLostRole(t, r)
	r.fake.LeaveControlGroup(unitOf(role), true)
	r.advance(pastTheWindow)
	rr := r.mustSettle(role)
	if rr.Action != ActionUnknown || rr.Reason != codeNotEmpty {
		t.Fatalf("report %+v", rr)
	}
	if e := r.get(role); e.Intent.Outcome != leadregistry.OutcomePending || e.Shown.State != leadregistry.StateUnknown {
		t.Fatalf("entry %+v", e)
	}
	r.startsNothing(1)
}

// A manager that does not answer changes nothing.
func TestSettleLaunchChangesNothingWhenTheManagerDoesNotAnswer(t *testing.T) {
	r := newRig(t)
	role := replyLostRole(t, r)
	r.advance(pastTheWindow)
	rev := r.revision()
	r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
	rr := r.mustSettle(role)
	if rr.Action != ActionUnavailable || r.revision() != rev {
		t.Fatalf("report %+v", rr)
	}
	// The next call settles it.
	if rr = r.mustSettle(role); rr.Action != ActionStopped {
		t.Fatalf("report %+v", rr)
	}
}

// A changed boot settles an unloaded unit at once, as it does in Reconcile.
func TestSettleLaunchSettlesAtOnceWhenTheBootChanged(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	_, err := r.reg.BeginLaunchWith(role, leadregistry.LaunchOptions{BootIdentity: r.fake.BootID()})
	noErr(t, err)
	r.restart()
	r.fake.Reboot()
	rr := r.mustSettle(role)
	if rr.Action != ActionStopped || rr.Reason != "launch-abandoned" {
		t.Fatalf("report %+v", rr)
	}
	r.expectState(role, leadregistry.StateStopped, "launch-abandoned")
	r.startsNothing(0)
}

// A role with no pending intent is not touched, and the manager is not asked.
func TestSettleLaunchLeavesARoleWithoutAPendingIntent(t *testing.T) {
	r := newRig(t)
	defined := r.define("alpha")
	started := r.define("beta")
	r.mustStart(started)
	tmux := r.defineTmux("gamma")
	rev := r.revision()
	inspects := r.callsOf(leadunit.MethodInspect)
	for _, role := range []leadrole.Role{defined, started} {
		rr := r.mustSettle(role)
		if rr.Action != ActionUnchanged || rr.Reason != reasonNoPendingIntent {
			t.Fatalf("%s: report %+v", role, rr)
		}
	}
	rr := r.mustSettle(tmux)
	if rr.Action != ActionNotManaged {
		t.Fatalf("report %+v", rr)
	}
	if r.revision() != rev || r.callsOf(leadunit.MethodInspect) != inspects {
		t.Fatal("a role without a pending intent was looked at or changed")
	}
	r.noSignals()
	r.auditClean()
}

// A role that is unknown because the registry's journal was cut keeps its
// state; only its intent is closed (here: the boot changed), exactly as in
// Reconcile, and nothing is settled before Reconcile has acted on the cut.
func TestSettleLaunchKeepsARegistryRecoveredRoleUnknown(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	_, err := r.reg.BeginLaunchWith(role, leadregistry.LaunchOptions{BootIdentity: r.fake.BootID()})
	noErr(t, err)
	r.cutJournal()
	r.fake.Reboot()

	// Before Reconcile has acted on the cut.
	rev := r.revision()
	rr, err := r.settle(role)
	isErr(t, err, ErrNotReconciled)
	if rr.Action != ActionUnchanged || r.revision() != rev {
		t.Fatalf("report %+v", rr)
	}

	rr, _ = r.reconcile().Find(role)
	if rr.Reason != codeRegistryRecov {
		t.Fatalf("report %+v", rr)
	}
	// Reconcile closed the intent of a changed boot; with a pending intent that
	// the window holds, SettleLaunch does not move the role either.
	r.advance(pastTheWindow)
	rr = r.mustSettle(role)
	if rr.Action != ActionUnchanged || rr.Reason != reasonNoPendingIntent {
		t.Fatalf("report %+v", rr)
	}
	r.expectState(role, leadregistry.StateUnknown, codeRegistryRecov)
}

func TestSettleLaunchOfARegistryRecoveredRoleWithAPendingIntentKeepsItsState(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	_, err := r.reg.BeginLaunchWith(role, leadregistry.LaunchOptions{BootIdentity: r.fake.BootID()})
	noErr(t, err)
	r.cutJournal()
	rr, _ := r.reconcile().Find(role)
	if rr.Reason != codeRegistryRecov {
		t.Fatalf("report %+v", rr)
	}
	r.advance(pastTheWindow)
	rr = r.mustSettle(role)
	if rr.Action != ActionAbandoned {
		t.Fatalf("report %+v", rr)
	}
	e := r.expectState(role, leadregistry.StateUnknown, codeRegistryRecov)
	if e.Intent.Outcome != leadregistry.OutcomeAbandoned {
		t.Fatalf("entry %+v", e)
	}
	r.startsNothing(0)
}
