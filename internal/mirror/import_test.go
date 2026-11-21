package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestImportUpstreamCreatesTheBranch(t *testing.T) {
	g, m := newMirror(t)
	src := newSource(t, g)
	ctx := context.Background()
	if err := m.ImportUpstream(ctx, src.dir, "main"); err != nil {
		t.Fatal(err)
	}
	ref, err := m.Resolve(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != src.commit || ref.Type != "commit" {
		t.Fatalf("the branch is %+v, want the commit %s", ref, src.commit)
	}
	// Tags are not carried, and no lead namespace is touched.
	up, err := m.Upstream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range up {
		if r.Name != "refs/heads/main" {
			t.Errorf("an unexpected upstream ref %q", r.Name)
		}
	}
	roles, err := m.Roles(ctx)
	if err != nil || len(roles) != 0 {
		t.Fatalf("roles %v, %v", roles, err)
	}
}

// TestImportUpstreamRefusals holds that a bad source or branch is refused and
// leaves nothing in the mirror, and that a branch the source lacks fails.
func TestImportUpstreamRefusals(t *testing.T) {
	g, m := newMirror(t)
	src := newSource(t, g)
	ctx := context.Background()
	for name, c := range map[string]struct{ source, branch string }{
		"a branch the source lacks":  {src.dir, "develop"},
		"a bad branch name":          {src.dir, "bad..name"},
		"an empty branch":            {src.dir, ""},
		"a relative source":          {"src/repo", "main"},
		"an unclean source":          {src.dir + "/../" + filepath.Base(src.dir), "main"},
		"an empty source":            {"", "main"},
		"the root as a source":       {"/", "main"},
		"a source that is not there": {filepath.Join(src.dir, "absent"), "main"},
		"a file as the source":       {filepath.Join(src.dir, "file.txt"), "main"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := m.ImportUpstream(ctx, c.source, c.branch); err == nil {
				t.Fatal("the import succeeded")
			}
			up, err := m.Upstream(ctx)
			if err != nil || len(up) != 0 {
				t.Fatalf("the mirror holds %v after a refused import (%v)", up, err)
			}
		})
	}
}

func TestImportUpstreamNeverRewinds(t *testing.T) {
	g, m := newMirror(t)
	src := newSource(t, g)
	ctx := context.Background()
	if err := m.ImportUpstream(ctx, src.dir, "main"); err != nil {
		t.Fatal(err)
	}
	// A second import of the same tip changes nothing.
	if err := m.ImportUpstream(ctx, src.dir, "main"); err != nil {
		t.Fatalf("the same tip again: %v", err)
	}
	// The source moves forward: the mirror follows.
	if err := os.WriteFile(filepath.Join(src.dir, "file.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, g, src.dir, "commit", "--quiet", "--no-gpg-sign", "-a", "-m", "two")
	second := fixtureGit(t, g, src.dir, "rev-parse", "HEAD")
	if err := m.ImportUpstream(ctx, src.dir, "main"); err != nil {
		t.Fatalf("a fast-forward: %v", err)
	}
	if ref, _ := m.Resolve(ctx, "refs/heads/main"); ref.ID != second {
		t.Fatalf("the mirror is at %s, want %s", ref.ID, second)
	}
	// The source is rewound to a different history: the mirror refuses to follow.
	fixtureGit(t, g, src.dir, "reset", "--quiet", "--hard", src.commit)
	if err := os.WriteFile(filepath.Join(src.dir, "other.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, g, src.dir, "add", "other.txt")
	fixtureGit(t, g, src.dir, "commit", "--quiet", "--no-gpg-sign", "-m", "diverged")
	err := m.ImportUpstream(ctx, src.dir, "main")
	if !errors.Is(err, ErrImport) {
		t.Fatalf("a diverged branch: %v", err)
	}
	if ref, _ := m.Resolve(ctx, "refs/heads/main"); ref.ID != second {
		t.Fatalf("the mirror moved to %s after a refused import", ref.ID)
	}
}

// TestImportUpstreamTakesOnlyTheNamedBranch holds that only the named branch
// arrives.
func TestImportUpstreamTakesOnlyTheNamedBranch(t *testing.T) {
	g, m := newMirror(t)
	src := newSource(t, g)
	fixtureGit(t, g, src.dir, "branch", "side")
	ctx := context.Background()
	if err := m.ImportUpstream(ctx, src.dir, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(ctx, "refs/heads/side"); !errors.Is(err, ErrNoRef) {
		t.Errorf("the other branch arrived: %v", err)
	}
	if _, err := m.Resolve(ctx, "refs/tags/v1"); !errors.Is(err, ErrNoRef) {
		t.Errorf("a tag arrived: %v", err)
	}
}
