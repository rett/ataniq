package main

import (
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/intake"
	"ataniq/internal/mirror"
)

// TestRawReadRefusalsAreRefusals: a commit that declares an encoding other
// than UTF-8, a commit object in a form the intake does not read, and a
// template repository path that exists on the host are refusals the owner can
// act on, each with a hint of its own.
func TestRawReadRefusalsAreRefusals(t *testing.T) {
	for _, tc := range []struct {
		err  error
		hint string
	}{
		{intake.ErrCommitEncoding, "UTF-8"},
		{intake.ErrCommitFormat, "commit the branch again"},
		{mirror.ErrRepositoryPath, "does not exist here"},
		{mirror.ErrRepositoryPath, "or cannot be checked here"},
		{mirror.ErrRepositoryPath, "The mirror was not changed."},
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

// TestRepositoryPathHintIsTrueForBothChecks: the refusal comes from the check
// before the measure (nothing has run) or from the check before git starts
// (the measure has run and the staging folder was made and removed), and the
// path may exist or be one that cannot be checked. The hint claims only what
// holds for all of these: the mirror is unchanged. It does not say that
// nothing was run, and it names both kinds of refusal.
func TestRepositoryPathHintIsTrueForBothChecks(t *testing.T) {
	h := hintFor("fetch", fmt.Errorf("%w: detail", mirror.ErrRepositoryPath))
	const want = "The sandbox template's serve.repository names a path that exists on this host or cannot be checked here. Choose a path that does not exist here. The mirror was not changed."
	if h != want {
		t.Errorf("hint = %q, want %q", h, want)
	}
	if strings.Contains(h, "Nothing was run") {
		t.Errorf("the hint says that nothing was run: %q", h)
	}
}
