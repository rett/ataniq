package leadlifecycle

import (
	"strings"
	"testing"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// replyLostRole returns a role whose start lost its reply with nothing loaded:
// the intent is pending and the role shown unknown.
func replyLostRole(t *testing.T, r *rig) leadrole.Role {
	t.Helper()
	role := r.define("alpha")
	r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
		r.fake.DropNextStart(unitOf(rl))
	}
	_, err := r.start(role)
	isErr(t, err, ErrLaunchUncertain)
	r.holder.hook = nil
	e := r.get(role)
	if e.Intent.Outcome != leadregistry.OutcomePending || e.Shown.Reason.Code != "launch-reply-lost" {
		t.Fatalf("entry %+v", e)
	}
	return role
}

func TestEveryLaunchJournalsTheBootIdentity(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	e := r.mustStart(role)
	if e.Intent.BootIdentity == "" || e.Intent.BootIdentity != r.fake.BootID() {
		t.Fatalf("intent boot identity %q, boot %q", e.Intent.BootIdentity, r.fake.BootID())
	}
	// A launch after a reboot journals the new boot.
	r.fake.Reboot()
	r.reconcile()
	e = r.mustStart(role)
	if e.Intent.BootIdentity != r.fake.BootID() || e.Epoch != 2 {
		t.Fatalf("entry %+v", e)
	}
}

func TestBootChangeSettlesALostReplyAtOnce(t *testing.T) {
	t.Run("Reconcile", func(t *testing.T) {
		r := newRig(t)
		role := replyLostRole(t, r)
		was := r.fake.BootID()
		now := r.fake.Reboot()
		// No time has passed: the window is open.
		rep := r.reconcile()
		rr, _ := rep.Find(role)
		if rr.Action != ActionStopped || rr.Reason != "launch-abandoned" || rr.After != leadregistry.StateStopped {
			t.Fatalf("report %+v", rr)
		}
		e := r.expectState(role, leadregistry.StateStopped, "launch-abandoned")
		if e.Intent.Outcome != leadregistry.OutcomeAbandoned {
			t.Fatalf("entry %+v", e)
		}
		for _, want := range []string{was, now, e.Intent.ID} {
			if !strings.Contains(e.Shown.Proof, want) {
				t.Fatalf("proof %q lacks %q", e.Shown.Proof, want)
			}
		}
		r.noSignals()
		if e := r.mustStart(role); e.Epoch != 2 {
			t.Fatalf("epoch %d", e.Epoch)
		}
		r.auditClean()
	})
	t.Run("Stop", func(t *testing.T) {
		r := newRig(t)
		role := replyLostRole(t, r)
		r.fake.Reboot()
		e, err := r.stop(role)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped || e.Intent.Outcome != leadregistry.OutcomeAbandoned {
			t.Fatalf("entry %+v", e)
		}
		r.noSignals()
	})
	t.Run("the same boot waits for the window", func(t *testing.T) {
		r := newRig(t)
		role := replyLostRole(t, r)
		rev := r.revision()
		rr, _ := r.reconcile().Find(role)
		if rr.Action != ActionPending || r.revision() != rev {
			t.Fatalf("report %+v", rr)
		}
		_, err := r.stop(role)
		isErr(t, err, ErrIntentPending)
	})
	t.Run("a late request of the earlier boot is gone with it", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.LateNextStart(unitOf(rl))
		}
		_, err := r.start(role)
		isErr(t, err, ErrLaunchUncertain)
		r.fake.Reboot()
		rr, _ := r.reconcile().Find(role)
		if rr.Action != ActionStopped {
			t.Fatalf("report %+v", rr)
		}
	})
}

func TestBootChangeSettlesAnIntentNoStartWasMadeFor(t *testing.T) {
	// The daemon died between the intent and the unit call.
	r := newRig(t)
	role := r.define("alpha")
	_, err := r.reg.BeginLaunchWith(role, leadregistry.LaunchOptions{BootIdentity: r.fake.BootID()})
	noErr(t, err)
	r.restart()
	r.fake.Reboot()
	rr, _ := r.reconcile().Find(role)
	if rr.Action != ActionStopped || rr.Reason != "launch-abandoned" {
		t.Fatalf("report %+v", rr)
	}
	r.expectState(role, leadregistry.StateStopped, "launch-abandoned")
	r.startsNothing(0)
}

func TestAnIntentWithoutABootIdentityWaitsForTheWindow(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	_, err := r.reg.BeginLaunch(role)
	noErr(t, err)
	r.restart()
	r.fake.Reboot()
	rr, _ := r.reconcile().Find(role)
	if rr.Action != ActionPending {
		t.Fatalf("report %+v", rr)
	}
	r.advance(pastTheWindow)
	rr, _ = r.reconcile().Find(role)
	if rr.Action != ActionStopped {
		t.Fatalf("report %+v", rr)
	}
}

func TestBootChangeClosesAnIntentOfACutRoleWithoutMovingIt(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	_, err := r.reg.BeginLaunchWith(role, leadregistry.LaunchOptions{BootIdentity: r.fake.BootID()})
	noErr(t, err)
	r.cutJournal()
	r.fake.Reboot()
	rep := r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Reason != "registry-recovered" || rr.After != leadregistry.StateUnknown {
		t.Fatalf("report %+v", rr)
	}
	if e := r.get(role); e.Intent.Outcome != leadregistry.OutcomeAbandoned || e.Shown.State != leadregistry.StateUnknown {
		t.Fatalf("entry %+v", e)
	}
	// The intent is closed, so the owner can decide.
	_, err = r.reg.ResolveUnknown(role, leadregistry.OwnerDecision{Epoch: 1, State: leadregistry.StateStopped,
		Reason: leadregistry.Reason{Code: "owner"}, Decision: "the host rebooted"})
	noErr(t, err)
}
