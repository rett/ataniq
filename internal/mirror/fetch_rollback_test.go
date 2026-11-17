package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// rollbackEnv is the fixture mirror, where the lead "coder" has the branches
// develop and feature/x, and a lead clone for the same role that has a main
// and a new commit on it, so that a fetch prunes both branches and adds main.
type rollbackEnv struct {
	*fetchEnv
	f      *fixture
	prefix string
	tip    string // the new commit of the lead's clone
}

func newRollbackEnv(t *testing.T) *rollbackEnv {
	t.Helper()
	f := newFixture(t)
	e := &fetchEnv{g: f.g, m: f.m, state: stateOf(f.m), planner: newPlanner(t)}
	e.lead = newLeadClone(t, f.g, e.state, "coder")
	tip := e.lead.commit(t, "new.txt", "a commit that the mirror lacks\n", "new")
	prefix, err := LeadPrefix("coder")
	if err != nil {
		t.Fatal(err)
	}
	return &rollbackEnv{fetchEnv: e, f: f, prefix: prefix, tip: tip}
}

// leadRefs returns the refs of the lead's namespace in the mirror, as
// "name id" lines.
func (r *rollbackEnv) leadRefs(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, line := range mirrorRefs(t, r.m) {
		if strings.HasPrefix(line, r.prefix) {
			out = append(out, line)
		}
	}
	return out
}

// atStep runs fn right before the given step of the next fetch, as another
// process of the owner would. The hook is removed when the test ends.
func atStep(t *testing.T, step int, fn func()) {
	t.Helper()
	fetchHook = func(s int) {
		if s == step {
			fn()
		}
	}
	t.Cleanup(func() { fetchHook = nil })
}

// ownerGit runs git in the mirror as the owner's other process.
func (r *rollbackEnv) ownerGit(t *testing.T, args ...string) string {
	t.Helper()
	return fixtureGit(t, r.g, ".", append([]string{"--git-dir=" + r.m.Path()}, args...)...)
}

func TestFetchPutsTheLeadRefsBackOnAStagedUpstreamRef(t *testing.T) {
	// The review case: the lead's refs are develop and feature/x. After the
	// move, an upstream ref holds the tip that this fetch staged, which only
	// a bug could do. The fetch fails and the lead's refs are as before.
	r := newRollbackEnv(t)
	before := r.leadRefs(t)
	if len(before) != 2 {
		t.Fatalf("the fixture has lead refs %v", before)
	}
	atStep(t, 9, func() { r.ownerGit(t, "update-ref", "refs/heads/develop", r.tip) })

	rep, err := r.m.Fetch(context.Background(), r.req())
	if !errors.Is(err, ErrUpstreamChanged) || !strings.Contains(err.Error(), `"refs/heads/develop"`) {
		t.Fatalf("Fetch = %v, want ErrUpstreamChanged naming refs/heads/develop", err)
	}
	if strings.Contains(err.Error(), "put back") {
		t.Errorf("the lead's refs were put back, and the error says they were not: %v", err)
	}
	if got := r.leadRefs(t); !reflect.DeepEqual(got, before) {
		t.Errorf("the lead's refs after the failure:\n%q\nbefore:\n%q", got, before)
	}
	if len(rep.Updates) != 0 || len(rep.OutsideChanged) != 0 {
		t.Errorf("a failed fetch reports updates %+v and outside refs %v", rep.Updates, rep.OutsideChanged)
	}
	// The other writer's ref is not ours to undo.
	if got := r.ownerGit(t, "rev-parse", "refs/heads/develop"); got != r.tip {
		t.Errorf("refs/heads/develop = %s, want the writer's value %s", got, r.tip)
	}
	noLeftovers(t, r.m)

	// The mirror is usable: with the writer's ref in place, the next fetch
	// finds no staged tip any more (the mirror holds it) and goes through.
	fetchHook = nil
	rep, err = r.m.Fetch(context.Background(), r.req())
	if err != nil || len(rep.Updates) != 3 || len(rep.OutsideChanged) != 0 {
		t.Errorf("the next Fetch = %+v, %v", rep, err)
	}
}

