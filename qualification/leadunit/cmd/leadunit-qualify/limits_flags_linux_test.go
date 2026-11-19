//go:build linux

package main

import (
	"errors"
	"testing"

	"ataniq/internal/leadunit"
)

// The flag check is the specification's own check of the limits, so that the
// command and the unit cannot disagree about the bounds.
func TestLimitFlagsAreRefusedByTheBoundsOfTheSpecification(t *testing.T) {
	for _, args := range [][]string{
		{"--memory-max", "255M"}, {"--memory-max", "65G"}, {"--swap-max", "65G"}, {"--tasks-max", "15"}, {"--tasks-max", "65537"},
	} {
		_, err := parseArgs(args)
		if !errors.Is(err, leadunit.ErrInvalidSpec) {
			t.Errorf("%q: %v", args, err)
		}
	}
	// The edges of the bounds are accepted.
	for _, args := range [][]string{
		{"--memory-max", "64G", "--swap-max", "64G", "--tasks-max", "65536", "--memory-lowered", "256M"},
		{"--tasks-max", "16", "--swap-max", "0"},
	} {
		if _, err := parseArgs(args); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}
