package mirror

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/sandboxplan/plantest"
)

// fetchEnv is a mirror, a lead's clone under the same state root, and a fake
// planner whose upload-pack is the host's git-upload-pack: nothing here is
// sandboxed, and nothing starts a namespace.
type fetchEnv struct {
	g       gitenv.Git
	m       *Mirror
	state   string
	lead    *leadClone
	planner *plantest.Fake
	gup     string // the path of git-upload-pack
}

func newFetchEnv(t *testing.T) *fetchEnv {
	t.Helper()
	g, m := newMirror(t)
	state := stateOf(m)
	planner := newPlanner(t)
	return &fetchEnv{
		g: g, m: m, state: state,
		lead:    newLeadClone(t, g, state, "coder"),
		planner: planner,
		gup:     planner.UploadPack[0],
	}
}

// stateOf returns the state root of a mirror made by newMirror.
func stateOf(m *Mirror) string { return filepath.Dir(filepath.Dir(m.Path())) }

// newPlanner returns a fake planner whose upload-pack is the host's.
func newPlanner(t *testing.T) *plantest.Fake {
	t.Helper()
	gup, err := plantest.GitUploadPack()
	if err != nil {
		t.Fatalf("these tests need git-upload-pack: %v", err)
	}
	return &plantest.Fake{UploadPack: gup, MeasureBytes: 1000}
}

func (e *fetchEnv) req() FetchRequest {
	return FetchRequest{StateRoot: e.state, Role: "coder", Project: "proj", Planner: e.planner}
}

// wrapper returns an upload-pack argv that runs script first, then the real
// git-upload-pack with the arguments git gives. The script sees them as "$@"
// and is one line: gitenv refuses a newline in the command.
func (e *fetchEnv) wrapper(script string) []string {
	return []string{"/bin/sh", "-c", script + "; exec '" + e.gup + "' \"$@\"", "sh"}
}

// mirrorRefs returns every ref of the mirror, as "name id" lines.
func mirrorRefs(t *testing.T, m *Mirror) []string {
	t.Helper()
	recs, err := m.snapshotMirror(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.Name+" "+r.ID+" "+r.Type+" "+r.Symref)
	}
	return out
}

// objectCount returns the number of objects in the mirror, loose and packed.
func objectCount(t *testing.T, g gitenv.Git, m *Mirror) int {
	t.Helper()
	n := 0
	for _, line := range strings.Split(mirrorGit(t, g, m.Path(), nil, "count-objects", "-v"), "\n") {
		k, v, _ := strings.Cut(line, ": ")
		if k == "count" || k == "in-pack" {
			c, err := strconv.Atoi(v)
			if err != nil {
				t.Fatal(err)
			}
			n += c
		}
	}
	return n
}

