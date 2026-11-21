package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ataniq/internal/exchange/gitenv"
)

// Limits of the summary's reads.
const (
	// TextCap is the size over which a blob is binary whatever it holds: 1
	// MiB.
	TextCap = 1 << 20
	// maxScanBytes is the most content of blobs that [Summarize] reads to
	// look for a NUL byte. A change that holds more is refused with
	// [ErrLimit].
	maxScanBytes = 64 << 20
)

// PathSummary is one changed path of a [Summary].
type PathSummary struct {
	// Path is the path as git stores it: raw lead text.
	Path string
	// Status and the modes are those of the [Change].
	Status           byte
	OldMode, NewMode uint32
	// Binary is true when either side is binary by content: a NUL byte
	// anywhere in it, or more than [TextCap] bytes. Git's heuristic and
	// attributes do not decide.
	Binary bool
	// Added and Removed are the lines of the change, counted as if every
	// side were text. They are zero for a binary path and for a submodule.
	Added, Removed int64
	// OldSize and NewSize are the bytes of the blob on each side. They are
	// zero where there is no entry and for a submodule.
	OldSize, NewSize int64
}

// Summary is the fuller account of a review that the owner's card shows. It
// is computed from the ids of the review, never from ref names, and holds
// raw lead text in its paths.
type Summary struct {
	// ReviewDigest is [Review.Digest] of the review summarised, and Base
	// and Tip are its commits.
	ReviewDigest, Base, Tip string
	// Files is the number of changed paths. Added and Removed are the
	// lines of all non-binary paths. NewBytes is the size of the new side
	// of every added or changed blob, binary ones too: a path whose blob
	// did not change (a change of mode alone) adds none, and neither does
	// a deletion or a submodule.
	Files          int
	Added, Removed int64
	NewBytes       int64
	// Paths is every changed path, sorted by path bytes.
	Paths []PathSummary
	// Binary lists the paths that are binary by content. Symlinks lists
	// those with a symbolic link on either side. ModeChanges lists those
	// with an entry on both sides and a different mode, which includes a
	// change of type. Submodules lists those with a gitlink on either
	// side. OddPaths lists those flagged [FlagOddPath]. Each is sorted by
	// path bytes and is never cut.
	Binary, Symlinks, ModeChanges, Submodules, OddPaths []string
}

// Summarize computes the summary of rev. It first runs the check of
// "info/attributes" ([ErrAttributes]), since git reads that file whatever the
// attribute source is. Then it reads, through gitenv, the sizes of the blobs
// on both sides, the content of those that are not over [TextCap] (to look for
// a NUL byte), and the line counts of git diff --numstat with renames off.
// Git calls a file binary by its own heuristic, a NUL in the first 8000
// bytes, and by attributes, which the pin removes; this package calls a path
// binary by content alone, so numstat counts are used only for the paths that
// are not binary, where they are the counts of a diff that treats every file
// as text. Only the lines of a path that holds a NUL beyond git's window, or
// is over the cap, differ, and those are not counted at all.
//
// It writes nothing. A path of the review that numstat does not list, or one
// it lists that the review lacks, is an error.
func Summarize(ctx context.Context, src Source, rev *Review, lim Limits) (*Summary, error) {
	if rev == nil || !gitenv.IsObjectID(rev.Base) || !gitenv.IsObjectID(rev.Tip) {
		return nil, errors.New("intake: the review has no base and tip commit ids")
	}
	lim, err := lim.resolved()
	if err != nil {
		return nil, err
	}
	if err := checkAttributes(src); err != nil {
		return nil, err
	}
	sum := &Summary{ReviewDigest: rev.Digest(), Base: rev.Base, Tip: rev.Tip, Files: len(rev.Changes)}
	if len(rev.Changes) == 0 {
		return sum, nil
	}
	r := newRunner(src, lim)
	sizes, binary, err := r.blobFacts(ctx, rev.Changes)
	if err != nil {
		return nil, err
	}
	lines, err := r.numstat(ctx, rev, lim)
	if err != nil {
		return nil, err
	}
	for _, ch := range rev.Changes {
		ps := PathSummary{Path: ch.Path, Status: ch.Status, OldMode: ch.OldMode, NewMode: ch.NewMode}
		hasBlob := func(mode uint32) bool { return mode != 0 && mode != modeSubmodule }
		if hasBlob(ch.OldMode) {
			ps.OldSize = sizes[ch.OldID]
			ps.Binary = ps.Binary || binary[ch.OldID]
		}
		if hasBlob(ch.NewMode) {
			ps.NewSize = sizes[ch.NewID]
			ps.Binary = ps.Binary || binary[ch.NewID]
			if ch.NewID != ch.OldID {
				sum.NewBytes += ps.NewSize
			}
		}
		if !ps.Binary && (hasBlob(ch.OldMode) || hasBlob(ch.NewMode)) {
			c, ok := lines[ch.Path]
			if !ok {
				return nil, errors.New("intake: a changed path is missing from the output of git diff --numstat")
			}
			ps.Added, ps.Removed = c[0], c[1]
		}
		sum.Added += ps.Added
		sum.Removed += ps.Removed
		sum.Paths = append(sum.Paths, ps)
		if ps.Binary {
			sum.Binary = append(sum.Binary, ch.Path)
		}
		if ch.OldMode == modeSymlink || ch.NewMode == modeSymlink {
			sum.Symlinks = append(sum.Symlinks, ch.Path)
		}
		if ch.OldMode != 0 && ch.NewMode != 0 && ch.OldMode != ch.NewMode {
			sum.ModeChanges = append(sum.ModeChanges, ch.Path)
		}
		if ch.OldMode == modeSubmodule || ch.NewMode == modeSubmodule {
			sum.Submodules = append(sum.Submodules, ch.Path)
		}
		if hasFlagKind(ch.Flags, FlagOddPath) {
			sum.OddPaths = append(sum.OddPaths, ch.Path)
		}
	}
	return sum, nil
}

