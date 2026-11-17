package leadlifecycle

import (
	"context"
	"errors"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

func (r *rig) stop(role leadrole.Role) (leadregistry.Entry, error) {
	r.t.Helper()
	var e leadregistry.Entry
	var err error
	r.drive(func() { e, err = r.ctl.Stop(context.Background(), role) })
	return e, err
}

// signalsOf returns the signals sent, in order.
func signalsOf(calls []leadunit.Call) []syscall.Signal {
	var out []syscall.Signal
	for _, c := range calls {
		if c.Err == nil {
			out = append(out, c.Signal)
		}
	}
	return out
}

func TestStopCleanExit(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	started := r.mustStart(role)
	e, err := r.stop(role)
	noErr(t, err)
	if e.Shown.State != leadregistry.StateStopped || e.Shown.Reason.Code != "stopped" || e.Desired != leadregistry.DesiredStopped || e.Epoch != 1 {
		t.Fatalf("entry %+v", e)
	}
	for _, want := range []string{started.Unit.InvocationID, started.Unit.ControlGroupPath} {
		if !strings.Contains(e.Shown.Proof, want) {
			t.Fatalf("proof %q lacks %q", e.Shown.Proof, want)
		}
	}
	if got := signalsOf(r.signalCalls()); len(got) != 1 || got[0] != syscall.SIGTERM {
		t.Fatalf("signals %v", got)
	}
	if len(r.quiet.epochs) != 1 || r.quiet.epochs[0] != 1 {
		t.Fatalf("quiesce %v", r.quiet.epochs)
	}
	r.auditClean()
}

func TestStopSigkillOnlyAfterTwoSeconds(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.fake.SetProcs(unitOf(role), leadunit.ProcScript{Behavior: leadunit.IgnoreTerm}, leadunit.ProcScript{Behavior: leadunit.IgnoreTerm})
	r.mustStart(role)
	begin := r.clock.Now()
	_, err := r.stop(role)
	noErr(t, err)
	r.expectState(role, leadregistry.StateStopped, "stopped")
	sig := r.signalCalls()
	if len(sig) != 2 || sig[0].Signal != syscall.SIGTERM || sig[1].Signal != syscall.SIGKILL {
		t.Fatalf("signals %+v", sig)
	}
	if !sig[0].At.Equal(begin) || !sig[1].At.Equal(begin.Add(2*time.Second)) {
		t.Fatalf("TERM at %v, KILL at %v, begun at %v", sig[0].At, sig[1].At, begin)
	}
	if sig[0].Invocation == "" || sig[0].Invocation != sig[1].Invocation {
		t.Fatalf("signals name invocations %q %q", sig[0].Invocation, sig[1].Invocation)
	}
	r.auditClean()
}

func TestStopTermHonouredBeforeTheDeadline(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.fake.SetProcs(unitOf(role), leadunit.ProcScript{Behavior: leadunit.ExitOnTermAfter, Delay: 1500 * time.Millisecond})
	r.mustStart(role)
	begin := r.clock.Now()
	_, err := r.stop(role)
	noErr(t, err)
	if got := signalsOf(r.signalCalls()); len(got) != 1 || got[0] != syscall.SIGTERM {
		t.Fatalf("signals %v", got)
	}
	if took := r.clock.Now().Sub(begin); took != 1500*time.Millisecond {
		t.Fatalf("took %v", took)
	}
	r.expectState(role, leadregistry.StateStopped, "stopped")
}

func TestL4ProcessesSurviveSigkill(t *testing.T) {
	setup := func(t *testing.T) (*rig, leadrole.Role) {
		r := newRig(t)
		role := r.define("alpha")
		r.fake.SetProcs(unitOf(role), leadunit.ProcScript{Behavior: leadunit.SurviveKill})
		r.mustStart(role)
		begin := r.clock.Now()
		e, err := r.stop(role)
		isErr(t, err, ErrStopUnproven)
		if e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != "stop-unproven" {
			t.Fatalf("entry %+v", e)
		}
		if took := r.clock.Now().Sub(begin); took != TermWait+DefaultProofBound {
			t.Fatalf("gave up after %v", took)
		}
		sig := signalsOf(r.signalCalls())
		if len(sig) != 2 || sig[0] != syscall.SIGTERM || sig[1] != syscall.SIGKILL {
			t.Fatalf("signals %v", sig)
		}
		// A later start is refused: the role is unknown and the group not empty.
		_, err = r.start(role)
		isErr(t, err, leadregistry.ErrLaunchRefused)
		if r.callsOf(leadunit.MethodStart) != 1 {
			t.Fatal("a second Start reached the manager")
		}
		return r, role
	}
	t.Run("the processes go and Stop proves it", func(t *testing.T) {
		r, role := setup(t)
		r.fake.Crash(unitOf(role))
		e, err := r.stop(role)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped {
			t.Fatalf("entry %+v", e)
		}
		r.auditClean()
	})
	t.Run("the owner decides", func(t *testing.T) {
		r, role := setup(t)
		e, err := r.reg.ResolveUnknown(role, leadregistry.OwnerDecision{Epoch: 1, State: leadregistry.StateStopped,
			Reason: leadregistry.Reason{Code: "owner"}, Decision: "killed the leftover by hand"})
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped {
			t.Fatalf("entry %+v", e)
		}
	})
}

func TestStopControlGroupLeftBehindAfterUnload(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.mustStart(role)
	r.fake.LeaveControlGroup(unitOf(role), true)
	_, err := r.stop(role)
	isErr(t, err, ErrStopUnproven)
	e := r.expectState(role, leadregistry.StateUnknown, "stop-unproven")
	if !strings.Contains(e.Shown.Reason.Detail, "not proven empty") {
		t.Fatalf("detail %q", e.Shown.Reason.Detail)
	}
	r.fake.LeaveControlGroup(unitOf(role), false)
	_, err = r.stop(role)
	noErr(t, err)
	r.expectState(role, leadregistry.StateStopped, "stop-completed")
}

func TestStopNeverSignalsAUnitThatIsNotOurs(t *testing.T) {
	// replace puts a different unit in the place of the one that ran.
	for _, tc := range []struct {
		name string
		do   func(r *rig, role leadrole.Role)
		code string
	}{
		{"foreign", func(r *rig, role leadrole.Role) {
			r.fake.Crash(unitOf(role))
			noErr(t, r.fake.AddForeign(unitOf(role), "somebody's unit"))
		}, "foreign-unit"},
		{"ours with another epoch", func(r *rig, role leadrole.Role) {
			r.fake.Crash(unitOf(role))
			noErr(t, r.fake.AddForeign(unitOf(role), "ataniq lead epoch=9 intent=zzz"))
		}, "stale-unit"},
		{"ours with this epoch and intent but another invocation", func(r *rig, role leadrole.Role) {
			e := r.get(role)
			r.fake.Crash(unitOf(role))
			_, err := r.fake.Start(context.Background(), leadunit.Spec{Unit: unitOf(role), Epoch: e.Epoch, Intent: e.Intent.ID,
				Argv: []string{"/bin/x"}, WorkDir: "/w"})
			noErr(t, err)
		}, "stale-unit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			role := r.define("alpha")
			r.mustStart(role)
			tc.do(r, role)
			e, err := r.stop(role)
			isErr(t, err, ErrForeignUnit)
			isErr(t, err, ErrUnitLoaded)
			if e.Shown.State != leadregistry.StateUnknown || e.Shown.Reason.Code != tc.code {
				t.Fatalf("entry %+v", e)
			}
			r.noSignals()
			r.auditClean()
		})
	}
	t.Run("a role that was never launched", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		noErr(t, r.fake.AddForeign(unitOf(role), "somebody's unit"))
		_, err := r.stop(role)
		isErr(t, err, ErrForeignUnit)
		r.expectState(role, leadregistry.StateUnknown, "foreign-unit")
		r.noSignals()
	})
}

