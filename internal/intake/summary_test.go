package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The contents of the known change set (OC15). Each case of the set has its
// expected row written out by hand in TestSummaryKnownChangeSet, and the
// whole summary is compared with an independent computation as well.
var (
	oldNameText = lines(10, "old line")
	mainBefore  = "package main\n"
	mainAfter   = "package main\n\nfunc main() {}\n"
	notesBefore = "a\nb\nc\n"
	notesAfter  = "a\nB\nc\n"
	lateNUL     = strings.Repeat("x", 9000) + "\x00" + "tail\n"
	earlyNUL    = "a\x00b"
	bigText     = strings.Repeat("0123456789abcde\n", TextCap/16+1) // TextCap + 16 bytes, no NUL
	edgeText    = strings.Repeat("0123456789abcde\n", TextCap/16)   // exactly TextCap bytes, no NUL
	runScript   = "#!/bin/sh\necho hi\n"
)

func lines(n int, word string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%s %d\n", word, i)
	}
	return b.String()
}

func knownRoot(l *lab) {
	l.put("old/name.txt", oldNameText)
	l.put("run.sh", runScript)
	l.put("notes.txt", notesBefore)
	l.gitlinkAt("vendor/sub", "ab")
}

func knownChange(l *lab) {
	// A rename: with renames off it is a deletion and an addition.
	l.remove("old/name.txt")
	l.put("new/name.txt", oldNameText)
	// A mode change alone.
	l.putMode("run.sh", "100755", runScript)
	// A submodule bump.
	l.gitlinkAt("vendor/sub", "cd")
	// A NUL after the window git looks at, and one at the start.
	l.put("late.bin", lateNUL)
	l.put("early.bin", earlyNUL)
	// A new symbolic link.
	l.symlink("link", "src/main.go")
	// A deleted file.
	l.remove("README.md")
	// Over the cap by one line and exactly at it, both with no NUL.
	l.put("big.txt", bigText)
	l.put("edge.txt", edgeText)
	l.put("src/main.go", mainAfter)
	l.put("notes.txt", notesAfter)
}

func (m *mscene) summarize(r *Review) *Summary {
	m.t.Helper()
	sum, err := Summarize(context.Background(), m.source(), r, Limits{})
	if err != nil {
		m.t.Fatalf("Summarize: %v", err)
	}
	return sum
}

func pathRow(t *testing.T, sum *Summary, path string) PathSummary {
	t.Helper()
	for _, p := range sum.Paths {
		if p.Path == path {
			return p
		}
	}
	t.Fatalf("no row for %q", path)
	return PathSummary{}
}

