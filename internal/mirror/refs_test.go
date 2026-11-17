package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

// fixture is a mirror filled from a source repository.
type fixture struct {
	g      gitenv.Git
	m      *Mirror
	first  string // a commit on main and develop
	second string // a commit on the branch "lead"
	src    source
}

// newFixture makes a mirror and fetches into it, with git through gitenv:
//   - upstream: main and develop at the first commit, the annotated tag v1,
//     and tags that point at a commit, a tree and a blob;
//   - lead "coder": develop (the second commit, so it differs from the
//     upstream develop) and feature/x;
//   - lead "other": develop.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	g, m := newMirror(t)
	src := newSource(t, g)
	fixtureGit(t, g, src.dir, "branch", "develop")
	fixtureGit(t, g, src.dir, "checkout", "--quiet", "-b", "lead")
	if err := os.WriteFile(filepath.Join(src.dir, "file.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, g, src.dir, "commit", "--quiet", "--no-gpg-sign", "-a", "-m", "two")
	second := fixtureGit(t, g, src.dir, "rev-parse", "HEAD")

	file := []string{"file"}
	fetch := func(refspecs ...string) {
		args := append([]string{"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", src.dir}, refspecs...)
		mirrorGit(t, g, m.Path(), file, args...)
	}
	fetch("+refs/heads/main:refs/heads/main", "+refs/heads/develop:refs/heads/develop", "+refs/tags/v1:refs/tags/v1")
	fetch("+refs/heads/lead:refs/leads/coder/heads/develop", "+refs/heads/lead:refs/leads/coder/heads/feature/x")
	fetch("+refs/heads/main:refs/leads/other/heads/develop")
	mirrorGit(t, g, m.Path(), nil, "update-ref", "refs/tags/t-commit", src.commit)
	mirrorGit(t, g, m.Path(), nil, "update-ref", "refs/tags/t-tree", src.tree)
	mirrorGit(t, g, m.Path(), nil, "update-ref", "refs/tags/t-blob", src.blob)
	return &fixture{g: g, m: m, first: src.commit, second: second, src: src}
}

func (f *fixture) updateRef(t *testing.T, ref, id string) {
	t.Helper()
	mirrorGit(t, f.g, f.m.Path(), nil, "update-ref", ref, id)
}

func names(refs []Ref) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out
}

