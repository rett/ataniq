//go:build linux

package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ataniq/internal/intake/verify"
)

// rig is a scene with an owner's records store.
type rig struct {
	*scene
	store *Store
	dir   string
}

func newRig(t *testing.T, s *scene) *rig {
	t.Helper()
	st, dir := newTestStore(t)
	return &rig{scene: s, store: st, dir: dir}
}

var testNow = time.Date(2026, 11, 17, 12, 30, 0, 0, time.UTC)

// record stores a verification result for the tip of the lead branch.
func (g *rig) record(mod func(*verify.Result)) {
	g.t.Helper()
	g.recordFor(g.tip, mod)
}

func (g *rig) recordFor(tip string, mod func(*verify.Result)) {
	g.t.Helper()
	res := passResult(tip)
	if mod != nil {
		mod(&res)
	}
	project := testProject
	if _, err := g.store.PutVerification(project, res, testGates); err != nil {
		g.t.Fatal(err)
	}
}

// req is the request for the review as the owner saw it, fully signed.
func (g *rig) req(r *Review) IntegrateRequest {
	return IntegrateRequest{
		Project: testProject, Review: g.request(), SignOff: okSignOff(r),
		Store: g.store, Now: func() time.Time { return testNow },
	}
}

// refs lists every ref with its id, one per line.
func (g *rig) refs() string { return g.b("for-each-ref", "--format=%(refname) %(objectname)") }

// refsExcept lists the refs but the named one.
func (g *rig) refsExcept(name string) string {
	var keep []string
	for _, l := range strings.Split(g.refs(), "\n") {
		if !strings.HasPrefix(l, name+" ") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func (g *rig) id(ref string) string { return g.b("rev-parse", "--verify", ref) }

func (g *rig) intakes() []IntakeRecord {
	g.t.Helper()
	recs, err := g.store.Intakes()
	if err != nil {
		g.t.Fatal(err)
	}
	return recs
}

// refuse runs Integrate and requires that it fails and that nothing changed:
// the same refs, no intake record, and no work tree. It returns the error.
func (g *rig) refuse(name string, req IntegrateRequest) error {
	g.t.Helper()
	before := g.refs()
	rec, err := Integrate(context.Background(), g.source(), req)
	if err == nil {
		g.t.Errorf("%s: integration accepted: %+v", name, rec)
		return nil
	}
	if after := g.refs(); after != before {
		g.t.Errorf("%s: refs changed:\nbefore:\n%s\nafter:\n%s", name, before, after)
	}
	if n := len(g.intakes()); n != 0 {
		g.t.Errorf("%s: %d intake records", name, n)
	}
	assertNoWorkTree(g.t, g.scene)
	return err
}

// assertNoWorkTree fails when the bare repository has an index, a work-tree
// file, a log of refs or anything else a checkout or a merge leaves.
func assertNoWorkTree(t *testing.T, s *scene) {
	t.Helper()
	for _, name := range []string{"index", "index.lock", "FETCH_HEAD", "ORIG_HEAD", "MERGE_HEAD", "logs", "worktrees"} {
		if _, err := os.Lstat(filepath.Join(s.bare, name)); err == nil {
			t.Errorf("%s appeared in the repository", name)
		}
	}
	entries, err := os.ReadDir(s.bare)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "HEAD", "config", "description", "hooks", "info", "objects", "refs", "branches", "packed-refs":
		default:
			t.Errorf("unexpected entry %q in the repository", e.Name())
		}
	}
}