// TestSummaryKnownChangeSet is OC15: a rename, a mode change, a submodule
// bump, a late NUL, a new symbolic link and a deleted file, with the rows
// written out by hand.
func TestSummaryKnownChangeSet(t *testing.T) {
	m := newMergeSceneRoot(t, knownRoot, knownChange)
	r := m.inspectMerge()
	sum := m.summarize(r)
	if sum.ReviewDigest != r.Digest() || sum.Base != m.target || sum.Tip != m.merge {
		t.Errorf("header: %+v", sum)
	}
	rows := []PathSummary{
		{Path: "README.md", Status: 'D', OldMode: 0o100644, Removed: 1, OldSize: 7},
		{Path: "big.txt", Status: 'A', NewMode: 0o100644, Binary: true, NewSize: int64(len(bigText))},
		{Path: "early.bin", Status: 'A', NewMode: 0o100644, Binary: true, NewSize: 3},
		{Path: "edge.txt", Status: 'A', NewMode: 0o100644, Added: TextCap / 16, NewSize: TextCap},
		{Path: "late.bin", Status: 'A', NewMode: 0o100644, Binary: true, NewSize: int64(len(lateNUL))},
		{Path: "link", Status: 'A', NewMode: 0o120000, Added: 1, NewSize: int64(len("src/main.go"))},
		{Path: "new/name.txt", Status: 'A', NewMode: 0o100644, Added: 10, NewSize: int64(len(oldNameText))},
		{Path: "notes.txt", Status: 'M', OldMode: 0o100644, NewMode: 0o100644, Added: 1, Removed: 1, OldSize: 6, NewSize: 6},
		{Path: "old/name.txt", Status: 'D', OldMode: 0o100644, Removed: 10, OldSize: int64(len(oldNameText))},
		{Path: "run.sh", Status: 'M', OldMode: 0o100644, NewMode: 0o100755, OldSize: int64(len(runScript)), NewSize: int64(len(runScript))},
		{Path: "src/main.go", Status: 'M', OldMode: 0o100644, NewMode: 0o100644, Added: 2, OldSize: int64(len(mainBefore)), NewSize: int64(len(mainAfter))},
		{Path: "vendor/sub", Status: 'M', OldMode: 0o160000, NewMode: 0o160000},
	}
	if !reflect.DeepEqual(sum.Paths, rows) {
		for i := 0; i < max(len(rows), len(sum.Paths)); i++ {
			var got, want any
			if i < len(sum.Paths) {
				got = sum.Paths[i]
			}
			if i < len(rows) {
				want = rows[i]
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("row %d:\n got  %+v\n want %+v", i, got, want)
			}
		}
	}
	if sum.Files != 12 || sum.Added != 10+2+1+1+TextCap/16 || sum.Removed != 1+10+1 {
		t.Errorf("totals: files %d added %d removed %d", sum.Files, sum.Added, sum.Removed)
	}
	// New bytes: the new side of every added or changed blob. The mode
	// change alone and the submodule bump add none; the binary ones count.
	wantBytes := int64(len(oldNameText) + len(mainAfter) + len(notesAfter) + len("src/main.go") +
		len(lateNUL) + len(earlyNUL) + len(bigText) + len(edgeText))
	if sum.NewBytes != wantBytes {
		t.Errorf("NewBytes = %d, want %d", sum.NewBytes, wantBytes)
	}
	eq := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	eq("Binary", sum.Binary, []string{"big.txt", "early.bin", "late.bin"})
	eq("Symlinks", sum.Symlinks, []string{"link"})
	eq("ModeChanges", sum.ModeChanges, []string{"run.sh"})
	eq("Submodules", sum.Submodules, []string{"vendor/sub"})
	eq("OddPaths", sum.OddPaths, nil)
	// The first row of the card's flagged list: the symlink and the
	// submodule are flagged by the review.
	var flagged []string
	for _, f := range r.Flagged {
		flagged = append(flagged, string(f.Kind)+":"+f.Path)
	}
	for _, want := range []string{"symlink:link", "submodule:vendor/sub"} {
		if !slices.Contains(flagged, want) {
			t.Errorf("flagged = %q lacks %q", flagged, want)
		}
	}
	checkAgainstIndependent(t, m, sum)
}

// TestSummaryTextCapEdges pins the two sides of the text cap on their own, so
// that a change of the comparison fails here and not only in the table above.
func TestSummaryTextCapEdges(t *testing.T) {
	m := newMergeScene(t, func(l *lab) {
		l.put("at.txt", edgeText)
		l.put("over.txt", edgeText+"x")
		l.put("nul-at-end.txt", edgeText[:TextCap-1]+"\x00")
	})
	sum := m.summarize(m.inspectMerge())
	at, over, nul := pathRow(t, sum, "at.txt"), pathRow(t, sum, "over.txt"), pathRow(t, sum, "nul-at-end.txt")
	if at.Binary || at.Added != TextCap/16 {
		t.Errorf("at the cap: %+v", at)
	}
	if !over.Binary || over.Added != 0 {
		t.Errorf("over the cap: %+v", over)
	}
	if !nul.Binary || nul.Added != 0 {
		t.Errorf("a NUL as the last byte: %+v", nul)
	}
}

