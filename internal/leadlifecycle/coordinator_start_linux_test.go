package leadlifecycle

import (
	"context"
	"errors"
	"testing"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// defineCoord defines the coordinator of a feature of a project, run by
// ataniq, with the default profile.
func (r *rig) defineCoord(project, feature string) leadrole.Role {
	r.t.Helper()
	role, err := leadrole.Coordinator(project, feature)
	noErr(r.t, err)
	_, err = r.reg.Define(role, leadregistry.ManagerAtaniq, leadregistry.Requested{})
	noErr(r.t, err)
	return role
}

// coordRig is a started lead and the defined coordinator of one feature.
func coordRig(t *testing.T) (r *rig, lead, coord leadrole.Role) {
	t.Helper()
	r = newRig(t)
	lead = r.define("alpha")
	coord = r.defineCoord("alpha", "feat")
	r.mustStart(lead)
	return r, lead, coord
}

func TestCoordinatorStartsBoundToItsLead(t *testing.T) {
	r, lead, coord := coordRig(t)
	e := r.mustStart(coord)
	if e.Epoch != 1 || e.Intent.ParentEpoch != 1 || e.Intent.Identity != "alpha-lead@e1:coord:feat:1" {
		t.Fatalf("entry %+v intent %+v", e, e.Intent)
	}
	specs := r.fake.StartedSpecs()
	if len(specs) != 2 {
		t.Fatalf("%d units started", len(specs))
	}
	if sp := specs[1]; sp.Unit != "ataniq-coord-alpha_feat.service" || sp.Parent != unitOf(lead) || sp.Epoch != 1 || sp.Intent != e.Intent.ID {
		t.Fatalf("spec %+v", sp)
	}
	if specs[0].Parent != "" {
		t.Fatalf("the lead is bound to %q", specs[0].Parent)
	}
	// The holder was built for the coordinator, with the intent that carries
	// its identity.
	if in := r.holder.seen[1]; in.Identity != e.Intent.Identity || in.ParentEpoch != 1 {
		t.Fatalf("holder saw %+v", in)
	}
	if e.Intent.BootIdentity != r.fake.BootID() {
		t.Fatalf("boot identity %q", e.Intent.BootIdentity)
	}
	if got, err := r.fake.Inspect(context.Background(), unitOf(coord)); err != nil || got.Parent != unitOf(lead) {
		t.Fatalf("inspect %+v %v", got, err)
	}
	r.auditClean()
}

func TestCoordinatorStartIsRefusedWithoutALiveParent(t *testing.T) {
	refused := func(t *testing.T, r *rig, coord leadrole.Role, want error) {
		t.Helper()
		before := r.get(coord)
		rev := r.revision()
		starts := r.callsOf(leadunit.MethodStart)
		checks := r.pre.calls
		_, err := r.start(coord)
		isErr(t, err, ErrParentNotLive)
		isErr(t, err, want)
		isErr(t, err, leadregistry.ErrLaunchRefused)
		if r.revision() != rev || r.callsOf(leadunit.MethodStart) != starts || r.get(coord).Desired != before.Desired {
			t.Fatalf("a refused start changed something: revision %d to %d", rev, r.revision())
		}
		if r.pre.calls != checks {
			t.Fatal("the preflight ran for a refused start")
		}
	}
	t.Run("the lead is not defined", func(t *testing.T) {
		r := newRig(t)
		coord := r.defineCoord("alpha", "feat")
		refused(t, r, coord, leadregistry.ErrLaunchNoParent)
	})
	t.Run("the lead is run by the tmux-manager", func(t *testing.T) {
		r := newRig(t)
		r.defineTmux("alpha")
		coord := r.defineCoord("alpha", "feat")
		refused(t, r, coord, leadregistry.ErrLaunchParentManager)
	})
	t.Run("the lead was never started", func(t *testing.T) {
		r := newRig(t)
		r.define("alpha")
		coord := r.defineCoord("alpha", "feat")
		refused(t, r, coord, leadregistry.ErrLaunchParentState)
	})
	t.Run("the lead is stopped", func(t *testing.T) {
		r, lead, coord := coordRig(t)
		_, err := r.stop(lead)
		noErr(t, err)
		refused(t, r, coord, leadregistry.ErrLaunchParentState)
	})
	t.Run("the lead is unknown", func(t *testing.T) {
		r, lead, coord := coordRig(t)
		_, err := r.reg.SetState(lead, leadregistry.StateChange{Epoch: 1, State: leadregistry.StateUnknown,
			Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test"}})
		noErr(t, err)
		refused(t, r, coord, leadregistry.ErrLaunchParentState)
	})
}

func TestCoordinatorStartWithTheParentGoneBeforeTheUnitStart(t *testing.T) {
	// The registry still shows the lead starting when the coordinator is
	// looked at; the lead's unit dies before the coordinator's unit is
	// started. How the manager answers is not known, so both are tried.
	for name, outcome := range map[string]leadunit.MissingParent{
		"never active": leadunit.MissingParentNeverActive,
		"refused":      leadunit.MissingParentRefused,
	} {
		t.Run(name, func(t *testing.T) {
			r, lead, coord := coordRig(t)
			r.fake.SetMissingParentOutcome(outcome)
			r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
				if rl == coord {
					r.fake.Crash(unitOf(lead))
				}
			}
			e, err := r.start(coord)
			isErr(t, err, ErrStartFailed)
			if e.Intent.Outcome != leadregistry.OutcomeFailed {
				t.Fatalf("entry %+v", e)
			}
			// No unit, and the absence is proven.
			r.expectState(coord, leadregistry.StateStopped, "")
			if r.fake.Loaded(unitOf(coord)) || len(r.fake.StartedSpecs()) != 1 {
				t.Fatalf("a unit was started: %+v", r.fake.StartedSpecs())
			}
			if r.callsOf(leadunit.MethodStart) != 2 {
				t.Fatalf("%d Start calls", r.callsOf(leadunit.MethodStart))
			}
			r.auditClean()
		})
	}
}

func TestCoordinatorStartFailurePaths(t *testing.T) {
	t.Run("the manager refuses", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.fake.FailNextStart(unitOf(coord), errors.New("scripted"))
		e, err := r.start(coord)
		isErr(t, err, ErrStartFailed)
		if e.Intent.Outcome != leadregistry.OutcomeFailed {
			t.Fatalf("entry %+v", e)
		}
		r.expectState(coord, leadregistry.StateStopped, "launch-failed")
		// And the instance after it has the next number.
		e = r.mustStart(coord)
		if e.Epoch != 2 || e.Intent.Identity != "alpha-lead@e1:coord:feat:2" {
			t.Fatalf("entry %+v", e)
		}
	})
	t.Run("the reply is lost and the unit runs", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.fake.LoseNextStartReply(unitOf(coord))
		e, err := r.start(coord)
		noErr(t, err)
		if e.Intent.Outcome != leadregistry.OutcomeAdopted || e.Shown.State != leadregistry.StateStarting {
			t.Fatalf("entry %+v", e)
		}
		if r.callsOf(leadunit.MethodStart) != 2 {
			t.Fatal("Start was retried")
		}
	})
	t.Run("the reply is lost and nothing runs", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.fake.DropNextStart(unitOf(coord))
		e, err := r.start(coord)
		isErr(t, err, ErrLaunchUncertain)
		if e.Intent.Outcome != leadregistry.OutcomePending || e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("entry %+v", e)
		}
		r.advance(pastTheWindow)
		rr, _ := r.reconcile().Find(coord)
		if rr.Action != ActionStopped {
			t.Fatalf("report %+v", rr)
		}
		r.expectState(coord, leadregistry.StateStopped, "launch-abandoned")
	})
	t.Run("the unit name is taken", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			if rl == coord {
				noErr(t, r.fake.AddForeign(unitOf(rl), "intruder"))
			}
		}
		e, err := r.start(coord)
		isErr(t, err, ErrStartFailed)
		isErr(t, err, ErrForeignUnit)
		if e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "unit-exists" {
			t.Fatalf("entry %+v", e)
		}
	})
	t.Run("the command cannot be built", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {}
		r.holder.err = errors.New("no holder")
		e, err := r.start(coord)
		isErr(t, err, ErrStartFailed)
		if e.Intent.Outcome != leadregistry.OutcomeAbandoned {
			t.Fatalf("entry %+v", e)
		}
		r.expectState(coord, leadregistry.StateStopped, "holder-command-failed")
	})
	t.Run("the preflight refuses", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.pre.err = errors.New("no login")
		rev := r.revision()
		_, err := r.start(coord)
		isErr(t, err, ErrPreflight)
		if r.revision() != rev {
			t.Fatal("the registry changed")
		}
	})
}

