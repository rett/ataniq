package leadlifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

func unitOf(role leadrole.Role) string { return role.UnitName() }

func TestNewRequiresWhatItNeeds(t *testing.T) {
	r := newRig(t)
	base := Options{Registry: r.reg, Manager: r.fake, Preflight: r.pre, Holder: r.holder}
	for name, mut := range map[string]func(*Options){
		"registry":  func(o *Options) { o.Registry = nil },
		"manager":   func(o *Options) { o.Manager = nil },
		"preflight": func(o *Options) { o.Preflight = nil },
		"holder":    func(o *Options) { o.Holder = nil },
		"duration":  func(o *Options) { o.ProofBound = -1 },
	} {
		o := base
		mut(&o)
		if _, err := New(o); err == nil {
			t.Errorf("New without %s succeeded", name)
		}
	}
	if _, err := New(base); err != nil {
		t.Fatalf("New with the required options: %v", err)
	}
}

func TestStartLaunchesAndNeverSetsReady(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	var atBuild leadregistry.Entry
	var startedAtBuild int
	r.holder.hook = func(_ leadrole.Role, e leadregistry.Entry, in leadregistry.Intent) {
		atBuild = e
		startedAtBuild = len(r.fake.StartedSpecs())
	}
	e, err := r.start(role)
	noErr(t, err)

	// The intent was journaled before the command was built and before any
	// Start reached the manager.
	if atBuild.Intent == nil || atBuild.Intent.Outcome != leadregistry.OutcomePending || atBuild.Shown.State != leadregistry.StateStarting || startedAtBuild != 0 {
		t.Fatalf("at Build: %+v started=%d", atBuild, startedAtBuild)
	}
	if e.Epoch != 1 || e.Shown.State != leadregistry.StateStarting || e.Desired != leadregistry.DesiredRunning {
		t.Fatalf("entry %+v", e)
	}
	if e.Intent.Outcome != leadregistry.OutcomeLaunched || e.Intent.UnitName != unitOf(role) {
		t.Fatalf("intent %+v", e.Intent)
	}
	live, _ := r.fake.Inspect(context.Background(), unitOf(role))
	if e.Unit.InvocationID != live.InvocationID || e.Unit.ControlGroupPath != live.ControlGroup ||
		e.Unit.ControlGroupID != live.ControlGroupID || e.Unit.BootID != r.fake.BootID() || e.Unit.InvocationID == "" {
		t.Fatalf("unit evidence %+v, live %+v", e.Unit, live)
	}
	specs := r.fake.StartedSpecs()
	if len(specs) != 1 || specs[0].Epoch != e.Epoch || specs[0].Intent != e.Intent.ID || specs[0].Unit != unitOf(role) ||
		specs[0].WorkDir != "/work" || len(specs[0].Argv) != 3 || specs[0].Env[0] != "HOLDER_EPOCH=1" {
		t.Fatalf("spec %+v", specs)
	}
	// Start never waits for readiness: it is starting, not ready, however long we wait.
	r.advance(time.Hour)
	if got := r.get(role).Shown.State; got != leadregistry.StateStarting {
		t.Fatalf("state %s", got)
	}
	r.auditClean()
}

func TestStartRefusals(t *testing.T) {
	r := newRig(t)
	if _, err := r.start(mkRole(t, "nobody")); !errors.Is(err, leadregistry.ErrNoRole) {
		t.Fatalf("undefined role: %v", err)
	}
	tmux := r.defineTmux("old")
	rev := r.revision()
	if _, err := r.start(tmux); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("tmux-manager role: %v", err)
	}
	role := r.define("alpha")
	rev = r.revision()
	r.pre.err = errors.New("no login")
	_, err := r.start(role)
	isErr(t, err, ErrPreflight)
	if !strings.Contains(err.Error(), "no login") {
		t.Fatalf("preflight cause lost: %v", err)
	}
	if r.revision() != rev || len(r.fake.Calls()) != 0 || r.holder.calls != 0 {
		t.Fatalf("a refused start changed something: rev %d->%d calls %d", rev, r.revision(), len(r.fake.Calls()))
	}
	r.pre.err = nil

	// An unknown role is not launchable: the registry's rule, before any unit call.
	if _, err := r.reg.SetState(role, leadregistry.StateChange{Epoch: 0, State: leadregistry.StateUnknown,
		Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test"}}); err != nil {
		t.Fatal(err)
	}
	rev = r.revision()
	_, err = r.start(role)
	isErr(t, err, leadregistry.ErrLaunchRefused)
	isErr(t, err, leadregistry.ErrLaunchState)
	if r.revision() != rev || len(r.fake.Calls()) != 0 {
		t.Fatal("a refusal for the shown state touched the registry or the manager")
	}
}

