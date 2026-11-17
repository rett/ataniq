package leadmirror

import (
	"context"
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
	cs.fact("clone", "What the harness planted in the lead's clone, and how the serving half sees the clone's path.", map[string]any{
		"hook_names":           hookNames,
		"hook_folders":         []string{".git/hooks", ".git/" + leadHooksFolder},
		"config_keys":          []string{"core.fsmonitor", "core.hooksPath", "uploadpack.packObjectsHook", "core.alternateRefsCommand"},
		"lead_branches":        []string{"main (not the upstream commit)", "feature"},
		"lead_tags":            []string{"lead-only", upstreamTag + " (not the upstream tag's object)"},
		"repository_inside":    inside,
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
	return ctx.Err()
}