func TestUpstream(t *testing.T) {
	f := newFixture(t)
	got, err := f.m.Upstream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{
		{"refs/heads/develop", f.first, "commit"},
		{"refs/heads/main", f.first, "commit"},
		{"refs/tags/t-blob", f.src.blob, "blob"},
		{"refs/tags/t-commit", f.first, "commit"},
		{"refs/tags/t-tree", f.src.tree, "tree"},
		{"refs/tags/v1", f.src.tag, "tag"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Upstream =\n%v\nwant\n%v", got, want)
	}
	// An empty mirror has no refs, and no error.
	_, empty := newMirror(t)
	if refs, err := empty.Upstream(context.Background()); err != nil || len(refs) != 0 {
		t.Errorf("Upstream of an empty mirror = %v, %v", refs, err)
	}
}

func TestLeadBranchesAndRoles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	got, err := f.m.LeadBranches(ctx, "coder")
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{
		{"refs/leads/coder/heads/develop", f.second, "commit"},
		{"refs/leads/coder/heads/feature/x", f.second, "commit"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LeadBranches(coder) = %v, want %v", got, want)
	}
	got, err = f.m.LeadBranches(ctx, "other")
	if err != nil || !reflect.DeepEqual(names(got), []string{"refs/leads/other/heads/develop"}) {
		t.Errorf("LeadBranches(other) = %v, %v", got, err)
	}
	// A role without refs, and a role that is a prefix of a real one.
	for _, role := range []string{"nobody", "code", "coder-2", "c"} {
		if got, err := f.m.LeadBranches(ctx, role); err != nil || len(got) != 0 {
			t.Errorf("LeadBranches(%q) = %v, %v; want none", role, got, err)
		}
	}
	for _, role := range []string{"", "Coder", "co/der", "..", "-x", "a b"} {
		if _, err := f.m.LeadBranches(ctx, role); !errors.Is(err, ErrBadName) {
			t.Errorf("LeadBranches(%q) err = %v, want ErrBadName", role, err)
		}
	}
	roles, err := f.m.Roles(ctx)
	if err != nil || !reflect.DeepEqual(roles, []string{"coder", "other"}) {
		t.Errorf("Roles = %v, %v", roles, err)
	}
	_, empty := newMirror(t)
	if roles, err := empty.Roles(ctx); err != nil || len(roles) != 0 {
		t.Errorf("Roles of an empty mirror = %v, %v", roles, err)
	}
	// Upstream refs are not lead refs, and the other way round.
	up, _ := f.m.Upstream(ctx)
	for _, r := range up {
		if strings.HasPrefix(r.Name, "refs/leads/") {
			t.Errorf("Upstream lists a lead ref: %s", r.Name)
		}
	}
}

func TestLeadAndUpstreamDevelopAreDistinct(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	up, err := f.m.Resolve(ctx, "refs/heads/develop")
	if err != nil {
		t.Fatal(err)
	}
	lead, err := f.m.Resolve(ctx, "refs/leads/coder/heads/develop")
	if err != nil {
		t.Fatal(err)
	}
	if up.ID != f.first || lead.ID != f.second || up.ID == lead.ID {
		t.Errorf("upstream develop %s, lead develop %s; want %s and %s", up.ID, lead.ID, f.first, f.second)
	}
	// The helpers build exactly these names.
	if ref, _ := UpstreamRef("develop"); ref != up.Name {
		t.Errorf("UpstreamRef = %q", ref)
	}
	if ref, _ := LeadRef("coder", "develop"); ref != lead.Name {
		t.Errorf("LeadRef = %q", ref)
	}
}

func TestResolve(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		ref, id, typ string
	}{
		{"refs/heads/main", f.first, "commit"},
		{"refs/heads/develop", f.first, "commit"},
		{"refs/tags/v1", f.src.tag, "tag"},
		{"refs/tags/t-tree", f.src.tree, "tree"},
		{"refs/tags/t-blob", f.src.blob, "blob"},
		{"refs/leads/coder/heads/feature/x", f.second, "commit"},
		{"refs/leads/other/heads/develop", f.first, "commit"},
	} {
		got, err := f.m.Resolve(ctx, tc.ref)
		if err != nil {
			t.Errorf("Resolve(%q): %v", tc.ref, err)
			continue
		}
		if got != (Ref{tc.ref, tc.id, tc.typ}) {
			t.Errorf("Resolve(%q) = %v", tc.ref, got)
		}
	}
	// Exact full names only: nothing here may find a ref.
	for _, ref := range []string{
		"develop", "main", "heads/develop", "refs/heads", "refs/heads/", "refs/heads/dev", "refs/heads/develo",
		"refs/heads/develop/", "refs/heads/develop/x", "refs/heads/feature", "refs/heads/feature/x", "refs/heads/nothing",
		"refs/tags/v", "refs/tags/v1/", "v1", "HEAD", "refs/leads", "refs/leads/coder", "refs/leads/coder/heads",
		"refs/leads/coder/heads/", "refs/leads/coder/heads/feature", "refs/leads/coder/develop", "refs/leads/code/heads/develop",
		"refs/leads/coder/heads/dev", "refs/leads/nobody/heads/develop", "refs/leads/Coder/heads/develop", "refs/remotes/origin/main",
		"refs/heads/*", "refs/heads/dev*", "refs/heads/d?velop", "refs/heads/[d]evelop", "refs/*/main", "refs/heads/develop\x00",
		"refs/heads/develop\n", "refs/heads/develop ", "refs/heads/../heads/develop", "refs//heads/develop", "/refs/heads/develop",
		" refs/heads/develop", "refs/heads/-develop", "--all", "-", "", "refs/heads/main@{0}", "main^", "refs/heads/main~1",
		"refs/heads/refs/heads/main", "refs/leads/coder/heads/../../../heads/develop",
	} {
		if got, err := f.m.Resolve(ctx, ref); !errors.Is(err, ErrNoRef) {
			t.Errorf("Resolve(%q) = %v, %v; want ErrNoRef", ref, got, err)
		}
	}
	// A cancelled context is an error, and not a missing ref.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.m.Resolve(cctx, "refs/heads/main"); err == nil || errors.Is(err, ErrNoRef) {
		t.Errorf("Resolve with a cancelled context: %v", err)
	}
}

