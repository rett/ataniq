package leadmirror

import (
	"context"
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

// casePlanted looks at what the owner-side check of the clone refuses before
// any sandbox is asked: an alternates file, a ".git" that is a symlink, and a
// clone folder that is a symlink.
func (r *runner) casePlanted(ctx context.Context) error {
	cs := r.add("planted", "A lead's clone with an alternates file, with a .git that is a symlink, and with a clone folder that is a symlink.")
	root := filepath.Join(r.opts.StateRoot, "cases", "planted")
	c, err := r.newCase(ctx, root, r.plantedLead, false)
	if err != nil {
		return cs.harnessError(ctx, err)
	}
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

	// An alternates file in the clone.
	alt := filepath.Join(c.tree, ".git", "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alt), 0o700); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := os.WriteFile(alt, []byte(filepath.Join(root, "fixture", "no-such-objects")+"\n"), 0o600); err != nil {
		return cs.harnessError(ctx, err)
	}
	aerr := attempt("alternates", "A fetch with .git/objects/info/alternates present in the clone.")
	if err := os.Remove(alt); err != nil {
		return cs.harnessError(ctx, err)
	}
	if aerr != nil {
		return cs.harnessError(ctx, aerr)
	}

	// .git replaced by a symlink to the real folder.
	dotGit := filepath.Join(c.tree, ".git")
	if err := os.Rename(dotGit, filepath.Join(moved, "dotgit")); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := os.Symlink(filepath.Join(moved, "dotgit"), dotGit); err != nil {
		_ = os.Rename(filepath.Join(moved, "dotgit"), dotGit)
		return cs.harnessError(ctx, err)
	}
	gerr := attempt("git-symlink", "A fetch with .git of the clone replaced by a symlink to the real folder.")
	rerr := os.Remove(dotGit)
	if rerr == nil {
		rerr = os.Rename(filepath.Join(moved, "dotgit"), dotGit)
	}
	if rerr != nil {
		return cs.harnessError(ctx, rerr)
	}
	if gerr != nil {
		return cs.harnessError(ctx, gerr)
	}

	// The clone's own folder replaced by a symlink.
	if err := os.Rename(c.tree, filepath.Join(moved, "tree")); err != nil {
		return cs.harnessError(ctx, err)
	}
	if err := os.Symlink(filepath.Join(moved, "tree"), c.tree); err != nil {
		_ = os.Rename(filepath.Join(moved, "tree"), c.tree)
		return cs.harnessError(ctx, err)
	}
	terr := attempt("tree-symlink", "A fetch with the clone's folder (leads/<role>/tree) replaced by a symlink to the real folder.")
	rerr = os.Remove(c.tree)
	if rerr == nil {
		rerr = os.Rename(filepath.Join(moved, "tree"), c.tree)
	}
	if rerr != nil {
		return cs.harnessError(ctx, rerr)
	}
	if terr != nil {
		return cs.harnessError(ctx, terr)
	}

	if err := attempt("control-after", "A fetch after the plants were taken away again. It shows that the refusals above came from the plants."); err != nil {
		return cs.harnessError(ctx, err)
	}
	return ctx.Err()
}
