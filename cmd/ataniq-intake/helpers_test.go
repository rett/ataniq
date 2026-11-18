package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/sandboxplan"
	"ataniq/internal/exchange/sandboxplan/plantest"
	"ataniq/internal/intake"
	"ataniq/internal/intake/verify"
)

// These tests build temporary state roots and repositories. The sandbox is
// the fake planner of plantest: it runs plain programs and no namespace.
// Fixtures run git with an explicit environment, so the developer's own git
// configuration plays no part.

const (
	testProject = "proj"
	testRole    = "lead"
)

// needGit returns the installed git, or fails the test.
func needGit(t *testing.T) gitenv.Git {
	t.Helper()
	g, err := gitenv.Find()
	if err != nil {
		t.Fatalf("these tests need an installed git: %v", err)
	}
	return g
}

// gitEnv is the environment of fixtures and of the fake sandbox.
func gitEnv(extra ...string) []string {
	return append([]string{
		"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	}, extra...)
}

// fixtureGit runs git in dir with the fixture environment.
func fixtureGit(t *testing.T, g gitenv.Git, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(g.Path, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("fixture git %v: %v: %s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// failOnRead is a standard input that fails the test when it is read: the
// command is not interactive.
type failOnRead struct{ t *testing.T }

func (f failOnRead) Read([]byte) (int, error) {
	f.t.Error("the command read its standard input")
	return 0, fmt.Errorf("stdin must not be read")
}

// fakeRunner is a verify.Runner that returns a result chosen by the test.
type fakeRunner struct {
	mu    sync.Mutex
	res   boundedexec.Result
	err   error
	calls int
}

func (f *fakeRunner) Run(ctx context.Context, c boundedexec.Cmd) (boundedexec.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.res, f.err
}

// world is one test's state root, mirror, folders and fakes.
type world struct {
	t       *testing.T
	git     gitenv.Git
	state   string
	records string
	work    string
	other   string // a folder for the owner's other files, outside the state
	planner *plantest.Fake
	runner  verify.Runner
	loader  func(string) (sandboxplan.Planner, error)
	now     time.Time
	loads   []string
	// integrate and putVer replace the integration and the storing of a
	// verification record, when set.
	integrate func(context.Context, intake.Source, intake.IntegrateRequest) (intake.IntakeRecord, error)
	putVer    func(*intake.Store, string, verify.Result, []verify.Gate) (intake.VerificationRecord, error)
}

func newWorld(t *testing.T) *world {
	t.Helper()
	g := needGit(t)
	up, err := plantest.GitUploadPack()
	if err != nil {
		t.Fatalf("these tests need git-upload-pack: %v", err)
	}
	root := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	w := &world{
		t: t, git: g,
		state: mk("state"), records: mk("records"), work: mk("work"), other: mk("other"),
		planner: &plantest.Fake{
			UploadPack:   up,
			MeasureBytes: 1000,
			VerifyPrefix: []string{"/usr/bin/env"},
			VerifyEnv:    gitEnv(),
		},
		runner: verify.ExecRunner{},
		now:    time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
	w.loader = func(path string) (sandboxplan.Planner, error) {
		w.loads = append(w.loads, path)
		return w.planner, nil
	}
	return w
}

// run calls run() with the world's dependencies. The standard input fails the
// test when it is read.
func (w *world) run(args ...string) (code int, stdout, stderr string) {
	w.t.Helper()
	var out, errb bytes.Buffer
	d := deps{Git: w.git, LoadPlanner: w.loader, Runner: w.runner, Now: func() time.Time { return w.now },
		Integrate: w.integrate, PutVerification: w.putVer}
	code = run(context.Background(), args, failOnRead{w.t}, &out, &errb, d)
	return code, out.String(), errb.String()
}

// sub runs a subcommand with the --state and --project flags added.
func (w *world) sub(name string, args ...string) (int, string, string) {
	w.t.Helper()
	all := append([]string{name, "--state", w.state, "--project", testProject}, args...)
	return w.run(all...)
}

// template writes a sandbox template file outside the state and returns its
// path. The loader of the world ignores its content.
func (w *world) template() string {
	p := filepath.Join(w.other, "sandbox.json")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		w.t.Fatal(err)
	}
	return p
}

func (w *world) mustOK(code int, out, errb string) string {
	w.t.Helper()
	if code != 0 {
		w.t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errb)
	}
	return out
}

// mirrorPath is the place of the mirror.
func (w *world) mirrorPath() string {
	return filepath.Join(w.state, "mirrors", testProject+".git")
}

// mirrorGit runs git on the mirror, as a fixture.
func (w *world) mirrorGit(args ...string) string {
	w.t.Helper()
	all := append([]string{"--git-dir=" + w.mirrorPath()}, args...)
	return fixtureGit(w.t, w.git, w.other, gitEnv(), all...)
}

// refs lists every ref of the mirror with its id.
func (w *world) refs() string {
	w.t.Helper()
	return w.mirrorGit("for-each-ref", "--format=%(refname) %(objectname)")
}

// tree is the clone of a lead.
func (w *world) tree(role string) string {
	return filepath.Join(w.state, "leads", role, "tree")
}

// lead makes a lead's clone with one commit on main, pushes that commit to
// the mirror as the upstream branch main, and returns its id.
func (w *world) lead(role string) string {
	w.t.Helper()
	tree := w.tree(role)
	if err := os.MkdirAll(tree, 0o700); err != nil {
		w.t.Fatal(err)
	}
	fixtureGit(w.t, w.git, tree, gitEnv(), "init", "--quiet", "--initial-branch=main")
	w.write(role, map[string]string{"base.txt": "base\n"})
	id := w.commit(role, "base")
	fixtureGit(w.t, w.git, tree, gitEnv(), "push", "--quiet", w.mirrorPath(), id+":refs/heads/main")
	return id
}

// write writes files in a lead's clone.
func (w *world) write(role string, files map[string]string) {
	w.t.Helper()
	for name, content := range files {
		p := filepath.Join(w.tree(role), name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			w.t.Fatal(err)
		}
	}
}

// commit commits everything in a lead's clone and returns the commit id.
func (w *world) commit(role, msg string, env ...string) string {
	w.t.Helper()
	tree := w.tree(role)
	e := gitEnv(env...)
	fixtureGit(w.t, w.git, tree, e, "add", "-A")
	fixtureGit(w.t, w.git, tree, e, "commit", "--quiet", "--no-gpg-sign", "-m", msg)
	return fixtureGit(w.t, w.git, tree, e, "rev-parse", "HEAD")
}

// branch switches a lead's clone to a new branch from the current commit.
func (w *world) branch(role, name string) {
	w.t.Helper()
	fixtureGit(w.t, w.git, w.tree(role), gitEnv(), "checkout", "--quiet", "-b", name)
}

// initMirror runs init.
func (w *world) initMirror() {
	w.t.Helper()
	w.mustOK(w.sub("init"))
}

// fetch runs fetch for the lead.
func (w *world) fetch(role string, extra ...string) string {
	w.t.Helper()
	args := append([]string{"--role", role, "--sandbox", w.template()}, extra...)
	return w.mustOK(w.sub("fetch", args...))
}

// reviewOut is what review printed.
type reviewOut struct {
	text   string
	tip    string
	digest string
}

var (
	tipLine    = regexp.MustCompile(`(?m)^Tip: ([0-9a-f]{40,64})$`)
	digestLine = regexp.MustCompile(`(?m)^Digest: ([0-9a-f]{64})$`)
)

// review runs review and reads the tip and the digest from its output.
func (w *world) review(role, branch, target string, extra ...string) reviewOut {
	w.t.Helper()
	args := append([]string{"--role", role, "--branch", branch, "--target", target}, extra...)
	out := w.mustOK(w.sub("review", args...))
	tip, dig := tipLine.FindStringSubmatch(out), digestLine.FindStringSubmatch(out)
	if tip == nil || dig == nil {
		w.t.Fatalf("review printed no tip or digest:\n%s", out)
	}
	return reviewOut{text: out, tip: tip[1], digest: dig[1]}
}

// verifyArgs are the flags of a verify with one gate.
func (w *world) verifyArgs(role, branch, command string) []string {
	return []string{
		"--role", role, "--branch", branch, "--gate", "check=" + command,
		"--sandbox", w.template(), "--records", w.records, "--work", w.work,
	}
}

// integrateArgs are the flags of an integrate that signs off everything
// given.
func (w *world) integrateArgs(role, branch, target string, r reviewOut, paths ...string) []string {
	args := []string{
		"--role", role, "--branch", branch, "--target", target,
		"--tip", r.tip, "--digest", r.digest, "--accept-change", "--records", w.records,
	}
	for _, p := range paths {
		args = append(args, "--accept-path", p)
	}
	return args
}

// store opens the records folder.
func (w *world) store() *intake.Store {
	w.t.Helper()
	s, err := intake.OpenStore(w.records)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { s.Close() })
	return s
}

// acceptArgs reads the exact --accept-path values that a review printed, in
// order, as the owner would type them.
func acceptArgs(t *testing.T, text string) []string {
	t.Helper()
	var out []string
	const marker = "to accept it, add: --accept-path "
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		word := line[i+len(marker):]
		if len(word) < 2 || word[0] != '\'' || word[len(word)-1] != '\'' || strings.Contains(word[1:len(word)-1], "'") {
			t.Fatalf("not one simple quoted word: %q", word)
		}
		out = append(out, word[1:len(word)-1])
	}
	return out
}

// noControl fails the test when s holds a control byte other than newline.
func noControl(t *testing.T, what, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != '\n' && (c < 0x20 || c == 0x7f) {
			t.Fatalf("%s holds the control byte %#x at %d: %q", what, c, i, s)
		}
	}
}
