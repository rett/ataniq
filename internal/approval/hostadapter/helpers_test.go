//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/mergebuild"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/intake"
	"ataniq/internal/intake/execconfig"
	"ataniq/internal/intake/verify"
)

// These tests run the installed git on temporary bare repositories. The
// fixtures are made with plain git (init, hash-object, update-index,
// write-tree, commit-tree, update-ref) because the pinned environment of the
// host writes no blob and no tree; everything the adapter does goes through
// gitenv, as in production.

var start = time.Date(2026, 11, 20, 14, 2, 4, 0, time.UTC)

// lab is a temporary bare repository, the mirror, with an index file of its
// own for building commits.
type lab struct {
	t    *testing.T
	git0 string // the git the repository's lookup found
	dir  string
	idx  string
	n    int
}

func newLab(t *testing.T) *lab {
	t.Helper()
	gitBin := needGit(t).Path
	dir := filepath.Join(t.TempDir(), "mirror.git")
	l := &lab{t: t, git0: gitBin, dir: dir, idx: filepath.Join(t.TempDir(), "index")}
	cmd := exec.Command(gitBin, "init", "--bare", "--quiet", "--template=", dir)
	cmd.Env = l.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return l
}

func (l *lab) env() []string {
	return []string{
		"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_DIR=" + l.dir, "GIT_INDEX_FILE=" + l.idx,
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
		fmt.Sprintf("GIT_AUTHOR_DATE=%d +0000", 1_700_000_000+l.n), fmt.Sprintf("GIT_COMMITTER_DATE=%d +0000", 1_700_000_000+l.n),
	}
}

// try runs plain git on the mirror.
func (l *lab) try(stdin string, args ...string) (string, error) {
	l.t.Helper()
	cmd := exec.Command(l.git0, args...)
	cmd.Env = l.env()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	return string(out), err
}

// git is try that must succeed; it returns the output without its last
// newline.
func (l *lab) git(stdin string, args ...string) string {
	l.t.Helper()
	out, err := l.try(stdin, args...)
	if err != nil {
		l.t.Fatalf("git %q: %v", args, err)
	}
	return strings.TrimSuffix(out, "\n")
}

// put adds a file to the index.
func (l *lab) put(path, content string) { l.putMode("100644", path, content) }

func (l *lab) putMode(mode, path, content string) {
	l.t.Helper()
	id := l.git(content, "hash-object", "-w", "--stdin")
	l.git("", "update-index", "--add", "--cacheinfo", mode+","+id+","+path)
}

// load puts the tree of a commit into the index.
func (l *lab) load(commit string) {
	l.t.Helper()
	l.git("", "read-tree", commit)
}