func TestStopManagerUnavailable(t *testing.T) {
	t.Run("inspect once", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
		_, err := r.stop(role)
		noErr(t, err)
		r.expectState(role, leadregistry.StateStopped, "stopped")
	})
	t.Run("signal once, retried on the next poll", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		begin := r.clock.Now()
		r.fake.MakeUnavailable(leadunit.MethodSignal, 1)
		_, err := r.stop(role)
		noErr(t, err)
		sig := r.signalCalls()
		if len(sig) != 2 || !errors.Is(sig[0].Err, leadunit.ErrUnavailable) || sig[1].Err != nil || sig[1].Signal != syscall.SIGTERM ||
			!sig[1].At.Equal(begin.Add(DefaultPollInterval)) {
			t.Fatalf("signals %+v", sig)
		}
	})
	t.Run("empty once", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.fake.MakeUnavailable(leadunit.MethodEmpty, 1)
		_, err := r.stop(role)
		noErr(t, err)
		r.expectState(role, leadregistry.StateStopped, "stopped")
		r.auditClean()
	})
	t.Run("inspect to the bound", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		begin := r.clock.Now()
		r.fake.MakeUnavailable(leadunit.MethodInspect, 1<<20)
		_, err := r.stop(role)
		isErr(t, err, ErrStopUnproven)
		isErr(t, err, leadunit.ErrUnavailable)
		r.expectState(role, leadregistry.StateUnknown, "stop-unproven")
		if took := r.clock.Now().Sub(begin); took != DefaultProofBound {
			t.Fatalf("waited %v", took)
		}
		r.noSignals()
		r.fake.MakeUnavailable(leadunit.MethodInspect, 0)
		_, err = r.stop(role)
		noErr(t, err)
		r.expectState(role, leadregistry.StateStopped, "stopped")
	})
	t.Run("signal to the bound", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.fake.MakeUnavailable(leadunit.MethodSignal, 1<<20)
		_, err := r.stop(role)
		isErr(t, err, ErrStopUnproven)
		r.expectState(role, leadregistry.StateUnknown, "stop-unproven")
		if !r.fake.Loaded(unitOf(role)) || len(signalsOf(r.signalCalls())) != 0 {
			t.Fatal("a signal got through")
		}
		r.fake.MakeUnavailable(leadunit.MethodSignal, 0)
		_, err = r.stop(role)
		noErr(t, err)
		r.expectState(role, leadregistry.StateStopped, "stopped")
	})
	t.Run("empty to the bound", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.fake.MakeUnavailable(leadunit.MethodEmpty, 1<<20)
		_, err := r.stop(role)
		isErr(t, err, ErrStopUnproven)
		r.expectState(role, leadregistry.StateUnknown, "stop-unproven")
		r.fake.MakeUnavailable(leadunit.MethodEmpty, 0)
		_, err = r.stop(role)
		noErr(t, err)
		r.expectState(role, leadregistry.StateStopped, "stop-completed")
		r.auditClean()
	})
}