func hasFlagKind(flags []Flag, kind FlagKind) bool {
	return slices.ContainsFunc(flags, func(f Flag) bool { return f.Kind == kind })
}

// blobFacts reads the size of every blob on either side of the changes and
// whether it is binary by content: over [TextCap], or holding a NUL byte. It
// runs a size check first, then reads only the blobs that are not over the
// cap, so no long blob is read.
func (r *runner) blobFacts(ctx context.Context, changes []Change) (sizes map[string]int64, binary map[string]bool, err error) {
	var ids []string
	seen := map[string]bool{}
	for _, ch := range changes {
		for _, e := range [2]struct {
			mode uint32
			id   string
		}{{ch.OldMode, ch.OldID}, {ch.NewMode, ch.NewID}} {
			if e.mode != 0 && e.mode != modeSubmodule && !seen[e.id] {
				seen[e.id] = true
				ids = append(ids, e.id)
			}
		}
	}
	bad := errors.New("intake: unexpected output of git cat-file")
	sizes, binary = map[string]int64{}, map[string]bool{}
	if len(ids) == 0 {
		return sizes, binary, nil
	}
	out, err := r.git(ctx, []string{"cat-file", "--batch-check"}, strings.NewReader(strings.Join(ids, "\n")+"\n"),
		capFor(int64(len(ids))*256), nil)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(ids) {
		return nil, nil, bad
	}
	var scan []string
	var scanBytes int64
	for i, line := range lines {
		f := strings.Split(line, " ")
		if len(f) != 3 || f[0] != ids[i] || f[1] != "blob" {
			return nil, nil, bad
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil || n < 0 {
			return nil, nil, bad
		}
		sizes[ids[i]] = n
		if n > TextCap {
			binary[ids[i]] = true
			continue
		}
		scan = append(scan, ids[i])
		scanBytes += n
	}
	if scanBytes > maxScanBytes {
		return nil, nil, fmt.Errorf("%w: %d bytes of content to read for the summary, the limit is %d", ErrLimit, scanBytes, maxScanBytes)
	}
	if len(scan) == 0 {
		return sizes, binary, nil
	}
	out, err = r.git(ctx, []string{"cat-file", "--batch"}, strings.NewReader(strings.Join(scan, "\n")+"\n"),
		capFor(scanBytes+int64(len(scan))*256), nil)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range scan {
		nl := bytes.IndexByte(out, '\n')
		if nl < 0 || string(out[:nl]) != fmt.Sprintf("%s blob %d", id, sizes[id]) {
			return nil, nil, bad
		}
		out = out[nl+1:]
		n := int(sizes[id])
		if len(out) < n+1 || out[n] != '\n' {
			return nil, nil, bad
		}
		binary[id] = bytes.IndexByte(out[:n], 0) >= 0
		out = out[n+1:]
	}
	if len(out) != 0 {
		return nil, nil, bad
	}
	return sizes, binary, nil
}

// numstat runs git diff --numstat on the ids of the review, with renames
// off, and returns the added and removed lines of each path. A path git
// prints as binary ("-") has no entry. The result holds exactly the paths of
// the review, or the call fails.
func (r *runner) numstat(ctx context.Context, rev *Review, lim Limits) (map[string][2]int64, error) {
	sink := newCapture(lim.MaxPaths+1, maxFieldBytes, "paths")
	_, err := r.git(ctx, []string{
		"diff", "--numstat", "-z", "--no-renames", "--no-color", "--full-index",
		"--end-of-options", rev.Base, rev.Tip,
	}, nil, capFor(int64(lim.MaxPaths)*bytesPerRecord), sink)
	if err != nil {
		return nil, err
	}
	bad := errors.New("intake: unexpected output of git diff --numstat")
	out := sink.buf
	got := map[string][2]int64{}
	listed := map[string]bool{}
	for len(out) > 0 {
		nul := bytes.IndexByte(out, 0)
		if nul < 0 {
			return nil, bad
		}
		rec := string(out[:nul])
		out = out[nul+1:]
		a, rest, ok1 := strings.Cut(rec, "\t")
		d, path, ok2 := strings.Cut(rest, "\t")
		if !ok1 || !ok2 || path == "" {
			return nil, bad
		}
		if listed[path] {
			return nil, bad
		}
		listed[path] = true
		if a == "-" && d == "-" {
			continue
		}
		na, err1 := strconv.ParseInt(a, 10, 64)
		nd, err2 := strconv.ParseInt(d, 10, 64)
		if err1 != nil || err2 != nil || na < 0 || nd < 0 {
			return nil, bad
		}
		got[path] = [2]int64{na, nd}
	}
	if len(listed) != len(rev.Changes) {
		return nil, bad
	}
	for _, ch := range rev.Changes {
		if !listed[ch.Path] {
			return nil, bad
		}
	}
	return got, nil
}