func TestFetchPutsTheLeadRefsBackOnAnAlternatesFile(t *testing.T) {
	r := newRollbackEnv(t)
	before := r.leadRefs(t)
	alt := filepath.Join(r.m.Path(), "objects", "info", "alternates")
	atStep(t, 9, func() {
		if err := os.WriteFile(alt, []byte(t.TempDir()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	_, err := r.m.Fetch(context.Background(), r.req())
	if err == nil || !strings.Contains(err.Error(), "alternates") || errors.Is(err, ErrUpstreamChanged) {
		t.Fatalf("Fetch = %v, want the alternates error", err)
	}
	if err := os.Remove(alt); err != nil {
		t.Fatal(err)
	}
	if got := r.leadRefs(t); !reflect.DeepEqual(got, before) {
		t.Errorf("the lead's refs after the failure:\n%q\nbefore:\n%q", got, before)
	}
	noLeftovers(t, r.m)
}

func TestFetchSaysWhenTheLeadRefsCannotBePutBack(t *testing.T) {
	// The same failure as above, and a stale lock on one of the lead's refs
	// that makes the restore fail: the error says both, and the refs are
	// left where the move put them, which is the case this error is for.
	r := newRollbackEnv(t)
	lock := filepath.Join(r.m.Path(), r.prefix+"develop.lock")
	atStep(t, 9, func() {
		r.ownerGit(t, "update-ref", "refs/heads/develop", r.tip)
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	_, err := r.m.Fetch(context.Background(), r.req())
	if !errors.Is(err, ErrUpstreamChanged) || !strings.Contains(err.Error(), "could not be put back") {
		t.Fatalf("Fetch = %v, want ErrUpstreamChanged and the failure to put the refs back", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	noLeftovers(t, r.m)
}

func TestFetchMoveThatFailsPartwayChangesNoRef(t *testing.T) {
	// The lead has main (a new ref in the mirror), aaa and zzz. A stale lock
	// on zzz makes git fail while it takes the locks of the move. Without
	// --atomic the other refs would have moved by then, and develop and
	// feature/x pruned. The lead's refs are as before.
	r := newRollbackEnv(t)
	r.lead.git(t, "branch", "aaa")
	r.lead.git(t, "branch", "zzz")
	before := mirrorRefs(t, r.m)
	lock := filepath.Join(r.m.Path(), r.prefix+"zzz.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Fetch(context.Background(), r.req()); err == nil {
		t.Fatal("Fetch with a stale lock on a ref succeeded")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if got := mirrorRefs(t, r.m); !reflect.DeepEqual(got, before) {
		t.Errorf("the refs after a failed move:\n%q\nbefore:\n%q", got, before)
	}
	noLeftovers(t, r.m)

	// Without the lock the same fetch goes through.
	rep, err := r.m.Fetch(context.Background(), r.req())
	if err != nil || len(rep.Updates) != 5 {
		t.Errorf("Fetch without the lock = %+v, %v", rep, err)
	}
}

func TestFetchOutsideChangeIsANoteAndNotAnError(t *testing.T) {
	// An owner process writes refs outside the lead's namespace while the
	// fetch runs: an upstream branch to a commit that the mirror held
	// before, a new upstream branch to a commit that the owner just made, a
	// tag, and a ref of another lead. The move names none of them, so none of
	// them is undone and the fetch goes through. The report lists them.
	r := newRollbackEnv(t)
	var owner string
	atStep(t, 9, func() {
		owner = r.ownerGit(t, "commit-tree", r.f.src.tree, "-p", r.f.first, "-m", "the owner's own work")
		r.ownerGit(t, "update-ref", "refs/heads/develop", r.f.second)
		r.ownerGit(t, "update-ref", "refs/heads/owner-work", owner)
		r.ownerGit(t, "update-ref", "refs/tags/v2", owner)
		r.ownerGit(t, "update-ref", "refs/leads/other/heads/develop", owner)
	})
	rep, err := r.m.Fetch(context.Background(), r.req())
	if err != nil {
		t.Fatalf("Fetch = %v", err)
	}
	want := []string{
		`"refs/heads/develop"`, `"refs/heads/owner-work"`,
		`"refs/leads/other/heads/develop"`, `"refs/tags/v2"`,
	}
	if !reflect.DeepEqual(rep.OutsideChanged, want) {
		t.Errorf("OutsideChanged = %q, want %q", rep.OutsideChanged, want)
	}
	if len(rep.Updates) != 3 {
		t.Errorf("Updates = %+v, want the three changes of the lead's refs", rep.Updates)
	}
	for ref, id := range map[string]string{
		"refs/heads/develop": r.f.second, "refs/heads/owner-work": owner,
		"refs/tags/v2": owner, "refs/leads/other/heads/develop": owner,
		r.prefix + "main": r.tip,
	} {
		if got := r.ownerGit(t, "rev-parse", ref); got != id {
			t.Errorf("%s = %s, want %s", ref, got, id)
		}
	}
	noLeftovers(t, r.m)
}

func TestFetchOutsideNoteHoldsAtMostTenNames(t *testing.T) {
	r := newRollbackEnv(t)
	atStep(t, 9, func() {
		for i := 0; i < 14; i++ {
			r.ownerGit(t, "update-ref", "refs/heads/owner"+string(rune('a'+i)), r.f.first)
		}
	})
	rep, err := r.m.Fetch(context.Background(), r.req())
	if err != nil || len(rep.OutsideChanged) != maxReportedRefs {
		t.Errorf("Fetch = %+v, %v, want ten names", rep.OutsideChanged, err)
	}
}

func TestFetchNoOutsideChangeLeavesTheNoteEmpty(t *testing.T) {
	r := newRollbackEnv(t)
	rep, err := r.m.Fetch(context.Background(), r.req())
	if err != nil || rep.OutsideChanged != nil || len(rep.Updates) != 3 {
		t.Errorf("Fetch = %+v, %v", rep, err)
	}
}

func TestNewTipsAreWhatTheMirrorLacks(t *testing.T) {
	r := newRollbackEnv(t)
	ctx := context.Background()
	s, err := r.m.newStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.remove()
	// A staging repository with a tip that the mirror holds and one that it
	// does not: the second commit lives in the staging repository only.
	mirrorGit(t, r.g, s.path, nil, "update-ref", r.prefix+"old", r.f.first)
	file := []string{"file"}
	mirrorGit(t, r.g, s.path, file, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--end-of-options",
		r.lead.tree, "+refs/heads/main:"+r.prefix+"main")
	got, err := r.m.newTips(ctx, s, DefaultFetchLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]bool{r.tip: true}) {
		t.Errorf("newTips = %v, want only %s", got, r.tip)
	}
	// An empty staging repository has no tips.
	s2, err := r.m.newStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.remove()
	if got, err := r.m.newTips(ctx, s2, DefaultFetchLimits()); err != nil || len(got) != 0 {
		t.Errorf("newTips of an empty staging repository = %v, %v", got, err)
	}
}

func TestRestoreNamespace(t *testing.T) {
	// Put back a changed ref, a pruned ref and a new ref in one go, and fail
	// with nothing changed when one ref is not what the caller says it is.
	f := newFixture(t)
	ctx := context.Background()
	prefix, _ := LeadPrefix("coder")
	before, err := f.m.snapshotMirror(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.updateRef(t, prefix+"develop", f.first)                                                // changed
	f.updateRef(t, prefix+"new", f.first)                                                    // new
	fixtureGit(t, f.g, ".", "--git-dir="+f.m.Path(), "update-ref", "-d", prefix+"feature/x") // pruned
	f.updateRef(t, "refs/heads/develop", f.second)                                           // outside: stays
	after, err := f.m.snapshotMirror(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Another writer moved a ref since the snapshot: the restore fails and
	// changes nothing.
	f.updateRef(t, prefix+"new", f.second)
	mid := mirrorRefs(t, f.m)
	if err := f.m.restoreNamespace(ctx, prefix, before, after); err == nil {
		t.Fatal("restoreNamespace succeeded against a ref that moved")
	}
	if got := mirrorRefs(t, f.m); !reflect.DeepEqual(got, mid) {
		t.Errorf("a failed restore changed refs:\n%q\n%q", got, mid)
	}
	f.updateRef(t, prefix+"new", f.first)

	if err := f.m.restoreNamespace(ctx, prefix, before, after); err != nil {
		t.Fatal(err)
	}
	got, err := f.m.snapshotMirror(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if before[i].Name == "refs/heads/develop" {
			before[i].ID = f.second // outside the namespace: not restored
		}
	}
	if !reflect.DeepEqual(got, before) {
		t.Errorf("refs after the restore:\n%+v\nwant:\n%+v", got, before)
	}
	// Nothing to restore is not an error and runs no git.
	if err := f.m.restoreNamespace(ctx, prefix, got, got); err != nil {
		t.Errorf("restoreNamespace with nothing to do: %v", err)
	}
}
