package leadlifecycle

import (
	"fmt"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// impostor is a loaded unit of ours that is not the pending launch's: the
// right kind of description with another epoch or another intent. Adopting it
// would hand the role a unit that was never started for this intent.
type impostor struct {
	name string
	desc func(epoch uint64) string
}

// impostors lists the descriptions of a unit that must never be adopted for a
// launch of the given epoch.
var impostors = []impostor{
	{"same epoch, another intent", func(epoch uint64) string { return fmt.Sprintf("ataniq lead epoch=%d intent=other", epoch) }},
	{"an older epoch", func(epoch uint64) string { return fmt.Sprintf("ataniq lead epoch=%d intent=zzz", epoch-1) }},
	{"a newer epoch", func(epoch uint64) string { return fmt.Sprintf("ataniq lead epoch=%d intent=zzz", epoch+1) }},
}

// secondEpoch returns a role whose epoch 1 ran and was stopped, so that a
// launch of epoch 2 has an older epoch to be confused with.
func secondEpoch(r *rig) leadrole.Role {
	role := r.define("alpha")
	r.mustStart(role)
	if _, err := r.stop(role); err != nil {
		r.t.Fatal(err)
	}
	return role
}

// signalsSince fails the test when the number of Signal calls is not n: the
// signals of the stop of the first epoch are the only ones allowed.
func (r *rig) signalsSince(n int) {
	r.t.Helper()
	if got := r.signalCalls(); len(got) != n {
		r.t.Fatalf("%d Signal calls, want %d: %+v", len(got), n, got)
	}
}

// notAdopted fails when the intent of the role was adopted.
func notAdopted(t *testing.T, e leadregistry.Entry) {
	t.Helper()
	if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomeAdopted {
		t.Fatalf("a unit of another epoch or intent was adopted: %+v", e)
	}
	if e.Unit.InvocationID != "" && e.Intent != nil && e.Intent.Outcome != leadregistry.OutcomeLaunched {
		t.Fatalf("unit evidence recorded for a unit that was not started: %+v", e)
	}
}

// TestAdoptionMatchesExactEpochAndIntent drives every path that can adopt a
// loaded unit (a lost reply and a refused start from Start, a pending intent
// from Stop and from Reconcile) with units that carry another intent or
// another epoch, and requires that none is adopted and none is signalled.
func TestAdoptionMatchesExactEpochAndIntent(t *testing.T) {
	for _, imp := range impostors {
		t.Run("Start, reply lost, "+imp.name, func(t *testing.T) {
			r := newRig(t)
			role := secondEpoch(r)
			signals := len(r.signalCalls())
			r.fake.DropNextStart(unitOf(role))
			r.aud.afterStart = func(spec leadunit.Spec) {
				noErr(t, r.fake.AddForeign(spec.Unit, imp.desc(spec.Epoch)))
			}
			e, err := r.start(role)
			isErr(t, err, ErrLaunchUncertain)
			notAdopted(t, e)
			if e.Epoch != 2 || e.Intent.Outcome != leadregistry.OutcomePending {
				t.Fatalf("entry %+v", e)
			}
			r.expectState(role, leadregistry.StateUnknown, "launch-reply-lost")
			r.signalsSince(signals)
			r.auditClean()
		})
		t.Run("Start, unit exists, "+imp.name, func(t *testing.T) {
			r := newRig(t)
			role := secondEpoch(r)
			signals := len(r.signalCalls())
			r.aud.beforeStart = func(spec leadunit.Spec) {
				noErr(t, r.fake.AddForeign(spec.Unit, imp.desc(spec.Epoch)))
			}
			e, err := r.start(role)
			isErr(t, err, ErrStartFailed)
			isErr(t, err, ErrForeignUnit)
			notAdopted(t, e)
			if e.Epoch != 2 || e.Intent.Outcome != leadregistry.OutcomeFailed {
				t.Fatalf("entry %+v", e)
			}
			r.expectState(role, leadregistry.StateUnknown, "unit-exists")
			r.signalsSince(signals)
			r.auditClean()
		})
		for _, late := range []bool{false, true} {
			when := "inside the window"
			if late {
				when = "after the window"
			}
			// pending leaves epoch 2 pending with a lost reply and the
			// impostor loaded.
			pending := func(t *testing.T) (*rig, leadrole.Role, int) {
				r := newRig(t)
				role := secondEpoch(r)
				signals := len(r.signalCalls())
				r.fake.DropNextStart(unitOf(role))
				_, err := r.start(role)
				isErr(t, err, ErrLaunchUncertain)
				noErr(t, r.fake.AddForeign(unitOf(role), imp.desc(2)))
				if late {
					r.advance(DefaultLateStartWindow + time.Second)
				}
				return r, role, signals
			}
			t.Run("Stop, pending intent, "+imp.name+", "+when, func(t *testing.T) {
				r, role, signals := pending(t)
				e, err := r.stop(role)
				isErr(t, err, ErrIntentPending)
				notAdopted(t, e)
				r.signalsSince(signals)
				notAdopted(t, r.get(role))
				r.auditClean()
			})
			t.Run("Reconcile, pending intent, "+imp.name+", "+when, func(t *testing.T) {
				r, role, signals := pending(t)
				rep := r.reconcile()
				rr, _ := rep.Find(role)
				if rr.Action == ActionAdopted || rr.After != leadregistry.StateUnknown {
					t.Fatalf("report %+v", rr)
				}
				if rr.Reason != "stale-unit" {
					t.Fatalf("report %+v", rr)
				}
				notAdopted(t, r.get(role))
				r.signalsSince(signals)
				r.auditClean()
			})
		}
		// An explicit Stop signals a unit of an older epoch (a start that
		// landed late); every other impostor it must refuse.
		if imp.name != "an older epoch" {
			t.Run("Stop, resolved launch, "+imp.name, func(t *testing.T) {
				r := newRig(t)
				role := secondEpoch(r)
				r.mustStart(role)
				signals := len(r.signalCalls())
				r.fake.Crash(unitOf(role))
				noErr(t, r.fake.AddForeign(unitOf(role), imp.desc(2)))
				e, err := r.stop(role)
				isErr(t, err, ErrForeignUnit)
				isErr(t, err, ErrUnitLoaded)
				if e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "stale-unit" {
					t.Fatalf("entry %+v", e)
				}
				r.signalsSince(signals)
				r.auditClean()
			})
		}
		// With the launch abandoned there is no recorded invocation to
		// compare, so only the epoch and intent keep Stop from signalling.
		if imp.name != "an older epoch" {
			t.Run("Stop, abandoned launch, "+imp.name, func(t *testing.T) {
				r := newRig(t)
				role := secondEpoch(r)
				signals := len(r.signalCalls())
				r.fake.DropNextStart(unitOf(role))
				_, err := r.start(role)
				isErr(t, err, ErrLaunchUncertain)
				r.advance(DefaultLateStartWindow + time.Second)
				rep := r.reconcile()
				if rr, _ := rep.Find(role); rr.Action != ActionStopped {
					t.Fatalf("report %+v", rr)
				}
				noErr(t, r.fake.AddForeign(unitOf(role), imp.desc(2)))
				e, err := r.stop(role)
				isErr(t, err, ErrForeignUnit)
				if e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "stale-unit" {
					t.Fatalf("entry %+v", e)
				}
				r.signalsSince(signals)
				r.auditClean()
			})
		}
		t.Run("Reconcile, resolved launch, "+imp.name, func(t *testing.T) {
			r := newRig(t)
			role := secondEpoch(r)
			r.mustStart(role)
			signals := len(r.signalCalls())
			r.fake.Crash(unitOf(role))
			noErr(t, r.fake.AddForeign(unitOf(role), imp.desc(2)))
			rev := r.revision()
			rep := r.reconcile()
			rr, _ := rep.Find(role)
			if rr.Action != ActionUnknown || rr.Reason != "stale-unit" || rr.After != leadregistry.StateUnknown {
				t.Fatalf("report %+v", rr)
			}
			if r.revision() == rev {
				t.Fatal("the finding was not recorded")
			}
			r.signalsSince(signals)
			r.auditClean()
		})
	}
}
