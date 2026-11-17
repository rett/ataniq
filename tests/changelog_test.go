package tests

import (
	"context"
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

// What "qargi release start 0.0.2" leaves behind: the changelog dated, with a
// fresh empty [Unreleased] above the release's section and one older release
// below it.
const changelogDated = `# Changelog

Intro.

## [Unreleased]

## [0.0.2] - 2026-02-02

### Added

- Dated added.

### Fixed

- Dated fixed.

## [0.0.1] - 2026-01-01

### Added

- Released.
`

func TestChangelogFoldIntoDatedSection(t *testing.T) {
	// The entries go to the end of their headings in the release's section,
	// a missing heading is created in order (Changed between Added and
	// Fixed, Security after Fixed), and [Unreleased] and the older release are
	// left alone.
	dir := changelogRoot(t, changelogDated, map[string]string{
		"added-a.md":    "- A added.\n",
		"changed-c.md":  "- C changed.\n",
		"fixed-f.md":    "- F fixed.\n",
		"security-s.md": "- S security.\n",
	})
	out, code := runFold(t, dir, "--into", "0.0.2")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := `# Changelog

Intro.

## [Unreleased]

## [0.0.2] - 2026-02-02

### Added

- Dated added.
- A added.

### Changed

- C changed.

### Fixed

- Dated fixed.
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
	if names := fragmentNames(t, dir); len(names) != 0 {
		t.Errorf("fragments left: %v", names)
	}
	if out, code := runFold(t, dir, "--check"); code != 0 {
		t.Errorf("check after the fold: exit %d: %s", code, out)
	}
}

func TestChangelogFoldIntoCreatesHeadingsInOrder(t *testing.T) {
	// The dated section has no headings yet, or only a later one, and is the
	// last one in the file, followed by link reference definitions.
	base := "# Changelog\n\n## [Unreleased]\n\n## [0.0.2] - 2026-02-02\n\n### Fixed\n\n- Old.\n\n[Unreleased]: https://example.invalid/compare/0.0.2...HEAD\n[0.0.2]: https://example.invalid/releases/0.0.2\n"
	dir := changelogRoot(t, base, map[string]string{
		"added-a.md":    "- A.\n",
		"removed-r.md":  "- R.\n",
		"security-s.md": "- S.\n",
	})
	if out, code := runFold(t, dir, "--into", "0.0.2"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := "# Changelog\n\n## [Unreleased]\n\n## [0.0.2] - 2026-02-02\n\n### Added\n\n- A.\n\n### Removed\n\n- R.\n\n### Fixed\n\n- Old.\n\n### Security\n\n- S.\n\n[Unreleased]: https://example.invalid/compare/0.0.2...HEAD\n[0.0.2]: https://example.invalid/releases/0.0.2\n"
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != want {
		t.Errorf("CHANGELOG.md:\n%s\nwant:\n%s", got, want)
	}

	empty := "# Changelog\n\n## [Unreleased]\n\n## [0.0.2] - 2026-02-02\n"
	dir = changelogRoot(t, empty, map[string]string{"changed-c.md": "- C.\n"})
	if out, code := runFold(t, dir, "--into", "0.0.2"); code != 0 {
		t.Fatalf("empty section: exit %d: %s", code, out)
	}
	want = "# Changelog\n\n## [Unreleased]\n\n## [0.0.2] - 2026-02-02\n\n### Changed\n\n- C.\n"
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != want {
		t.Errorf("CHANGELOG.md:\n%s\nwant:\n%s", got, want)
	}
}

func TestChangelogFoldIntoRefusesMissingSection(t *testing.T) {
	cases := map[string]struct {
		changelog string
		version   string
	}{
		"absent version":    {changelogDated, "0.0.3"},
		"prefix only":       {changelogDated, "0.0"},
		"undated heading":   {"# Changelog\n\n## [Unreleased]\n\n## [0.0.2]\n\n### Added\n\n- X.\n", "0.0.2"},
		"unreleased":        {changelogDated, "Unreleased"},
		"heading twice":     {changelogDated + "\n## [0.0.2] - 2026-02-03\n", "0.0.2"},
		"bad date":          {"# Changelog\n\n## [Unreleased]\n\n## [0.0.2] - soon\n", "0.0.2"},
		"version in a body": {"# Changelog\n\n## [Unreleased]\n\n- See ## [0.0.2] - 2026-02-02 here.\n", "0.0.2"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := changelogRoot(t, c.changelog, map[string]string{"added-a.md": "- A.\n"})
			out, code := runFold(t, dir, "--into", c.version)
			if code != 2 {
				t.Fatalf("exit %d, want 2: %s", code, out)
			}
			if !strings.Contains(out, "nothing was changed") {
				t.Errorf("no refusal in the output: %s", out)
			}
			if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != c.changelog {
				t.Errorf("CHANGELOG.md changed:\n%s", got)
			}
			if names := fragmentNames(t, dir); len(names) != 1 {
				t.Errorf("fragments changed: %v", names)
			}
			if got := entriesOf(t, dir); len(got) != 3 {
				t.Errorf("files left in the root: %v", got)
			}
			// A dry run refuses the same way.
			if out, code := runFold(t, dir, "--into", c.version, "--dry-run"); code != 2 {
				t.Errorf("dry run: exit %d, want 2: %s", code, out)
			}
		})
	}
}

func TestChangelogFoldIntoRefusesBadArguments(t *testing.T) {
	dir := changelogRoot(t, changelogDated, map[string]string{"added-a.md": "- A.\n"})
	for _, args := range [][]string{
		{"--into"},
		{"--into", ""},
		{"--into", "0.0.*"},
		{"--into", "[0.0.2]"},
		{"--into", "0.0.2", "--check"},
	} {
		if out, code := runFold(t, dir, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2: %s", args, code, out)
		}
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != changelogDated {
		t.Errorf("CHANGELOG.md changed:\n%s", got)
	}
	if names := fragmentNames(t, dir); len(names) != 1 {
		t.Errorf("fragments changed: %v", names)
	}
}

func TestChangelogFoldIntoRefusesInvalidFragment(t *testing.T) {
	dir := changelogRoot(t, changelogDated, map[string]string{
		"added-a.md":   "- A.\n",
		"unknown-b.md": "- B.\n",
	})
	if out, code := runFold(t, dir, "--into", "0.0.2"); code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, out)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != changelogDated {
		t.Errorf("CHANGELOG.md changed:\n%s", got)
	}
	if names := fragmentNames(t, dir); len(names) != 2 {
		t.Errorf("fragments changed: %v", names)
	}
}

func TestChangelogFoldIntoIsIdempotent(t *testing.T) {
	frags := map[string]string{"added-a.md": "- A added.\n", "fixed-f.md": "- F fixed.\n"}
	dir := changelogRoot(t, changelogDated, frags)
	if out, code := runFold(t, dir, "--into", "0.0.2"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	once := readFile(t, filepath.Join(dir, "CHANGELOG.md"))
	if out, code := runFold(t, dir, "--into", "0.0.2"); code != 0 {
		t.Fatalf("second run exit %d: %s", code, out)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != once {
		t.Errorf("second run changed CHANGELOG.md:\n%s", got)
	}

	// A run that stopped before removing the fragments is repeated without
	// duplicating the entries, and says it skipped them.
	for name, content := range frags {
		if err := os.WriteFile(filepath.Join(dir, "changelog.d", name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out, code := runFold(t, dir, "--into", "0.0.2")
	if code != 0 {
		t.Fatalf("repeat exit %d: %s", code, out)
	}
	if !strings.Contains(out, "skip changelog.d/added-a.md: already in [0.0.2]") {
		t.Errorf("output: %s", out)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != once {
		t.Errorf("repeat changed CHANGELOG.md:\n%s", got)
	}
	if names := fragmentNames(t, dir); len(names) != 0 {
		t.Errorf("fragments left: %v", names)
	}

	// An entry that is only in [Unreleased] is not "already folded" into the
	// release.
	base := strings.Replace(changelogDated, "## [Unreleased]\n", "## [Unreleased]\n\n### Added\n\n- A added.\n", 1)
	dir = changelogRoot(t, base, map[string]string{"added-a.md": "- A added.\n"})
	if out, code := runFold(t, dir, "--into", "0.0.2"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	got := readFile(t, filepath.Join(dir, "CHANGELOG.md"))
	if want := strings.Replace(base, "- Dated added.\n", "- Dated added.\n- A added.\n", 1); got != want {
		t.Errorf("CHANGELOG.md:\n%s\nwant:\n%s", got, want)
	}
}

func TestChangelogFoldIntoDryRun(t *testing.T) {
	dir := changelogRoot(t, changelogDated, map[string]string{
		"added-a.md": "- A added.\n",
		"fixed-f.md": "- F fixed.\n",
	})
	out, code := runFold(t, dir, "--into", "0.0.2", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := "would fold changelog.d/added-a.md into Added\nwould fold changelog.d/fixed-f.md into Fixed\n"
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != changelogDated {
		t.Errorf("dry run changed CHANGELOG.md:\n%s", got)
	}
	if names := fragmentNames(t, dir); len(names) != 2 {
		t.Errorf("dry run removed fragments: %v", names)
	}
	if got := entriesOf(t, dir); len(got) != 3 {
		t.Errorf("the dry run left files in the root: %v", got)
	}
}

// The Makefile target passes ARGS through, so the release flow's command is
// the one that is tested.
func TestMakeChangelogFoldPassesInto(t *testing.T) {
	cmd := exec.Command("make", "-n", "changelog-fold", "ARGS=--into 0.0.2")
	cmd.Dir = root(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "sh scripts/changelog-fold.sh --into 0.0.2") {
		t.Errorf("--into not passed on: %s", out)
	}
}

// A name that exists in any form at the fold's temporary file name is refused
// (exit 1) and left in place, and CHANGELOG.md and the fragments are not
// changed. Noclobber alone accepts a FIFO, a device and a link to one, and the
// exit handler then removed the object; a FIFO also made the create wait. The
// shell makes the object under the name of its own process id and then
// replaces itself with the script, which keeps that id.
func TestChangelogFoldRefusesExistingTemporaryName(t *testing.T) {
	isLink := func(i os.FileInfo) bool { return i.Mode()&os.ModeSymlink != 0 }
	for _, tc := range []struct {
		name string
		make string
		kind func(os.FileInfo) bool
	}{
		{"symlink to /dev/null", `ln -s /dev/null "$1/CHANGELOG.md.fold.$$"`, isLink},
		{"dangling symlink", `ln -s /nonexistent/target "$1/CHANGELOG.md.fold.$$"`, isLink},
		{"FIFO", `mkfifo "$1/CHANGELOG.md.fold.$$"`, func(i os.FileInfo) bool { return i.Mode()&os.ModeNamedPipe != 0 }},
		{"regular file", `echo foreign >"$1/CHANGELOG.md.fold.$$"`, func(i os.FileInfo) bool { return i.Mode().IsRegular() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := changelogRoot(t, changelogBase, map[string]string{"added-a.md": "- A added.\n"})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			script := tc.make + `; exec /bin/sh "$1/scripts/changelog-fold.sh" --root "$1"`
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script, "sh", dir)
			cmd.Dir = t.TempDir()
			cmd.WaitDelay = time.Second
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("the fold did not stop: %s", out)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("exit = %v, want 1: %s", err, out)
			}
			if !strings.Contains(string(out), "cannot create a temporary file") {
				t.Errorf("output: %s", out)
			}
			if got := readFile(t, filepath.Join(dir, "CHANGELOG.md")); got != changelogBase {
				t.Errorf("CHANGELOG.md changed:\n%s", got)
			}
			if names := fragmentNames(t, dir); len(names) != 1 {
				t.Errorf("fragments changed: %v", names)
			}
			found := 0
			for _, name := range entriesOf(t, dir) {
				if !strings.HasPrefix(name, "CHANGELOG.md.fold.") {
					continue
				}
				found++
				info, err := os.Lstat(filepath.Join(dir, name))
				if err != nil || !tc.kind(info) {
					t.Errorf("%s is not the object that was put there: %v %v", name, info, err)
				}
			}
			if found != 1 {
				t.Errorf("%d objects at the temporary name, want 1", found)
			}
		})
	}
}
