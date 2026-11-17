package intake

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/intake/execconfig"
)

// These tests run the installed git on temporary repositories. The code
// under test only ever gets the bare repository and runs through gitenv.
// Fixtures are built here, with plumbing, in a scratch work repository that
// has an index and no checked-out files, and then pushed into the bare one.
// The environment of the fixture commands is explicit, so the developer's own
// git configuration plays no part.

const (
	upstreamRef = "refs/heads/main"
	leadRef     = "refs/leads/role/heads/work"
)

func needGit(t *testing.T) gitenv.Git {
	t.Helper()
	g, err := gitenv.Find()
	if err != nil {
		t.Fatalf("these tests need an installed git: %v", err)
	}
	return g
}

// lab is a scratch work repository with a bare repository beside it.
type lab struct {
	t     *testing.T
	git   gitenv.Git
	work  string
	bare  string
	cur   string // the commit the index was last read from or written as
	clock int64
}

func newLab(t *testing.T) *lab {
	t.Helper()
	l := &lab{t: t, git: needGit(t), work: t.TempDir(), bare: t.TempDir(), clock: 1_700_000_000}
	l.run(nil, l.work, "init", "--quiet")
	l.run(nil, l.bare, "init", "--quiet", "--bare")
	return l
}

func (l *lab) env() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=/dev/null",
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Lead Author",
		"GIT_AUTHOR_EMAIL=author@example.invalid",
		"GIT_COMMITTER_NAME=Lead Committer",
		"GIT_COMMITTER_EMAIL=committer@example.invalid",
		fmt.Sprintf("GIT_AUTHOR_DATE=%d +0000", l.clock),
		fmt.Sprintf("GIT_COMMITTER_DATE=%d +0000", l.clock+1),
	}
}

// run runs the installed git in dir with the fixture environment.
func (l *lab) run(stdin []byte, dir string, args ...string) string {
	l.t.Helper()
	cmd := exec.Command(l.git.Path, args...)
	cmd.Dir = dir
	cmd.Env = l.env()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		l.t.Fatalf("fixture git %q: %v: %s", args, err, errb.String())
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func (l *lab) w(args ...string) string { l.t.Helper(); return l.run(nil, l.work, args...) }
func (l *lab) b(args ...string) string {
	l.t.Helper()
	return l.run(nil, l.work, append([]string{"--git-dir=" + l.bare}, args...)...)
}

// blob stores content in the work repository and returns its id.
func (l *lab) blob(content string) string {
	l.t.Helper()
	return l.run([]byte(content), l.work, "hash-object", "-w", "--stdin")
}

// putMode sets a path in the index to a blob with the given mode.
func (l *lab) putMode(path, mode, content string) {
	l.t.Helper()
	l.w("update-index", "--add", "--cacheinfo", mode, l.blob(content), path)
}

// put sets a regular file.
func (l *lab) put(path, content string) { l.t.Helper(); l.putMode(path, "100644", content) }

// symlink sets a symlink entry with the given target text.
func (l *lab) symlink(path, target string) { l.t.Helper(); l.putMode(path, "120000", target) }

// gitlink sets a submodule entry.
func (l *lab) gitlink(path string) {
	l.t.Helper()
	l.w("update-index", "--add", "--cacheinfo", "160000", strings.Repeat("ab", 20), path)
}

// remove deletes a path from the index.
func (l *lab) remove(path string) {
	l.t.Helper()
	l.w("update-index", "--force-remove", "--", path)
}

// commit writes the index as a commit with the message as it is, on top of
// the current commit or of the given parents, and makes it the current one.
func (l *lab) commit(msg string, parents ...string) string {
	l.t.Helper()
	if parents == nil && l.cur != "" {
		parents = []string{l.cur}
	}
	l.clock += 60
	file := filepath.Join(l.t.TempDir(), "msg")
	if err := os.WriteFile(file, []byte(msg), 0o600); err != nil {
		l.t.Fatal(err)
	}
	args := []string{"commit-tree", l.w("write-tree")}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-F", file)
	l.cur = l.w(args...)
	return l.cur
}

// checkout reads a commit into the index and makes it the current one.
func (l *lab) checkout(c string) {
	l.t.Helper()
	l.w("read-tree", c)
	l.cur = c
}

// publish makes ref in the bare repository point at the commit.
func (l *lab) publish(ref, commit string) {
	l.t.Helper()
	l.w("push", "--quiet", "--force", l.bare, commit+":"+ref)
}

// scene is a bare repository with an upstream branch at base and a lead
// branch at tip.
type scene struct {
	*lab
	base, tip string
}

// newScene makes the root commit (README.md and src/main.go, and the files
// given as pairs of path and content) and publishes it as the upstream
// branch. The caller adds commits with commit and ends with finish.
func newScene(t *testing.T, files ...string) *scene {
	t.Helper()
	l := newLab(t)
	l.put("README.md", "readme\n")
	l.put("src/main.go", "package main\n")
	for i := 0; i+1 < len(files); i += 2 {
		l.put(files[i], files[i+1])
	}
	s := &scene{lab: l, base: l.commit("root\n")}
	l.publish(upstreamRef, s.base)
	return s
}

// finish publishes the current commit as the lead branch.
func (s *scene) finish() {
	s.t.Helper()
	s.tip = s.cur
	s.publish(leadRef, s.tip)
}

func (s *scene) source() Source { return Source{Git: s.git, GitDir: s.bare} }

func (s *scene) request() Request {
	return Request{BaseRef: upstreamRef, TipRef: leadRef, Rules: execconfig.Default()}
}

func (s *scene) inspect() *Review {
	s.t.Helper()
	r, err := Inspect(context.Background(), s.source(), s.request())
	if err != nil {
		s.t.Fatalf("Inspect: %v", err)
	}
	return r
}

// byPath returns the change of a path, or fails the test.
func byPath(t *testing.T, r *Review, path string) Change {
	t.Helper()
	for _, c := range r.Changes {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("no change for %q in %+v", path, r.Changes)
	return Change{}
}

func hasFlag(flags []Flag, kind FlagKind) bool {
	for _, f := range flags {
		if f.Kind == kind {
			return true
		}
	}
	return false
}
