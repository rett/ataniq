package leadmirror

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

// The fixtures are the harness's own. They are made with git in folders that
// no sandbox is given (<root>/fixture), with an explicit environment, before
// any hostile file is put in place. They are not lead data: the harness wrote
// every byte. The finished lead clone is then moved to leads/<role>/tree, and
// git never runs there again.

const fixtureTimeout = 2 * time.Minute

// fixtures runs git for the fixtures.
type fixtures struct{ git gitenv.Git }

// env is the whole environment of a fixture command. It reads no user or
// system configuration.
func (f fixtures) env() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=/dev/null",
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

// run runs git in dir, which must be a fixture folder, and returns its
// standard output without the final newline.
func (f fixtures) run(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := boundedexec.Run(ctx, boundedexec.Cmd{
		Argv:      append([]string{f.git.Path}, args...),
		Env:       f.env(),
		Dir:       dir,
		Timeout:   fixtureTimeout,
		MaxStdout: 1 << 20,
		MaxStderr: 1 << 16,
	})
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(res.Stderr)))
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		return "", fmt.Errorf("fixture git %s: %w: %s", args[0], err, msg)
	}
	return strings.TrimSuffix(string(res.Stdout), "\n"), nil
}

// file is a path and its content, for a fixture commit.
type file struct{ path, content string }

// put writes the files below dir.
func put(dir string, files ...file) error {
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f.content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// commit writes the files, adds them (also when ignored) and commits.
func (f fixtures) commit(ctx context.Context, dir, msg string, files ...file) error {
	if err := put(dir, files...); err != nil {
		return err
	}
	add := []string{"add", "--force", "--"}
	for _, fl := range files {
		add = append(add, fl.path)
	}
	if _, err := f.run(ctx, dir, add...); err != nil {
		return err
	}
	_, err := f.run(ctx, dir, "commit", "--quiet", "--no-gpg-sign", "-m", msg)
	return err
}

// upstreamFiles are the files of the first upstream commit. The review case
// changes the Makefile, the scripts and go.mod, so each exists upstream.
var upstreamFiles = []file{
	{"README.md", "upstream readme\n"},
	{"Makefile", "all:\n\t@echo build\n"},
	{"scripts/check.sh", "#!/bin/sh\necho check\n"},
	{"go.mod", "module example.invalid/x\n\ngo 1.21\n"},
	{"docs/guide.md", "guide\n"},
}

// upstreamBranch and upstreamTag are the upstream branch and tag that the
// mirror is seeded with.
const (
	upstreamBranch = "main"
	upstreamTag    = "v1"
)

// buildUpstream makes the upstream repository in dir: one commit on
// upstreamBranch and an annotated tag upstreamTag on it.
func (f fixtures) buildUpstream(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := f.run(ctx, dir, "init", "--quiet", "--template=", "--initial-branch="+upstreamBranch); err != nil {
		return err
	}
	if err := f.commit(ctx, dir, "upstream: first commit", upstreamFiles...); err != nil {
		return err
	}
	_, err := f.run(ctx, dir, "tag", "--annotate", "--no-sign", "-m", "upstream tag", upstreamTag)
	return err
}

// buildLead makes a lead's clone in dir from the upstream repository: the
// upstream branch and tag, checked out. The caller adds the lead's own work.
func (f fixtures) buildLead(ctx context.Context, upstream, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := f.run(ctx, dir, "init", "--quiet", "--template=", "--initial-branch=lead-scratch"); err != nil {
		return err
	}
	if _, err := f.run(ctx, dir, "fetch", "--quiet", "--no-tags", upstream,
		"+refs/heads/"+upstreamBranch+":refs/heads/"+upstreamBranch,
		"+refs/tags/"+upstreamTag+":refs/tags/"+upstreamTag); err != nil {
		return err
	}
	_, err := f.run(ctx, dir, "checkout", "--quiet", upstreamBranch)
	return err
}

// randomText returns n bytes of text that does not compress, the same for
// the same seed.
func randomText(seed int64, n int) string {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = "abcdefghijklmnopqrstuvwxyz0123456789"[r.Intn(36)]
	}
	return string(b)
}