// TestIntegrateFullPath: review, verification record, sign-off, integration.
// The review has a flag of every kind and all are signed.
func TestIntegrateFullPath(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	r := s.inspect()
	g.record(nil)
	req := g.req(r)
	req.SignOff.Paths = append(slices.Clone(req.SignOff.Paths), req.SignOff.Paths[0]) // a repeated path is no error
	others := g.refsExcept(upstreamRef)
	leadBefore := g.id(leadRef)

	rec, err := Integrate(context.Background(), s.source(), req)
	if err != nil {
		t.Fatalf("Integrate: %v", err)
	}
	if got := g.id(upstreamRef); got != s.tip {
		t.Errorf("the target is at %s, want the tip %s", got, s.tip)
	}
	if got := g.id(leadRef); got != leadBefore {
		t.Errorf("the lead ref moved: %s", got)
	}
	if g.refsExcept(upstreamRef) != others {
		t.Errorf("another ref changed:\n%s\n--\n%s", others, g.refsExcept(upstreamRef))
	}
	assertNoWorkTree(t, s)
	if rec.Base != s.base || rec.Tip != s.tip || rec.TargetRef != upstreamRef || rec.TipRef != leadRef ||
		rec.Project != testProject || rec.ReviewDigest != r.Digest() || rec.Commits != len(r.Commits) ||
		rec.Paths != len(r.Changes) || !rec.Time.Equal(testNow) || rec.Version != 1 {
		t.Errorf("record: %+v", rec)
	}
	if !slices.IsSorted(rec.SignedPaths) || len(rec.SignedPaths) != len(okSignOff(r).Paths) {
		t.Errorf("signed paths: %q", rec.SignedPaths)
	}
	if !rec.Verification.passing() || rec.Verification.Tip != s.tip {
		t.Errorf("verification: %+v", rec.Verification)
	}
	stored := g.intakes()
	if len(stored) != 1 {
		t.Fatalf("%d intake records", len(stored))
	}
	got := stored[0]
	if got.Base != rec.Base || got.Tip != rec.Tip || got.ReviewDigest != rec.ReviewDigest || !slices.Equal(got.SignedPaths, rec.SignedPaths) ||
		!got.Time.Equal(rec.Time) || got.Verification.OutputSHA256 != rec.Verification.OutputSHA256 {
		t.Errorf("stored record:\n got %+v\nwant %+v", got, rec)
	}

	// A second integration of the same sign-off finds the branch at the tip.
	if _, err := Integrate(context.Background(), s.source(), req); err == nil {
		t.Error("the same integration twice")
	}
	if len(g.intakes()) != 1 {
		t.Error("a second record")
	}
}

// TestIntegrateNeedsEverySignOff (design case S19): flagged paths each need
// their sign-off, and so does the change as a whole.
func TestIntegrateNeedsEverySignOff(t *testing.T) {
	s := newScene(t)
	s.put(".claude/settings.json", `{"hooks":1}`+"\n")
	s.put(".githooks/pre-commit", "#!/bin/sh\n")
	s.put("Makefile", "all:\n")
	s.put(".mcp.json", "{}\n")
	s.put("src/ok.go", "package main\n")
	s.commit("work with execution config\n")
	s.finish()
	g := newRig(t, s)
	r := s.inspect()
	g.record(nil)
	if len(r.Flagged) < 4 {
		t.Fatalf("Flagged: %+v", r.Flagged)
	}
	full := g.req(r)

	// No path signed.
	req := full
	req.SignOff.Paths = nil
	var se *SignOffError
	if err := g.refuse("no paths", req); !errors.As(err, &se) || len(se.Missing) != len(r.Flagged) {
		t.Errorf("no paths: %v", err)
	}
	// All but one: the error names the missing one.
	for _, f := range r.Flagged {
		req = full
		req.SignOff.Paths = slices.DeleteFunc(slices.Clone(full.SignOff.Paths), func(p string) bool { return p == f.Path })
		err := g.refuse("without "+f.Path, req)
		if !errors.As(err, &se) || len(se.Missing) != 1 || se.Missing[0].Path != f.Path || len(se.Unknown) != 0 {
			t.Errorf("without %s: %v", f.Path, err)
		} else if !strings.Contains(err.Error(), f.Path) {
			t.Errorf("the message lacks the path %s: %v", f.Path, err)
		}
	}
	// An unknown extra path.
	req = full
	req.SignOff.Paths = append(slices.Clone(full.SignOff.Paths), "src/ok.go")
	if err := g.refuse("unknown path", req); !errors.As(err, &se) || !slices.Equal(se.Unknown, []string{"src/ok.go"}) {
		t.Errorf("unknown path: %v", err)
	}
	// The change is not accepted.
	req = full
	req.SignOff.Change = false
	if err := g.refuse("change", req); !errors.As(err, &se) || !se.ChangeMissing {
		t.Errorf("change not accepted: %v", err)
	}
	// A sign-off of the right paths for another tip or another review is a
	// moved branch.
	req = full
	req.SignOff.Tip = s.base
	if err := g.refuse("other tip", req); !errors.Is(err, ErrMoved) {
		t.Errorf("other tip: %v", err)
	}
	req = full
	req.SignOff.Digest = strings.Repeat("0", 64)
	if err := g.refuse("other digest", req); !errors.Is(err, ErrMoved) {
		t.Errorf("other digest: %v", err)
	}
	// The full sign-off goes through.
	if _, err := Integrate(context.Background(), s.source(), full); err != nil {
		t.Fatalf("the full sign-off: %v", err)
	}
	if g.id(upstreamRef) != s.tip {
		t.Error("the target did not move")
	}
}

