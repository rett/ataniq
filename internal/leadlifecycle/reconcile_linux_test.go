package leadlifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

func (r *rig) reconcile() Report {
	r.t.Helper()
	var rep Report
	var err error
	r.drive(func() { rep, err = r.ctl.Reconcile(context.Background()) })
	if err != nil {
		r.t.Fatalf("Reconcile: %v", err)
	}
	return rep
}

// startsNothing fails the test when the manager saw more Start calls than
// allowedStarts or any Signal at all: the tests that use it did all their
// stopping through the registry.
func (r *rig) startsNothing(allowedStarts int) {
	r.t.Helper()
	if n := r.callsOf(leadunit.MethodStart); n != allowedStarts {
		r.t.Fatalf("%d Start calls, want %d", n, allowedStarts)
	}
	r.noSignals()
}

func TestL12RebootWithThreeLeads(t *testing.T) {
	r := newRig(t)
	roles := []leadrole.Role{r.define("alpha"), r.define("beta"), r.define("gamma")}
	epochs := map[leadrole.Role]uint64{}
	for _, role := range roles {
		epochs[role] = r.mustStart(role).Epoch
	}
	// One of them is on its way out when the host goes down.
	if _, err := r.reg.SetState(roles[2], leadregistry.StateChange{Epoch: 1, State: leadregistry.StateStopping,
		Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "stop-requested"}}); err != nil {
		t.Fatal(err)
	}
	r.fake.Reboot()
	starts := r.callsOf(leadunit.MethodStart)
	rep := r.reconcile()
	for _, role := range roles {
		rr, ok := rep.Find(role)
		if !ok || rr.Action != ActionStopped || rr.Reason != "host-rebooted" || rr.After != leadregistry.StateStopped {
			t.Fatalf("%s: report %+v", role, rr)
		}
		e := r.expectState(role, leadregistry.StateStopped, "host-rebooted")
		if e.Epoch != epochs[role] || e.Intent.Epoch != epochs[role] {
			t.Fatalf("%s: epoch %d, was %d", role, e.Epoch, epochs[role])
		}
		if !strings.Contains(e.Shown.Proof, "boot identity changed") {
			t.Fatalf("%s: proof %q", role, e.Shown.Proof)
		}
	}
	// No lead starts by itself, and a second Reconcile changes nothing.
	r.startsNothing(starts)
	rev := r.revision()
	r.reconcile()
	if r.revision() != rev {
		t.Fatal("a second Reconcile wrote to the registry")
	}
	r.auditClean()
}

func TestReconcileHolderCrash(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	started := r.mustStart(role)
	r.fake.Crash(unitOf(role))
	rep := r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Action != ActionEnded || rr.Reason != "holder-gone" || rr.Before != leadregistry.StateStarting || rr.After != leadregistry.StateEnded {
		t.Fatalf("report %+v", rr)
	}
	e := r.expectState(role, leadregistry.StateEnded, "holder-gone")
	if !strings.Contains(e.Shown.Proof, started.Unit.InvocationID) || e.Epoch != 1 {
		t.Fatalf("entry %+v", e)
	}
	r.startsNothing(1)
}

func TestReconcileHolderCrashWithControlGroupLeft(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.mustStart(role)
	r.fake.Crash(unitOf(role))
	r.fake.LeaveControlGroup(unitOf(role), true)
	rep := r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Action != ActionUnknown || rr.Reason != "control-group-not-proven-empty" {
		t.Fatalf("report %+v", rr)
	}
	r.expectState(role, leadregistry.StateUnknown, "control-group-not-proven-empty")
	// The same finding twice writes nothing the second time.
	rev := r.revision()
	r.reconcile()
	if r.revision() != rev {
		t.Fatal("a repeated finding was journaled again")
	}
	// And it resolves when the group is proven empty.
	r.fake.LeaveControlGroup(unitOf(role), false)
	rep = r.reconcile()
	if rr, _ := rep.Find(role); rr.Action != ActionEnded {
		t.Fatalf("report %+v", rr)
	}
	r.startsNothing(1)
}

