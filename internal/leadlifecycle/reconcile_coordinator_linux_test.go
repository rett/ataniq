package leadlifecycle

import (
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// settled moves the fake clock until the unit is no longer loaded, for a unit
// that a stop of its parent is bringing down. It does not depend on when the
// fake stops a bound unit relative to its parent.
func (r *rig) settled(unit string) {
	r.t.Helper()
	for i := 0; i < 20 && r.fake.Loaded(unit); i++ {
		r.advance(time.Second)
	}
	if r.fake.Loaded(unit) {
		r.t.Fatalf("%s is still loaded", unit)
	}
}

func TestReconcileDaemonRestartWithALiveLeadAndALiveCoordinator(t *testing.T) {
	r, lead, coord := coordRig(t)
	r.mustStart(coord)
	r.setShown(coord, leadregistry.StateReady)
	starts := r.callsOf(leadunit.MethodStart)
	r.restart()
	rep := r.reconcile()
	for _, role := range []leadrole.Role{lead, coord} {
		rr, ok := rep.Find(role)
		if !ok || rr.Action != ActionAdopted || rr.Reason != "adopted" {
			t.Fatalf("%s: report %+v", role, rr)
		}
	}
	rr, _ := rep.Find(coord)
	if rr.After != leadregistry.StateReady || rr.Kind != leadrole.KindCoordinator || rr.Feature != "feat" || rr.Parent != lead {
		t.Fatalf("report %+v", rr)
	}
	if rr, _ := rep.Find(lead); rr.Kind != leadrole.KindLead || rr.Feature != "" || !rr.Parent.IsZero() {
		t.Fatalf("report %+v", rr)
	}
	r.startsNothing(starts)
	// The report is in the registry's order, coordinators included.
	var got []string
	for _, x := range rep.Roles {
		got = append(got, x.Role.String())
	}
	if strings.Join(got, " ") != "alpha/coord/feat alpha/lead" {
		t.Fatalf("report order %v", got)
	}
	r.auditClean()
}

func TestReconcileRebootWithALeadAndTwoCoordinators(t *testing.T) {
	r, lead, ca, cb := twoCoordinators(t, leadunit.ProcScript{}, leadunit.ProcScript{})
	r.setShown(cb, leadregistry.StateReady)
	starts := r.callsOf(leadunit.MethodStart)
	r.fake.Reboot()
	rep := r.reconcile()
	for _, role := range []leadrole.Role{lead, ca, cb} {
		rr, ok := rep.Find(role)
		if !ok || rr.Action != ActionStopped || rr.Reason != "host-rebooted" || rr.After != leadregistry.StateStopped {
			t.Fatalf("%s: report %+v", role, rr)
		}
		r.expectState(role, leadregistry.StateStopped, "host-rebooted")
	}
	r.startsNothing(starts)
	// Nothing starts by itself, and the lead can start again with a new epoch.
	if e := r.mustStart(lead); e.Epoch != 2 {
		t.Fatalf("lead epoch %d", e.Epoch)
	}
	r.auditClean()
}

func TestReconcileParentCrash(t *testing.T) {
	r, lead, ca, cb := twoCoordinators(t, leadunit.ProcScript{}, leadunit.ProcScript{})
	starts := r.callsOf(leadunit.MethodStart)
	signals := len(r.ownSignals())
	if !r.fake.Crash(unitOf(lead)) {
		t.Fatal("no lead unit")
	}
	// The manager stops the bound units; wait for the final state.
	r.settled(unitOf(ca))
	r.settled(unitOf(cb))
	rep := r.reconcile()
	if rr, _ := rep.Find(lead); rr.Action != ActionEnded || rr.Reason != "holder-gone" {
		t.Fatalf("lead: report %+v", rr)
	}
	for _, co := range []leadrole.Role{ca, cb} {
		rr, _ := rep.Find(co)
		if rr.Action != ActionEnded || rr.Reason != "parent-epoch-ended" || rr.After != leadregistry.StateEnded {
			t.Fatalf("%s: report %+v", co, rr)
		}
		e := r.expectState(co, leadregistry.StateEnded, "parent-epoch-ended")
		if !strings.Contains(e.Shown.Proof, "epoch 1") {
			t.Fatalf("proof %q", e.Shown.Proof)
		}
	}
	if r.callsOf(leadunit.MethodStart) != starts || len(r.ownSignals()) != signals {
		t.Fatal("Reconcile started or signalled")
	}
	// The lead starts a new epoch: its coordinators are proven stopped.
	if e := r.mustStart(lead); e.Epoch != 2 {
		t.Fatalf("lead epoch %d", e.Epoch)
	}
	if e := r.mustStart(ca); e.Epoch != 2 || e.Intent.Identity != "alpha-lead@e2:coord:aaa:2" || e.Intent.ParentEpoch != 2 {
		t.Fatalf("entry %+v", e)
	}
	r.auditClean()
}

func TestReconcileOrphanCoordinatorWhoseUnitIsStillLoaded(t *testing.T) {
	setup := func(t *testing.T) (*rig, leadrole.Role, leadrole.Role) {
		r, lead, coord := coordRig(t)
		r.fake.SetProcs(unitOf(coord), leadunit.ProcScript{Behavior: leadunit.IgnoreTerm})
		r.mustStart(coord)
		r.fake.Crash(unitOf(lead))
		if !r.fake.Loaded(unitOf(coord)) {
			t.Fatal("the coordinator's unit was gone at once")
		}
		starts := r.callsOf(leadunit.MethodStart)
		own := len(r.ownSignals())
		rep := r.reconcile()
		if rr, _ := rep.Find(lead); rr.Action != ActionEnded {
			t.Fatalf("lead: report %+v", rr)
		}
		rr, _ := rep.Find(coord)
		if rr.Action != ActionUnknown || rr.Reason != "orphan-coordinator" || rr.After != leadregistry.StateUnknown {
			t.Fatalf("report %+v", rr)
		}
		// Reconcile never signals.
		if r.callsOf(leadunit.MethodStart) != starts || len(r.ownSignals()) != own {
			t.Fatal("Reconcile started or signalled")
		}
		return r, lead, coord
	}
	t.Run("the manager ends it later", func(t *testing.T) {
		r, _, coord := setup(t)
		r.settled(unitOf(coord))
		rr, _ := r.reconcile().Find(coord)
		if rr.Action != ActionEnded || rr.Reason != "parent-epoch-ended" {
			t.Fatalf("report %+v", rr)
		}
		r.expectState(coord, leadregistry.StateEnded, "parent-epoch-ended")
	})
	t.Run("an explicit Stop ends it", func(t *testing.T) {
		r, _, coord := setup(t)
		e, err := r.stop(coord)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped || e.Shown.Proof == "" {
			t.Fatalf("entry %+v", e)
		}
		if r.fake.Loaded(unitOf(coord)) {
			t.Fatal("the unit is still there")
		}
	})
	t.Run("a start of the lead is refused until it is", func(t *testing.T) {
		r, lead, _ := setup(t)
		_, err := r.start(lead)
		isErr(t, err, ErrCoordinatorNotStopped)
	})
}

func TestReconcileACoordinatorWhoseOwnUnitDiedUnderALiveParent(t *testing.T) {
	r, lead, coord := coordRig(t)
	r.mustStart(coord)
	r.fake.Crash(unitOf(coord))
	rep := r.reconcile()
	rr, _ := rep.Find(coord)
	if rr.Action != ActionEnded || rr.Reason != "holder-gone" {
		t.Fatalf("report %+v", rr)
	}
	if rr, _ := rep.Find(lead); rr.Action != ActionAdopted {
		t.Fatalf("lead: report %+v", rr)
	}
	// Instance 2 under the same lead epoch.
	if e := r.mustStart(coord); e.Epoch != 2 || e.Intent.ParentEpoch != 1 {
		t.Fatalf("entry %+v", e)
	}
}

func TestReconcileCoordinatorsAreJudgedLikeLeadsOtherwise(t *testing.T) {
	t.Run("a foreign unit takes its name", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		r.fake.Crash(unitOf(coord))
		noErr(t, r.fake.AddForeign(unitOf(coord), "someone else's unit"))
		rr, _ := r.reconcile().Find(coord)
		if rr.Action != ActionUnknown || rr.Reason != "foreign-unit" {
			t.Fatalf("report %+v", rr)
		}
		r.noSignals()
	})
	t.Run("a stopped coordinator with a unit of its name loaded", func(t *testing.T) {
		r, _, coord := coordRig(t)
		noErr(t, r.fake.AddForeign(unitOf(coord), "someone else's unit"))
		rr, _ := r.reconcile().Find(coord)
		if rr.Action != ActionUnknown || rr.Reason != "competing-unit" {
			t.Fatalf("report %+v", rr)
		}
	})
	t.Run("the manager does not answer", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		r.fake.MakeUnavailable(leadunit.MethodInspect, 3)
		rev := r.revision()
		rep := r.reconcile()
		if rr, _ := rep.Find(coord); rr.Action != ActionUnavailable {
			t.Fatalf("report %+v", rr)
		}
		if r.revision() != rev {
			t.Fatal("the registry changed")
		}
	})
	t.Run("a pending launch is settled by the window", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.fake.DropNextStart(unitOf(coord))
		_, err := r.start(coord)
		isErr(t, err, ErrLaunchUncertain)
		if rr, _ := r.reconcile().Find(coord); rr.Action != ActionPending {
			t.Fatalf("report %+v", rr)
		}
		r.advance(pastTheWindow)
		if rr, _ := r.reconcile().Find(coord); rr.Action != ActionStopped {
			t.Fatalf("report %+v", rr)
		}
	})
}

// The parent of a coordinator has ended when its epoch is not the one the
// coordinator recorded. The registry does not let that state be reached with a
// live coordinator, so the rule is checked on the record itself.
func TestParentEndedByEpoch(t *testing.T) {
	r, lead, coord := coordRig(t)
	e := r.mustStart(coord)
	if why := r.ctl.parentEnded(e); why != "" {
		t.Fatalf("parent ended: %s", why)
	}
	_, err := r.stop(lead)
	noErr(t, err)
	r.mustStart(lead) // epoch 2
	why := r.ctl.parentEnded(e)
	if !strings.Contains(why, "epoch 2") || !strings.Contains(why, "epoch 1") {
		t.Fatalf("parent ended: %q", why)
	}
	// A parent at the same epoch that is not live has ended too.
	r2, lead2, coord2 := coordRig(t)
	e2 := r2.mustStart(coord2)
	r2.setShown(lead2, leadregistry.StateStopping)
	if why := r2.ctl.parentEnded(e2); !strings.Contains(why, "stopping") {
		t.Fatalf("parent ended: %q", why)
	}
}