func TestCoordinatorReplacementRuleAppliesToItsOwnUnit(t *testing.T) {
	r, _, coord := coordRig(t)
	r.mustStart(coord)
	r.fake.Crash(unitOf(coord))
	r.fake.LeaveControlGroup(unitOf(coord), true)
	// Not shown ended yet: Reconcile proves it and a group left behind is not
	// proof.
	rr, _ := r.reconcile().Find(coord)
	if rr.Action != ActionUnknown || rr.Reason != "control-group-not-proven-empty" {
		t.Fatalf("report %+v", rr)
	}
	starts := r.callsOf(leadunit.MethodStart)
	_, err := r.start(coord)
	isErr(t, err, leadregistry.ErrLaunchRefused)
	r.fake.LeaveControlGroup(unitOf(coord), false)
	rr, _ = r.reconcile().Find(coord)
	if rr.Action != ActionEnded {
		t.Fatalf("report %+v", rr)
	}
	r.fake.LeaveControlGroup(unitOf(coord), true)
	_, err = r.start(coord)
	isErr(t, err, ErrNoProof)
	if r.callsOf(leadunit.MethodStart) != starts {
		t.Fatal("a unit was started without the proof")
	}
	r.fake.LeaveControlGroup(unitOf(coord), false)
	if e := r.mustStart(coord); e.Epoch != 2 {
		t.Fatalf("epoch %d", e.Epoch)
	}
	r.auditClean()
}

func TestLeadIsNotStartedWhileACoordinatorIsNotProvenStopped(t *testing.T) {
	r, lead, coord := coordRig(t)
	r.mustStart(coord)
	// The lead is recorded stopped by the owner's own means while the
	// coordinator still runs.
	_, err := r.reg.SetState(lead, leadregistry.StateChange{Epoch: 1, State: leadregistry.StateUnknown,
		Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test"}})
	noErr(t, err)
	_, err = r.reg.ResolveUnknown(lead, leadregistry.OwnerDecision{Epoch: 1, State: leadregistry.StateStopped,
		Reason: leadregistry.Reason{Code: "owner"}, Decision: "the lead is gone"})
	noErr(t, err)
	rev := r.revision()
	desired := r.get(lead).Desired
	_, err = r.start(lead)
	isErr(t, err, ErrCoordinatorNotStopped)
	isErr(t, err, leadregistry.ErrLaunchCoordinator)
	isErr(t, err, leadregistry.ErrLaunchRefused)
	if r.revision() != rev || r.get(lead).Desired != desired {
		t.Fatal("a refused start changed the registry")
	}
}