func TestReconcileDaemonRestartWithLiveHolder(t *testing.T) {
	for _, state := range []leadregistry.State{leadregistry.StateStarting, leadregistry.StateReady, leadregistry.StateDegraded, leadregistry.StateStopping} {
		t.Run(string(state), func(t *testing.T) {
			r := newRig(t)
			role := r.define("alpha")
			started := r.mustStart(role)
			if state != leadregistry.StateStarting {
				if _, err := r.reg.SetState(role, leadregistry.StateChange{Epoch: 1, State: state,
					Evidence: leadregistry.EvidenceProvider, Reason: leadregistry.Reason{Code: "test"}}); err != nil {
					t.Fatal(err)
				}
			}
			r.restart()
			rev := r.revision()
			rep := r.reconcile()
			rr, _ := rep.Find(role)
			if rr.Action != ActionAdopted || rr.After != state || rr.Before != state {
				t.Fatalf("report %+v", rr)
			}
			// Kept as it was: no journal line, never raised to ready.
			if r.revision() != rev {
				t.Fatal("adopting wrote to the registry")
			}
			if e := r.get(role); e.Unit.InvocationID != started.Unit.InvocationID || e.Epoch != 1 {
				t.Fatalf("entry %+v", e)
			}
			r.startsNothing(1)
			// And the holder it adopted can be stopped.
			e, err := r.stop(role)
			noErr(t, err)
			if e.Shown.State != leadregistry.StateStopped {
				t.Fatalf("entry %+v", e)
			}
		})
	}
}

func TestReconcileForeignAndStaleUnitsAreNeverSignalled(t *testing.T) {
	for _, tc := range []struct {
		name string
		put  func(r *rig, role leadrole.Role)
		code string
	}{
		{"foreign", func(r *rig, role leadrole.Role) { noErr(t, r.fake.AddForeign(unitOf(role), "foreign")) }, "foreign-unit"},
		{"other epoch", func(r *rig, role leadrole.Role) {
			noErr(t, r.fake.AddForeign(unitOf(role), "ataniq lead epoch=9 intent=zzz"))
		}, "stale-unit"},
	} {
		t.Run(tc.name+" instead of a live unit", func(t *testing.T) {
			r := newRig(t)
			role := r.define("alpha")
			r.mustStart(role)
			r.fake.Crash(unitOf(role))
			tc.put(r, role)
			rep := r.reconcile()
			rr, _ := rep.Find(role)
			if rr.Action != ActionUnknown || rr.Reason != tc.code || rr.After != leadregistry.StateUnknown {
				t.Fatalf("report %+v", rr)
			}
			r.startsNothing(1)
			// Still there: the same finding, no new line.
			rev := r.revision()
			r.reconcile()
			if r.revision() != rev {
				t.Fatal("repeated finding journaled")
			}
		})
		t.Run(tc.name+" next to a stopped role", func(t *testing.T) {
			r := newRig(t)
			role := r.define("alpha")
			tc.put(r, role)
			rep := r.reconcile()
			rr, _ := rep.Find(role)
			wantCode := "competing-unit"
			if tc.code == "stale-unit" {
				wantCode = "stale-unit"
			}
			if rr.Action != ActionUnknown || rr.Reason != wantCode {
				t.Fatalf("report %+v", rr)
			}
			r.startsNothing(0)
		})
	}
}

func TestReconcileLeavesTheTmuxManagerAlone(t *testing.T) {
	r := newRig(t)
	role := r.defineTmux("old")
	noErr(t, r.fake.AddForeign(unitOf(role), "tmux-managed"))
	rev := r.revision()
	calls := len(r.fake.Calls())
	rep := r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Action != ActionNotManaged || rr.Manager != leadregistry.ManagerTmux {
		t.Fatalf("report %+v", rr)
	}
	if r.revision() != rev || len(r.fake.Calls()) != calls {
		t.Fatal("a role of the tmux-manager was touched")
	}
}

func TestReconcileManagerUnavailable(t *testing.T) {
	r := newRig(t)
	live := r.define("alpha")
	stopped := r.define("beta")
	r.mustStart(live)
	r.fake.MakeUnavailable(leadunit.MethodInspect, 1<<20)
	rev := r.revision()
	rep := r.reconcile()
	for _, role := range []leadrole.Role{live, stopped} {
		rr, _ := rep.Find(role)
		if rr.Before != rr.After {
			t.Fatalf("%s: report %+v", role, rr)
		}
	}
	// The live role is left as it was, reported as unavailable; the registry
	// does not make a role unknown for a failure to ask.
	if rr, _ := rep.Find(live); rr.Action != ActionUnavailable || rr.After != leadregistry.StateStarting {
		t.Fatalf("report %+v", rr)
	}
	if rr, _ := rep.Find(stopped); rr.Action != ActionUnavailable && rr.Action != ActionUnchanged {
		t.Fatalf("report %+v", rr)
	}
	if r.revision() != rev {
		t.Fatal("registry changed without an answer from the manager")
	}
	r.fake.MakeUnavailable(leadunit.MethodInspect, 0)
	rep = r.reconcile()
	if rr, _ := rep.Find(live); rr.Action != ActionAdopted {
		t.Fatalf("report %+v", rr)
	}
}

