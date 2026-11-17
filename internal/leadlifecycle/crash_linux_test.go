package leadlifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// A crash scenario is one operation of the controller, killed at each point
// at which a daemon could die: before each registry write, and before and
// after each call to the unit manager. After every kill the registry is
// closed and reopened and a new controller (the daemon coming back) runs
// Reconcile against the same fake manager, and then the operation again; the
// invariants of the contract must hold at each of those moments.
type crashScenario struct {
	name string
	// setup builds the world before the operation; nothing in it is killed.
	setup func(r *rig) leadrole.Role
	// op is the operation that is killed. It reads r.ctl when it runs, so a
	// restart replaces the controller it uses.
	op func(r *rig, role leadrole.Role)
	// retry is the operation again after the restart and Reconcile.
	retry func(r *rig, role leadrole.Role)
	// after runs after the first Reconcile of a restart, for what happens in
	// the world meanwhile (a late start request that lands).
	after func(r *rig, role leadrole.Role)
}

// startOp and stopOp call the controller directly: the harness around them
// already moves the fake clock. Their errors are not what the scenarios test.
func startOp(r *rig, role leadrole.Role) { _, _ = r.ctl.Start(context.Background(), role) }
func stopOp(r *rig, role leadrole.Role)  { _, _ = r.ctl.Stop(context.Background(), role) }

func plainRole(r *rig) leadrole.Role { return r.define("alpha") }

func runningRole(script func(r *rig, unit string)) func(r *rig) leadrole.Role {
	return func(r *rig) leadrole.Role {
		role := r.define("alpha")
		if script != nil {
			script(r, unitOf(role))
		}
		r.mustStart(role)
		return role
	}
}

func endedRole(r *rig) leadrole.Role {
	role := r.define("alpha")
	r.mustStart(role)
	r.fake.Crash(unitOf(role))
	r.reconcile()
	r.expectState(role, leadregistry.StateEnded, "holder-gone")
	return role
}

func scripted(f func(r *rig, unit string)) func(r *rig) leadrole.Role {
	return func(r *rig) leadrole.Role {
		role := r.define("alpha")
		f(r, unitOf(role))
		return role
	}
}

var crashScenarios = []crashScenario{
	{name: "start", setup: plainRole, op: startOp, retry: startOp},
	{name: "start after an ended epoch", setup: endedRole, op: startOp, retry: startOp},
	{name: "start, reply lost, unit running", setup: scripted(func(r *rig, u string) { r.fake.LoseNextStartReply(u) }), op: startOp, retry: startOp},
	{name: "start, context ends after the request", setup: scripted(func(r *rig, u string) { r.fake.ExpireNextStart(u) }), op: startOp, retry: startOp},
	{name: "start, request dropped", setup: scripted(func(r *rig, u string) { r.fake.DropNextStart(u) }), op: startOp, retry: startOp},
	{name: "start, request lands late", setup: scripted(func(r *rig, u string) { r.fake.LateNextStart(u) }), op: startOp, retry: startOp,
		after: func(r *rig, role leadrole.Role) { r.fake.LandLateStart(unitOf(role)) }},
	{name: "start, the manager refuses", setup: scripted(func(r *rig, u string) { r.fake.FailNextStart(u, errors.New("scripted")) }), op: startOp, retry: startOp},
	{name: "start, a foreign unit takes the name", setup: func(r *rig) leadrole.Role {
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.AddForeign(unitOf(rl), "intruder")
		}
		return role
	}, op: startOp, retry: startOp},
	{name: "start, the command cannot be built", setup: func(r *rig) leadrole.Role {
		role := r.define("alpha")
		r.holder.err = errors.New("no holder")
		return role
	}, op: startOp, retry: startOp},
	{name: "stop", setup: runningRole(nil), op: stopOp, retry: stopOp},
	{name: "stop, TERM ignored", setup: runningRole(func(r *rig, u string) {
		r.fake.SetProcs(u, leadunit.ProcScript{Behavior: leadunit.IgnoreTerm})
	}), op: stopOp, retry: stopOp},
	{name: "stop, TERM honoured late", setup: runningRole(func(r *rig, u string) {
		r.fake.SetProcs(u, leadunit.ProcScript{Behavior: leadunit.ExitOnTermAfter, Delay: 700 * time.Millisecond})
	}), op: stopOp, retry: stopOp},
	{name: "stop, processes survive SIGKILL", setup: runningRole(func(r *rig, u string) {
		r.fake.SetProcs(u, leadunit.ProcScript{Behavior: leadunit.SurviveKill})
	}), op: stopOp, retry: stopOp, after: func(r *rig, role leadrole.Role) { r.fake.Crash(unitOf(role)) }},
	{name: "stop, the unit is already gone", setup: func(r *rig) leadrole.Role {
		role := runningRole(nil)(r)
		r.fake.Crash(unitOf(role))
		return role
	}, op: stopOp, retry: stopOp},
	{name: "stop after a reboot", setup: func(r *rig) leadrole.Role {
		role := runningRole(nil)(r)
		r.fake.Reboot()
		return role
	}, op: stopOp, retry: stopOp},
	{name: "stop with the reply of the start lost", setup: func(r *rig) leadrole.Role {
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.LoseNextStartReply(unitOf(rl))
			r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
		}
		r.start(role)
		r.holder.hook = nil
		return role
	}, op: stopOp, retry: stopOp},
}