func TestRefCap(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// 6 upstream refs and 3 lead refs in the fixture.
	for _, tc := range []struct {
		max     int
		upstr   bool
		lead    bool
		roles   bool
		comment string
	}{
		{max: 6, upstr: true, lead: true, roles: true, comment: "at the cap"},
		{max: 3, upstr: false, lead: true, roles: true, comment: "lead refs fit, upstream does not"},
		{max: 2, upstr: false, lead: true, roles: false, comment: "two lead refs fit, all three do not"},
		{max: 1, upstr: false, lead: false, roles: false, comment: "nothing fits"},
	} {
		m, err := f.m.WithLimits(Limits{MaxRefs: tc.max})
		if err != nil {
			t.Fatal(err)
		}
		check := func(what string, ok bool, err error) {
			t.Helper()
			switch {
			case ok && err != nil:
				t.Errorf("cap %d (%s): %s: %v", tc.max, tc.comment, what, err)
			case !ok && !errors.Is(err, ErrTooManyRefs):
				t.Errorf("cap %d (%s): %s: err = %v, want ErrTooManyRefs", tc.max, tc.comment, what, err)
			}
		}
		refs, err := m.Upstream(ctx)
		check("Upstream", tc.upstr, err)
		if err != nil && refs != nil {
			t.Errorf("a cut list came with the error: %v", refs)
		}
		refs, err = m.LeadBranches(ctx, "coder")
		check("LeadBranches(coder)", tc.lead, err)
		if err != nil && refs != nil {
			t.Errorf("a cut list came with the error: %v", refs)
		}
		roles, err := m.Roles(ctx)
		check("Roles", tc.roles, err)
		if err != nil && roles != nil {
			t.Errorf("a cut list came with the error: %v", roles)
		}
		// Resolve is a single ref and does not depend on the cap.
		if _, err := m.Resolve(ctx, "refs/heads/main"); err != nil {
			t.Errorf("cap %d: Resolve: %v", tc.max, err)
		}
	}
}

func TestListingHostileRefs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// A lead branch may hold odd text within the ref rules. It is returned
	// raw, in the lead's namespace only.
	odd := []string{"-dash", "--end-of-options", "é/ü", "a.b", "refs/heads/x", "HEAD", "@", "x.lock.y"}
	for _, b := range odd {
		f.updateRef(t, "refs/leads/coder/heads/"+b, f.second)
	}
	got, err := f.m.LeadBranches(ctx, "coder")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, r := range got {
		have[r.Name] = true
	}
	for _, b := range odd {
		if !have["refs/leads/coder/heads/"+b] {
			t.Errorf("lead branch %q is not listed", b)
		}
		if r, err := f.m.Resolve(ctx, "refs/leads/coder/heads/"+b); err != nil || r.ID != f.second {
			t.Errorf("Resolve of lead branch %q = %v, %v", b, r, err)
		}
	}
	// None of them is an upstream ref.
	up, _ := f.m.Upstream(ctx)
	if len(up) != 6 {
		t.Errorf("Upstream grew to %v", names(up))
	}
}

func TestListingRefusesRefsOffTheLayout(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat(strings.Repeat("a", 100)+"/", 3) + "a" // valid for git, over the bound of this package (git limits each element to 255 bytes)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *fixture)
		call  func(f *fixture) error
	}{
		{"lead branch name too long", func(t *testing.T, f *fixture) {
			f.updateRef(t, "refs/leads/coder/heads/"+long, f.second)
		}, func(f *fixture) error { _, err := f.m.LeadBranches(ctx, "coder"); return err }},
		{"upstream branch name too long", func(t *testing.T, f *fixture) {
			f.updateRef(t, "refs/heads/"+long, f.first)
		}, func(f *fixture) error { _, err := f.m.Upstream(ctx); return err }},
		{"upstream tag name too long", func(t *testing.T, f *fixture) {
			f.updateRef(t, "refs/tags/"+long, f.first)
		}, func(f *fixture) error { _, err := f.m.Upstream(ctx); return err }},
		{"symbolic ref in a lead namespace", func(t *testing.T, f *fixture) {
			mirrorGit(t, f.g, f.m.Path(), nil, "symbolic-ref", "refs/leads/coder/heads/sym", "refs/heads/main")
		}, func(f *fixture) error { _, err := f.m.LeadBranches(ctx, "coder"); return err }},
		{"symbolic ref upstream", func(t *testing.T, f *fixture) {
			mirrorGit(t, f.g, f.m.Path(), nil, "symbolic-ref", "refs/heads/sym", "refs/heads/main")
		}, func(f *fixture) error { _, err := f.m.Upstream(ctx); return err }},
		{"role with upper case", func(t *testing.T, f *fixture) {
			f.updateRef(t, "refs/leads/Coder/heads/x", f.second)
		}, func(f *fixture) error { _, err := f.m.Roles(ctx); return err }},
		{"a lead tag", func(t *testing.T, f *fixture) {
			f.updateRef(t, "refs/leads/coder/tags/v1", f.second)
		}, func(f *fixture) error { _, err := f.m.Roles(ctx); return err }},
		{"a ref below the role", func(t *testing.T, f *fixture) {
			f.updateRef(t, "refs/leads/coder/other", f.second)
		}, func(f *fixture) error { _, err := f.m.Roles(ctx); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if err := tc.call(f); err != nil {
				t.Fatalf("before the setup: %v", err)
			}
			tc.setup(t, f)
			err := tc.call(f)
			if !errors.Is(err, ErrBadRef) {
				t.Fatalf("err = %v, want ErrBadRef", err)
			}
			for _, s := range []string{"aaaa", "Coder", "sym"} {
				if strings.Contains(err.Error(), s) {
					t.Errorf("error text %q holds ref text", err)
				}
			}
		})
	}
}

