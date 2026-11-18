package leadlifecycle

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// limitsRig is a rig whose controller has the Limits seam, answering from a
// function the test sets. It returns the roles the seam was asked about.
func limitsRig(t *testing.T, answer func(role leadrole.Role) *leadunit.Limits) (*rig, func() []leadrole.Role) {
	t.Helper()
	r := newRig(t)
	var mu sync.Mutex
	var asked []leadrole.Role
	ctl, err := New(Options{
		Registry: r.reg, Manager: r.aud, Clock: r.clock, Preflight: r.pre, Holder: r.holder, Quiesce: r.quiet,
		Limits: func(role leadrole.Role) *leadunit.Limits {
			mu.Lock()
			asked = append(asked, role)
			mu.Unlock()
			return answer(role)
		},
	})
	noErr(t, err)
	r.ctl = ctl
	return r, func() []leadrole.Role {
		mu.Lock()
		defer mu.Unlock()
		return append([]leadrole.Role(nil), asked...)
	}
}

func TestStartPutsTheLimitsOfTheSeamInTheSpec(t *testing.T) {
	want := leadunit.Limits{MemoryMax: 2 << 30, SwapMax: 0, TasksMax: 256}
	r, asked := limitsRig(t, func(leadrole.Role) *leadunit.Limits { l := want; return &l })
	role := r.define("alpha")
	r.mustStart(role)

	if got := asked(); len(got) != 1 || got[0] != role {
		t.Fatalf("the seam was asked %v, want once for %s", got, role)
	}
	specs := r.fake.StartedSpecs()
	if len(specs) != 1 || specs[0].Limits == nil || *specs[0].Limits != want {
		t.Fatalf("spec limits: %+v", specs)
	}
	if got, ok := r.fake.UnitLimits(unitOf(role)); !ok || got != want {
		t.Fatalf("the unit has limits %+v, %v", got, ok)
	}
	r.auditClean()
}

func TestStartAsksTheSeamOncePerLaunch(t *testing.T) {
	r, asked := limitsRig(t, func(leadrole.Role) *leadunit.Limits {
		return &leadunit.Limits{MemoryMax: 1 << 30, SwapMax: 1 << 20, TasksMax: 64}
	})
	role := r.define("alpha")
	r.mustStart(role)
	if n := len(asked()); n != 1 {
		t.Fatalf("asked %d times for the first launch", n)
	}
	_, err := r.ctl.Stop(t.Context(), role)
	noErr(t, err)
	r.mustStart(role)
	if n := len(asked()); n != 2 {
		t.Fatalf("asked %d times for two launches", n)
	}
}

func TestStartAsksTheSeamForTheRoleOfACoordinator(t *testing.T) {
	r, asked := limitsRig(t, func(role leadrole.Role) *leadunit.Limits {
		if role.Kind() == leadrole.KindCoordinator {
			return &leadunit.Limits{MemoryMax: 512 << 20, SwapMax: 0, TasksMax: 32}
		}
		return nil
	})
	lead := r.define("alpha")
	coord := r.defineCoord("alpha", "feat")
	r.mustStart(lead)
	r.mustStart(coord)
	if got := asked(); len(got) != 2 || got[0] != lead || got[1] != coord {
		t.Fatalf("asked %v", got)
	}
	if _, ok := r.fake.UnitLimits(unitOf(lead)); ok {
		t.Fatal("the lead, for which the seam answered nil, has limits")
	}
	if l, ok := r.fake.UnitLimits(unitOf(coord)); !ok || l.TasksMax != 32 {
		t.Fatalf("the coordinator's limits: %+v, %v", l, ok)
	}
}

func TestStartWithANilAnswerHasNoLimits(t *testing.T) {
	r, asked := limitsRig(t, func(leadrole.Role) *leadunit.Limits { return nil })
	role := r.define("alpha")
	r.mustStart(role)
	if len(asked()) != 1 {
		t.Fatalf("asked %v", asked())
	}
	if specs := r.fake.StartedSpecs(); len(specs) != 1 || specs[0].Limits != nil {
		t.Fatalf("spec limits: %+v", specs)
	}
	if l, ok := r.fake.UnitLimits(unitOf(role)); ok {
		t.Fatalf("the unit has limits %+v", l)
	}
}

