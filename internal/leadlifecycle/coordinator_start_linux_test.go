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
	r.enroll(project)
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
	// A lead whose own launch failed or was given up never ran as that epoch,
	// even while the record still shows it starting: the pre-check applies the
	// registry's rule, so the refusal costs no journal line.
	for name, outcome := range map[string]leadregistry.Outcome{
		"failed":    leadregistry.OutcomeFailed,
		"abandoned": leadregistry.OutcomeAbandoned,
	} {
		t.Run("the launch of the lead "+name, func(t *testing.T) {
			r := newRig(t)
			lead := r.define("alpha")
			coord := r.defineCoord("alpha", "feat")
			in, err := r.reg.BeginLaunch(lead)
			noErr(t, err)
			_, err = r.reg.RecordOutcome(lead, in.ID, in.Epoch, outcome, leadregistry.UnitEvidence{})
			noErr(t, err)
			if got := r.get(lead); got.Shown.State != leadregistry.StateStarting || got.Intent.Outcome != outcome {
				t.Fatalf("lead %+v", got)
			}
			refused(t, r, coord, leadregistry.ErrLaunchParentState)
		})
	}
	t.Run("the launch of the lead is pending or launched", func(t *testing.T) {
		// Only failed and abandoned make the parent not live.
		r := newRig(t)
		lead := r.define("alpha")
		coord := r.defineCoord("alpha", "feat")
		_, err := r.reg.BeginLaunch(lead)
		noErr(t, err)
		// The pre-check admits it and the registry journals the launch; no
		// unit of the lead was started in this rig, so the manager's start
		// under it comes to nothing, which is another case.
		_, err = r.start(coord)
		if errors.Is(err, ErrParentNotLive) {
			t.Fatalf("refused as not live: %v", err)
		}
		if e := r.get(coord); e.Epoch != 1 || e.Intent == nil || e.Intent.ParentEpoch != 1 {
			t.Fatalf("entry %+v", e)
		}
	})
}

func TestCoordinatorStartWithTheParentGoneBeforeTheUnitStart(t *testing.T) {
	// The registry still shows the lead starting when the coordinator is
	// looked at; the lead's unit dies before the coordinator's unit is
	// started. How the manager answers is not known to the lifecycle, so both
	// are tried; they end differently, because only the first is an answer.
	crashLead := func(r *rig, lead, coord leadrole.Role) {
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			if rl == coord {
				r.fake.Crash(unitOf(lead))
			}
		}
	}
	t.Run("never active", func(t *testing.T) {
		// The manager accepts the request and the unit never becomes active:
		// an answer, so nothing was started and the absence is proven.
		r, lead, coord := coordRig(t)
		r.fake.SetMissingParentOutcome(leadunit.MissingParentNeverActive)
		crashLead(r, lead, coord)
		e, err := r.start(coord)
		isErr(t, err, ErrStartFailed)
		if e.Intent.Outcome != leadregistry.OutcomeFailed {
			t.Fatalf("entry %+v", e)
		}
		r.expectState(coord, leadregistry.StateStopped, "")
		if r.fake.Loaded(unitOf(coord)) || len(r.fake.StartedSpecs()) != 1 {
			t.Fatalf("a unit was started: %+v", r.fake.StartedSpecs())
		}
		if r.callsOf(leadunit.MethodStart) != 2 {
			t.Fatalf("%d Start calls", r.callsOf(leadunit.MethodStart))
		}
		r.auditClean()
	})
	t.Run("refused", func(t *testing.T) {
		// The manager's error is a lost reply that names the missing parent.
		// The real manager cannot tell a refusal from a request that may still
		// land, so the lifecycle must not either: the intent stays pending,
		// the role is unknown, and the way forward is the one of every lost
		// reply, a proof after the late-start window.
		r, lead, coord := coordRig(t)
		r.fake.SetMissingParentOutcome(leadunit.MissingParentRefused)
		crashLead(r, lead, coord)
		e, err := r.start(coord)
		isErr(t, err, ErrLaunchUncertain)
		isErr(t, err, leadunit.ErrReplyLost)
		if errors.Is(err, ErrStartFailed) {
			t.Fatalf("a lost reply was reported as a start that failed: %v", err)
		}
		if e.Intent == nil || e.Intent.Outcome != leadregistry.OutcomePending {
			t.Fatalf("entry %+v", e)
		}
		r.expectState(coord, leadregistry.StateUnknown, "launch-reply-lost")
		if r.fake.Loaded(unitOf(coord)) || len(r.fake.StartedSpecs()) != 1 {
			t.Fatalf("a unit was started: %+v", r.fake.StartedSpecs())
		}
		if r.callsOf(leadunit.MethodStart) != 2 {
			t.Fatalf("%d Start calls", r.callsOf(leadunit.MethodStart))
		}

		// Another Start is refused while the intent is pending, and nothing is
		// retried.
		_, err = r.start(coord)
		isErr(t, err, leadregistry.ErrLaunchPending)
		if r.callsOf(leadunit.MethodStart) != 2 {
			t.Fatal("Start was retried")
		}

		// Before the window closes Reconcile and Stop prove nothing: the
		// request could still land.
		rep := r.reconcile()
		if rr, _ := rep.Find(coord); rr.Action != ActionPending {
			t.Fatalf("report %+v", rr)
		}
		// The lead's own unit is gone, and that pass ended it.
		r.expectState(lead, leadregistry.StateEnded, "")
		r.expectState(coord, leadregistry.StateUnknown, "launch-reply-lost")
		_, err = r.stop(coord)
		isErr(t, err, ErrIntentPending)
		r.expectState(coord, leadregistry.StateUnknown, "launch-reply-lost")

		// The parent is gone, the unit is not loaded: after the window the
		// role reaches a proven state with the intent abandoned.
		r.advance(pastTheWindow)
		rep = r.reconcile()
		rr, _ := rep.Find(coord)
		if rr.Action != ActionStopped || rr.Reason != "launch-abandoned" {
			t.Fatalf("report %+v", rr)
		}
		e = r.expectState(coord, leadregistry.StateStopped, "launch-abandoned")
		if e.Intent.Outcome != leadregistry.OutcomeAbandoned {
			t.Fatalf("entry %+v", e)
		}
		// Nothing was started or signalled to get there, and the coordinator is
		// not started again under an ended parent.
		r.startsNothing(2)
		_, err = r.start(coord)
		isErr(t, err, ErrParentNotLive)
		r.auditClean()
	})
	t.Run("refused, then Stop", func(t *testing.T) {
		// The same lost reply, resolved by an explicit Stop after the window
		// and not by Reconcile.
		r, lead, coord := coordRig(t)
		r.fake.SetMissingParentOutcome(leadunit.MissingParentRefused)
		crashLead(r, lead, coord)
		_, err := r.start(coord)
		isErr(t, err, ErrLaunchUncertain)
		r.advance(pastTheWindow)
		e, err := r.stop(coord)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped || e.Intent.Outcome != leadregistry.OutcomeAbandoned {
			t.Fatalf("entry %+v", e)
		}
		r.expectState(coord, leadregistry.StateStopped, "launch-abandoned")
		r.startsNothing(2)
		r.auditClean()
	})
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