// TestIntegrateEachKindNeedsItsSignOff: history-only, symlink, submodule and
// aliased flags need their sign-off as well.
func TestIntegrateEachKindNeedsItsSignOff(t *testing.T) {
	for _, tc := range []struct {
		kind  FlagKind
		path  string
		build func() *scene
	}{
		{FlagHistory, ".envrc", func() *scene {
			s := newScene(t)
			s.put(".envrc", "x\n")
			s.put("src/a.go", "package main\n")
			s.commit("add\n")
			s.remove(".envrc")
			s.commit("remove\n")
			s.finish()
			return s
		}},
		{FlagSymlink, "docs/link", func() *scene {
			s := newScene(t)
			s.symlink("docs/link", "../README.md")
			s.commit("a link\n")
			s.finish()
			return s
		}},
		{FlagSubmodule, "vendor/lib", func() *scene {
			s := newScene(t)
			s.gitlink("vendor/lib")
			s.commit("a submodule\n")
			s.finish()
			return s
		}},
		{FlagAliased, "cfg/settings.json", func() *scene {
			s := aliasScene(t)
			s.symlink(".claude", "cfg")
			s.commit("link in upstream\n")
			s.publish(upstreamRef, s.cur)
			s.put("cfg/settings.json", `{"hooks":1}`+"\n")
			s.commit("change\n")
			s.finish()
			return s
		}},
		{FlagExecConfig, "Makefile", func() *scene {
			s := newScene(t)
			s.put("Makefile", "all:\n")
			s.commit("a makefile\n")
			s.finish()
			return s
		}},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			s := tc.build()
			g := newRig(t, s)
			r := s.inspect()
			flagFor(t, r.Flagged, tc.kind, tc.path)
			g.record(nil)
			full := g.req(r)
			req := full
			req.SignOff.Paths = slices.DeleteFunc(slices.Clone(full.SignOff.Paths), func(p string) bool { return p == tc.path })
			var se *SignOffError
			err := g.refuse("without "+tc.path, req)
			if !errors.As(err, &se) || len(se.Missing) == 0 || se.Missing[0].Path != tc.path {
				t.Fatalf("error = %v", err)
			}
			var kinds []FlagKind
			for _, f := range se.Missing {
				kinds = append(kinds, f.Kind)
			}
			if !slices.Contains(kinds, tc.kind) {
				t.Errorf("missing kinds %v lack %s", kinds, tc.kind)
			}
			if _, err := Integrate(context.Background(), s.source(), full); err != nil {
				t.Errorf("with the sign-off: %v", err)
			}
		})
	}
}

