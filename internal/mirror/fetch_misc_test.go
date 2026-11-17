package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFetchLock(t *testing.T) {
	e := newFetchEnv(t)
	ctx := context.Background()
	dir := t.TempDir()
	started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
	e.planner.UploadPack = e.wrapper("touch '" + started + "'; while [ ! -e '" + release + "' ]; do sleep 0.05; done")

	done := make(chan error, 1)
	go func() {
		_, err := e.m.Fetch(ctx, e.req())
		done <- err
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Lstat(started); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the first Fetch ended before it held the lock: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the first Fetch did not start")
		}
	}

	// A second fetch, through the same Mirror and through another one that
	// Open made, is refused at once and changes nothing.
	again, err := Open(ctx, e.g, e.m.Path())
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]*Mirror{"same": e.m, "reopened": again} {
		start := time.Now()
		if _, err := m.Fetch(ctx, e.req()); !errors.Is(err, ErrBusy) {
			t.Errorf("%s: second Fetch = %v, want ErrBusy", name, err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("%s: ErrBusy took %v: the lock must not block", name, time.Since(start))
		}
	}
	// The second fetch must not have removed the first one's staging
	// folder: it is refused before it looks for leftovers.
	entries, _ := os.ReadDir(filepath.Dir(e.m.Path()))
	staging := 0
	for _, ent := range entries {
		if strings.Contains(ent.Name(), stagingInfix) {
			staging++
		}
	}
	if staging != 1 {
		t.Errorf("%d staging folders while the first fetch runs, want 1", staging)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the first Fetch: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the first Fetch did not end")
	}
	noLeftovers(t, e.m)
	// The lock is free again.
	if _, err := e.m.Fetch(ctx, e.req()); err != nil {
		t.Errorf("Fetch after the lock was released: %v", err)
	}
}