// commit writes the index as a commit with the parents.
func (l *lab) commit(msg string, parents ...string) string {
	l.t.Helper()
	l.n++
	tree := l.git("", "write-tree")
	args := []string{"commit-tree", tree, "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	return l.git("", args...)
}

func (l *lab) setRef(name, id string) { l.t.Helper(); l.git("", "update-ref", name, id) }

func (l *lab) ref(name string) string {
	l.t.Helper()
	out, err := l.try("", "rev-parse", "--verify", "--quiet", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (l *lab) refs() string {
	l.t.Helper()
	return l.git("", "for-each-ref", "--format=%(refname) %(objectname)")
}

// has reports whether the object is in the store.
func (l *lab) has(id string) bool {
	l.t.Helper()
	_, err := l.try("", "cat-file", "-e", id)
	return err == nil
}

// scene is the history the tests start from: a root, the target's tip D that
// moved on, and the feature's tip F that branched from the root. refs/heads/
// develop is D and the lead's feature branch is F.
type scene struct {
	root, target, feature string
}

const (
	targetBranch = "refs/heads/develop"
	featureRef   = "refs/leads/role-a/heads/feature/x"
)

// newScene makes the scene. change edits the index of the feature's side,
// which holds the root's files when it runs; nil adds feature.txt.
func newScene(l *lab, change func(l *lab)) scene {
	l.t.Helper()
	l.put("README.md", "readme\n")
	l.put("src/main.go", "package main\n")
	s := scene{root: l.commit("root\n")}
	l.put("target-side.txt", "target\n")
	s.target = l.commit("target moves\n", s.root)
	l.load(s.root)
	if change == nil {
		change = func(l *lab) { l.put("feature.txt", "one\n") }
	}
	change(l)
	s.feature = l.commit("feature\n", s.root)
	l.setRef(targetBranch, s.target)
	l.setRef(featureRef, s.feature)
	return s
}

// fakeSuite stands for the suite runner of the verification sandbox.
type fakeSuite struct {
	mu     sync.Mutex
	calls  []SuiteRequest
	result SuiteResult
	err    error
	// onRun runs when a call starts, with the call.
	onRun func(SuiteRequest)
}

func newSuite() *fakeSuite {
	return &fakeSuite{result: SuiteResult{Passed: true, Exit: 0, Output: []byte("ok\n"), Started: start, Duration: time.Second}}
}

func (f *fakeSuite) Run(_ context.Context, req SuiteRequest) (SuiteResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	res, err, hook := f.result, f.err, f.onRun
	f.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	return res, err
}

func (f *fakeSuite) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSuite) set(res SuiteResult, err error) {
	f.mu.Lock()
	f.result, f.err = res, err
	f.mu.Unlock()
}

// testProject is the project the tests serve.
func testProject(l *lab) Project {
	return Project{
		Name: "project-a", Mirror: l.dir, Role: "role-a", Target: targetBranch, Rules: execconfig.Default(),
		Identity: mergebuild.Identity{Name: "Host Identity", Email: "host@example.invalid"},
		Gates:    []verify.Gate{{Name: "suite", Argv: []string{"make", "test"}}},
	}
}

func needGit(t *testing.T) gitenv.Git {
	t.Helper()
	g, err := gitenv.Find()
	if err != nil {
		t.Skipf("these tests need an installed git, which the repository's lookup (gitenv.Find) does not find: %v", err)
	}
	return g
}

// rig is one test's world: a mirror, a stand-in door, a fake suite and an
// opened owner channel on a state folder, with no loop running.
type rig struct {
	t     *testing.T
	lab   *lab
	scene scene
	clock *doortest.Clock
	stand *doortest.Door
	suite *fakeSuite
	proj  Project
	state string
	drop  string
	o     *Opened

	mu      sync.Mutex
	notices []string
	// fault, when set, is the service's fault hook.
	fault func(point string) error
	n     int
	// git, when set, is the git the host runs instead of the installed one:
	// a wrapper of a test.
	git *gitenv.Git
}

func newRig(t *testing.T, change func(l *lab)) *rig {
	t.Helper()
	l := newLab(t)
	r := &rig{t: t, lab: l, scene: newScene(l, change), clock: doortest.NewClock(start), suite: newSuite(), proj: testProject(l)}
	cfg := doortest.Sample(r.clock.Now)
	cfg.Command = "qargi"
	r.stand = doortest.New(cfg)
	r.state = privateDir(t)
	r.drop = privateDir(t)
	r.open()
	return r
}

// privateDir makes a folder that only the user can reach.
func privateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func (r *rig) config() OpenConfig {
	return OpenConfig{
		Config: Config{
			Git: r.gitOf(), Projects: []Project{r.proj}, Suite: r.suite, Now: r.clock.Now,
			Notice: func(l string) { r.mu.Lock(); r.notices = append(r.notices, l); r.mu.Unlock() },
		},
		StateDir: r.state, Door: r.stand, Ender: ender{r.stand}, DropDir: r.drop, CollectProject: "project-a", Manual: true,
		Service: ownerchannel.Config{Fault: func(point string) error {
			if r.fault != nil {
				return r.fault(point)
			}
			return nil
		}},
	}
}

func (r *rig) gitOf() gitenv.Git {
	if r.git != nil {
		return *r.git
	}
	return needGit(r.t)
}

type ender struct{ d *doortest.Door }

func (e ender) EndRequest(_ context.Context, id string) error { return e.d.Withdraw(id) }

// open opens the owner channel on the state folder.
func (r *rig) open() {
	r.t.Helper()
	o, err := Open(context.Background(), r.config())
	if err != nil {
		r.t.Fatalf("Open: %v", err)
	}
	r.o = o
	r.t.Cleanup(func() { _ = o.Close() })
}

// restart closes the owner channel and opens it again on the same state
// folder, door and mirror.
func (r *rig) restart() {
	r.t.Helper()
	if err := r.o.Close(); err != nil {
		r.t.Fatalf("Close: %v", err)
	}
	r.open()
}

func (r *rig) noticed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.notices...)
}

