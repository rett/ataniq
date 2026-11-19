package leadunit

import (
	"errors"
	"testing"
)

func TestLimitsValidateIsTheBoundCheckOfTheSpec(t *testing.T) {
	good := []Limits{
		{MemoryMax: MinMemoryMax, SwapMax: MinSwapMax, TasksMax: MinTasksMax},
		{MemoryMax: MaxMemoryMax, SwapMax: MaxSwapMax, TasksMax: MaxTasksMax},
		*sampleLimits(),
	}
	for _, l := range good {
		if err := l.Validate(); err != nil {
			t.Errorf("%+v: %v", l, err)
		}
	}
	bad := []Limits{
		{MemoryMax: MinMemoryMax - 1, SwapMax: 0, TasksMax: 64},
		{MemoryMax: MaxMemoryMax + 1, SwapMax: 0, TasksMax: 64},
		{MemoryMax: 1 << 30, SwapMax: MaxSwapMax + 1, TasksMax: 64},
		{MemoryMax: 1 << 30, SwapMax: 0, TasksMax: MinTasksMax - 1},
		{MemoryMax: 1 << 30, SwapMax: 0, TasksMax: MaxTasksMax + 1},
		{},
	}
	for _, l := range bad {
		err := l.Validate()
		if !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("%+v: %v", l, err)
		}
		// The specification refuses the same values, for the same reason.
		s := validSpec()
		s.Limits = &l
		if serr := s.Validate(); !errors.Is(serr, ErrInvalidSpec) || serr.Error() != err.Error() {
			t.Errorf("%+v: spec says %v, limits say %v", l, serr, err)
		}
	}
}
