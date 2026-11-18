package leadmirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"ataniq/internal/mirror"
)

// plantedLead is the lead's work in the planted case: one small branch.
func (r *runner) plantedLead(ctx context.Context, dir string) error {
	f := r.fx
	if _, err := f.run(ctx, dir, "checkout", "--quiet", "-b", "work"); err != nil {
		return err
	}
	if err := f.commit(ctx, dir, "lead: some work", file{"work.txt", "work\n"}); err != nil {
		return err
	}
	_, err := f.run(ctx, dir, "checkout", "--quiet", upstreamBranch)
	return err
}

// plantedCase makes the clones of the planted case. The plants are made in a
// clone no sandbox has served, and only then is that clone put in place of the
// lead's clone. A clone that a sandbox has served is moved away whole and never
// written to by path: a sandbox that could write it may have left a symlink in
// it, and a write by path would follow that.
type plantedCase struct {
	r    *runner
	c    *caseEnv
	root string
	n    int
}

// fresh makes a new clone at name below the fixture folder, with the lead's
// work, and returns its path. Nothing has served it.
func (p *plantedCase) fresh(ctx context.Context, name string) (string, error) {
	dir := filepath.Join(p.root, "fixture", name)
	if err := p.r.fx.buildLead(ctx, p.c.upstream, dir); err != nil {
		return "", err
	}
	if err := p.r.plantedLead(ctx, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// next returns a name for a new folder in the fixture folder.
func (p *plantedCase) next(prefix string) string {
	p.n++
	return fmt.Sprintf("%s-%d", prefix, p.n)
}

// retire moves what is at the lead's clone path aside, whole, to the fixture
// folder. It writes nothing into it.
func (p *plantedCase) retire() error {
	return os.Rename(p.c.tree, filepath.Join(p.root, "fixture", p.next("retired")))
}

// install puts dir where the lead's clone is. What was there is moved aside.
func (p *plantedCase) install(dir string) error {
	if err := p.retire(); err != nil {
		return err
	}
	return os.Rename(dir, p.c.tree)
}

// casePlanted looks at what the owner-side check of the clone refuses before
// any sandbox is asked: an alternates file, a ".git" that is a symlink, and a
// clone folder that is a symlink. Each plant is made in a clone of its own
// before anything serves it; the first clone, which the control served, is
// moved away.
func (r *runner) casePlanted(ctx context.Context) error {
	cs := r.add("planted", "A lead's clone with an alternates file, with a .git that is a symlink, and with a clone folder that is a symlink.")
	root := filepath.Join(r.opts.StateRoot, "cases", "planted")
	c, err := r.newCase(ctx, root, r.plantedLead, false)
	if err != nil {
		return cs.harnessError(ctx, err)
	}
	p := &plantedCase{r: r, c: c, root: root}
	moved := filepath.Join(root, "fixture", "moved")
	if err := os.Mkdir(moved, 0o700); err != nil {
		return cs.harnessError(ctx, err)
	}

	attempt := func(id, detail string) error {
		before, err := readState(ctx, c.m, r.opts.Role, maxEntries)
		if err != nil {
			return err
		}
		rep, serves, ferr := r.fetch(ctx, c, mirror.FetchLimits{})
		after, err := readState(ctx, c.m, r.opts.Role, maxEntries)
		if err != nil {
			return err
		}
		cs.fact(id, detail, merge(fetchFacts(rep, ferr), map[string]any{
			"serve_requests":          serves,
			"lead_refs_before":        before.Lead,
			"lead_refs_after":         after.Lead,
			"upstream_refs_identical": sameRefs(before.Upstream, after.Upstream),
		}))
		return nil
	}

	if err := attempt("control-before", "A fetch of the clone as it was made, before anything is planted. It shows that the clone is fetchable."); err != nil {
		return cs.harnessError(ctx, err)
	}

	// An alternates file in the clone. The clone is new and unserved.
	dir, err := p.fresh(ctx, p.next("lead"))
	if err != nil {
		return cs.harnessError(ctx, err)
	}
	alt := filepath.Join(dir, ".git", "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alt), 0o700); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := os.WriteFile(alt, []byte(filepath.Join(root, "fixture", "no-such-objects")+"\n"), 0o600); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := p.install(dir); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := attempt("alternates", "A fetch with .git/objects/info/alternates present in the clone."); err != nil {
		return cs.harnessError(ctx, err)
	}

	// .git replaced by a symlink to the real folder, in a new unserved clone.
	if dir, err = p.fresh(ctx, p.next("lead")); err != nil {
		return cs.harnessError(ctx, err)
	}
	dotGit := filepath.Join(dir, ".git")
	if err := os.Rename(dotGit, filepath.Join(moved, "dotgit")); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := os.Symlink(filepath.Join(moved, "dotgit"), dotGit); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := p.install(dir); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := attempt("git-symlink", "A fetch with .git of the clone replaced by a symlink to the real folder."); err != nil {
		return cs.harnessError(ctx, err)
	}

	// The clone's own folder replaced by a symlink: the real clone is new, in
	// the moved folder, and the link is made where the clone is. What was
	// there is moved aside whole.
	if _, err := p.fresh(ctx, filepath.Join("moved", "tree")); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := p.retire(); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := os.Symlink(filepath.Join(moved, "tree"), c.tree); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := attempt("tree-symlink", "A fetch with the clone's folder (leads/<role>/tree) replaced by a symlink to the real folder."); err != nil {
		return cs.harnessError(ctx, err)
	}

	// The control after: a new clone as it is made, put in place of the link.
	if dir, err = p.fresh(ctx, p.next("lead")); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := p.install(dir); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := attempt("control-after", "A fetch of a new clone, made as the first one was, after the plants. It shows that the refusals above came from the plants."); err != nil {
		return cs.harnessError(ctx, err)
	}
	return ctx.Err()
}