func TestStartWithoutTheSeamHasNoLimits(t *testing.T) {
	r := newRig(t)
	role := r.define("alpha")
	r.mustStart(role)
	if specs := r.fake.StartedSpecs(); len(specs) != 1 || specs[0].Limits != nil {
		t.Fatalf("spec limits: %+v", specs)
	}
	if l, ok := r.fake.UnitLimits(unitOf(role)); ok {
		t.Fatalf("the unit has limits %+v", l)
	}
}

func TestStartWithLimitsOutsideTheBoundsChangesNothing(t *testing.T) {
	bad := map[string]leadunit.Limits{
		"memory below": {MemoryMax: leadunit.MinMemoryMax - 1, SwapMax: 0, TasksMax: 64},
		"memory above": {MemoryMax: leadunit.MaxMemoryMax + 1, SwapMax: 0, TasksMax: 64},
		"swap above":   {MemoryMax: 1 << 30, SwapMax: leadunit.MaxSwapMax + 1, TasksMax: 64},
		"tasks below":  {MemoryMax: 1 << 30, SwapMax: 0, TasksMax: leadunit.MinTasksMax - 1},
		"tasks above":  {MemoryMax: 1 << 30, SwapMax: 0, TasksMax: leadunit.MaxTasksMax + 1},
	}
	for name, l := range bad {
		r, asked := limitsRig(t, func(leadrole.Role) *leadunit.Limits { c := l; return &c })
		role := r.define("alpha")
		before := r.get(role)
		rev := r.revision()
		for try := 0; try < 2; try++ {
			_, err := r.start(role)
			if !errors.Is(err, leadunit.ErrInvalidSpec) {
				t.Fatalf("%s: the refusal is not named: %v", name, err)
			}
			if errors.Is(err, ErrStartFailed) {
				t.Fatalf("%s: a refusal before the launch is a failed start: %v", name, err)
			}
			if got := r.get(role); !reflect.DeepEqual(got, before) || got.Epoch != before.Epoch || got.Desired != before.Desired ||
				got.Intent != nil || got.Shown.Reason.Code == "launch-failed" {
				t.Fatalf("%s: the refusal changed the role: %+v, was %+v", name, got, before)
			}
			if r.revision() != rev {
				t.Fatalf("%s: the registry's revision moved %d to %d", name, rev, r.revision())
			}
		}
		if n := len(asked()); n != 2 {
			t.Fatalf("%s: asked %d times for two starts", name, n)
		}
		if len(r.fake.Calls()) != 0 || len(r.fake.StartedSpecs()) != 0 || r.fake.Loaded(unitOf(role)) || r.holder.calls != 0 {
			t.Fatalf("%s: something was started or built", name)
		}
		r.auditClean()
	}
}

func TestStartRefusesBadLimitsBeforeTheUnitIsLookedAt(t *testing.T) {
	// The refusal comes before the replacement rule: no unit call at all.
	r, _ := limitsRig(t, func(leadrole.Role) *leadunit.Limits { return &leadunit.Limits{MemoryMax: 1, TasksMax: 64} })
	role := r.define("alpha")
	if _, err := r.start(role); !errors.Is(err, leadunit.ErrInvalidSpec) {
		t.Fatalf("%v", err)
	}
	if n := len(r.fake.Calls()); n != 0 {
		t.Fatalf("%d unit calls before the refusal", n)
	}
	// And the preflight comes first: a refused preflight does not ask the seam.
	r, asked := limitsRig(t, func(leadrole.Role) *leadunit.Limits { return &leadunit.Limits{MemoryMax: 1, TasksMax: 64} })
	role = r.define("alpha")
	r.pre.err = errors.New("no login")
	_, err := r.start(role)
	isErr(t, err, ErrPreflight)
	if len(asked()) != 0 {
		t.Fatalf("the seam was asked %v after a preflight refusal", asked())
	}
}

func TestStartGivesTheSpecACopyOfTheAnswer(t *testing.T) {
	var kept *leadunit.Limits
	r, _ := limitsRig(t, func(leadrole.Role) *leadunit.Limits {
		kept = &leadunit.Limits{MemoryMax: 1 << 30, SwapMax: 0, TasksMax: 64}
		return kept
	})
	var seen *leadunit.Limits
	r.aud.beforeStart = func(spec leadunit.Spec) { seen = spec.Limits }
	r.mustStart(r.define("alpha"))
	if seen == nil || *seen != *kept {
		t.Fatalf("the spec had %+v, the seam answered %+v", seen, kept)
	}
	if seen == kept {
		t.Fatal("the spec holds the pointer the seam answered, not a copy")
	}
}
