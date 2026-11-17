package intake

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"ataniq/internal/intake/execconfig"
)

// historyFactor is how many times MaxPaths the commit-by-commit listing may
// have records.
const historyFactor = 4

// seen is what the commits of the branch did to one path.
type seen struct {
	match     *execconfig.Match
	symlink   bool
	submodule bool
}

// historyFlags returns the history-only flags. It lists the change of every
// commit of the branch against each of its parents (diff-tree -m, so a merge
// commit counts once for each parent). A path that was execution config, a
// symlink or a submodule entry in the change of some commit, and that is not
// in the net change, was added and removed again inside the branch. It enters
// upstream history although not the tip's tree, so it needs a sign-off. A
// path that is in the net change gets the flag too when it was a symlink or a
// submodule entry in a commit and is neither at the tip: the net change then
// does not show it.
func (r *runner) historyFlags(ctx context.Context, rules execconfig.Set, commits []Commit, changes []Change, lim Limits) ([]Flag, error) {
	if len(commits) == 0 {
		return nil, nil
	}
	var in bytes.Buffer
	headers := 0
	for _, c := range commits {
		in.WriteString(c.ID + "\n")
		headers += max(len(c.Parents), 1)
	}
	records := historyFactor * lim.MaxPaths
	sink := newCapture(2*records+headers, maxFieldBytes, "paths in the commits")
	_, err := r.git(ctx, []string{
		"diff-tree", "--stdin", "-m", "-r", "-z", "--raw", "--no-renames", "--root",
		"--no-ext-diff", "--no-textconv", "--full-index",
	}, &in, capFor(int64(records+headers)*bytesPerRecord), sink)
	if err != nil {
		return nil, err
	}
	recs, err := parseRaw(sink.buf, true)
	if err != nil {
		return nil, err
	}
	hist := map[string]*seen{}
	for _, rec := range recs {
		s := hist[rec.path]
		if s == nil {
			s = &seen{}
			hist[rec.path] = s
		}
		if m, ok := rules.Match(rec.path); ok && s.match == nil {
			s.match = &m
		}
		if rec.oldMode == modeSymlink || rec.newMode == modeSymlink {
			s.symlink = true
		}
		if rec.oldMode == modeSubmodule || rec.newMode == modeSubmodule {
			s.submodule = true
		}
	}
	net := make(map[string]Change, len(changes))
	for _, ch := range changes {
		net[ch.Path] = ch
	}
	paths := make([]string, 0, len(hist))
	for p := range hist {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var flags []Flag
	for _, p := range paths {
		s := hist[p]
		if !(s.match != nil || s.symlink || s.submodule) {
			continue
		}
		var parts []string
		f := Flag{Kind: FlagHistory, Path: p}
		if s.match != nil {
			f.Pattern = s.match.Pattern
			parts = append(parts, "execution config ("+s.match.Reason+")")
		}
		if s.symlink {
			parts = append(parts, "a symlink entry")
		}
		if s.submodule {
			parts = append(parts, "a submodule entry")
		}
		ch, inNet := net[p]
		switch {
		case !inNet:
			f.Reason = "added and removed again inside the branch, so it enters history but not the tip's tree: " +
				strings.Join(parts, ", ")
		case (s.symlink || s.submodule) && !hasKind(ch.Flags, FlagSymlink) && !hasKind(ch.Flags, FlagSubmodule):
			f.Reason = "was a symlink or submodule entry inside the branch and is not one at its tip: " +
				strings.Join(parts, ", ")
		default:
			continue
		}
		flags = append(flags, f)
	}
	return flags, nil
}

func hasKind(flags []Flag, kind FlagKind) bool {
	for _, f := range flags {
		if f.Kind == kind {
			return true
		}
	}
	return false
}