func TestStopQuiesce(t *testing.T) {
	t.Run("a failure does not prevent the stop and is recorded", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.quiet.err = errors.New("delivery side timed out")
		_, err := r.stop(role)
		noErr(t, err)
		e := r.expectState(role, leadregistry.StateStopped, "stopped")
		if !strings.Contains(e.Shown.Reason.Detail, "quiesce failed: delivery side timed out") {
			t.Fatalf("detail %q", e.Shown.Reason.Detail)
		}
	})
	t.Run("it is not asked about a role that is stopped", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		_, err := r.stop(role)
		noErr(t, err)
		if len(r.quiet.epochs) != 0 {
			t.Fatalf("quiesce %v", r.quiet.epochs)
		}
	})
}

func TestStopProofWithAnotherInvocation(t *testing.T) {
	spec := func(unit string) leadunit.Spec {
		return leadunit.Spec{Unit: unit, Epoch: 5, Intent: "again", Argv: []string{"/bin/x"}, WorkDir: "/w"}
	}
	t.Run("in another control group it is proof", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.aud.afterSignal = func(st leadunit.State, sig syscall.Signal) {
			_, err := r.fake.Start(context.Background(), spec(st.Unit))
			noErr(t, err)
		}
		_, err := r.stop(role)
		noErr(t, err)
		e := r.expectState(role, leadregistry.StateStopped, "stopped")
		if !strings.Contains(e.Shown.Proof, "another invocation") {
			t.Fatalf("proof %q", e.Shown.Proof)
		}
	})
	t.Run("in the same control group it proves nothing", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.aud.afterSignal = func(st leadunit.State, sig syscall.Signal) {
			if sig != syscall.SIGTERM {
				return
			}
			r.fake.ReuseNextControlGroup(st.Unit)
			_, err := r.fake.Start(context.Background(), spec(st.Unit))
			noErr(t, err)
		}
		_, err := r.stop(role)
		isErr(t, err, ErrStopUnproven)
		r.expectState(role, leadregistry.StateUnknown, "stop-unproven")
	})
}