// checkInvariants checks the invariants of the contract: no start twice for
// one intent, nothing stopped or ended without proof or while its unit is
// loaded (an owner's decision excepted), and, once Reconcile has run, a role
// shown live has a running unit of its own epoch and a pending intent is
// shown unknown.
func (r *rig) checkInvariants(when string, reconciled bool, roles ...leadrole.Role) {
	r.t.Helper()
	r.auditClean()
	seen := map[string]bool{}
	for _, s := range r.fake.StartedSpecs() {
		if seen[s.Intent] {
			r.t.Errorf("%s: intent %s was started twice", when, s.Intent)
		}
		seen[s.Intent] = true
	}
	for _, role := range roles {
		e := r.get(role)
		loaded := r.fake.Loaded(unitOf(role))
		switch e.Shown.State {
		case leadregistry.StateStopped, leadregistry.StateEnded:
			if e.Shown.Proof == "" && e.Shown.Reason.Code != "defined" {
				r.t.Errorf("%s: %s %s without a proof", when, role, e.Shown.State)
			}
			if loaded && e.Shown.Evidence != leadregistry.EvidenceOwner {
				r.t.Errorf("%s: %s is %s but its unit is loaded", when, role, e.Shown.State)
			}
			if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
				r.t.Errorf("%s: %s is %s with a pending intent", when, role, e.Shown.State)
			}
		case leadregistry.StateStarting, leadregistry.StateReady, leadregistry.StateDegraded, leadregistry.StateStopping:
			if !reconciled {
				break
			}
			// After Reconcile a role shown live has a running unit of its own epoch.
			st, err := r.fake.Inspect(context.Background(), unitOf(role))
			if err != nil || !st.Loaded || !st.Ours || st.Epoch != e.Epoch {
				r.t.Errorf("%s: %s is %s but the unit is %+v (%v)", when, role, e.Shown.State, st, err)
			}
		}
		if reconciled && e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending && e.Shown.State != leadregistry.StateUnknown {
			r.t.Errorf("%s: %s has a pending intent and is shown %s", when, role, e.Shown.State)
		}
	}
}

// wayForward checks that every role that is not settled can be moved on by an
// operation or by the owner.
func (r *rig) wayForward(when string, roles ...leadrole.Role) {
	r.t.Helper()
	r.advance(pastTheWindow)
	r.reconcile()
	for _, role := range roles {
		e := r.get(role)
		switch e.Shown.State {
		case leadregistry.StateStopped, leadregistry.StateEnded:
			continue
		case leadregistry.StateUnknown:
			if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
				r.t.Errorf("%s: %s is unknown with an intent that is still pending after the window", when, role)
				continue
			}
			// Stop moves it on when the unit is ours; the owner when it is not.
			if _, err := r.stop(role); err != nil {
				if _, err := r.reg.ResolveUnknown(role, leadregistry.OwnerDecision{Epoch: e.Epoch, State: leadregistry.StateStopped,
					Reason: leadregistry.Reason{Code: "owner"}, Decision: "decided by the test"}); err != nil {
					r.t.Errorf("%s: %s is stuck unknown: %v", when, role, err)
				}
			}
		default:
			if _, err := r.stop(role); err != nil {
				if _, err := r.reg.ResolveUnknown(role, leadregistry.OwnerDecision{Epoch: e.Epoch, State: leadregistry.StateStopped,
					Reason: leadregistry.Reason{Code: "owner"}, Decision: "decided by the test"}); err != nil {
					r.t.Errorf("%s: %s is stuck %s: %v", when, role, e.Shown.State, err)
				}
			}
		}
		if got := r.get(role).Shown.State; got != leadregistry.StateStopped && got != leadregistry.StateEnded {
			r.t.Errorf("%s: %s ended up %s", when, role, got)
		}
	}
}

// pointsOf runs the operation once without killing it and returns how many
// points it passed.
func pointsOf(t *testing.T, sc crashScenario) []string {
	r := newRig(t)
	role := sc.setup(r)
	r.plan.armed = true
	if cs := r.drive(func() { sc.op(r, role) }); cs != nil {
		t.Fatalf("killed in the dry run: %+v", cs)
	}
	r.plan.armed = false
	return append([]string(nil), r.plan.labels...)
}

func TestCrashSafety(t *testing.T) {
	for _, sc := range crashScenarios {
		t.Run(sc.name, func(t *testing.T) {
			labels := pointsOf(t, sc)
			if len(labels) < 3 {
				t.Fatalf("only %d crash points", len(labels))
			}
			for k := range labels {
				k := k
				t.Run(fmt.Sprintf("%02d %s", k, labels[k]), func(t *testing.T) {
					r := newRig(t)
					role := sc.setup(r)
					r.plan.at = k
					r.plan.armed = true
					cs := r.drive(func() { sc.op(r, role) })
					r.plan.armed = false
					if cs == nil {
						t.Fatalf("the operation finished before crash point %d", k)
					}
					roles := []leadrole.Role{role}

					// The daemon comes back. What the journal says must already be sane.
					r.restart()
					r.checkInvariants("after the crash", false, roles...)

					r.reconcile()
					r.checkInvariants("after Reconcile", true, roles...)
					// A second Reconcile finds nothing more to do.
					rev := r.revision()
					r.reconcile()
					if r.revision() != rev {
						t.Errorf("a second Reconcile changed the registry")
					}
					if sc.after != nil {
						sc.after(r, role)
						r.reconcile()
						r.checkInvariants("after the world moved", true, roles...)
					}

					// The operation again, where the registry admits it.
					r.drive(func() { sc.retry(r, role) })
					r.checkInvariants("after the operation again", false, roles...)
					r.reconcile()
					r.checkInvariants("after Reconcile again", true, roles...)

					r.wayForward("way forward", roles...)
					r.checkInvariants("settled", true, roles...)
				})
			}
		})
	}
}