func TestStartRefusedByTheRegistryAfterThePreCheck(t *testing.T) {
	// The pre-checks of the controller passed; between them and the launch
	// (the preflight is the moment) another operation changed another role.
	// The registry's refusal is mapped, and the role stays as it was.
	unchanged := func(t *testing.T, r *rig, role leadrole.Role, before leadregistry.Entry) {
		t.Helper()
		got := r.get(role)
		if got.Desired != before.Desired || got.Epoch != before.Epoch || got.Shown != before.Shown || (got.Intent == nil) != (before.Intent == nil) {
			t.Fatalf("a refused start changed the role: %+v, was %+v", got, before)
		}
		if r.callsOf(leadunit.MethodStart) != 1 {
			t.Fatalf("%d Start calls", r.callsOf(leadunit.MethodStart))
		}
	}
	t.Run("the parent stops being live", func(t *testing.T) {
		r, lead, coord := coordRig(t)
		before := r.get(coord)
		if before.Desired != leadregistry.DesiredStopped {
			t.Fatalf("a coordinator is defined wanted %s", before.Desired)
		}
		r.pre.hook = func(rl leadrole.Role) {
			if rl == coord {
				r.setShown(lead, leadregistry.StateUnknown)
			}
		}
		_, err := r.start(coord)
		isErr(t, err, ErrParentNotLive)
		isErr(t, err, leadregistry.ErrLaunchParentState)
		isErr(t, err, leadregistry.ErrLaunchRefused)
		unchanged(t, r, coord, before)
		// The parent is unknown now, and the pre-check refuses without any
		// write.
		r.pre.hook = nil
		rev := r.revision()
		_, err = r.start(coord)
		isErr(t, err, ErrParentNotLive)
		if r.revision() != rev {
			t.Fatal("the refusal at the pre-check wrote")
		}
		r.auditClean()
	})
	t.Run("a coordinator of the lead stops being proven stopped", func(t *testing.T) {
		r, lead, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.stop(lead)
		noErr(t, err)
		before := r.get(lead)
		before.Desired = leadregistry.DesiredStopped
		r.pre.hook = func(rl leadrole.Role) {
			if rl == lead {
				r.setShown(coord, leadregistry.StateUnknown)
			}
		}
		starts := r.callsOf(leadunit.MethodStart)
		_, err = r.start(lead)
		isErr(t, err, ErrCoordinatorNotStopped)
		isErr(t, err, leadregistry.ErrLaunchCoordinator)
		isErr(t, err, leadregistry.ErrLaunchRefused)
		got := r.get(lead)
		if got.Desired != leadregistry.DesiredStopped || got.Epoch != before.Epoch || got.Shown.State != leadregistry.StateStopped {
			t.Fatalf("lead %+v", got)
		}
		if r.callsOf(leadunit.MethodStart) != starts {
			t.Fatal("a unit was started")
		}
		r.auditClean()
	})
}
