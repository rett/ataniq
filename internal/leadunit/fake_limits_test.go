package leadunit

import (
	"errors"
	"testing"
)

func TestFakeRecordsTheLimitsOfAStartedUnit(t *testing.T) {
	r := newRig(t)
	s := r.spec("ataniq-lead-a.service", 1)
	s.Limits = sampleLimits()
	if _, err := r.fake.Start(r.ctx, s); err != nil {
		t.Fatal(err)
	}
	got, ok := r.fake.UnitLimits(s.Unit)
	if !ok || got != *sampleLimits() {
		t.Fatalf("UnitLimits = %+v, %v", got, ok)
	}
	// The recorded copy is the fake's own: the caller's later change of its
	// pointer does not reach the unit or the started specs.
	s.Limits.TasksMax = 4000
	if got, _ := r.fake.UnitLimits(s.Unit); got != *sampleLimits() {
		t.Fatalf("a change of the caller's Limits reached the unit: %+v", got)
	}
	specs := r.fake.StartedSpecs()
	if len(specs) != 1 || specs[0].Limits == nil || *specs[0].Limits != *sampleLimits() {
		t.Fatalf("StartedSpecs does not carry the limits: %+v", specs)
	}
	specs[0].Limits.MemoryMax = 1
	if got, _ := r.fake.UnitLimits(s.Unit); got != *sampleLimits() {
		t.Fatalf("a change of a returned spec reached the unit: %+v", got)
	}
}

func TestFakeUnitWithoutLimits(t *testing.T) {
	r := newRig(t)
	s := r.spec("ataniq-lead-a.service", 1)
	if _, err := r.fake.Start(r.ctx, s); err != nil {
		t.Fatal(err)
	}
	if l, ok := r.fake.UnitLimits(s.Unit); ok {
		t.Fatalf("a unit started with none has limits %+v", l)
	}
	if l, ok := r.fake.UnitLimits("ataniq-lead-none.service"); ok {
		t.Fatalf("a unit that is not loaded has limits %+v", l)
	}
	if err := r.fake.AddForeign("ataniq-lead-f.service", "foreign"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.fake.UnitLimits("ataniq-lead-f.service"); ok {
		t.Fatal("a foreign unit has limits")
	}
}

func TestFakeRefusesLimitsOutsideTheBounds(t *testing.T) {
	r := newRig(t)
	s := r.spec("ataniq-lead-a.service", 1)
	s.Limits = &Limits{MemoryMax: MinMemoryMax - 1, SwapMax: 0, TasksMax: 128}
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("err = %v, want ErrInvalidSpec", err)
	}
	if r.fake.Loaded(s.Unit) {
		t.Fatal("a refused start loaded the unit")
	}
}

func TestFakeLateStartKeepsTheLimits(t *testing.T) {
	r := newRig(t)
	s := r.spec("ataniq-lead-a.service", 1)
	s.Limits = sampleLimits()
	r.fake.LateNextStart(s.Unit)
	if _, err := r.fake.Start(r.ctx, s); !errors.Is(err, ErrReplyLost) {
		t.Fatalf("err = %v", err)
	}
	s.Limits.MemoryMax = MaxMemoryMax
	if _, err := r.fake.LandLateStart(s.Unit); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.fake.UnitLimits(s.Unit); !ok || got != *sampleLimits() {
		t.Fatalf("late unit limits = %+v, %v", got, ok)
	}
}
