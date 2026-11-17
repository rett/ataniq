package leadlifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

func (r *rig) markStalled(role leadrole.Role, reason string) (leadregistry.Entry, error) {
	r.t.Helper()
	return r.ctl.MarkStalled(context.Background(), role, reason)
}

func (r *rig) clearStalled(role leadrole.Role) (leadregistry.Entry, error) {
	r.t.Helper()
	return r.ctl.ClearStalled(context.Background(), role)
}

func (r *rig) setShown(role leadrole.Role, st leadregistry.State) {
	r.t.Helper()
	_, err := r.reg.SetState(role, leadregistry.StateChange{Epoch: r.get(role).Epoch, State: st,
		Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test"}})
	noErr(r.t, err)
}

func TestMarkStalled(t *testing.T) {
	t.Run("a starting coordinator", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		e, err := r.markStalled(coord, "no output for ten minutes")
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStalled || e.Shown.Reason.Code != "stalled" ||
			!strings.Contains(e.Shown.Reason.Detail, "no output for ten minutes") || e.Shown.Evidence != leadregistry.EvidenceObservation {
			t.Fatalf("entry %+v", e.Shown)
		}
		// Recording it touches no unit.
		r.noSignals()
		if r.callsOf(leadunit.MethodStart) != 2 {
			t.Fatal("a unit was started")
		}
		// Marking it again changes nothing.
		rev := r.revision()
		if _, err := r.markStalled(coord, "again"); err != nil || r.revision() != rev {
			t.Fatalf("error %v, revision %d to %d", err, rev, r.revision())
		}
	})
	t.Run("an empty reason and a long one", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		e, err := r.markStalled(coord, strings.Repeat("x\n", 1000))
		noErr(t, err)
		if len(e.Shown.Reason.Detail) > leadregistry.MaxReasonDetailBytes {
			t.Fatalf("detail is %d bytes", len(e.Shown.Reason.Detail))
		}
		r2, _, coord2 := coordRig(t)
		r2.mustStart(coord2)
		e, err = r2.markStalled(coord2, "")
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStalled || e.Shown.Reason.Detail == "" {
			t.Fatalf("entry %+v", e.Shown)
		}
	})
	t.Run("refused for a lead and for a coordinator that is not live", func(t *testing.T) {
		r, lead, coord := coordRig(t)
		rev := r.revision()
		_, err := r.markStalled(lead, "x")
		isErr(t, err, leadregistry.ErrStateTransition)
		_, err = r.markStalled(coord, "x") // defined, never launched: stopped
		isErr(t, err, leadregistry.ErrStateTransition)
		if r.revision() != rev {
			t.Fatal("a refused mark changed the registry")
		}
	})
	t.Run("refused for a role run by the tmux-manager", func(t *testing.T) {
		r := newRig(t)
		role := r.defineTmux("alpha")
		_, err := r.markStalled(role, "x")
		isErr(t, err, ErrNotManaged)
	})
}

