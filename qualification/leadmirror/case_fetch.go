package leadmirror

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sort"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/exchange/safefs"
	"ataniq/internal/mirror"
)

// s16Lead is the lead's work in the fetch case: a branch with the upstream
// branch's name and other content, a second branch, a tag with the upstream
// tag's name that points elsewhere, and a tag of its own.
func (r *runner) s16Lead(ctx context.Context, dir string) error {
	f := r.fx
	if err := f.commit(ctx, dir, "lead: change the readme", file{"README.md", "the lead's own text\n"}); err != nil {
		return err
	}
	if _, err := f.run(ctx, dir, "checkout", "--quiet", "-b", "feature"); err != nil {
		return err
	}
	if err := f.commit(ctx, dir, "lead: a feature", file{"feature.txt", "feature\n"}); err != nil {
		return err
	}
	if _, err := f.run(ctx, dir, "tag", "lead-only"); err != nil {
		return err
	}
	if _, err := f.run(ctx, dir, "tag", "--force", upstreamTag, "feature"); err != nil {
		return err
	}
	_, err := f.run(ctx, dir, "checkout", "--quiet", upstreamBranch)
	return err
}

// canaryFacts reads the canary folder. It lists which of the planted canary
// names exist and any other name in the folder. It reads names only.
func canaryFacts(dir string, planted []string) (map[string]any, error) {
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	present := []string{}
	for _, n := range planted {
		if _, err := root.Lstat(n); err == nil {
			present = append(present, n)
		}
	}
	// The listing goes through an os.Root, which follows no symlink out of
	// the folder, for the names only.
	or, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer or.Close()
	d, err := or.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, n := range planted {
		known[n] = true
	}
	other := []string{}
	for _, e := range entries {
		if !known[e.Name()] {
			other = append(other, leadtext.Escape(e.Name()))
		}
	}
	sort.Strings(other)
	return map[string]any{
		"planted_count":    len(planted),
		"present":          present,
		"other_entries":    other,
		"entries_in_total": len(entries),
	}, nil
}

// fetchFacts describes the report and the error of a fetch. The ref names of
// the updates are escaped.
func fetchFacts(rep mirror.FetchReport, err error) map[string]any {
	updates := make([]map[string]any, 0, len(rep.Updates))
	for _, u := range rep.Updates {
		updates = append(updates, map[string]any{"ref": leadtext.Escape(u.Ref), "old": u.Old, "new": u.New})
	}
	outside := make([]string, 0, len(rep.OutsideChanged))
	for _, o := range rep.OutsideChanged {
		outside = append(outside, leadtext.Escape(o))
	}
	return merge(errorFacts(err), map[string]any{
		"updates":               updates,
		"outside_changed":       outside,
		"source_bytes":          rep.Source,
		"mirror_before_bytes":   rep.MirrorBefore,
		"staged_bytes":          rep.Staged,
		"mirror_after_bytes":    rep.MirrorAfter,
		"duration_milliseconds": rep.Duration.Milliseconds(),
	})
}

// fetch runs one fetch of the lead's branches through the recorder, with the
// limits. It returns the report, the error and the number of serve requests
// the fetch made.
func (r *runner) fetch(ctx context.Context, c *caseEnv, lim mirror.FetchLimits) (mirror.FetchReport, int64, error) {
	before := r.rec.serves.Load()
	if lim.Timeout == 0 {
		lim.Timeout = r.opts.FetchTimeout
	}
	rep, err := c.m.Fetch(ctx, mirror.FetchRequest{
		StateRoot: c.root, Role: r.opts.Role, Project: r.opts.Project, Planner: r.rec, Limits: lim,
	})
	return rep, r.rec.serves.Load() - before, err
}