func TestReconcilePendingIntentWithoutStart(t *testing.T) {
	// The daemon died after journaling the intent and before (or during) the
	// manager call: the registry holds a pending intent and nothing else.
	t.Run("not loaded, window open then closed", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		in, err := r.reg.BeginLaunch(role)
		noErr(t, err)
		r.restart()
		rep := r.reconcile()
		rr, _ := rep.Find(role)
		if rr.Action != ActionPending || rr.After != leadregistry.StateUnknown || rr.Reason != "launch-unsettled" {
			t.Fatalf("report %+v", rr)
		}
		e := r.expectState(role, leadregistry.StateUnknown, "launch-unsettled")
		if e.Intent.Outcome != leadregistry.OutcomePending || e.Intent.ID != in.ID {
			t.Fatalf("entry %+v", e)
		}
		r.advance(DefaultLateStartWindow + DefaultLateStartMargin)
		rep = r.reconcile()
		rr, _ = rep.Find(role)
		if rr.Action != ActionStopped || rr.After != leadregistry.StateStopped {
			t.Fatalf("report %+v", rr)
		}
		e = r.expectState(role, leadregistry.StateStopped, "launch-abandoned")
		if e.Intent.Outcome != leadregistry.OutcomeAbandoned || e.Epoch != 1 {
			t.Fatalf("entry %+v", e)
		}
		r.startsNothing(0)
	})
	t.Run("a matching unit is adopted", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		in, err := r.reg.BeginLaunch(role)
		noErr(t, err)
		_, err = r.fake.Start(context.Background(), leadunit.Spec{Unit: in.UnitName, Epoch: in.Epoch, Intent: in.ID,
			Argv: []string{"/bin/x"}, WorkDir: "/w"})
		noErr(t, err)
		r.restart()
		rep := r.reconcile()
		rr, _ := rep.Find(role)
		if rr.Action != ActionAdopted || rr.After != leadregistry.StateStarting {
			t.Fatalf("report %+v", rr)
		}
		e := r.get(role)
		if e.Intent.Outcome != leadregistry.OutcomeAdopted || e.Unit.InvocationID == "" {
			t.Fatalf("entry %+v", e)
		}
		r.noSignals()
	})
	t.Run("a unit that is not this launch's", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		_, err := r.reg.BeginLaunch(role)
		noErr(t, err)
		noErr(t, r.fake.AddForeign(unitOf(role), "foreign"))
		rep := r.reconcile()
		rr, _ := rep.Find(role)
		if rr.Action != ActionUnknown || rr.Reason != "foreign-unit" {
			t.Fatalf("report %+v", rr)
		}
		// Inside the window the intent stays pending; after it, the foreign
		// unit cannot be this launch's any more, so the intent is closed and
		// the owner can decide.
		if e := r.get(role); e.Intent.Outcome != leadregistry.OutcomePending {
			t.Fatalf("entry %+v", e)
		}
		r.advance(DefaultLateStartWindow + DefaultLateStartMargin)
		r.reconcile()
		e := r.get(role)
		if e.Intent.Outcome != leadregistry.OutcomeAbandoned || e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("entry %+v", e)
		}
		_, err = r.reg.ResolveUnknown(role, leadregistry.OwnerDecision{Epoch: 1, State: leadregistry.StateStopped,
			Reason: leadregistry.Reason{Code: "owner"}, Decision: "the foreign unit is not ours"})
		noErr(t, err)
		r.noSignals()
	})
	t.Run("control group left, not proven empty", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		_, err := r.reg.BeginLaunch(role)
		noErr(t, err)
		r.fake.LeaveControlGroup(unitOf(role), true)
		r.advance(time.Hour)
		rep := r.reconcile()
		rr, _ := rep.Find(role)
		if rr.Action != ActionUnknown || rr.Reason != "control-group-not-proven-empty" {
			t.Fatalf("report %+v", rr)
		}
		if e := r.get(role); e.Intent.Outcome != leadregistry.OutcomePending {
			t.Fatalf("entry %+v", e)
		}
	})
}