// request makes the bytes of a request to finish the feature.
func (r *rig) request(feature string, change func(*record.Request)) (id string, b []byte) {
	r.t.Helper()
	r.n++
	rq := record.Request{
		Schema: record.SchemaRequest, RequestID: fmt.Sprintf("rq-test-%02d", r.n), Epoch: 7,
		Verb: record.VerbFeatureFinish, Argv: []string{feature}, FeatureSHA: r.scene.feature,
		NoteSHA256: record.Digest([]byte("a note")), At: record.FormatTime(r.clock.Now()),
	}
	if change != nil {
		change(&rq)
	}
	b, err := record.EncodeRequest(rq)
	if err != nil {
		r.t.Fatal(err)
	}
	return rq.RequestID, b
}

// dropFile writes a request file, and a note, into the drop folder.
func (r *rig) dropFile(id string, b []byte) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.drop, id+requestSuffix), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.drop, id+noteSuffix), []byte("a note"), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// ask drops a request for feature x and collects it, and returns the id.
func (r *rig) ask() string {
	r.t.Helper()
	id, b := r.request("x", nil)
	r.dropFile(id, b)
	if n, err := r.o.Collector.Scan(context.Background()); err != nil || n != 1 {
		r.t.Fatalf("Scan took %d requests: %v", n, err)
	}
	return id
}

// card asks and returns the pending record the request led to.
func (r *rig) card() record.Pending {
	r.t.Helper()
	id := r.ask()
	st, ok := r.o.Service.Status(id)
	if !ok || st.State != ownerchannel.RequestPending {
		r.t.Fatalf("the request is %+v, want pending", st)
	}
	return r.pending(st.PendingID)
}

func (r *rig) pending(id string) record.Pending {
	r.t.Helper()
	shown, err := r.stand.Show(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	p, err := record.DecodePending(shown.Pending.Bytes)
	if err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) state0(id string) record.State {
	r.t.Helper()
	shown, err := r.stand.Show(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return shown.Entry.State
}

// src is the connection of the owner's console, which may decide.
var src = ownerchannel.Source{Surface: "console", Session: "0123456789abcdef", Decider: true, PID: 41822, TTY: "/dev/pts/4", Client: "console-a 0.0.1"}

// frame makes the owner's frame for a card: an approve that signs off every
// flagged path, or a deny.
func frame(p record.Pending, approve bool) ownerwire.DecisionFrame {
	f := ownerwire.DecisionFrame{ID: p.ID, BindingSHA256: p.BindingSHA256, Decision: record.DecisionDeny, SignedOff: []record.SignOff{}}
	if approve {
		f.Decision = record.DecisionApprove
		for _, fl := range p.Binding.Summary.Flagged {
			f.SignedOff = append(f.SignedOff, record.SignOff{Kind: fl.Kind, Path: fl.Path})
		}
	}
	return f
}

// decide beats and decides.
func (r *rig) decide(p record.Pending, approve bool) (ownerwire.DecideResult, error) {
	r.t.Helper()
	if err := r.o.Service.Beat(src); err != nil {
		r.t.Fatal(err)
	}
	return r.o.Service.Decide(context.Background(), src, frame(p, approve))
}

// reviewOf makes the intake's own review of the merge, as a test of the
// package would, to compare digests with.
func (r *rig) reviewOf(pin string) *intake.Review {
	r.t.Helper()
	rev, err := intake.Inspect(context.Background(), intake.Source{Git: needGit(r.t), GitDir: r.lab.dir}, intake.Request{
		BaseRef: targetBranch, TipRef: pin, Rules: execconfig.Default(), Merge: true,
	})
	if err != nil {
		r.t.Fatalf("Inspect: %v", err)
	}
	return rev
}

// pinOfMerge returns the pin that holds the commit.
func (r *rig) pinOfMerge(commit string) string {
	r.t.Helper()
	for _, line := range strings.Split(r.lab.refs(), "\n") {
		name, id, _ := strings.Cut(line, " ")
		if id == commit && strings.HasPrefix(name, intake.PendingPrefix) {
			return name
		}
	}
	return ""
}

// replyOf reads the reply file of a request id, or "" when there is none.
func (r *rig) replyOf(id string) string {
	b, err := os.ReadFile(filepath.Join(r.drop, id+replySuffix))
	if err != nil {
		return ""
	}
	return string(b)
}

// openStore opens an intake records folder of its own.
func openStore(t *testing.T) *intake.Store {
	t.Helper()
	st, err := intake.OpenStore(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