func TestStopSettlesAPendingIntent(t *testing.T) {
	t.Run("a unit that landed is adopted and then stopped", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.LoseNextStartReply(unitOf(rl))
			r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
		}
		_, err := r.start(role)
		isErr(t, err, ErrLaunchUncertain)
		e, err := r.stop(role)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped || e.Intent.Outcome != leadregistry.OutcomeAdopted {
			t.Fatalf("entry %+v", e)
		}
		if r.callsOf(leadunit.MethodStart) != 1 || len(r.fake.StartedSpecs()) != 1 {
			t.Fatal("more than one start for the intent")
		}
		r.auditClean()
	})
	t.Run("with nothing loaded it waits for the window", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.DropNextStart(unitOf(rl))
		}
		_, err := r.start(role)
		isErr(t, err, ErrLaunchUncertain)
		_, err = r.stop(role)
		isErr(t, err, ErrIntentPending)
		r.noSignals()
		e := r.get(role)
		if e.Desired != leadregistry.DesiredStopped || e.Shown.State != leadregistry.StateUnknown || e.Intent.Outcome != leadregistry.OutcomePending {
			t.Fatalf("entry %+v", e)
		}
		r.advance(DefaultLateStartWindow + DefaultLateStartMargin)
		e, err = r.stop(role)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped || e.Intent.Outcome != leadregistry.OutcomeAbandoned {
			t.Fatalf("entry %+v", e)
		}
	})
}

func TestStopOfAUnitThatIsAlreadyGone(t *testing.T) {
	t.Run("holder crashed", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.fake.Crash(unitOf(role))
		e, err := r.stop(role)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped || !strings.Contains(e.Shown.Proof, e.Unit.InvocationID) {
			t.Fatalf("entry %+v", e)
		}
		r.noSignals()
	})
	t.Run("host rebooted", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.mustStart(role)
		r.fake.Reboot()
		before := r.callsOf(leadunit.MethodEmpty)
		e, err := r.stop(role)
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStopped || !strings.Contains(e.Shown.Proof, "boot identity changed") {
			t.Fatalf("entry %+v", e)
		}
		if r.callsOf(leadunit.MethodEmpty) != before {
			t.Fatal("Empty needed after a reboot")
		}
	})
	t.Run("already stopped", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		rev := r.revision()
		_, err := r.stop(role)
		noErr(t, err)
		if r.revision() != rev {
			t.Fatal("a stop of a stopped role wrote to the registry")
		}
		r.noSignals()
	})
}

func TestStopWithContextEndedLeavesStopping(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.fake.SetProcs(unitOf(role), leadunit.ProcScript{Behavior: leadunit.IgnoreTerm})
	r.mustStart(role)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.ctl.Stop(ctx, role)
		done <- err
	}()
	bctx, bcancel := context.WithTimeout(context.Background(), 10*time.Second)
	noErr(t, r.clock.BlockUntil(bctx, 1))
	bcancel()
	cancel()
	err := <-done
	isErr(t, err, context.Canceled)
	e := r.get(role)
	if e.Shown.State != leadregistry.StateStopping || e.Desired != leadregistry.DesiredStopped {
		t.Fatalf("entry %+v", e)
	}
	// Stop again finishes it.
	_, err = r.stop(role)
	noErr(t, err)
	r.expectState(role, leadregistry.StateStopped, "stopped")
	r.auditClean()
}

func TestOperationsOnOneRoleDoNotInterleave(t *testing.T) {
	r := newRig(t)
	slow := r.define("slow")
	other := r.define("other")
	r.fake.SetProcs(unitOf(slow), leadunit.ProcScript{Behavior: leadunit.IgnoreTerm})
	r.mustStart(slow)
	r.mustStart(other)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := r.ctl.Stop(context.Background(), slow)
		errs <- err
	}()
	bctx, bcancel := context.WithTimeout(context.Background(), 10*time.Second)
	noErr(t, r.clock.BlockUntil(bctx, 1))
	bcancel()

	// A role of its own is not held up by the slow one.
	if _, err := r.ctl.Stop(context.Background(), other); err != nil {
		t.Fatalf("Stop of the other role: %v", err)
	}
	// A second operation on the slow role waits for the first.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := r.ctl.Stop(context.Background(), slow)
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond)
	// The slow role's TERM and the other role's TERM, and nothing more.
	if n := len(r.signalCalls()); n != 2 {
		t.Fatalf("signals so far %d", n)
	}
	r.drive(func() { wg.Wait() })
	close(errs)
	for err := range errs {
		noErr(t, err)
	}
	// slow: TERM then KILL, once. The second Stop found it stopped.
	var slowSignals []syscall.Signal
	for _, c := range r.signalCalls() {
		if c.Unit == unitOf(slow) {
			slowSignals = append(slowSignals, c.Signal)
		}
	}
	if len(slowSignals) != 2 || slowSignals[0] != syscall.SIGTERM || slowSignals[1] != syscall.SIGKILL {
		t.Fatalf("signals to the slow unit %v", slowSignals)
	}
	r.auditClean()
}
