package leadmirror

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"ataniq/internal/exchange/leadtext"
	"ataniq/internal/mirror"
)

// oversizeLead is the lead's work in the oversize case: a branch with about
// 300 KiB of text that does not compress.
func (r *runner) oversizeLead(ctx context.Context, dir string) error {
	f := r.fx
	if _, err := f.run(ctx, dir, "checkout", "--quiet", "-b", "big"); err != nil {
		return err
	}
	if err := f.commit(ctx, dir, "lead: a big branch",
		file{"big0.bin", randomText(1, 100<<10)},
		file{"big1.bin", randomText(2, 100<<10)},
		file{"big2.bin", randomText(3, 100<<10)},
	); err != nil {
		return err
	}
	_, err := f.run(ctx, dir, "checkout", "--quiet", upstreamBranch)
	return err
}

// folderNames lists the names in a folder, escaped and sorted.
func folderNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, leadtext.Escape(e.Name()))
	}
	sort.Strings(names)
	return names, nil
}

// caseOversize looks at the two size limits of a fetch: the measure that the
// sandbox reports, and what a fetch adds.
func (r *runner) caseOversize(ctx context.Context) error {
	cs := r.add("oversize", "Fetches that pass a small limit: the sandbox's measure of the clone, and the size of what one fetch adds.")
	c, err := r.newCase(ctx, filepath.Join(r.opts.StateRoot, "cases", "oversize"), r.oversizeLead, false)
	if err != nil {
		return cs.harnessError(ctx, err)
	}
	mirrors := filepath.Dir(c.m.Path())
	for _, step := range []struct {
		id, detail string
		lim        mirror.FetchLimits
	}{
		{"measure", "A fetch with MaxSource set to " + itoa(r.opts.SmallMaxSource) + " bytes. The measure that the template's command prints is compared with it.",
			mirror.FetchLimits{MaxSource: r.opts.SmallMaxSource}},
		{"staged", "A fetch with MaxStaged set to " + itoa(r.opts.SmallMaxStaged) + " bytes. The clone holds about 300 KiB that does not compress.",
			mirror.FetchLimits{MaxStaged: r.opts.SmallMaxStaged}},
		{"control", "The same fetch with the default limits, after the two above. It shows that the limits and not the clone caused the errors.",
			mirror.FetchLimits{}},
	} {
		before, err := readState(ctx, c.m, r.opts.Role, maxEntries)
		if err != nil {
			return cs.harnessError(ctx, err)
		}
		rep, serves, ferr := r.fetch(ctx, c, step.lim)
		after, err := readState(ctx, c.m, r.opts.Role, maxEntries)
		if err != nil {
			return cs.harnessError(ctx, err)
		}
		names, err := folderNames(mirrors)
		if err != nil {
			return cs.harnessError(ctx, err)
		}
		obs := merge(fetchFacts(rep, ferr), map[string]any{
			"serve_requests": serves,
			"mirror_before":  before,
			"mirror_after":   after,
			"mirrors_folder": names,
		})
		cs.fact(step.id, step.detail, obs)
	}
	return ctx.Err()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
