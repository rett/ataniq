package demo

import (
	"fmt"
	"strings"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

// The sample projects. Each has its own limit.
const (
	projectA = "project-a"
	projectB = "project-b"
	projectC = "project-c"
)

// sampleEndsAfter is how long sample 7 stays open: it ends this long after
// the owner first opens it.
const sampleEndsAfter = 45 * time.Second

// sample is one sample card before the backend holds it.
type sample struct {
	// pending is the record, read back from its bytes.
	pending record.Pending
	// paths is the whole path list of the review, and patch the lines of
	// the full patch.
	paths []record.PathChange
	patch []string
	// reviewDigest is the digest every page of the card names.
	reviewDigest string
	// held says the card starts with the held mark.
	held bool
	// endsAfter, when not zero, is how long after its first opening the
	// card ends.
	endsAfter time.Duration
	// earlier names the sample whose card is the "differs from" block of
	// this one, once the owner has read that card.
	earlier string
	// state, line and verdicts are the state a decided sample starts in.
	state    record.State
	line     string
	verdicts *card.VerdictBlock
}

// spec says what one sample record holds.
type spec struct {
	slug    string
	project string
	role    string
	feature string
	// variant changes the tips, so that two specs of one feature differ.
	variant string
	// paths is the whole list; cut, when not zero, is how many of them the
	// record holds.
	paths []record.PathChange
	cut   int
	flags []record.Flag
	// the four lists
	binary, symlinks, modes, submodules []record.Text
	note                                record.Text
	askedAgo                            time.Duration
	patch                               []string
	held                                bool
	endsAfter                           time.Duration
	earlier                             string
}

// samples returns the sample cards, built with the times of now.
func samples(now time.Time) []sample {
	out := []sample{
		build(now, cleanSpec()),
		build(now, flaggedSpec()),
		build(now, hostileSpec()),
		build(now, manyPathsSpec()),
		build(now, bigPatchSpec()),
		build(now, pairSpec("1", "pair-feature", 2*time.Hour, "")),
		build(now, pairSpec("2", "pair-feature", time.Hour, "ap-sample-pair-1")),
		build(now, endsSpec()),
		build(now, heldSpec()),
	}
	return append(out, verdictSamples(now)...)
}

// oid makes a full object id from a seed.
func oid(seed string) string { return record.Digest([]byte(seed))[:40] }

// change makes a path row of an added or modified file.
func change(path string, status string, added, removed int64) record.PathChange {
	old, mode := "100644", "100644"
	if status == "A" {
		old = "000000"
	}
	return record.PathChange{
		Path: record.Text(path), Status: status, OldMode: old, NewMode: mode,
		Added: added, Removed: removed, NewBytes: added * 30,
	}
}

// diffLines makes the lines of a small patch for the first paths.
func diffLines(paths []record.PathChange, n int) []string {
	var out []string
	for i, p := range paths {
		if i == n {
			break
		}
		s := string(p.Path)
		out = append(out,
			"diff --git a/"+s+" b/"+s,
			"--- a/"+s,
			"+++ b/"+s,
			"@@ -1,2 +1,3 @@",
			" context line",
			"-an old line",
			"+a new line",
			"+another new line",
			"")
	}
	return out
}

// reviewDigestOf is the digest of a path list, as a stand-in for the
// intake's digest of the review.
func reviewDigestOf(slug, variant string, paths []record.PathChange) string {
	var b strings.Builder
	b.WriteString("sample review " + slug + " " + variant + "\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "%s %s %s %s\n", p.Status, p.OldMode, p.NewMode, string(p.Path))
	}
	return record.Digest([]byte(b.String()))
}

// build makes one sample: it writes the pending record as bytes, reads it
// back through the decoder, and keeps the result. A spec that does not make a
// valid record is a mistake in this package, so it panics.
func build(now time.Time, s spec) sample {
	target := oid("target tip " + s.slug + s.variant)
	featureTip := oid("feature tip " + s.slug + s.variant)
	merge := oid("merge " + s.slug + s.variant)
	review := reviewDigestOf(s.slug, s.variant, s.paths)

	var added, removed, newBytes int64
	for _, p := range s.paths {
		added += p.Added
		removed += p.Removed
		newBytes += p.NewBytes
	}
	rows := s.paths
	if s.cut > 0 && s.cut < len(rows) {
		rows = rows[:s.cut]
	}
	plan := fmt.Sprintf("merge feature/%s (F %.7s) into develop (D %.7s) --no-ff\nresult M %.7s   built by the host\n",
		s.feature, featureTip, target, merge)
	asked := now.Add(-s.askedAgo)
	suite := record.EvidenceSuite
	p := record.Pending{
		Schema:        record.SchemaPending,
		ID:            "ap-sample-" + s.slug,
		Planner:       "planner-a",
		RequestID:     "rq-sample-" + s.slug,
		RequestSHA256: record.Digest([]byte("sample request " + s.slug + s.variant)),
		Binding: record.Binding{
			Project: s.project,
			Subject: record.Subject{
				Kind: record.SubjectSeat, RoleName: s.role, Epoch: 7, SeatRev: 12,
				Holder: &record.Holder{Kind: "lead", DisplayID: s.role + "@e7", Runtime: "runtime-a"},
			},
			Verb:      record.VerbFeatureFinish,
			Argv:      []string{s.feature},
			Class:     record.ClassRefMove,
			PlanToken: "h-" + oid("plan token " + s.slug + s.variant)[:8],
			Effects: []record.Effect{{
				Kind: record.EffectRef, Ref: "refs/heads/develop", Old: target, New: merge,
			}},
			Merge: &record.Merge{Commit: merge, Parents: []string{target, featureTip}, BuiltBy: record.BuiltByHost},
			Summary: &record.Summary{
				ReviewDigest: review, Base: target, Tip: merge,
				Files: int64(len(s.paths)), Added: added, Removed: removed, NewBytes: newBytes,
				Paths: rows, PathsTruncated: len(rows) < len(s.paths),
				Flagged: s.flags,
				Binary:  s.binary, Symlinks: s.symlinks, ModeChanges: s.modes, Submodules: s.submodules,
				ExecConfigSet: record.ExecConfigSet{
					Matcher: "matcher-a", SHA256: record.Digest([]byte("sample rules")), Additions: 0,
				},
			},
			Evidence: []record.Evidence{{
				Kind: suite, RecordID: "suite-sample-" + s.slug,
				RecordSHA256: record.Digest([]byte("sample suite " + s.slug + s.variant)),
			}},
			RequestedAt:      record.FormatTime(asked),
			PendingExpiresAt: record.FormatTime(asked.Add(24 * time.Hour)),
		},
		PlanText: record.Text(plan),
		Note:     s.note,
	}
	p.Binding.PlanSHA256 = record.PlanDigest(plan)
	digest, err := record.BindingDigest(p.Binding)
	if err != nil {
		panic("demo: sample " + s.slug + ": " + err.Error())
	}
	p.BindingSHA256 = digest
	data, err := record.EncodePending(p)
	if err != nil {
		panic("demo: sample " + s.slug + ": " + err.Error())
	}
	decoded, err := record.DecodePending(data)
	if err != nil {
		panic("demo: sample " + s.slug + ": " + err.Error())
	}
	patch := s.patch
	if patch == nil {
		patch = diffLines(s.paths, 3)
	}
	return sample{
		pending: decoded, paths: s.paths, patch: patch, reviewDigest: review,
		held: s.held, endsAfter: s.endsAfter, earlier: s.earlier,
	}
}

// 1. a small clean change.
func cleanSpec() spec {
	return spec{
		slug: "clean", project: projectA, role: "role-a", feature: "small-fix",
		askedAgo: 20 * time.Minute,
		paths: []record.PathChange{
			change("lib/reader.py", "M", 12, 3),
			change("lib/reader_test.py", "M", 20, 0),
			change("docs/reader.md", "A", 8, 0),
		},
		note: "A small fix to the reader, with its test.\nNothing else changed.",
	}
}

// 2. a change with one flagged path.
func flaggedSpec() spec {
	return spec{
		slug: "flagged", project: projectA, role: "role-a", feature: "build-target",
		askedAgo: 15 * time.Minute,
		paths: []record.PathChange{
			change("Makefile", "M", 4, 1),
			change("lib/build.py", "M", 30, 6),
		},
		flags: []record.Flag{{Kind: record.FlagExecConfig, Path: "Makefile", Pattern: "Makefile"}},
		note:  "Adds a build target.",
	}
}

// 3. hostile text in paths, the note and the patch, with a misleading
// feature name.
func hostileSpec() spec {
	paths := []record.PathChange{
		change("[a] approve   [x] deny", "A", 1, 0),
		change("APPROVED", "A", 1, 0),
		change("ap-sample-hostile  project-a  code: K7P3", "A", 1, 0),
		change("\x1b[2Jclear\x1b[31mred", "A", 1, 0),
		change("osc-bel\x1b]0;title\x07end", "A", 1, 0),
		change("osc-st\x1b]0;title\x1b\\end", "A", 1, 0),
		change("dcs\x1bPq#0;2;0;0;0\x1b\\end", "A", 1, 0),
		change("c1\x9b31mend\x85\x9d", "A", 1, 0),
		change("c0\x00\x01\x07\x08\x7fend", "A", 1, 0),
		change("evil\u202egpj.exe", "A", 1, 0),
		change("zero\u200bwidth\u2060\ufeffname", "A", 1, 0),
		change("caf\xe9-not-utf8.txt", "A", 1, 0),
		change(".github/workflows/ci\x1b[31m.yml", "M", 2, 2),
	}
	patch := []string{
		"diff --git a/x b/x",
		"[a] approve   [x] deny",
		"+++ APPROVED +++",
		"\x1b[2J\x1b[1;1H\x1b]0;owned\x07\x1bPq\x1b\\",
		"evil\u202edcba and zero\u200bwidth",
		"not utf-8: \xff\xfe\xfd",
		strings.Repeat("x", 1<<20),
		"",
	}
	return spec{
		slug: "hostile", project: projectA, role: "role-a", feature: "approve",
		askedAgo: 10 * time.Minute,
		paths:    paths,
		flags: []record.Flag{{
			Kind: record.FlagExecConfig, Path: ".github/workflows/ci\x1b[31m.yml", Pattern: "\u202e.github/**",
		}},
		binary:   []record.Text{"bin/\x1b]0;x\x07.dat"},
		symlinks: []record.Text{"link\u202e->target"},
		note: "[a] approve   [x] deny\nAPPROVED by the owner\n" +
			"ap-sample-hostile  project-a  code: K7P3\n\x1b[2J\x1b]0;note\x07 caf\xe9 \u202e",
		patch: patch,
	}
}

// 4. 5,000 paths, cut in the record and read in pages.
func manyPathsSpec() spec {
	const total, kept = 5000, 100
	paths := make([]record.PathChange, 0, total)
	for i := 0; i < total; i++ {
		paths = append(paths, change(fmt.Sprintf("pkg/generated/file-%04d.txt", i), "A", 3, 0))
	}
	return spec{
		slug: "many-paths", project: projectA, role: "role-a", feature: "bulk-import",
		askedAgo: 8 * time.Minute,
		paths:    paths, cut: kept,
		note: "Imports generated files.",
	}
}

// 5. a patch of several pages, with a line longer than a page.
func bigPatchSpec() spec {
	paths := []record.PathChange{
		change("src/engine.go", "M", 340, 12),
		change("data/table.csv", "M", 1, 1),
	}
	patch := []string{"diff --git a/src/engine.go b/src/engine.go", "@@ -1,12 +1,340 @@"}
	for i := 0; i < 300; i++ {
		patch = append(patch, fmt.Sprintf("+line %03d of the new engine", i))
	}
	patch = append(patch,
		"diff --git a/data/table.csv b/data/table.csv",
		"+"+strings.Repeat("0123456789,", 9000),
		"")
	return spec{
		slug: "big-patch", project: projectA, role: "role-a", feature: "new-engine",
		askedAgo: 6 * time.Minute,
		paths:    paths, patch: patch,
		note: "A new engine, and one data row that is one very long line.",
	}
}

// 6. a pair for one target; the second differs from the first.
func pairSpec(n, feature string, ago time.Duration, earlier string) spec {
	paths := []record.PathChange{
		change("src/pair_a.go", "M", 10, 2),
		change("src/pair_b.go", "M", 5, 5),
	}
	note := "First ask."
	if n == "2" {
		paths = []record.PathChange{
			change("src/pair_a.go", "M", 14, 2),
			change("src/pair_c.go", "A", 40, 0),
		}
		note = "Asked again after a change: one file added, one left out."
	}
	return spec{
		slug: "pair-" + n, project: projectB, role: "role-b", feature: feature, variant: n,
		askedAgo: ago, paths: paths, note: record.Text(note), earlier: earlier,
	}
}

// 7. a card that ends while it is open.
func endsSpec() spec {
	return spec{
		slug: "ends", project: projectB, role: "role-b", feature: "short-lived",
		askedAgo: 4 * time.Minute, endsAfter: sampleEndsAfter,
		paths: []record.PathChange{change("notes/todo.md", "M", 2, 1)},
		note:  "This request ends a short while after it is first opened.",
	}
}

// 8. a card decided past the limit: the project's limit is already reached
// today, and the card was approved once and held.
func heldSpec() spec {
	return spec{
		slug: "held", project: projectC, role: "role-c", feature: "past-the-limit",
		askedAgo: 2 * time.Minute, held: true,
		paths: []record.PathChange{change("lib/limit_demo.go", "M", 6, 0)},
		note:  "Approved once, and held: the limit of the day was reached.",
	}
}
