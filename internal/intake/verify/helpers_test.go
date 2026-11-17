package verify

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testRef = "refs/leads/lead/heads/main"

// The tests run these programs by these fixed paths, so that they do not
// depend on PATH.
const (
	gitTool = "/usr/bin/git"
	envTool = "/usr/bin/env"
)

// checkTool reports why the program at path cannot be run.
func checkTool(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("these tests need %s and it is missing: %w", path, err)
	}
	if st.IsDir() || st.Mode()&0o111 == 0 {
		return fmt.Errorf("these tests need %s and it is not an executable file", path)
	}
	return nil
}

// requireTool stops the test with a message that names path when the
// program is not there. It does not skip: a skipped test would hide that the
// checks did not run.
func requireTool(t *testing.T, path string) {
	t.Helper()
	if err := checkTool(path); err != nil {
		t.Fatal(err)
	}
}

// gitEnv is the environment of every git command the tests run, and of the
// fake sandbox: it does not depend on the developer's git configuration.
func gitEnv() []string {
	return []string{
		"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	requireTool(t, gitTool)
	cmd := exec.Command(gitTool, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// mirror holds a bare repository with one lead ref.
type mirror struct {
	Path string // the bare repository
	Work string // the working clone the commits were made in
	Tip  string // the commit testRef points to
}

// newMirror writes a temporary bare mirror whose testRef points to a commit
// with the files hello.txt and run.sh.
func newMirror(t *testing.T) mirror {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "work")
	bare := filepath.Join(base, "mirror.git")
	git(t, base, "init", "--quiet", "--initial-branch=main", work)
	git(t, base, "init", "--quiet", "--bare", bare)
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "run.sh"), []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "--quiet", "-m", "one")
	tip := git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "--quiet", bare, "HEAD:"+testRef)
	return mirror{Path: bare, Work: work, Tip: tip}
}

// advance adds a commit to the mirror's lead ref and returns its id.
func (m mirror) advance(t *testing.T) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(m.Work, "more.txt"), []byte("more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, m.Work, "add", ".")
	git(t, m.Work, "commit", "--quiet", "-m", "two")
	git(t, m.Work, "push", "--quiet", m.Path, "HEAD:"+testRef)
	return git(t, m.Work, "rev-parse", "HEAD")
}

func TestCheckToolNamesTheMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-tool")
	err := checkTool(missing)
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("a missing tool: %v", err)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkTool(plain); err == nil || !strings.Contains(err.Error(), plain) {
		t.Fatalf("a file that is not executable: %v", err)
	}
	if err := checkTool(t.TempDir()); err == nil {
		t.Fatal("a folder passed as a tool")
	}
	for _, p := range []string{gitTool, envTool} {
		if err := checkTool(p); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNoTestRunsInParallel keeps the package's tests in sequence: one of them
// lowers the descriptor limit of the whole process.
func TestNoTestRunsInParallel(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("test files: %v %v", files, err)
	}
	needle := "t.Par" + "allel("
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), needle) {
			t.Errorf("%s calls %s", f, needle)
		}
	}
}