// TestSummaryBinaryEitherSide: a file that was binary and became text, and one
// that was text and became binary, are binary rows with no counts.
func TestSummaryBinaryEitherSide(t *testing.T) {
	m := newMergeSceneRoot(t, func(l *lab) {
		l.put("was-bin", "a\x00b\n")
		l.put("was-text", "plain\n")
		l.put("gone-bin", "q\x00q")
	}, func(l *lab) {
		l.put("was-bin", "now text\n")
		l.put("was-text", "plain\x00\n")
		l.remove("gone-bin")
	})
	sum := m.summarize(m.inspectMerge())
	for _, p := range []string{"was-bin", "was-text", "gone-bin"} {
		row := pathRow(t, sum, p)
		if !row.Binary || row.Added != 0 || row.Removed != 0 {
			t.Errorf("%s: %+v", p, row)
		}
	}
	if !slices.Equal(sum.Binary, []string{"gone-bin", "was-bin", "was-text"}) {
		t.Errorf("Binary = %q", sum.Binary)
	}
	if sum.Added != 0 || sum.Removed != 0 {
		t.Errorf("binary paths add lines: %d %d", sum.Added, sum.Removed)
	}
	checkAgainstIndependent(t, m, sum)
}

// TestSummaryAttributesDoNotChangeIt is OC27's part of the summary: a
// .gitattributes that says "* binary" and "* -diff", already on the target and
// new in the change, does not make a text file binary or hide its lines, and
// the new file is flagged.
func TestSummaryAttributesDoNotChangeIt(t *testing.T) {
	attr := "* binary\n* -diff\n* merge=union\n"
	m := newMergeSceneRoot(t, func(l *lab) { l.put(".gitattributes", attr) }, func(l *lab) {
		l.put("src/main.go", mainAfter)
		l.put("sub/.gitattributes", attr)
		l.put("sub/new.txt", "one\ntwo\n")
	})
	r := m.inspectMerge()
	sum := m.summarize(r)
	if row := pathRow(t, sum, "src/main.go"); row.Binary || row.Added != 2 {
		t.Errorf("main.go: %+v", row)
	}
	if row := pathRow(t, sum, "sub/new.txt"); row.Binary || row.Added != 2 {
		t.Errorf("new.txt: %+v", row)
	}
	if len(sum.Binary) != 0 {
		t.Errorf("Binary = %q", sum.Binary)
	}
	if !hasFlag(byPath(t, r, "sub/.gitattributes").Flags, FlagExecConfig) {
		t.Errorf("a new .gitattributes is not flagged: %+v", byPath(t, r, "sub/.gitattributes"))
	}
	checkAgainstIndependent(t, m, sum)
}

func TestSummaryOddPathsAndEmpty(t *testing.T) {
	m := oddScene(t)
	sum := m.summarize(m.inspectMerge())
	want := []string{"PROGRA~1/run", "a:stream", "ctl\x01name", "e\u0301.txt", "raw\xffbytes"}
	if !slices.Equal(sum.OddPaths, want) {
		t.Errorf("OddPaths = %q, want %q", sum.OddPaths, want)
	}
	checkAgainstIndependent(t, m, sum)
	// A review with no change has an empty summary.
	m.publish(upstreamRef, m.merge)
	e := m.summarize(m.inspectMerge())
	if e.Files != 0 || len(e.Paths) != 0 || e.Base != e.Tip {
		t.Errorf("empty summary = %+v", e)
	}
}