func TestReconcileRegistryCut(t *testing.T) {
	r := newRig(t)
	live := r.define("alpha")
	gone := r.define("beta")
	busy := r.define("gamma")
	hidden := r.define("delta")
	r.mustStart(live)
	r.mustStart(hidden)
	r.mustStart(gone)
	if _, err := r.stop(gone); err != nil {
		t.Fatal(err)
	}
	// busy is recorded stopped but a unit with its name is loaded.
	noErr(t, r.fake.AddForeign(unitOf(busy), "foreign"))
	// hidden's unit is gone, and nothing in the registry knows it yet.
	r.fake.Crash(unitOf(hidden))

	// A write that never returned: half a line at the end of the journal.
	noErr(t, r.reg.Close())
	f, err := os.OpenFile(filepath.Join(r.dir, leadregistry.FileName), os.O_WRONLY|os.O_APPEND, 0)
	noErr(t, err)
	_, err = f.WriteString(`{"seq":99,"time":"2026`)
	noErr(t, err)
	noErr(t, f.Close())
	r.open()
	if !r.reg.Recovery().Cut() {
		t.Fatal("the registry did not report a cut")
	}

	starts := r.callsOf(leadunit.MethodStart)
	signals := len(r.signalCalls())
	rep := r.reconcile()
	if !rep.Recovered {
		t.Fatal("report does not say the cut was handled")
	}
	for _, role := range []leadrole.Role{live, busy, hidden} {
		rr, _ := rep.Find(role)
		if rr.Action != ActionUnknown || rr.Reason != "registry-recovered" || rr.After != leadregistry.StateUnknown {
			t.Fatalf("%s: report %+v", role, rr)
		}
	}
	// stopped with its unit proven absent: left as it is.
	if rr, _ := rep.Find(gone); rr.Action != ActionUnchanged || rr.After != leadregistry.StateStopped || rr.Reason != "recovered-proven-absent" {
		t.Fatalf("report %+v", rr)
	}
	if r.callsOf(leadunit.MethodStart) != starts || len(r.signalCalls()) != signals {
		t.Fatal("Reconcile started or signalled")
	}

	// The cut is acted on once. Roles shown unknown for it are not resolved by
	// Reconcile even though hidden's unit is provably gone: only the owner is.
	rep = r.reconcile()
	if rep.Recovered {
		t.Fatal("the cut was handled twice")
	}
	rev := r.revision()
	r.reconcile()
	if r.revision() != rev {
		t.Fatal("a second Reconcile wrote to the registry")
	}
	if e := r.get(hidden); e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "registry-recovered" {
		t.Fatalf("entry %+v", e)
	}
	if rr, _ := rep.Find(hidden); rr.Action != ActionUnchanged {
		t.Fatalf("report %+v", rr)
	}
	_, err = r.reg.ResolveUnknown(hidden, leadregistry.OwnerDecision{Epoch: 1, State: leadregistry.StateEnded,
		Reason: leadregistry.Reason{Code: "owner"}, Decision: "its unit is gone"})
	noErr(t, err)
	// And Stop moves a live one on.
	e, err := r.stop(live)
	noErr(t, err)
	if e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("entry %+v", e)
	}
}

func TestReconcileRegistryCutWithPendingIntent(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	_, err := r.reg.BeginLaunch(role)
	noErr(t, err)
	noErr(t, r.reg.Close())
	f, err := os.OpenFile(filepath.Join(r.dir, leadregistry.FileName), os.O_WRONLY|os.O_APPEND, 0)
	noErr(t, err)
	_, err = f.WriteString(`{"seq":99`)
	noErr(t, err)
	noErr(t, f.Close())
	r.open()
	rep := r.reconcile()
	if rr, _ := rep.Find(role); rr.Reason != "registry-recovered" {
		t.Fatalf("report %+v", rr)
	}
	// Pending intents block the owner's decision, so Reconcile closes it once
	// the window has passed, without moving the role.
	r.advance(DefaultLateStartWindow + DefaultLateStartMargin)
	rep = r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Action != ActionAbandoned || rr.After != leadregistry.StateUnknown {
		t.Fatalf("report %+v", rr)
	}
	_, err = r.reg.ResolveUnknown(role, leadregistry.OwnerDecision{Epoch: 1, State: leadregistry.StateStopped,
		Reason: leadregistry.Reason{Code: "owner"}, Decision: "nothing was started"})
	noErr(t, err)
	r.startsNothing(0)
}

func TestReconcileNeverUpgradesToReady(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.mustStart(role)
	for i := 0; i < 3; i++ {
		r.restart()
		r.reconcile()
		r.advance(time.Minute)
	}
	if got := r.get(role).Shown.State; got != leadregistry.StateStarting {
		t.Fatalf("state %s", got)
	}
}

func TestReconcileResolvesAnUnprovenStopOnceTheGroupIsEmpty(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.fake.SetProcs(unitOf(role), leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	r.mustStart(role)
	_, err := r.stop(role)
	isErr(t, err, ErrStopUnproven)
	// While the unit is still there, Reconcile keeps the role unknown.
	rep := r.reconcile()
	if rr, _ := rep.Find(role); rr.After != leadregistry.StateUnknown || rr.Action == ActionStopped || rr.Action == ActionEnded {
		t.Fatalf("report %+v", rr)
	}
	r.fake.Crash(unitOf(role))
	signals := len(r.signalCalls())
	rep = r.reconcile()
	rr, _ := rep.Find(role)
	if rr.Action != ActionStopped || rr.Reason != "stop-completed" {
		t.Fatalf("report %+v", rr)
	}
	if len(r.signalCalls()) != signals {
		t.Fatal("Reconcile signalled")
	}
}
