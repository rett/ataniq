//go:build linux && ataniq_canary

package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
)

// The tests of this file are about the checks that run after the work, on its
// result, and before the marker. The work itself never produces what they
// refuse, so each test makes a git, or a moment, that does.

// tamperingGit writes a program that runs the real git and, when one of its
// arguments is after and git succeeded, runs the shell text then in the same
// folder (the tree). It returns the program's path.
func tamperingGit(t testing.TB, after, then string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\n/usr/bin/git \"$@\" || exit $?\nfor a in \"$@\"; do\n  if [ \"$a\" = '" + after + "' ]; then\n" + then + "\n  fi\ndone\nexit 0\n"
	provisiontest.WriteFile(t, p, []byte(script), 0o700)
	return p
}

// useGit puts a program in the place of git for the test.
func useGit(t testing.TB, path string) {
	t.Helper()
	old := GitPath
	t.Cleanup(func() { GitPath = old })
	GitPath = path
}

// noMarkerLine fails when a line reports the marker.
func noMarkerLine(t *testing.T, p Paths, lines []string) {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, p.Marker) {
			t.Errorf("a marker line after a refusal: %q", l)
		}
	}
}

func TestACredentialHelperInTheClonesConfigRefusesTheRun(t *testing.T) {
	// The clone gains a credential helper after the remote is set, which is
	// the last change provisioning makes to the configuration: only the look
	// at the finished configuration can refuse it.
	w := provisiontest.New(t)
	p := mustPaths(t, w)
	useGit(t, tamperingGit(t, "set-url", "    /usr/bin/git config --local credential.helper store || exit 1"))
	lines, err := provisionWorld(w)
	if !errors.Is(err, ErrBadCloneConfig) {
		t.Fatalf("Provision: %v, want ErrBadCloneConfig\n%q", err, lines)
	}
	if strings.Contains(err.Error(), "store") {
		t.Errorf("the refusal holds a value of the configuration: %v", err)
	}
	// The helper really was in the tree's configuration: the refusal is the
	// check's, not a failure of the program.
	if got := provisiontest.Git(t, p.Tree, "config", "--local", "credential.helper"); got != "store" {
		t.Fatalf("the tree's credential helper is %q; the test shows nothing", got)
	}
	if exists(p.Marker) {
		t.Fatal("a marker was written")
	}
	noMarkerLine(t, p, lines)
}

func TestAnAlternatesFileInTheCloneRefusesTheRun(t *testing.T) {
	// The clone borrows objects from elsewhere: it is not the lead's own
	// clone. Provisioning's own git never writes the file (it copies the
	// objects), so only the own-clone check on the result can refuse it.
	w := provisiontest.New(t)
	p := mustPaths(t, w)
	useGit(t, tamperingGit(t, "clone",
		"    mkdir -p .git/objects/info && printf '%s\\n' '"+filepath.Join(w.Mirror, "objects")+"' > .git/objects/info/alternates || exit 1"))
	lines, err := provisionWorld(w)
	if err == nil || !strings.Contains(err.Error(), "the own-clone check") {
		t.Fatalf("Provision: %v, want a refusal of the own-clone check\n%q", err, lines)
	}
	if !exists(filepath.Join(p.Tree, ".git", "objects", "info", "alternates")) {
		t.Fatal("the tree has no alternates file; the test shows nothing")
	}
	if exists(p.Marker) {
		t.Fatal("a marker was written")
	}
	noMarkerLine(t, p, lines)
	// The checks come before the runtime folder, which is made between them
	// and the marker.
	if exists(p.RuntimeFolder) {
		t.Error("the runtime folder was made after the refusal")
	}
}

func TestAStateRootThatIsReplacedBeforeTheMarkerRefusesTheRun(t *testing.T) {
	// Everything is made beneath the handle of the state root, but the marker
	// is written by the root's path. When the name comes to hold another
	// folder after the checks of the result (here: as the runtime folder,
	// the last thing before the marker, is reported), the marker must not be
	// written there: that folder was never provisioned.
	w := provisiontest.New(t)
	p := mustPaths(t, w)
	moved := filepath.Join(w.Dir, "state-moved")
	var lines []string
	swapped := false
	logf := func(line string) {
		lines = append(lines, line)
		if line != "made folder "+p.RuntimeFolder {
			return
		}
		if err := os.Rename(w.State, moved); err != nil {
			t.Errorf("rename: %v", err)
			return
		}
		if err := os.Mkdir(w.State, 0o700); err != nil {
			t.Errorf("mkdir: %v", err)
			return
		}
		if err := os.Chmod(w.State, 0o700); err != nil {
			t.Errorf("chmod: %v", err)
			return
		}
		swapped = true
	}
	err := Provision(context.Background(), w.Cfg, Options{Getenv: w.Getenv(), Log: logf})
	if !swapped {
		t.Fatalf("the state root was never replaced; the test shows nothing (%v)\n%q", err, lines)
	}
	if err == nil || !strings.Contains(err.Error(), "is not the folder that was provisioned") {
		t.Fatalf("Provision: %v, want a refusal of the replaced state root\n%q", err, lines)
	}
	// No marker in the folder that took the name, which stays empty, nor in
	// the folder that was provisioned.
	if entries, err := os.ReadDir(w.State); err != nil || len(entries) != 0 {
		t.Fatalf("the folder that took the state root's name holds %v (%v)", entries, err)
	}
	if exists(filepath.Join(moved, filepath.Base(p.Marker))) {
		t.Fatal("a marker was written in the provisioned folder")
	}
	noMarkerLine(t, p, lines)
	// The provisioned folder is the one the work went into.
	if !exists(filepath.Join(moved, "leads")) {
		t.Fatal("the work is not in the folder that was moved away")
	}
}