func TestSummarizeRefusesWhatItCannotRead(t *testing.T) {
	m := newMergeScene(t, featureChange)
	r := m.inspectMerge()
	ctx := context.Background()
	if _, err := Summarize(ctx, m.source(), nil, Limits{}); err == nil {
		t.Errorf("a nil review")
	}
	if _, err := Summarize(ctx, m.source(), &Review{}, Limits{}); err == nil {
		t.Errorf("a review with no ids")
	}
	if _, err := Summarize(ctx, m.source(), r, Limits{Timeout: -1}); err == nil {
		t.Errorf("a negative limit")
	}
	// A change list that the diff does not match is an error, not a guess.
	bad := *r
	bad.Changes = append(slices.Clone(r.Changes), Change{Path: "invented", Status: 'A', NewMode: 0o100644, NewID: r.Changes[1].NewID, OldID: strings.Repeat("0", 40)})
	if _, err := Summarize(ctx, m.source(), &bad, Limits{}); err == nil {
		t.Errorf("a review with a path git does not list")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Summarize(cctx, m.source(), r, Limits{}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled context: %v", err)
	}
	// More content to read than the limit allows is refused, not cut.
	if got := maxScanBytes; got != 64<<20 {
		t.Errorf("maxScanBytes = %d", got)
	}
}

// TestSummaryFiveThousandPaths is the large case of OC15: 5,000 paths, in two
// blobs of different sizes, so that a sum by blob and a sum by path differ if
// either is wrong.
func TestSummaryFiveThousandPaths(t *testing.T) {
	const n = 5000
	short, long := "a\nb\n", lines(25, "a longer line")
	m := newMergeScene(t, func(l *lab) {
		bs, bl := l.blob(short), l.blob(long)
		var idx []string
		for i := 0; i < n; i++ {
			id := bs
			if i%2 == 1 {
				id = bl
			}
			idx = append(idx, fmt.Sprintf("100644 %s\tbulk/%04d/f.txt", id, i))
		}
		l.putIndex(idx)
	})
	r := m.inspectMerge()
	if len(r.Changes) != n {
		t.Fatalf("changes = %d", len(r.Changes))
	}
	sum := m.summarize(r)
	if sum.Files != n || len(sum.Paths) != n {
		t.Fatalf("files %d, rows %d", sum.Files, len(sum.Paths))
	}
	if want := int64(n/2*2 + n/2*25); sum.Added != want || sum.Removed != 0 {
		t.Errorf("Added = %d, want %d", sum.Added, want)
	}
	if want := int64(n/2*len(short) + n/2*len(long)); sum.NewBytes != want {
		t.Errorf("NewBytes = %d, want %d", sum.NewBytes, want)
	}
	if len(sum.Binary)+len(sum.Symlinks)+len(sum.ModeChanges)+len(sum.Submodules) != 0 {
		t.Errorf("lists are not empty")
	}
	if !slices.IsSortedFunc(sum.Paths, func(a, b PathSummary) int { return strings.Compare(a.Path, b.Path) }) {
		t.Errorf("rows are not sorted by path")
	}
	checkAgainstIndependent(t, m, sum)
}

func TestSummaryWritesNothing(t *testing.T) {
	m := newMergeSceneRoot(t, knownRoot, knownChange)
	r := m.inspectMerge()
	before := snapshot(t, m.scene)
	m.summarize(r)
	assertUntouched(t, m.scene, before)
}

// checkAgainstIndependent computes the summary of the merge in another way
// and requires that it equals got. The other way never uses the package's
// code or its runner: ls-tree lists the entries and their sizes of the two
// trees, cat-file reads each blob for a NUL byte, and git diff --numstat with
// the text option on counts the lines of every path that is not binary.
func checkAgainstIndependent(t *testing.T, m *mscene, got *Summary) {
	t.Helper()
	type entry struct {
		mode, id string
		size     int64
	}
	tree := func(commit string) map[string]entry {
		out := m.plainGit(nil, "ls-tree", "-r", "-z", "-l", commit)
		res := map[string]entry{}
		for _, rec := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
			if rec == "" {
				continue
			}
			meta, path, _ := strings.Cut(rec, "\t")
			f := strings.Fields(meta)
			e := entry{mode: f[0], id: f[2]}
			if f[3] != "-" {
				e.size, _ = strconv.ParseInt(f[3], 10, 64)
			}
			res[path] = e
		}
		return res
	}
	old, cur := tree(got.Base), tree(got.Tip)
	var paths []string
	for p, e := range old {
		if c, ok := cur[p]; !ok || c.id != e.id || c.mode != e.mode {
			paths = append(paths, p)
		}
	}
	for p := range cur {
		if _, ok := old[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	nul := map[string]bool{}
	hasNUL := func(e entry) bool {
		if e.mode == "160000" {
			return false
		}
		if v, ok := nul[e.id]; ok {
			return v
		}
		v := e.size > TextCap || bytes.IndexByte(m.plainGit(nil, "cat-file", "blob", e.id), 0) >= 0
		nul[e.id] = v
		return v
	}
	counts := map[string][2]int64{}
	numstat := m.plainGit(nil, "diff", "--numstat", "-a", "-z", "--no-renames", got.Base, got.Tip)
	for _, rec := range strings.Split(strings.TrimSuffix(string(numstat), "\x00"), "\x00") {
		f := strings.SplitN(rec, "\t", 3)
		a, _ := strconv.ParseInt(f[0], 10, 64)
		d, _ := strconv.ParseInt(f[1], 10, 64)
		counts[f[2]] = [2]int64{a, d}
	}
	want := &Summary{ReviewDigest: got.ReviewDigest, Base: got.Base, Tip: got.Tip, Files: len(paths)}
	for _, p := range paths {
		o, hasOld := old[p]
		c, hasCur := cur[p]
		row := PathSummary{Path: p}
		switch {
		case !hasOld:
			row.Status = 'A'
		case !hasCur:
			row.Status = 'D'
		case modeKind(o.mode) != modeKind(c.mode):
			row.Status = 'T'
		default:
			row.Status = 'M'
		}
		if hasOld {
			row.OldMode = mustOctal(o.mode)
			if o.mode != "160000" {
				row.OldSize = o.size
			}
			row.Binary = row.Binary || hasNUL(o)
		}
		if hasCur {
			row.NewMode = mustOctal(c.mode)
			if c.mode != "160000" {
				row.NewSize = c.size
				if !hasOld || o.id != c.id {
					want.NewBytes += c.size
				}
			}
			row.Binary = row.Binary || hasNUL(c)
		}
		if !row.Binary && o.mode != "160000" && c.mode != "160000" {
			row.Added, row.Removed = counts[p][0], counts[p][1]
		}
		want.Added += row.Added
		want.Removed += row.Removed
		want.Paths = append(want.Paths, row)
		if row.Binary {
			want.Binary = append(want.Binary, p)
		}
		if o.mode == "120000" || c.mode == "120000" {
			want.Symlinks = append(want.Symlinks, p)
		}
		if hasOld && hasCur && o.mode != c.mode {
			want.ModeChanges = append(want.ModeChanges, p)
		}
		if o.mode == "160000" || c.mode == "160000" {
			want.Submodules = append(want.Submodules, p)
		}
		if OddPath(p) != "" {
			want.OddPaths = append(want.OddPaths, p)
		}
	}
	if !reflect.DeepEqual(got, want) {
		if len(got.Paths) == len(want.Paths) {
			for i := range got.Paths {
				if !reflect.DeepEqual(got.Paths[i], want.Paths[i]) {
					t.Errorf("row %d differs:\n got  %+v\n want %+v", i, got.Paths[i], want.Paths[i])
					break
				}
			}
		}
		t.Errorf("the summary differs from the independent computation:\n got  %+v\n want %+v", brief(got), brief(want))
	}
}

func brief(s *Summary) Summary { c := *s; c.Paths = nil; return c }

func modeKind(mode string) string {
	switch mode {
	case "120000", "160000":
		return mode
	}
	return "file"
}

func mustOctal(s string) uint32 {
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		panic(err)
	}
	return uint32(n)
}