// noLeftovers fails the test when the folder of the mirrors holds anything but
// the mirror and its lock file.
func noLeftovers(t *testing.T, m *Mirror) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(m.Path()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	want := []string{"proj.git"}
	if _, err := os.Lstat(m.lockPath()); err == nil {
		want = append(want, "proj.git.lock")
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("the mirrors folder holds %v, want %v", names, want)
	}
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

func TestFetchFirstAndIncremental(t *testing.T) {
	e := newFetchEnv(t)
	ctx := context.Background()
	first := e.lead.git(t, "rev-parse", "HEAD")
	// A big file makes the first fetch much larger than the second.
	e.lead.git(t, "checkout", "--quiet", "-b", "feature/x")
	for i := 0; i < 4; i++ {
		e.lead.commit(t, "big"+strconv.Itoa(i)+".bin", randomText(int64(i), 100<<10), "big "+strconv.Itoa(i))
	}
	tip := e.lead.git(t, "rev-parse", "HEAD")

	rep, err := e.m.Fetch(ctx, e.req())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := []RefUpdate{
		{Ref: "refs/leads/coder/heads/feature/x", New: tip},
		{Ref: "refs/leads/coder/heads/main", New: first},
	}
	if !reflect.DeepEqual(rep.Updates, want) {
		t.Errorf("updates = %+v, want %+v", rep.Updates, want)
	}
	if rep.Source != 1000 || rep.MirrorBefore <= 0 || rep.Staged <= 0 || rep.MirrorAfter <= rep.MirrorBefore || rep.Duration <= 0 {
		t.Errorf("report = %+v", rep)
	}
	got, err := e.m.LeadBranches(ctx, "coder")
	if err != nil || !reflect.DeepEqual(names(got), []string{"refs/leads/coder/heads/feature/x", "refs/leads/coder/heads/main"}) {
		t.Errorf("LeadBranches = %v, %v", got, err)
	}
	if up, err := e.m.Upstream(ctx); err != nil || len(up) != 0 {
		t.Errorf("Upstream = %v, %v; want none", up, err)
	}
	noLeftovers(t, e.m)
	if got := mirrorGit(t, e.g, e.m.Path(), nil, "rev-list", "--objects", "--end-of-options", tip); !strings.Contains(got, tip) {
		t.Errorf("the tip is not in the mirror")
	}
	// The mirror is a plain mirror still: no alternates, no fetch head.
	if _, err := os.Lstat(filepath.Join(e.m.Path(), "objects", "info", "alternates")); err == nil {
		t.Error("the mirror has an alternates file")
	}
	if _, err := os.Lstat(filepath.Join(e.m.Path(), "FETCH_HEAD")); err == nil {
		t.Error("the fetch wrote FETCH_HEAD in the mirror")
	}
	if _, err := Open(ctx, e.g, e.m.Path()); err != nil {
		t.Errorf("Open after the fetch: %v", err)
	}

	// An incremental fetch: one small commit on one branch, one new branch.
	e.lead.commit(t, "small.txt", "x\n", "small")
	tip2 := e.lead.git(t, "rev-parse", "HEAD")
	e.lead.git(t, "branch", "other", first)
	rep2, err := e.m.Fetch(ctx, e.req())
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	want2 := []RefUpdate{
		{Ref: "refs/leads/coder/heads/feature/x", Old: tip, New: tip2},
		{Ref: "refs/leads/coder/heads/other", New: first},
	}
	if !reflect.DeepEqual(rep2.Updates, want2) {
		t.Errorf("second updates = %+v, want %+v", rep2.Updates, want2)
	}
	if rep2.Staged >= rep.Staged {
		t.Errorf("the second fetch staged %d bytes, the first %d: it must stage only what is new", rep2.Staged, rep.Staged)
	}
	if testing.Verbose() {
		t.Logf("staged: first %d, second %d", rep.Staged, rep2.Staged)
	}
	// Nothing new: no updates.
	rep3, err := e.m.Fetch(ctx, e.req())
	if err != nil || len(rep3.Updates) != 0 {
		t.Errorf("third Fetch = %+v, %v", rep3, err)
	}
	noLeftovers(t, e.m)
}

// hasObject reports whether the repository at gitDir holds the object.
func hasObject(t *testing.T, g gitenv.Git, gitDir, id string) bool {
	t.Helper()
	cmd := exec.Command(g.Path, "--git-dir="+gitDir, "cat-file", "-e", id)
	cmd.Env = fixtureEnv(t)
	return cmd.Run() == nil
}

// state is what a failed fetch must leave as it was: the refs, the number of
// objects and the folders beside the mirror.
type mirrorState struct {
	refs    []string
	objects int
}

func (e *fetchEnv) state0(t *testing.T) mirrorState {
	t.Helper()
	return mirrorState{refs: mirrorRefs(t, e.m), objects: objectCount(t, e.g, e.m)}
}

func (e *fetchEnv) unchanged(t *testing.T, was mirrorState) {
	t.Helper()
	if got := e.state0(t); !reflect.DeepEqual(got, was) {
		t.Errorf("the mirror changed:\nbefore %+v\nafter  %+v", was, got)
	}
	noLeftovers(t, e.m)
}

func TestFetchSeparatesNamespaces(t *testing.T) {
	// Design case S16: a lead has a branch develop and a tag v1, and the
	// mirror has an upstream develop and an upstream v1 with other content.
	f := newFixture(t)
	ctx := context.Background()
	state := stateOf(f.m)
	lead := newLeadClone(t, f.g, state, "coder")
	lead.git(t, "checkout", "--quiet", "-b", "develop")
	tip := lead.commit(t, "lead.txt", "the lead's develop\n", "lead develop")
	lead.git(t, "tag", "--no-sign", "-a", "-m", "the lead's tag", "v1")
	lead.git(t, "tag", "--no-sign", "-a", "-m", "another tag of the lead", "lead-only")
	lead.git(t, "tag", "light")
	leadTags := []string{
		lead.git(t, "rev-parse", "refs/tags/v1"),
		lead.git(t, "rev-parse", "refs/tags/lead-only"),
	}
	if leadTags[0] == f.src.tag {
		t.Fatal("the lead's tag v1 is the upstream tag: the case proves nothing")
	}
	if up, _ := f.m.Resolve(ctx, "refs/heads/develop"); up.ID == tip {
		t.Fatal("the lead's develop is the upstream develop: the case proves nothing")
	}

	upstream := func() []string {
		var out []string
		for _, line := range mirrorRefs(t, f.m) {
			if strings.HasPrefix(line, "refs/heads/") || strings.HasPrefix(line, "refs/tags/") {
				out = append(out, line)
			}
		}
		return out
	}
	otherRole := func() []string {
		var out []string
		for _, line := range mirrorRefs(t, f.m) {
			if strings.HasPrefix(line, "refs/leads/other/") {
				out = append(out, line)
			}
		}
		return out
	}
	upBefore, otherBefore := upstream(), otherRole()
	if len(upBefore) == 0 || len(otherBefore) == 0 {
		t.Fatal("the fixture has no upstream refs or no refs of the other role")
	}

	rep, err := f.m.Fetch(ctx, FetchRequest{
		StateRoot: state, Role: "coder", Project: "proj",
		Planner: newPlanner(t),
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := upstream(); !reflect.DeepEqual(got, upBefore) {
		t.Errorf("refs/heads and refs/tags changed:\nbefore %q\nafter  %q", upBefore, got)
	}
	if got := otherRole(); !reflect.DeepEqual(got, otherBefore) {
		t.Errorf("the other role's refs changed:\nbefore %q\nafter  %q", otherBefore, got)
	}
	got, err := f.m.Resolve(ctx, "refs/leads/coder/heads/develop")
	if err != nil || got.ID != tip {
		t.Errorf("the lead's develop = %+v, %v; want %s", got, err, tip)
	}
	// No tag of the lead is a ref anywhere, and none was carried as an object.
	for _, line := range mirrorRefs(t, f.m) {
		for _, id := range leadTags {
			if strings.Contains(line, id) {
				t.Errorf("a ref of the mirror holds a tag of the lead: %s", line)
			}
		}
	}
	if _, err := f.m.Resolve(ctx, "refs/tags/light"); !errors.Is(err, ErrNoRef) {
		t.Errorf("the lead's lightweight tag is a ref of the mirror: %v", err)
	}
	if _, err := f.m.Resolve(ctx, "refs/tags/lead-only"); !errors.Is(err, ErrNoRef) {
		t.Errorf("the lead's own tag is a ref of the mirror: %v", err)
	}
	for _, id := range leadTags {
		if hasObject(t, f.g, f.m.Path(), id) {
			t.Errorf("the lead's tag object %s was carried into the mirror", id)
		}
	}
	// The lead's own coder refs from the fixture were replaced: develop and
	// main are the lead's, feature/x is pruned.
	wantNames := []string{"refs/leads/coder/heads/develop", "refs/leads/coder/heads/main"}
	refs, err := f.m.LeadBranches(ctx, "coder")
	if err != nil || !reflect.DeepEqual(names(refs), wantNames) {
		t.Errorf("LeadBranches = %v, %v; want %v", names(refs), err, wantNames)
	}
	pruned := false
	for _, u := range rep.Updates {
		if u.Ref == "refs/leads/coder/heads/feature/x" && u.New == "" && u.Old == f.second {
			pruned = true
		}
	}
	if !pruned {
		t.Errorf("updates do not show feature/x pruned: %+v", rep.Updates)
	}
	noLeftovers(t, f.m)
}