// caseFetch is design case S16: a fetch from a hostile clone.
func (r *runner) caseFetch(ctx context.Context) error {
	cs := r.add("s16-fetch", "Fetch of a lead's branches from a hostile clone, through the template's serving half.")
	c, err := r.newCase(ctx, r.opts.StateRoot, r.s16Lead, true)
	if err != nil {
		return cs.harnessError(ctx, err)
	}
	planted := canaryNames()
	inside := "unknown: the planner gave no serve plan"
	if c.insideOK {
		inside = c.inside
	}
	onHost := "unknown: the planner gave no serve plan"
	if c.insideOK {
		onHost = pathOnHost(c.inside, c.tree)
	}
	cs.fact("clone", "What the harness planted in the lead's clone, how the serving half sees the clone's path, and whether that path exists on the host (the fetch names the path to git as a file:// URL, which git never reads as a bundle, and refuses a path that exists as a second layer).", map[string]any{
		"hook_names":           hookNames,
		"hook_folders":         []string{".git/hooks", ".git/" + leadHooksFolder},
		"config_keys":          []string{"core.fsmonitor", "core.hooksPath", "uploadpack.packObjectsHook", "core.alternateRefsCommand"},
		"lead_branches":        []string{"main (not the upstream commit)", "feature"},
		"lead_tags":            []string{"lead-only", upstreamTag + " (not the upstream tag's object)"},
		"repository_inside":    inside,
		"repository_on_host":   onHost,
		"canary_names_planted": len(planted),
	})

	before, err := readState(ctx, c.m, r.opts.Role, maxEntries)
	if err != nil {
		return cs.harnessError(ctx, err)
	}
	pre, err := canaryFacts(c.canary, planted)
	if err != nil {
		return cs.harnessError(ctx, err)
	}
	rep, serves, ferr := r.fetch(ctx, c, mirror.FetchLimits{})
	after, err := readState(ctx, c.m, r.opts.Role, maxEntries)
	if err != nil {
		return cs.harnessError(ctx, err)
	}
	post, err := canaryFacts(c.canary, planted)
	if err != nil {
		return cs.harnessError(ctx, err)
	}

	cs.fact("fetch", "The result of Mirror.Fetch of the lead's branches with the default limits.",
		merge(fetchFacts(rep, ferr), map[string]any{"serve_requests": serves}))
	cs.fact("upstream-refs", "refs/heads/* and refs/tags/* of the mirror before and after the fetch. The lead had a branch and a tag with the upstream names.",
		map[string]any{
			"before":    before.Upstream,
			"after":     after.Upstream,
			"identical": sameRefs(before.Upstream, after.Upstream),
		})
	cs.fact("lead-refs", "The lead's refs in the mirror (refs/leads/<role>/heads/*) before and after the fetch. Lead tags are not carried.",
		map[string]any{"before": before.Lead, "after": after.Lead})
	cs.fact("mirror-size", "The size of the mirror folder before and after the fetch.",
		map[string]any{"before": before.Size, "after": after.Size})
	cs.fact("canaries", "Which canary files exist on the host before and after the fetch. The canary folder is outside the lead's clone, under the scratch state root.",
		map[string]any{"before": pre, "after": post, "canary_folder": "canary (below the scratch state root)"})

	// The control comes last, after every fact about the fetch is taken.
	// It runs plain git in the control clone, which no sandbox served.
	ctl, err := r.controlFacts(ctx, c, planted)
	if err != nil {
		return cs.harnessError(ctx, err)
	}
	cs.fact("control", "Plain git status, run by the harness in a copy of the hostile clone that no sandbox served, in the fixture folder, with an explicit environment and not through the template. The copy has its own canary folder. It shows whether the planted canaries fire on this host when git runs in a clone: `present` is the canaries that fired. Without it, an absent canary in s16-fetch.canaries could also mean that the fixture is not armed.",
		ctl)
	return ctx.Err()
}

// controlFacts runs git status in the control clone and reports which of the
// planted canaries exist afterwards in the control's own canary folder. A
// failed git status is a fact, not an error of the harness.
func (r *runner) controlFacts(ctx context.Context, c *caseEnv, planted []string) (map[string]any, error) {
	_, serr := r.fx.run(ctx, c.control, "status", "--porcelain")
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cf, err := canaryFacts(c.ccanary, planted)
	if err != nil {
		return nil, err
	}
	return merge(errorFacts(serr), cf), nil
}

// pathOnHost says what the host has at the template's repository path, as the
// fetch's own check sees it (the entry itself, no symlink followed at the last
// name): "absent", "the clone's own path", or what exists there.
func pathOnHost(path, tree string) string {
	if path == tree {
		return "the clone's own path"
	}
	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		switch m := fi.Mode(); {
		case m.IsRegular():
			return "exists: a regular file"
		case m&fs.ModeSymlink != 0:
			return "exists: a symbolic link"
		case m.IsDir():
			return "exists: a folder"
		}
		return "exists: a file of another kind"
	case errors.Is(err, fs.ErrNotExist):
		return "absent"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return "cannot be checked: " + err.Error()
}
