package leadunit

import (
	"errors"
	"reflect"
	"testing"
)

// sampleLimits is a set of limits inside every bound, for tests only.
func sampleLimits() *Limits {
	return &Limits{MemoryMax: 1 << 30, SwapMax: 0, TasksMax: 128}
}

func TestLimitBoundsAreTheSpecifiedNumbers(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want uint64
	}{
		{"MinMemoryMax", MinMemoryMax, 256 * 1024 * 1024},
		{"MaxMemoryMax", MaxMemoryMax, 64 * 1024 * 1024 * 1024},
		{"MinTasksMax", MinTasksMax, 16},
		{"MaxTasksMax", MaxTasksMax, 65536},
		{"MinSwapMax", MinSwapMax, 0},
		{"MaxSwapMax", MaxSwapMax, 64 * 1024 * 1024 * 1024},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestSpecValidateLimits(t *testing.T) {
	ok := []Limits{
		*sampleLimits(),
		{MemoryMax: MinMemoryMax, SwapMax: MinSwapMax, TasksMax: MinTasksMax},
		{MemoryMax: MaxMemoryMax, SwapMax: MaxSwapMax, TasksMax: MaxTasksMax},
	}
	for _, l := range ok {
		s := validSpec()
		s.Limits = &l
		if err := s.Validate(); err != nil {
			t.Errorf("%+v refused: %v", l, err)
		}
	}
	if err := validSpec().Validate(); err != nil {
		t.Fatalf("a spec with no limits is refused: %v", err)
	}

	bad := []struct {
		name string
		mut  func(*Limits)
	}{
		{"memory zero", func(l *Limits) { l.MemoryMax = 0 }},
		{"memory below", func(l *Limits) { l.MemoryMax = MinMemoryMax - 1 }},
		{"memory above", func(l *Limits) { l.MemoryMax = MaxMemoryMax + 1 }},
		{"swap above", func(l *Limits) { l.SwapMax = MaxSwapMax + 1 }},
		{"tasks zero", func(l *Limits) { l.TasksMax = 0 }},
		{"tasks below", func(l *Limits) { l.TasksMax = MinTasksMax - 1 }},
		{"tasks above", func(l *Limits) { l.TasksMax = MaxTasksMax + 1 }},
		{"memory max uint64", func(l *Limits) { l.MemoryMax = ^uint64(0) }},
	}
	for _, c := range bad {
		l := *sampleLimits()
		c.mut(&l)
		s := validSpec()
		s.Limits = &l
		if err := s.Validate(); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%s: err = %v, want ErrInvalidSpec", c.name, err)
		}
	}
}

func TestLimitPropertiesOrderAndValues(t *testing.T) {
	got := LimitProperties(Limits{MemoryMax: 1 << 30, SwapMax: 0, TasksMax: 128})
	want := []Property{
		{Name: "MemoryMax", Value: "1073741824"},
		{Name: "MemorySwapMax", Value: "0"},
		{Name: "TasksMax", Value: "128"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LimitProperties = %v, want %v", got, want)
	}
}

func TestSpecCloneCopiesTheLimits(t *testing.T) {
	s := validSpec()
	s.Limits = sampleLimits()
	c := s.clone()
	if c.Limits == s.Limits {
		t.Fatal("clone shares the Limits pointer")
	}
	if *c.Limits != *s.Limits {
		t.Fatalf("clone changed the limits: %+v", *c.Limits)
	}
	s.Limits.TasksMax = 999
	if c.Limits.TasksMax != 128 {
		t.Fatalf("a change of the original reached the clone: %+v", *c.Limits)
	}
	if got := validSpec().clone(); got.Limits != nil {
		t.Fatalf("clone of a spec with no limits has %+v", got.Limits)
	}
}
