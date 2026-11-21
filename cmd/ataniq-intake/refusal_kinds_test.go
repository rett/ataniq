package main

import (
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/intake"
)

// TestRawReadRefusalsAreRefusals: a commit that declares an encoding other
// than UTF-8 and a commit object in a form the intake does not read are
// refusals the owner can act on, each with a hint of its own.
func TestRawReadRefusalsAreRefusals(t *testing.T) {
	for _, tc := range []struct {
		err  error
		hint string
	}{
		{intake.ErrCommitEncoding, "UTF-8"},
		{intake.ErrCommitFormat, "commit the branch again"},
	} {
		err := fmt.Errorf("%w: detail", tc.err)
		if got := classify(err); got != exitRefused {
			t.Errorf("%v: status %d, want %d", tc.err, got, exitRefused)
		}
		if h := hintFor("review", err); !strings.Contains(h, tc.hint) {
			t.Errorf("%v: hint %q", tc.err, h)
		}
	}
}