func TestFetchRemovesLeftoverStaging(t *testing.T) {
	e := newFetchEnv(t)
	ctx := context.Background()
	mirrors := filepath.Dir(e.m.Path())

	// The leftover of a crashed fetch: a staging repository that nobody
	// removed. And a folder with a similar name and no marker.
	crashed, err := e.m.newStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	similar := filepath.Join(mirrors, "proj.git.staging-0123456789abcdef")
	if err := os.Mkdir(similar, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(similar, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := e.m.Fetch(ctx, e.req()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Lstat(crashed.path); err == nil {
		t.Error("the leftover of a crashed fetch was not removed")
	}
	if _, err := os.Lstat(filepath.Join(similar, "keep")); err != nil {
		t.Errorf("a folder without the marker was touched: %v", err)
	}

	// A fetch that is refused before it holds the lock leaves them alone.
	crashed2, err := e.m.newStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := e.req()
	req.Role = "Bad"
	if _, err := e.m.Fetch(ctx, req); err == nil {
		t.Fatal("Fetch with a bad role succeeded")
	}
	if _, err := os.Lstat(crashed2.path); err != nil {
		t.Errorf("a refused request removed a staging folder: %v", err)
	}
}

func TestFetchCleansUpOnAPanic(t *testing.T) {
	// A panic at each step from the one after the staging repository is made
	// to the last: the staging folder is gone and the lock is released.
	for step := 6; step <= 10; step++ {
		t.Run("step "+strconv.Itoa(step), func(t *testing.T) {
			e := newFetchEnv(t)
			fetchHook = func(s int) {
				if s == step {
					panic("test panic")
				}
			}
			t.Cleanup(func() { fetchHook = nil })
			func() {
				defer func() {
					if recover() == nil {
						t.Error("the fetch did not panic")
					}
				}()
				_, _ = e.m.Fetch(context.Background(), e.req())
			}()
			fetchHook = nil
			noLeftovers(t, e.m)
			// The lock was released on the panic, and the mirror is usable.
			if _, err := e.m.Fetch(context.Background(), e.req()); err != nil {
				t.Errorf("Fetch after a panic: %v", err)
			}
			if _, err := Open(context.Background(), e.g, e.m.Path()); err != nil {
				t.Errorf("Open after a panic: %v", err)
			}
		})
	}
}

func TestFetchPrunesOnlyItsOwnNamespace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	state := stateOf(f.m)
	lead := newLeadClone(t, f.g, state, "coder") // main only
	e := &fetchEnv{g: f.g, m: f.m, state: state, lead: lead, planner: newPlanner(t)}
	upBefore, err := f.m.Upstream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, err := f.m.LeadBranches(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}

	rep, err := f.m.Fetch(ctx, e.req())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// The fixture's coder refs (develop, feature/x) are gone, main is new.
	mainTip := lead.git(t, "rev-parse", "HEAD")
	want := []RefUpdate{
		{Ref: "refs/leads/coder/heads/develop", Old: f.second},
		{Ref: "refs/leads/coder/heads/feature/x", Old: f.second},
		{Ref: "refs/leads/coder/heads/main", New: mainTip},
	}
	if !reflect.DeepEqual(rep.Updates, want) {
		t.Errorf("updates = %+v\nwant     %+v", rep.Updates, want)
	}
	if got, _ := f.m.Upstream(ctx); !reflect.DeepEqual(got, upBefore) {
		t.Errorf("upstream changed: %v", got)
	}
	if got, _ := f.m.LeadBranches(ctx, "other"); !reflect.DeepEqual(got, otherBefore) {
		t.Errorf("the other role's refs changed: %v", got)
	}

	// A lead whose branches are all gone has all its refs pruned.
	lead.git(t, "checkout", "--quiet", "--detach")
	lead.git(t, "branch", "-D", "main")
	rep, err = f.m.Fetch(ctx, e.req())
	if err != nil {
		t.Fatalf("Fetch with no branches: %v", err)
	}
	if !reflect.DeepEqual(rep.Updates, []RefUpdate{{Ref: "refs/leads/coder/heads/main", Old: mainTip}}) {
		t.Errorf("updates = %+v", rep.Updates)
	}
	if got, err := f.m.LeadBranches(ctx, "coder"); err != nil || len(got) != 0 {
		t.Errorf("LeadBranches = %v, %v", got, err)
	}
	if got, _ := f.m.LeadBranches(ctx, "other"); !reflect.DeepEqual(got, otherBefore) {
		t.Errorf("the other role's refs changed: %v", got)
	}
	roles, err := f.m.Roles(ctx)
	if err != nil || !reflect.DeepEqual(roles, []string{"other"}) {
		t.Errorf("Roles = %v, %v", roles, err)
	}
	noLeftovers(t, f.m)
}

func TestFetchTwoRoles(t *testing.T) {
	e := newFetchEnv(t)
	ctx := context.Background()
	second := newLeadClone(t, e.g, e.state, "reviewer")
	second.git(t, "branch", "extra")
	if _, err := e.m.Fetch(ctx, e.req()); err != nil {
		t.Fatal(err)
	}
	req := e.req()
	req.Role = "reviewer"
	rep, err := e.m.Fetch(ctx, req)
	if err != nil || len(rep.Updates) != 2 {
		t.Fatalf("Fetch(reviewer) = %+v, %v", rep, err)
	}
	roles, err := e.m.Roles(ctx)
	if err != nil || !reflect.DeepEqual(roles, []string{"coder", "reviewer"}) {
		t.Errorf("Roles = %v, %v", roles, err)
	}
	// The second fetch did not touch the first role.
	if got, err := e.m.LeadBranches(ctx, "coder"); err != nil || len(got) != 1 {
		t.Errorf("LeadBranches(coder) = %v, %v", got, err)
	}
}

func TestCompareSnapshots(t *testing.T) {
	r := func(name, id string) record {
		return record{Ref: Ref{Name: name, ID: id, Type: "commit"}}
	}
	prefix := "refs/leads/a/heads/"
	before := []record{r("refs/heads/main", "1"), r(prefix+"x", "2"), r(prefix+"y", "3"), r("refs/leads/b/heads/z", "4"), r("refs/tags/t", "5")}

	// x changes, w is new; the refs of the other namespaces stay.
	after := []record{r("refs/heads/main", "1"), r(prefix+"w", "7"), r(prefix+"x", "9"), r(prefix+"y", "3"), r("refs/leads/b/heads/z", "4"), r("refs/tags/t", "5")}
	got, outside := compareSnapshots(before, after, prefix)
	want := []RefUpdate{{Ref: prefix + "w", New: "7"}, {Ref: prefix + "x", Old: "2", New: "9"}}
	if !reflect.DeepEqual(got, want) || len(outside) != 0 {
		t.Errorf("updates = %+v, outside = %+v, want %+v and none", got, outside, want)
	}
	// A pruned ref.
	got, outside = compareSnapshots(before, []record{before[0], before[1], before[3], before[4]}, prefix)
	if len(outside) != 0 || !reflect.DeepEqual(got, []RefUpdate{{Ref: prefix + "y", Old: "3"}}) {
		t.Errorf("pruned: %+v, %+v", got, outside)
	}

	// A change outside the namespace, in each way it can happen: it is
	// listed with its new value, and the updates stay empty.
	for name, tc := range map[string]struct {
		after []record
		want  []outsideChange
	}{
		"an upstream branch moved": {[]record{r("refs/heads/main", "8"), before[1], before[2], before[3], before[4]}, []outsideChange{{"refs/heads/main", "8"}}},
		"an upstream branch gone":  {[]record{before[1], before[2], before[3], before[4]}, []outsideChange{{"refs/heads/main", ""}}},
		"a tag added":              {[]record{before[0], before[1], before[2], before[3], before[4], r("refs/tags/u", "6")}, []outsideChange{{"refs/tags/u", "6"}}},
		"a tag changed type":       {[]record{before[0], before[1], before[2], before[3], {Ref: Ref{Name: "refs/tags/t", ID: "5", Type: "tag"}}}, []outsideChange{{"refs/tags/t", "5"}}},
		"a ref became symbolic":    {[]record{before[0], before[1], before[2], before[3], {Ref: before[4].Ref, Symref: "refs/heads/main"}}, []outsideChange{{"refs/tags/t", "5"}}},
		"another lead's ref moved": {[]record{before[0], before[1], before[2], r("refs/leads/b/heads/z", "5"), before[4]}, []outsideChange{{"refs/leads/b/heads/z", "5"}}},
		"another lead's ref added": {[]record{before[0], before[1], before[2], before[3], r("refs/leads/b/heads/zz", "5"), before[4]}, []outsideChange{{"refs/leads/b/heads/zz", "5"}}},
	} {
		up, got := compareSnapshots(before, tc.after, prefix)
		if len(up) != 0 || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: updates = %+v, outside = %+v, want %+v", name, up, got, tc.want)
		}
	}
}

func TestStagedRefsAndNames(t *testing.T) {
	outside := []outsideChange{{"refs/heads/a", "1"}, {"refs/heads/b", "2"}, {"refs/heads/c", ""}, {"refs/heads/d", "2"}}
	if got := stagedRefs(outside, map[string]bool{"2": true}); !reflect.DeepEqual(got, []string{"refs/heads/b", "refs/heads/d"}) {
		t.Errorf("stagedRefs = %v", got)
	}
	if got := stagedRefs(outside, nil); got != nil {
		t.Errorf("stagedRefs with no staged tip = %v", got)
	}
	if got := stagedRefs([]outsideChange{{"refs/heads/gone", ""}}, map[string]bool{"": true}); got != nil {
		t.Errorf("stagedRefs for a deleted ref = %v", got)
	}

	// The names are escaped and the list is cut at ten.
	var many []outsideChange
	for i := 0; i < 30; i++ {
		many = append(many, outsideChange{Name: "refs/heads/a" + string(rune('a'+i%26)) + "\xff" + string(rune('a'+i/26)), New: "2"})
	}
	shown := quoteNames(many)
	if len(shown) != maxReportedRefs || strings.Contains(strings.Join(shown, ""), "\xff") || !strings.Contains(shown[0], `\xff`) {
		t.Errorf("quoteNames = %q", shown)
	}
	if got := quoteNames(nil); got != nil {
		t.Errorf("quoteNames(nil) = %v", got)
	}
	var names []string
	for _, c := range many {
		names = append(names, c.Name)
	}
	err := upstreamChanged(names)
	if !errors.Is(err, ErrUpstreamChanged) || strings.Contains(err.Error(), "\xff") || !strings.Contains(err.Error(), "30 ref(s)") || !strings.Contains(err.Error(), "and more") {
		t.Errorf("error = %q", err)
	}
	if n := strings.Count(err.Error(), "refs/heads/"); n != maxReportedRefs {
		t.Errorf("the error names %d refs, want %d", n, maxReportedRefs)
	}
}

func TestFetchLetsALeadOfferAnObjectThatIsOnlyInTheMirror(t *testing.T) {
	// A stated limit, pinned so that a change is deliberate: a lead can point
	// a ref at a commit that is in the mirror and that its own clone does
	// not hold, for example the private commit of another lead. The staging
	// repository has the mirror's objects as its alternate, so git takes the
	// object as already there. The mirror is read-only in every lead's
	// sandbox, so the lead could read the object and offer it itself: the
	// ref says that the role offered the commit, not that it authored it.
	e := newFetchEnv(t)
	ctx := context.Background()
	other := newLeadClone(t, e.g, e.state, "other")
	secret := other.commit(t, "private.txt", "the other lead's own work\n", "private")
	req := e.req()
	req.Role = "other"
	if _, err := e.m.Fetch(ctx, req); err != nil {
		t.Fatalf("Fetch(other): %v", err)
	}

	// The coder's clone has the id in a ref and none of the objects.
	if hasObject(t, e.g, e.lead.gitDir(), secret) {
		t.Fatal("the fixture is wrong: the coder's clone holds the other lead's commit")
	}
	ref := filepath.Join(e.lead.gitDir(), "refs", "heads", "borrowed")
	if err := os.WriteFile(ref, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := e.m.Fetch(ctx, e.req())
	if err != nil {
		t.Fatalf("Fetch(coder) = %v: the stated limit changed, and the package comment must change with it", err)
	}
	prefix, _ := LeadPrefix("coder")
	if got, err := e.m.Resolve(ctx, prefix+"borrowed"); err != nil || got.ID != secret {
		t.Errorf("Resolve = %+v, %v, want the other lead's commit %s", got, err, secret)
	}
	found := false
	for _, u := range rep.Updates {
		found = found || (u.Ref == prefix+"borrowed" && u.New == secret)
	}
	if !found {
		t.Errorf("Updates = %+v, want the borrowed ref", rep.Updates)
	}
}