func TestL3SecondStartRefusedAtTheRegistry(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.mustStart(role)
	rev := r.revision()
	calls := len(r.fake.Calls())
	_, err := r.start(role)
	isErr(t, err, leadregistry.ErrLaunchRefused)
	if r.revision() != rev || len(r.fake.Calls()) != calls || len(r.fake.StartedSpecs()) != 1 {
		t.Fatalf("second start did something: calls %d->%d starts %d", calls, len(r.fake.Calls()), len(r.fake.StartedSpecs()))
	}
	r.auditClean()
}

func TestStartUnitExistsAdoptsOurOwnUnit(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	// The unit with this very epoch and intent is already there (a request that
	// landed by another route): it is adopted, not reported as foreign.
	r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, in leadregistry.Intent) {
		_, err := r.fake.Start(context.Background(), leadunit.Spec{
			Unit: in.UnitName, Epoch: in.Epoch, Intent: in.ID,
			Argv: []string{"/bin/x"}, WorkDir: "/w",
		})
		if err != nil {
			t.Error(err)
		}
	}
	e, err := r.start(role)
	noErr(t, err)
	if e.Intent.Outcome != leadregistry.OutcomeAdopted || e.Shown.State != leadregistry.StateStarting || e.Unit.InvocationID == "" {
		t.Fatalf("entry %+v", e)
	}
}

func TestStartRefusesALoadedUnit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		desc    string
		foreign bool
	}{
		{"foreign", "somebody's unit", true},
		{"ours with another epoch", "ataniq lead epoch=9 intent=zzz", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			role := r.define("alpha")
			noErr(t, r.fake.AddForeign(unitOf(role), tc.desc))
			rev := r.revision()
			_, err := r.start(role)
			isErr(t, err, ErrUnitLoaded)
			if got := errors.Is(err, ErrForeignUnit); got != tc.foreign {
				t.Fatalf("ErrForeignUnit %v for %v", got, err)
			}
			// Nothing changes: not the registry, not the manager.
			if r.revision() != rev || r.callsOf(leadunit.MethodStart) != 0 || r.holder.calls != 0 {
				t.Fatal("refused start changed something")
			}
			if e := r.get(role); e.Epoch != 0 || e.Shown.State != leadregistry.StateStopped {
				t.Fatalf("entry %+v", e)
			}
			r.noSignals()
		})
	}
}

func TestStartManagerUnavailableAtEachStep(t *testing.T) {
	t.Run("inspect before the intent", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
		rev := r.revision()
		_, err := r.start(role)
		isErr(t, err, ErrNoProof)
		isErr(t, err, leadunit.ErrUnavailable)
		if r.revision() != rev || r.callsOf(leadunit.MethodStart) != 0 {
			t.Fatal("changed something on doubt")
		}
	})
	t.Run("empty before the intent", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.fake.MakeUnavailable(leadunit.MethodEmpty, 1)
		rev := r.revision()
		_, err := r.start(role)
		isErr(t, err, ErrNoProof)
		if r.revision() != rev || r.callsOf(leadunit.MethodStart) != 0 {
			t.Fatal("changed something on doubt")
		}
		if e := r.get(role); e.Shown.State != leadregistry.StateStopped || e.Epoch != 0 {
			t.Fatalf("entry %+v", e)
		}
		// Doubt is not sticky: the next attempt starts.
		r.mustStart(role)
	})
	t.Run("start, then a working inspect", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.MakeUnavailable(leadunit.MethodStart, 1)
		}
		e, err := r.start(role)
		isErr(t, err, ErrStartFailed)
		r.expectState(role, leadregistry.StateStopped, "launch-failed")
		if e.Intent.Outcome != leadregistry.OutcomeFailed || r.callsOf(leadunit.MethodStart) != 1 {
			t.Fatalf("entry %+v", e)
		}
		// The epoch was consumed and is not reused.
		r.holder.hook = nil
		if e := r.mustStart(role); e.Epoch != 2 {
			t.Fatalf("epoch %d", e.Epoch)
		}
	})
}

