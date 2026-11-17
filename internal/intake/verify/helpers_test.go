package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testRef = "refs/leads/lead/heads/main"

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
	cmd := exec.Command("/usr/bin/git", args...)
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
