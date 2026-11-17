package tests

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const changelogBase = `# Changelog

Intro.

## [Unreleased]

### Added

- Old added.
  Wrapped.

### Changed

- Old changed.

## [0.0.1] - 2026-01-01

### Added

- Released.
`

// changelogRoot makes a throwaway repository root holding the fold script, a
// CHANGELOG.md and the given fragments, so no test touches the real files.
func changelogRoot(t *testing.T, changelog string, fragments map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(root(t), "scripts/changelog-fold.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts/changelog-fold.sh"), script, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(changelog), 0644); err != nil {
		t.Fatal(err)
	}
	if fragments != nil {
		if err := os.Mkdir(filepath.Join(dir, "changelog.d"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range fragments {
		if err := os.WriteFile(filepath.Join(dir, "changelog.d", name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runFold runs the script of a throwaway root and returns its output and exit
// status.
func runFold(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(dir, "scripts/changelog-fold.sh")}, args...)...)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatal(err)
	return "", -1
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fragmentNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "changelog.d"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestChangelogFoldOrder(t *testing.T) {
	dir := changelogRoot(t, changelogBase, map[string]string{
		"added-b.md":      "- B added.\n  More.\n",
		"added-a.md":      "- A added.\n",
		"changed-x.md":    "- X changed.\n",
		"security-s.md":   "- S security.\n",
		"fixed-f.md":      "- F fixed.\n",
		"deprecated-d.md": "- D deprecated.\n",
		"removed-r.md":    "- R removed.\n",
		"README.md":       "# Not a fragment\n",
	})
	out, code := runFold(t, dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := `# Changelog

Intro.

## [Unreleased]

### Added

- Old added.
  Wrapped.
- A added.
- B added.
  More.

### Changed

- Old changed.
- X changed.

### Deprecated

- D deprecated.

### Removed

- R removed.

### Fixed

- F fixed.

### Security

- S security.

## [0.0.1] - 2026-01-01

### Added

- Released.
`
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != want {
		t.Errorf("CHANGELOG.md:\n%s\nwant:\n%s", got, want)
	}
	if names := fragmentNames(t, dir); len(names) != 1 || names[0] != "README.md" {
		t.Errorf("fragments left: %v", names)
	}
}

func TestChangelogFoldCreatesHeadingsInOrder(t *testing.T) {
	// A section with no heading yet goes before a later section's heading and
	// after an earlier one's, and the section at the end of the file works.
	base := "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- Old.\n"
	dir := changelogRoot(t, base, map[string]string{
		"added-a.md": "- A.\n",
		"fixed-f.md": "- F.\n",
	})
	if out, code := runFold(t, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- A.\n\n### Changed\n\n- Old.\n\n### Fixed\n\n- F.\n"
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != want {
		t.Errorf("CHANGELOG.md:\n%s\nwant:\n%s", got, want)
	}
}

func TestChangelogFoldEmptyUnreleased(t *testing.T) {
	base := "# Changelog\n\n## [Unreleased]\n\n## [0.0.1] - 2026-01-01\n\n### Added\n\n- Released.\n"
	dir := changelogRoot(t, base, map[string]string{"changed-c.md": "- C.\n"})
	if out, code := runFold(t, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := "# Changelog\n\n## [Unreleased]\n\n### Changed\n\n- C.\n\n## [0.0.1] - 2026-01-01\n\n### Added\n\n- Released.\n"
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != want {
		t.Errorf("CHANGELOG.md:\n%s\nwant:\n%s", got, want)
	}
}

func TestChangelogFoldIsIdempotent(t *testing.T) {
	dir := changelogRoot(t, changelogBase, map[string]string{
		"added-a.md": "- A added.\n",
		"fixed-f.md": "- F fixed.\n",
	})
	if out, code := runFold(t, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	once := readFile(t, filepath.Join(dir, "CHANGELOG.md"))
	out, code := runFold(t, dir)
	if code != 0 {
		t.Fatalf("second run exit %d: %s", code, out)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != once {
		t.Errorf("second run changed CHANGELOG.md:\n%s", got)
	}

	// A run that stopped after writing CHANGELOG.md but before removing the
	// fragments is repeated without duplicating the entries.
	for name, content := range map[string]string{"added-a.md": "- A added.\n", "fixed-f.md": "- F fixed.\n"} {
		if err := os.WriteFile(filepath.Join(dir, "changelog.d", name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out, code = runFold(t, dir)
	if code != 0 {
		t.Fatalf("repeat exit %d: %s", code, out)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != once {
		t.Errorf("repeat changed CHANGELOG.md:\n%s", got)
	}
	if names := fragmentNames(t, dir); len(names) != 0 {
		t.Errorf("fragments left: %v", names)
	}
}

func TestChangelogFoldRefusesBadFragments(t *testing.T) {
	for _, tc := range []struct{ name, file, content string }{
		{"unknown section", "bogus-x.md", "- X.\n"},
		{"capitalized section", "Added-x.md", "- X.\n"},
		{"no slug", "added.md", "- X.\n"},
		{"empty slug", "added-.md", "- X.\n"},
		{"bad slug", "added-X_y.md", "- X.\n"},
		{"not markdown", "added-x.txt", "- X.\n"},
		{"empty file", "added-x.md", ""},
		{"not a list item", "added-x.md", "X.\n"},
		{"blank line", "added-x.md", "- X.\n\n- Y.\n"},
		{"heading", "added-x.md", "- X.\n### Added\n"},
		{"carriage return", "added-x.md", "- X.\r\n  More.\r\n"},
		{"blank in the name", "added-a b.md", "- X.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := changelogRoot(t, changelogBase, map[string]string{
				"added-good.md": "- Good.\n",
				tc.file:         tc.content,
			})
			out, code := runFold(t, dir)
			if code != 2 {
				t.Fatalf("exit %d, want 2: %s", code, out)
			}
			if !strings.Contains(out, "changelog.d/"+tc.file) {
				t.Errorf("output does not name the file: %s", out)
			}
			if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != changelogBase {
				t.Errorf("CHANGELOG.md changed:\n%s", got)
			}
			if names := fragmentNames(t, dir); len(names) != 2 {
				t.Errorf("fragments changed: %v", names)
			}
			// The dry run refuses the same way.
			if out, code := runFold(t, dir, "--dry-run"); code != 2 {
				t.Errorf("dry run exit %d, want 2: %s", code, out)
			}
		})
	}
}

func TestChangelogFoldRefusesMissingUnreleased(t *testing.T) {
	base := "# Changelog\n\n## [0.0.1] - 2026-01-01\n"
	dir := changelogRoot(t, base, map[string]string{"added-a.md": "- A.\n"})
	out, code := runFold(t, dir)
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != base {
		t.Errorf("CHANGELOG.md changed:\n%s", got)
	}
	if names := fragmentNames(t, dir); len(names) != 1 {
		t.Errorf("fragments changed: %v", names)
	}
}

func TestChangelogFoldDryRun(t *testing.T) {
	dir := changelogRoot(t, changelogBase, map[string]string{
		"added-a.md": "- A added.\n",
		"fixed-f.md": "- F fixed.\n",
	})
	out, code := runFold(t, dir, "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := "would fold changelog.d/added-a.md into Added\nwould fold changelog.d/fixed-f.md into Fixed\n"
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != changelogBase {
		t.Errorf("dry run changed CHANGELOG.md:\n%s", got)
	}
	if names := fragmentNames(t, dir); len(names) != 2 {
		t.Errorf("dry run removed fragments: %v", names)
	}
	// A dry run writes no file at all, not even a temporary one.
	if got := entriesOf(t, dir); len(got) != 3 {
		t.Errorf("the dry run left files in the root: %v", got)
	}
}

func TestChangelogCheck(t *testing.T) {
	dir := changelogRoot(t, changelogBase, map[string]string{"added-a.md": "- A.\n"})
	out, code := runFold(t, dir, "--check")
	if code != 1 {
		t.Fatalf("with a fragment: exit %d, want 1: %s", code, out)
	}
	if !strings.Contains(out, "changelog.d/added-a.md") {
		t.Errorf("output does not name the fragment: %s", out)
	}
	// A file that is not a valid fragment is also left over.
	if err := os.WriteFile(filepath.Join(dir, "changelog.d/notes.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, code := runFold(t, dir, "--check"); code != 1 || !strings.Contains(out, "changelog.d/notes.txt") {
		t.Errorf("with a stray file: exit %d: %s", code, out)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != changelogBase {
		t.Errorf("check changed CHANGELOG.md:\n%s", got)
	}
	for _, name := range []string{"added-a.md", "notes.txt"} {
		if err := os.Remove(filepath.Join(dir, "changelog.d", name)); err != nil {
			t.Fatal(err)
		}
	}
	// Only the README is left: nothing unfolded.
	if err := os.WriteFile(filepath.Join(dir, "changelog.d/README.md"), []byte("# Guide\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, code := runFold(t, dir, "--check"); code != 0 {
		t.Errorf("with only the README: exit %d: %s", code, out)
	}
	// So is a missing directory.
	if err := os.RemoveAll(filepath.Join(dir, "changelog.d")); err != nil {
		t.Fatal(err)
	}
	if out, code := runFold(t, dir, "--check"); code != 0 {
		t.Errorf("without the directory: exit %d: %s", code, out)
	}
}

func TestChangelogCheckAfterFold(t *testing.T) {
	dir := changelogRoot(t, changelogBase, map[string]string{"added-a.md": "- A.\n"})
	if out, code := runFold(t, dir); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if out, code := runFold(t, dir, "--check"); code != 0 {
		t.Errorf("check after fold: exit %d: %s", code, out)
	}
}

func TestChangelogUsage(t *testing.T) {
	dir := changelogRoot(t, changelogBase, nil)
	if out, code := runFold(t, dir, "--bogus"); code != 2 {
		t.Errorf("unknown flag: exit %d: %s", code, out)
	}
	if out, code := runFold(t, dir, "--root"); code != 2 {
		t.Errorf("--root without a directory: exit %d: %s", code, out)
	}
	// --root selects the repository, wherever the script is.
	other := changelogRoot(t, changelogBase, map[string]string{"added-a.md": "- A.\n"})
	if out, code := runFold(t, dir, "--root", other, "--check"); code != 1 {
		t.Errorf("--root: exit %d: %s", code, out)
	}
}

// The repository's own fragments must be valid, and the real CHANGELOG.md is
// only read: the dry run changes nothing.
func TestRepositoryChangelogFragments(t *testing.T) {
	changelog := filepath.Join(root(t), "CHANGELOG.md")
	before := readFile(t, changelog)
	out, code := runFold(t, root(t), "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if got := readFile(t, changelog); got != before {
		t.Error("the dry run changed CHANGELOG.md")
	}
}

func TestMakeChangelogTargets(t *testing.T) {
	for target, want := range map[string]string{
		"changelog-check": "sh scripts/changelog-fold.sh --check",
		"changelog-fold":  "sh scripts/changelog-fold.sh",
	} {
		cmd := exec.Command("make", "-n", target)
		cmd.Dir = root(t)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n %s: %v: %s", target, err, out)
		}
		if !strings.Contains(string(out), want) {
			t.Errorf("make -n %s: %q not in %s", target, want, out)
		}
	}
}

// A fragment that only starts like an existing entry is a new entry; one
// whose every line is already there is folded away without being added again,
// including when it is the last lines of the file's entries.
func TestChangelogFoldComparesTheWholeFragment(t *testing.T) {
	base := strings.Replace(changelogBase, "- Old changed.\n", "- Fix a crash.\n  In the console.\n- Last one.\n", 1)
	dir := changelogRoot(t, base, map[string]string{
		"changed-same-start.md": "- Fix a crash.\n  In the session terminal.\n",
		"changed-prefix.md":     "- Fix a crash.\n",
		"changed-whole.md":      "- Fix a crash.\n  In the console.\n",
		"changed-last.md":       "- Last one.\n",
	})
	out, code := runFold(t, dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	// The two new entries are added by name: changed-prefix, changed-same-start.
	want := strings.Replace(base, "- Last one.\n", "- Last one.\n- Fix a crash.\n- Fix a crash.\n  In the session terminal.\n", 1)
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != want {
		t.Errorf("CHANGELOG.md:\n%s\nwant:\n%s", got, want)
	}
	for _, skipped := range []string{"changed-whole.md", "changed-last.md"} {
		if !strings.Contains(out, "skip changelog.d/"+skipped) {
			t.Errorf("%s not reported as skipped: %s", skipped, out)
		}
	}
	if names := fragmentNames(t, dir); len(names) != 0 {
		t.Errorf("fragments left: %v", names)
	}
}

// An interrupted fold leaves CHANGELOG.md and the fragments as they were, and
// no temporary file. The script's test hook holds it while it renders.
func TestChangelogFoldInterrupted(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := changelogRoot(t, changelogBase, map[string]string{"added-a.md": "- A added.\n"})
			hold := filepath.Join(t.TempDir(), "hold")
			if err := os.WriteFile(hold, nil, 0644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", filepath.Join(dir, "scripts/changelog-fold.sh"))
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(), "CHANGELOG_FOLD_TEST_HOLD="+hold)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			// Wait until the fold has made its temporary file: it is rendering.
			deadline := time.Now().Add(20 * time.Second)
			for len(entriesOf(t, dir)) < 4 {
				if time.Now().After(deadline) {
					cmd.Process.Kill()
					t.Fatal("the fold did not reach the render")
				}
				time.Sleep(20 * time.Millisecond)
			}
			cmd.Process.Signal(sig)
			// Release the hold: a handler that does not exit would now go on and
			// replace CHANGELOG.md.
			time.Sleep(100 * time.Millisecond)
			os.Remove(hold)
			var err error
			select {
			case err = <-done:
			case <-time.After(20 * time.Second):
				cmd.Process.Kill()
				t.Fatal("the fold did not stop")
			}
			if err == nil {
				t.Error("the interrupted fold succeeded")
			}
			if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != changelogBase {
				t.Errorf("CHANGELOG.md changed:\n%s", got)
			}
			if names := fragmentNames(t, dir); len(names) != 1 {
				t.Errorf("fragments changed: %v", names)
			}
			if got := entriesOf(t, dir); len(got) != 3 {
				t.Errorf("files left in the root: %v", got)
			}
		})
	}
}

func entriesOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
