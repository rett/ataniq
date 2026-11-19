package mergebuild

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/exchange/gitenv"
)

// racingGit returns a git that plays a second writer: before it passes an
// "update-ref" run on to the real git, it first runs the real git with the
// same command line and the instruction given as its input, so that the
// pin has changed by the time the package's own instruction arrives.
func racingGit(t *testing.T, real gitenv.Git, instruction string) gitenv.Git {
	t.Helper()
	dir := t.TempDir()
	first := filepath.Join(dir, "first")
	if err := os.WriteFile(first, []byte(instruction+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = update-ref ]; then\n" +
		"    " + shellWord(real.Path) + " \"$@\" < " + shellWord(first) + " || exit 97\n" +
		"    break\n" +
		"  fi\n" +
		"done\n" +
		"exec " + shellWord(real.Path) + " \"$@\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, err := gitenv.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// Build creates the pin and never overwrites one: when another writer makes
// the pin after Build looked and before its instruction runs, the error is
// ErrPinExists and the pin stays as the other writer left it. This is the
// instruction's own guard, not the look Build makes first.
func TestBuildCreatesThePinAndNeverUpdatesIt(t *testing.T) {
	noGoroutineLeft(t)
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	pin := "refs/ataniq/pending/k"
	repo := Repo{Git: racingGit(t, l.repo.Git, "create "+pin+" "+s.base), GitDir: l.repo.GitDir, Timeout: l.repo.Timeout}

	m, err := Build(ctx, repo, inputs(s.target, s.feature), "k")
	if !errors.Is(err, ErrPinExists) || m != (Merge{}) {
		t.Errorf("Build returned %+v, %v; want ErrPinExists", m, err)
	}
	if got, want := l.refs(), s.base+" "+pin; got != want {
		t.Errorf("the refs are %q, want the other writer's pin %q", got, want)
	}
}

// Unpin deletes with the old value: when another writer moves the pin after
// Unpin looked and before its instruction runs, the delete fails and the pin
// stays where the other writer put it.
func TestUnpinDeletesOnlyWithTheOldValue(t *testing.T) {
	noGoroutineLeft(t)
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	pin := "refs/ataniq/pending/k"
	m, err := Build(ctx, l.repo, inputs(s.target, s.feature), "k")
	if err != nil {
		t.Fatal(err)
	}
	repo := Repo{Git: racingGit(t, l.repo.Git, "update "+pin+" "+s.base), GitDir: l.repo.GitDir, Timeout: l.repo.Timeout}

	err = Unpin(ctx, repo, "k", m.Commit)
	var exit *gitExit
	if !errors.As(err, &exit) {
		t.Errorf("Unpin returned %v, want git's refusal", err)
	}
	if got, want := l.refs(), s.base+" "+pin; got != want {
		t.Errorf("the refs are %q, want the pin at the other writer's value %q", got, want)
	}
}