func TestParseRefsRefusesBadOutput(t *testing.T) {
	id := strings.Repeat("a", 40)
	rec := func(name, symref string) string { return id + " commit\x00" + name + "\x00" + symref + "\x00\n" }
	any := func(string) bool { return true }
	if refs, err := parseRefs([]byte(rec("refs/heads/a", "")+rec("refs/heads/b", "")), any); err != nil || len(refs) != 2 {
		t.Fatalf("good output: %v, %v", refs, err)
	}
	for name, out := range map[string]string{
		"no final newline":     strings.TrimSuffix(rec("refs/heads/a", ""), "\n"),
		"short id":             "abc commit\x00refs/heads/a\x00\x00\n",
		"upper case id":        strings.Repeat("A", 40) + " commit\x00refs/heads/a\x00\x00\n",
		"unknown type":         id + " submodule\x00refs/heads/a\x00\x00\n",
		"no type":              id + "\x00refs/heads/a\x00\x00\n",
		"symref":               rec("refs/heads/a", "refs/heads/b"),
		"missing field":        id + " commit\x00refs/heads/a\x00\n",
		"newline in a name":    id + " commit\x00refs/heads/a\nb\x00\x00\n",
		"records run together": strings.TrimSuffix(rec("refs/heads/a", ""), "\n") + rec("refs/heads/b", ""),
	} {
		if refs, err := parseRefs([]byte(out), func(n string) bool { return !strings.Contains(n, "\n") }); !errors.Is(err, ErrBadRef) {
			t.Errorf("%s: got %v, %v; want ErrBadRef", name, refs, err)
		}
	}
	if _, err := parseRefs([]byte(rec("refs/heads/a", "")), func(string) bool { return false }); !errors.Is(err, ErrBadRef) {
		t.Errorf("a name the layout refuses was accepted: %v", err)
	}
}

// fakeGit writes an executable shell script and returns it as a Git.
func fakeGit(t *testing.T, body string) gitenv.Git {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	g, err := gitenv.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGitRunsAreBounded(t *testing.T) {
	_, real := newMirror(t)
	ctx := context.Background()

	slow := &Mirror{git: fakeGit(t, "exec sleep 30"), path: real.Path(), limits: Limits{Timeout: 200 * time.Millisecond}}
	start := time.Now()
	if _, err := slow.Upstream(ctx); !errors.Is(err, boundedexec.ErrTimeout) {
		t.Errorf("a slow git: err = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("the time limit did not stop git")
	}

	// With a ref cap of 2 the output may hold 3 records at most.
	loud := &Mirror{git: fakeGit(t, "exec yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), path: real.Path(), limits: Limits{MaxRefs: 2}}
	if _, err := loud.Upstream(ctx); !errors.Is(err, boundedexec.ErrOutputLimit) {
		t.Errorf("a loud git: err = %v, want ErrOutputLimit", err)
	}

	// A failing git reports its status, and none of its text.
	failing := &Mirror{git: fakeGit(t, "echo 'secret-ref-name' >&2; exit 3"), path: real.Path()}
	_, err := failing.Upstream(ctx)
	var ge *gitExit
	if !errors.As(err, &ge) || ge.exit != 3 {
		t.Fatalf("a failing git: err = %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the error holds the text of git: %v", err)
	}
}
