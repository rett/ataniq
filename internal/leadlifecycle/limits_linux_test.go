package leadlifecycle

import (
	"errors"
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

func TestStartWithLimitsOutsideTheBoundsStartsNothing(t *testing.T) {
	r, _ := limitsRig(t, func(leadrole.Role) *leadunit.Limits {
		return &leadunit.Limits{MemoryMax: leadunit.MinMemoryMax - 1, SwapMax: 0, TasksMax: 64}
	})
	role := r.define("alpha")
	_, err := r.start(role)
	isErr(t, err, ErrStartFailed)
	if !errors.Is(err, leadunit.ErrInvalidSpec) {
		t.Fatalf("the refusal is not named: %v", err)
	}
	if len(r.fake.StartedSpecs()) != 0 || r.fake.Loaded(unitOf(role)) {
		t.Fatal("a unit was started with limits outside the bounds")
	}
}