// TestIntegrateBranchMovedAfterTheSignOff: the lead branch gets a new commit,
// or is rewritten. Nothing moves.
func TestIntegrateBranchMovedAfterTheSignOff(t *testing.T) {
	build := func() (*rig, IntegrateRequest) {
		s := flaggedScene(t)
		g := newRig(t, s)
		g.record(nil)
		return g, g.req(s.inspect())
	}
	t.Run("new commit", func(t *testing.T) {
		g, req := build()
		old := g.tip
		g.checkout(old)
		g.put("src/more.go", "package main\n")
		next := g.commit("one more commit\n")
		g.publish(leadRef, next)
		g.recordFor(next, nil) // verified, but not signed
		err := g.refuse("new commit", req)
		if !errors.Is(err, ErrMoved) || !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), next) {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("rewritten", func(t *testing.T) {
		g, req := build()
		old := g.tip
		g.checkout(g.base)
		g.put("src/other.go", "package main\n")
		next := g.commit("a different history\n")
		g.publish(leadRef, next)
		g.recordFor(next, nil)
		err := g.refuse("rewritten", req)
		if !errors.Is(err, ErrMoved) || !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), next) {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("same tree, new commit", func(t *testing.T) {
		g, req := build()
		g.checkout(g.tip)
		next := g.commit("the same tree again\n")
		g.publish(leadRef, next)
		g.recordFor(next, nil)
		if err := g.refuse("same tree", req); !errors.Is(err, ErrMoved) {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("lead branch removed", func(t *testing.T) {
		g, req := build()
		g.b("update-ref", "-d", leadRef)
		if err := g.refuse("removed", req); !errors.Is(err, ErrNoRef) {
			t.Errorf("error = %v", err)
		}
	})
}

// TestIntegrateTargetMovedAfterTheSignOff: the owner pushed to the target
// after looking. The compare-and-swap itself is tested with the hook.
func TestIntegrateTargetMovedAfterTheSignOff(t *testing.T) {
	build := func() (*rig, IntegrateRequest, string) {
		s := flaggedScene(t)
		g := newRig(t, s)
		g.record(nil)
		first := g.b("rev-parse", "--verify", s.tip+"~1")
		return g, g.req(s.inspect()), first
	}
	t.Run("moved to an ancestor of the tip", func(t *testing.T) {
		g, req, first := build()
		g.publish(upstreamRef, first)
		err := g.refuse("ancestor", req)
		if !errors.Is(err, ErrMoved) {
			t.Errorf("error = %v", err)
		}
		if g.id(upstreamRef) != first {
			t.Error("the target is not where the owner put it")
		}
	})
	t.Run("the owner pushed a commit of its own", func(t *testing.T) {
		g, req, _ := build()
		g.checkout(g.base)
		g.put("docs/owner.md", "owner\n")
		own := g.commit("owner's own commit\n")
		g.publish(upstreamRef, own)
		var nf *NotFastForwardError
		err := g.refuse("owner push", req)
		if !errors.Is(err, ErrMoved) || !errors.As(err, &nf) || nf.Base != own {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("target deleted", func(t *testing.T) {
		g, req, _ := build()
		g.b("update-ref", "-d", upstreamRef)
		if err := g.refuse("deleted", req); !errors.Is(err, ErrNoRef) {
			t.Errorf("error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(g.bare, "refs", "heads", "main")); err == nil {
			t.Error("the target was created")
		}
	})
}

// TestIntegrateCompareAndSwap: the target moves between the fresh inspection
// and the update. The update fails, and nothing else changes.
func TestIntegrateCompareAndSwap(t *testing.T) {
	for name, move := range map[string]func(g *rig, first string){
		"to an ancestor of the tip": func(g *rig, first string) { g.publish(upstreamRef, first) },
		"to the tip itself":         func(g *rig, first string) { g.publish(upstreamRef, g.tip) },
		"deleted":                   func(g *rig, first string) { g.b("update-ref", "-d", upstreamRef) },
		"to a commit of the owner": func(g *rig, first string) {
			g.checkout(g.base)
			g.put("docs/owner.md", "owner\n")
			g.publish(upstreamRef, g.commit("owner\n"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := flaggedScene(t)
			g := newRig(t, s)
			g.record(nil)
			first := g.b("rev-parse", "--verify", s.tip+"~1")
			req := g.req(s.inspect())
			req.afterChecks = func() { move(g, first) }
			others := g.refsExcept(upstreamRef)
			target := g.b("for-each-ref", "--format=%(refname) %(objectname)", upstreamRef)
			_, err := Integrate(context.Background(), s.source(), req)
			if !errors.Is(err, ErrMoved) {
				t.Fatalf("error = %v", err)
			}
			if name != "deleted" && !strings.Contains(err.Error(), s.base) {
				t.Errorf("the error does not name the old id: %v", err)
			}
			now := g.b("for-each-ref", "--format=%(refname) %(objectname)", upstreamRef)
			if now == target && name != "to the tip itself" {
				t.Error("the hook did not move the target")
			}
			if g.refsExcept(upstreamRef) != others {
				t.Error("another ref changed")
			}
			if g.id(leadRef) != s.tip {
				t.Error("the lead ref changed")
			}
			if len(g.intakes()) != 0 {
				t.Error("an intake record was written")
			}
			assertNoWorkTree(t, s)
			if strings.Contains(now, s.base) {
				t.Errorf("the target is back at the base: %s", now)
			}
			if name != "deleted" && name != "to the tip itself" && strings.Contains(now, s.tip) {
				t.Errorf("the target was moved to the tip over the owner's move: %s", now)
			}
		})
	}
}

func TestIntegrateNeedsAPassingVerification(t *testing.T) {
	cut := func(r *verify.Result) { r.OutputCut = true }
	fail := func(r *verify.Result) { r.Passed, r.Exit = false, 1 }
	for name, tc := range map[string]struct {
		put  func(g *rig)
		pass bool
	}{
		"no record": {put: func(g *rig) {}},
		"failed":    {put: func(g *rig) { g.record(fail) }},
		"timeout": {put: func(g *rig) {
			g.record(func(r *verify.Result) { r.Passed, r.Exit, r.Stopped = false, -1, verify.StoppedTimeout })
		}},
		"output limit": {put: func(g *rig) {
			g.record(func(r *verify.Result) { r.Passed, r.Stopped, r.OutputCut = false, verify.StoppedOutputLimit, true })
		}},
		"another tip":   {put: func(g *rig) { g.recordFor(g.base, nil) }},
		"a lead commit": {put: func(g *rig) { g.recordFor(g.b("rev-parse", "--verify", g.tip+"~1"), nil) }},
		"pass then failure": {put: func(g *rig) {
			g.record(nil)
			g.record(fail)
		}},
		"pass with the output cut": {put: func(g *rig) { g.record(cut) }},
		"pass that says it was stopped": {put: func(g *rig) {
			g.record(func(r *verify.Result) { r.Stopped = verify.StoppedError })
		}},
		"pass with a bad exit": {put: func(g *rig) { g.record(func(r *verify.Result) { r.Exit = 3 }) }},
		"pass of another project": {put: func(g *rig) {
			if _, err := g.store.PutVerification("other", passResult(g.tip), testGates); err != nil {
				g.t.Fatal(err)
			}
		}},
		"cut pass after a clean pass": {put: func(g *rig) {
			g.record(nil)
			g.record(cut)
		}},
		"failure then pass": {put: func(g *rig) {
			g.record(fail)
			g.record(nil)
		}, pass: true},
		"failure for another tip after a pass": {put: func(g *rig) {
			g.record(nil)
			g.recordFor(g.base, fail)
		}, pass: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := flaggedScene(t)
			g := newRig(t, s)
			tc.put(g)
			req := g.req(s.inspect())
			if tc.pass {
				if _, err := Integrate(context.Background(), s.source(), req); err != nil {
					t.Fatalf("error = %v", err)
				}
				if g.id(upstreamRef) != s.tip {
					t.Error("the target did not move")
				}
				return
			}
			if err := g.refuse(name, req); !errors.Is(err, ErrNotVerified) {
				t.Errorf("error = %v", err)
			}
		})
	}
}

// TestIntegrateUnreadableRecordStops: a record that cannot be read could be a
// failure that withdraws the pass, so the integration stops.
func TestIntegrateUnreadableRecordStops(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	if err := os.WriteFile(filepath.Join(g.dir, "20301231T000000.000000000Z-verification-0123456789abcdef.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.refuse("broken record", g.req(s.inspect())); !errors.Is(err, ErrStore) || errors.Is(err, ErrNotVerified) {
		t.Errorf("error = %v", err)
	}
}

func TestIntegrateRefusesWhatIsNotFastForward(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	req := g.req(s.inspect())
	// The upstream branch moves on and the lead branch does not contain it.
	g.checkout(g.base)
	g.put("docs/up.md", "up\n")
	up := g.commit("upstream moves on\n")
	g.publish(upstreamRef, up)
	var nf *NotFastForwardError
	if err := g.refuse("diverged", req); !errors.As(err, &nf) {
		t.Errorf("error = %v", err)
	}
	if g.id(upstreamRef) != up {
		t.Error("the target moved")
	}
	// A lead branch that is behind the target.
	g.publish(upstreamRef, s.tip)
	g.publish(leadRef, s.base)
	req.SignOff = SignOff{Tip: s.base, Change: true}
	if err := g.refuse("behind", req); !errors.As(err, &nf) {
		t.Errorf("behind: %v", err)
	}
	// A lead branch at the target: nothing to integrate.
	g.publish(leadRef, s.tip)
	if err := g.refuse("at the target", req); err == nil {
		t.Error("an empty integration was accepted")
	}
}

func TestIntegrateTargetMustBeAnExistingBranch(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	g.b("update-ref", "refs/tags/v1", s.base)
	g.b("update-ref", "refs/remotes/origin/main", s.base)
	g.b("symbolic-ref", "refs/heads/alias", upstreamRef)
	for name, tc := range map[string]struct {
		ref  string
		want error
	}{
		"missing branch":        {"refs/heads/nope", ErrNoRef},
		"missing nested branch": {"refs/heads/topic/nope", ErrNoRef},
		"a tag":                 {"refs/tags/v1", ErrRefName},
		"HEAD":                  {"HEAD", ErrRefName},
		"a lead ref":            {"refs/leads/role/heads/other", ErrRefName},
		"the lead's own ref":    {leadRef, ErrRefName},
		"a remote ref":          {"refs/remotes/origin/main", ErrRefName},
		"short name":            {"main", ErrRefName},
		"empty":                 {"", ErrRefName},
		"the folder":            {"refs/heads/", ErrRefName},
		"a dotted name":         {"refs/heads/../tags/v1", ErrRefName},
		"a symbolic ref":        {"refs/heads/alias", ErrNotCommit},
	} {
		r := s.inspect()
		req := g.req(r)
		req.Review.BaseRef = tc.ref
		if err := g.refuse(name, req); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", name, err, tc.want)
		}
	}
	for _, l := range strings.Split(g.refs(), "\n") {
		if strings.HasPrefix(l, "refs/heads/nope") || strings.HasPrefix(l, "refs/heads/topic") {
			t.Errorf("a branch was created: %s", l)
		}
	}
}

func TestIntegrateRequestChecks(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	r := s.inspect()
	req := g.req(r)
	req.Store = nil
	if err := g.refuse("no store", req); err == nil {
		t.Error("no store")
	}
	req = g.req(r)
	req.Project = "Not Valid"
	if err := g.refuse("project", req); err == nil {
		t.Error("bad project")
	}
	req = g.req(r)
	req.Review.Limits.MaxPaths = -1
	if err := g.refuse("limits", req); err == nil {
		t.Error("negative limit")
	}
	// A zero Now means the clock.
	req = g.req(r)
	req.Now = nil
	before := time.Now().Add(-time.Second)
	rec, err := Integrate(context.Background(), s.source(), req)
	if err != nil || rec.Time.Before(before) || time.Since(rec.Time) > time.Minute {
		t.Errorf("record time %v, error %v", rec.Time, err)
	}
}

// TestIntegrateRecordCannotBeWritten: the branch moved and the record is
// missing. The error says so and carries the record.
func TestIntegrateRecordCannotBeWritten(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	r := s.inspect()
	req := g.req(r)
	// Close the folder handle after the verification records were read.
	req.afterChecks = func() { g.store.dir.close() }
	rec, err := Integrate(context.Background(), s.source(), req)
	var re *RecordError
	if !errors.As(err, &re) || !errors.Is(err, ErrRecordMissing) || !errors.Is(err, ErrStore) {
		t.Fatalf("error = %v", err)
	}
	for _, want := range []string{"moved", "record is missing", s.base, s.tip, upstreamRef} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	if re.Record.Tip != s.tip || re.Record.Base != s.base || re.Record.ReviewDigest != r.Digest() || rec.Tip != s.tip ||
		len(re.Record.SignedPaths) == 0 || !re.Record.Verification.passing() {
		t.Errorf("record: %+v", re.Record)
	}
	if g.id(upstreamRef) != s.tip {
		t.Error("the target did not move")
	}
}

// TestIntegrateGitCommands: the one write is an update-ref transaction on
// standard input, and the package starts no checkout, merge, pull, reset,
// worktree or push.
func TestIntegrateGitCommands(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	rec, logPath := recordingGit(t, s.git)
	src := Source{Git: rec, GitDir: s.bare}
	if _, err := Integrate(context.Background(), src, g.req(s.inspect())); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	reads := []string{"for-each-ref", "rev-parse", "merge-base", "rev-list", "log", "diff-tree", "ls-tree", "cat-file"}
	updates := 0
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		args := strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f")
		i := 0
		for i < len(args) && args[i] == "-c" {
			i += 2
		}
		args = args[i+1:] // after --git-dir
		switch sub := args[0]; {
		case sub == "update-ref":
			updates++
			if !slices.Equal(args, []string{"update-ref", "--stdin", "-z", "--no-deref", "--end-of-options"}) {
				t.Errorf("update-ref line: %q", args)
			}
		case slices.Contains(reads, sub):
		default:
			t.Errorf("git %s was run", sub)
		}
		afterEnd := false
		for _, a := range args[1:] {
			switch {
			case afterEnd:
			case a == "--end-of-options" || a == "--":
				afterEnd = true
			case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			case valuelessFlags[a]:
			default:
				t.Errorf("git %s: option %q is neither --name=value nor an allowed flag", args[0], a)
			}
		}
	}
	if updates != 1 {
		t.Errorf("%d update-ref runs", updates)
	}
}

// TestPackageStartsNoWorkTreeCommand: no source file of the package names a
// git subcommand that touches a work tree, an index or a remote.
func TestPackageStartsNoWorkTreeCommand(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, sub := range []string{"checkout", "merge", "pull", "reset", "worktree", "push", "fetch", "clone", "read-tree", "restore", "switch", "stash", "rebase", "cherry-pick", "apply", "add", "rm", "gc", "init"} {
			if strings.Contains(string(data), `"`+sub+`"`) {
				t.Errorf("%s names the git subcommand %q", f, sub)
			}
		}
	}
}
