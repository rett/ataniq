package mirror

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

// These tests run the installed git on temporary repositories. Commands under
// test get their argument vector and environment from gitenv. Fixtures (a
// source repository with commits) are made with a separate, explicit
// environment, so the developer's own git configuration plays no part.

// needGit returns the installed git, or fails the test with a clear message.
func needGit(t *testing.T) gitenv.Git {
	t.Helper()
	g, err := gitenv.Find()
	if err != nil {
		t.Fatalf("these tests need an installed git: %v", err)
	}
	return g
}

// fixtureEnv is the environment of the commands that build fixtures.
func fixtureEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}
}

// fixtureGit runs the installed git with the fixture environment in dir.
func fixtureGit(t *testing.T, g gitenv.Git, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(g.Path, args...)
	cmd.Dir = dir
	cmd.Env = fixtureEnv(t)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// mirrorGit runs a git subcommand on the repository at gitDir through gitenv
// and boundedexec, and returns its standard output.
func mirrorGit(t *testing.T, g gitenv.Git, gitDir string, protocols []string, args ...string) string {
	t.Helper()
	c := gitenv.Cmd{GitDir: gitDir, Args: args, Protocols: protocols}
	argv, err := g.Argv(c)
	if err != nil {
		t.Fatalf("gitenv argv %v: %v", args, err)
	}
	res, err := boundedexec.Run(context.Background(), boundedexec.Cmd{
		Argv: argv, Env: g.Env(c), Timeout: 30 * time.Second,
		MaxStdout: 1 << 20, MaxStderr: 1 << 16,
	})
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, res.Stderr)
	}
	return strings.TrimSpace(string(res.Stdout))
}

// source is a plain repository with a few objects, used to fill a mirror.
type source struct {
	dir    string
	commit string // a commit
	tree   string // its tree
	blob   string // a blob in the tree
	tag    string // an annotated tag object that names the commit
}

// newSource builds a repository with one commit, one file and one annotated
// tag, and returns the ids.
func newSource(t *testing.T, g gitenv.Git) source {
	t.Helper()
	dir := t.TempDir()
	fixtureGit(t, g, dir, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, g, dir, "add", "file.txt")
	fixtureGit(t, g, dir, "commit", "--quiet", "--no-gpg-sign", "-m", "one")
	fixtureGit(t, g, dir, "tag", "--no-sign", "-a", "-m", "annotated", "v1")
	return source{
		dir:    dir,
		commit: fixtureGit(t, g, dir, "rev-parse", "HEAD"),
		tree:   fixtureGit(t, g, dir, "rev-parse", "HEAD^{tree}"),
		blob:   fixtureGit(t, g, dir, "rev-parse", "HEAD:file.txt"),
		tag:    fixtureGit(t, g, dir, "rev-parse", "refs/tags/v1"),
	}
}