func TestClearStalled(t *testing.T) {
	t.Run("a starting coordinator comes back degraded", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		e, err := r.clearStalled(coord)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateDegraded || e.Shown.Reason.Code != "stall-cleared" {
			t.Fatalf("entry %+v", e.Shown)
		}
		r.noSignals()
	})
	t.Run("a ready coordinator comes back ready", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		r.setShown(coord, leadregistry.StateReady)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		e, err := r.clearStalled(coord)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateReady {
			t.Fatalf("entry %+v", e.Shown)
		}
	})
	t.Run("a degraded coordinator comes back degraded", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		r.setShown(coord, leadregistry.StateDegraded)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		e, err := r.clearStalled(coord)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateDegraded {
			t.Fatalf("entry %+v", e.Shown)
		}
	})
	t.Run("a role that is not stalled", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		rev := r.revision()
		_, err := r.clearStalled(coord)
		isErr(t, err, ErrNotStalled)
		if r.revision() != rev {
			t.Fatal("the registry changed")
		}
	})
	t.Run("the unit is gone", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		r.fake.Crash(unitOf(coord))
		e, err := r.clearStalled(coord)
		isErr(t, err, ErrInstanceChanged)
		if e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "stalled-instance-changed" {
			t.Fatalf("entry %+v", e.Shown)
		}
	})
	t.Run("another instance holds the unit", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		r.fake.Crash(unitOf(coord))
		noErr(t, r.fake.AddForeign(unitOf(coord), "someone else's unit"))
		e, err := r.clearStalled(coord)
		isErr(t, err, ErrInstanceChanged)
		if e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("entry %+v", e.Shown)
		}
		r.noSignals()
	})
	t.Run("the manager does not answer", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
		e, err := r.clearStalled(coord)
		isErr(t, err, leadunit.ErrUnavailable)
		if e.Shown.State != leadregistry.StateStalled {
			t.Fatalf("entry %+v", e.Shown)
		}
	})
	// The parent is judged as Reconcile judges it, and a coordinator under a
	// parent that has ended is not returned to a live state, even while its
	// own unit is running.
	for name, parentState := range map[string]leadregistry.State{
		"the parent is unknown":  leadregistry.StateUnknown,
		"the parent is stopping": leadregistry.StateStopping,
		"the parent is ended":    leadregistry.StateEnded,
	} {
		t.Run(name, func(t *testing.T) {
			r, lead, coord := coordRig(t)
			r.mustStart(coord)
			_, err := r.markStalled(coord, "x")
			noErr(t, err)
			if parentState == leadregistry.StateEnded {
				// The lead's record says ended (its unit went away on its own and
				// was proven absent); the coordinator's own unit still runs, as
				// the manager has not got to it, so that the test does not
				// depend on the moment the fake stops a bound unit.
				_, err := r.reg.SetState(lead, leadregistry.StateChange{Epoch: 1, State: leadregistry.StateEnded,
					Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test"}, Proof: "test"})
				noErr(t, err)
			} else {
				r.setShown(lead, parentState)
			}
			e, err := r.clearStalled(coord)
			isErr(t, err, ErrParentNotLive)
			if errors.Is(err, ErrInstanceChanged) {
				t.Fatalf("the error says the instance changed: %v", err)
			}
			if e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "orphan-coordinator" {
				t.Fatalf("entry %+v", e.Shown)
			}
			// It stays unknown after Reconcile, which signals nothing; it was
			// not given a live state to be taken back from.
			r.reconcile()
			if got := r.get(coord).Shown.State; got == leadregistry.StateReady || got == leadregistry.StateDegraded || got == leadregistry.StateStalled {
				t.Fatalf("coordinator is %s", got)
			}
			r.noSignals()
		})
	}
}

// A stalled coordinator is replaced only by a stop with proof and then a start
// of instance n+1.
func TestStalledCoordinatorIsReplacedOnlyByStopThenStart(t *testing.T) {
	r, _, coord := coordRig(t)
	first := r.mustStart(coord)
	_, err := r.markStalled(coord, "no output")
	noErr(t, err)

	// No launch is admitted from stalled, and the instance is not proven
	// stopped: what an adoption of its runs would ask is false.
	startsBefore := r.callsOf(leadunit.MethodStart)
	_, err = r.start(coord)
	isErr(t, err, leadregistry.ErrLaunchRefused)
	if r.callsOf(leadunit.MethodStart) != startsBefore {
		t.Fatal("a unit was started for a stalled coordinator")
	}
	if ok, err := r.reg.CoordinatorStopped(coord, 1); err != nil || ok {
		t.Fatalf("CoordinatorStopped(1) = %v, %v while stalled", ok, err)
	}
	// Reconcile does not replace it either.
	r.reconcile()
	if e := r.get(coord); e.Shown.State != leadregistry.StateStalled || e.Epoch != 1 {
		t.Fatalf("entry %+v", e)
	}

	// Stop proves instance 1 stopped.
	e, err := r.stop(coord)
	noErr(t, err)
	if e.Shown.State != leadregistry.StateStopped || e.Shown.Proof == "" {
		t.Fatalf("entry %+v", e)
	}
	if ok, err := r.reg.CoordinatorStopped(coord, 1); err != nil || !ok {
		t.Fatalf("CoordinatorStopped(1) = %v, %v after the stop", ok, err)
	}
	// Only now instance 2 starts.
	e2 := r.mustStart(coord)
	if e2.Epoch != 2 || e2.Intent.Identity != "alpha-lead@e1:coord:feat:2" {
		t.Fatalf("entry %+v", e2)
	}
	if ok, err := r.reg.CoordinatorStopped(coord, 2); err != nil || ok {
		t.Fatalf("CoordinatorStopped(2) = %v, %v while it runs", ok, err)
	}

	// The call log says that no Start for instance 2 came before the proof
	// for instance 1: its control group proven empty after the signals.
	unit := unitOf(coord)
	var signal, proof, start2 = -1, -1, -1
	for i, c := range r.fake.Calls() {
		if c.Unit != unit {
			continue
		}
		switch {
		case c.Method == leadunit.MethodSignal && !c.Implied && c.Invocation == first.Unit.InvocationID:
			signal = i
		case c.Method == leadunit.MethodEmpty && c.Empty && c.Invocation == first.Unit.InvocationID && signal >= 0 && proof < 0:
			proof = i
		case c.Method == leadunit.MethodStart && c.Err == nil && i > startsBeforeIndex(r, unit):
			start2 = i
		}
	}
	if signal < 0 || proof < 0 || start2 < 0 || !(signal < proof && proof < start2) {
		t.Fatalf("call log order: signal %d, proof %d, second start %d", signal, proof, start2)
	}
	r.auditClean()
}

// startsBeforeIndex is the index in the call log of the first Start of the unit
// that took effect.
func startsBeforeIndex(r *rig, unit string) int {
	for i, c := range r.fake.Calls() {
		if c.Unit == unit && c.Method == leadunit.MethodStart {
			return i
		}
	}
	return -1
}

func TestStalledCoordinatorStopsWithItsLead(t *testing.T) {
	r, lead, coord := coordRig(t)
	r.mustStart(coord)
	_, err := r.markStalled(coord, "x")
	noErr(t, err)
	_, err = r.stop(lead)
	noErr(t, err)
	r.expectState(coord, leadregistry.StateStopped, "stopped")
	r.expectState(lead, leadregistry.StateStopped, "stopped")
}

func TestStalledCoordinatorAcrossADaemonRestart(t *testing.T) {
	t.Run("the unit is live and ours", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		starts := r.callsOf(leadunit.MethodStart)
		r.restart()
		rr, _ := r.reconcile().Find(coord)
		if rr.Action != ActionAdopted || rr.Before != leadregistry.StateStalled || rr.After != leadregistry.StateStalled {
			t.Fatalf("report %+v", rr)
		}
		r.startsNothing(starts)
		// And ClearStalled still works for the new daemon.
		e, err := r.clearStalled(coord)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateDegraded {
			t.Fatalf("entry %+v", e.Shown)
		}
	})
	t.Run("the unit is gone", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		r.restart()
		r.fake.Crash(unitOf(coord))
		rr, _ := r.reconcile().Find(coord)
		if rr.Action != ActionEnded {
			t.Fatalf("report %+v", rr)
		}
		r.expectState(coord, leadregistry.StateEnded, "holder-gone")
	})
	t.Run("the unit is not ours", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		r.restart()
		r.fake.Crash(unitOf(coord))
		noErr(t, r.fake.AddForeign(unitOf(coord), "someone else's unit"))
		rr, _ := r.reconcile().Find(coord)
		if rr.Action != ActionUnknown || rr.Reason != "foreign-unit" {
			t.Fatalf("report %+v", rr)
		}
		r.noSignals()
	})
}