func TestStartNeverRetries(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.fake.FailNextStart(unitOf(role), errors.New("scripted"))
	_, err := r.start(role)
	isErr(t, err, ErrStartFailed)
	if r.callsOf(leadunit.MethodStart) != 1 {
		t.Fatalf("Start called %d times", r.callsOf(leadunit.MethodStart))
	}
	e := r.expectState(role, leadregistry.StateStopped, "launch-failed")
	if e.Intent.Outcome != leadregistry.OutcomeFailed || !strings.Contains(e.Shown.Proof, "started nothing") {
		t.Fatalf("entry %+v", e)
	}
}

func TestStartInvalidSpecIsFailedAndStopped(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.holder.hook = nil
	bad := &badHolder{}
	ctl, err := New(Options{Registry: r.reg, Manager: r.aud, Clock: r.clock, Preflight: r.pre, Holder: bad})
	noErr(t, err)
	var e leadregistry.Entry
	r.drive(func() { e, err = ctl.Start(context.Background(), role) })
	isErr(t, err, ErrStartFailed)
	isErr(t, err, leadunit.ErrInvalidSpec)
	if e.Intent.Outcome != leadregistry.OutcomeFailed || e.Shown.State != leadregistry.StateStopped {
		t.Fatalf("entry %+v", e)
	}
}

type badHolder struct{}

func (badHolder) Build(role leadrole.Role, e leadregistry.Entry, in leadregistry.Intent) ([]string, string, []string, error) {
	return []string{"relative"}, "/w", nil, nil
}

func TestStartBuildFailure(t *testing.T) {
	t.Run("proof re-established", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.err = errors.New("no holder binary")
		e, err := r.start(role)
		isErr(t, err, ErrStartFailed)
		if e.Intent.Outcome != leadregistry.OutcomeAbandoned || e.Shown.State != leadregistry.StateStopped || e.Shown.Proof == "" || e.Epoch != 1 {
			t.Fatalf("entry %+v", e)
		}
		if r.callsOf(leadunit.MethodStart) != 0 {
			t.Fatal("Start called")
		}
		r.holder.err = nil
		if e := r.mustStart(role); e.Epoch != 2 {
			t.Fatalf("epoch %d", e.Epoch)
		}
	})
	t.Run("a unit appeared, no proof", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.err = errors.New("no holder binary")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			noErr(t, r.fake.AddForeign(unitOf(rl), "intruder"))
		}
		e, err := r.start(role)
		isErr(t, err, ErrStartFailed)
		if e.Intent.Outcome != leadregistry.OutcomeAbandoned || e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("entry %+v", e)
		}
	})
	t.Run("inspect unavailable, no proof", func(t *testing.T) {
		r := newRig(t)
		role := r.define("alpha")
		r.holder.err = errors.New("no holder binary")
		r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
			r.fake.MakeUnavailable(leadunit.MethodInspect, 1)
		}
		e, err := r.start(role)
		isErr(t, err, ErrStartFailed)
		if e.Intent.Outcome != leadregistry.OutcomeAbandoned || e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("entry %+v", e)
		}
	})
}

func TestStartL1ReplyLostUnitRunningIsAdopted(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
		r.fake.LoseNextStartReply(unitOf(rl))
	}
	e, err := r.start(role)
	noErr(t, err)
	if e.Intent.Outcome != leadregistry.OutcomeAdopted || e.Shown.State != leadregistry.StateStarting || e.Unit.InvocationID == "" {
		t.Fatalf("entry %+v", e)
	}
	if r.callsOf(leadunit.MethodStart) != 1 || len(r.fake.StartedSpecs()) != 1 {
		t.Fatalf("Start calls %d, started %d", r.callsOf(leadunit.MethodStart), len(r.fake.StartedSpecs()))
	}
	r.auditClean()
}

func TestStartContextEndedAfterTheRequestIsAdopted(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.holder.hook = func(rl leadrole.Role, _ leadregistry.Entry, _ leadregistry.Intent) {
		r.fake.ExpireNextStart(unitOf(rl))
	}
	e, err := r.start(role)
	noErr(t, err)
	if e.Intent.Outcome != leadregistry.OutcomeAdopted {
		t.Fatalf("entry %+v", e)
	}
}

func TestStartWithContextAlreadyDone(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rev := r.revision()
	_, err := r.ctl.Start(ctx, role)
	isErr(t, err, context.Canceled)
	if r.revision() != rev {
		t.Fatal("registry changed")
	}
}
