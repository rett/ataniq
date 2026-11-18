package card

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// line renders a lead text wide enough to hold all of it.
func line(t LeadText) string {
	s, _ := t.Line(1 << 20)
	return s
}

func lines(ts []LeadText) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = line(t)
	}
	return out
}

// The sample record gives every section of the card, in the order the
// model's fields have.
func TestNewBuildsEverySection(t *testing.T) {
	p := validPending(t)
	c := mustCard(t, p, Inputs{})

	wantTitle := Title{
		ID: "ap-20261120-0007", Project: "project-a",
		Subject: Subject{Kind: "seat", Role: "role-a", Epoch: 7, SeatRev: 12,
			HolderKind: "lead", DisplayID: "role-a@e7", Runtime: "runtime-a"},
		Asked: asked, Expires: expires,
	}
	if c.Title != wantTitle {
		t.Errorf("title: got %+v, want %+v", c.Title, wantTitle)
	}
	wantVerb := VerbLine{Name: "feature finish", Feature: "sample-feature", Target: "develop", Class: "ref-move"}
	if c.Verb != wantVerb {
		t.Errorf("verb line: got %+v, want %+v", c.Verb, wantVerb)
	}
	if c.Plan.Token != "h-3fa9c2e1" || c.Plan.SHA256 != p.Binding.PlanSHA256 {
		t.Errorf("plan: got token %q and digest %q", c.Plan.Token, c.Plan.SHA256)
	}
	wantPlan := []string{
		"merge feature/sample-feature (F 8281895) into develop (D 93c8d68) --no-ff",
		"result M 0194daf   built by the host",
	}
	if got := lines(c.Plan.Lines); !reflect.DeepEqual(got, wantPlan) {
		t.Errorf("plan lines: got %q, want %q", got, wantPlan)
	}
	if len(c.Refs) != 1 {
		t.Fatalf("got %d ref lines, want 1", len(c.Refs))
	}
	if r := c.Refs[0]; r.Kind != "ref" || line(r.Ref) != "refs/heads/develop" || r.Old != idD || r.New != idM || r.Deletes || r.MergedInto != nil {
		t.Errorf("ref line: got %+v", r)
	}
	if c.Merge == nil || c.Merge.Commit != idM || c.Merge.TargetTip() != idD || c.Merge.FeatureTip() != idF || c.Merge.BuiltBy != "host" {
		t.Errorf("merge: got %+v", c.Merge)
	}
	d := c.Diff
	if d == nil {
		t.Fatal("the card has no diff summary")
	}
	if d.ReviewDigest != p.Binding.Summary.ReviewDigest || d.Base != idD || d.Tip != idM ||
		d.Files != 2 || d.Added != 305 || d.Removed != 1 || d.NewBytes != 9240 || d.Truncated {
		t.Errorf("diff summary: got %+v", d)
	}
	if d.TruncatedLine() != "" {
		t.Errorf("a whole path list says it is cut: %q", d.TruncatedLine())
	}
	wantExec := ExecConfig{Matcher: "execconfig/1", SHA256: p.Binding.Summary.ExecConfigSet.SHA256}
	if d.ExecConfig != wantExec {
		t.Errorf("rule set: got %+v, want %+v", d.ExecConfig, wantExec)
	}
	wantRows := []Row{
		{Path: NewLeadText("lib/seat_read.py"), Status: "A", OldMode: "000000", NewMode: "100644", Added: 301, NewBytes: 9120},
		{Path: NewLeadText("Makefile"), Status: "M", OldMode: "100644", NewMode: "100644", Added: 4, Removed: 1, NewBytes: 120},
	}
	if !reflect.DeepEqual(d.Rows, wantRows) {
		t.Errorf("rows: got %+v, want %+v", d.Rows, wantRows)
	}
	wantFlagged := []FlaggedPath{{
		Kind: "exec-config", Path: NewLeadText("Makefile"), Pattern: NewLeadText("Makefile"),
		Reason: "matches an execution-config rule",
	}}
	if !reflect.DeepEqual(c.Flagged, wantFlagged) {
		t.Errorf("flagged: got %+v, want %+v", c.Flagged, wantFlagged)
	}
	if got, want := c.Flagged[0].SignOff(), (record.SignOff{Kind: "exec-config", Path: "Makefile"}); got != want {
		t.Errorf("sign-off: got %+v, want %+v", got, want)
	}
	wantEvidence := []Evidence{{Kind: "suite", RecordID: "v-0412", RecordSHA256: p.Binding.Evidence[0].RecordSHA256}}
	if !reflect.DeepEqual(c.Evidence, wantEvidence) {
		t.Errorf("evidence: got %+v, want %+v", c.Evidence, wantEvidence)
	}
	if line(c.Note) != "Wave 1 task merged." {
		t.Errorf("note: got %q", line(c.Note))
	}
	if c.BindingSHA256 != p.BindingSHA256 {
		t.Errorf("binding digest: got %q, want %q", c.BindingSHA256, p.BindingSHA256)
	}
	if c.Differs != nil || c.After != (After{}) || c.Verdicts != nil || c.Ended() || c.Decided() {
		t.Errorf("a new card has something after the decision: %+v", c)
	}

	// The fields stand in the order the sections are drawn.
	var names []string
	for i := 0; i < reflect.TypeOf(c).NumField(); i++ {
		names = append(names, reflect.TypeOf(c).Field(i).Name)
	}
	want := "Title Verb Plan Refs Merge Diff Lists Flagged Evidence BindingSHA256 Note Differs After Verdicts"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("the card's fields are %q, want %q", got, want)
	}
}

// Each of the four lists is on the card, in its order, also when it is
// empty.
func TestTheFourListsAreShownEvenWhenEmpty(t *testing.T) {
	c := mustCard(t, validPending(t), Inputs{})
	if len(c.Lists) != NumLists || NumLists != 4 {
		t.Fatalf("got %d lists, want 4", len(c.Lists))
	}
	for i, want := range []string{"Binary", "Symlinks", "Mode changes", "Submodules"} {
		l := c.Lists[i]
		if int(l.Kind) != i || l.Kind.Label() != want || l.Paths == nil || len(l.Paths) != 0 {
			t.Errorf("list %d: got kind %d (%q) with %d paths (nil: %v), want the empty list %q",
				i, l.Kind, l.Kind.Label(), len(l.Paths), l.Paths == nil, want)
		}
	}
	if ListKind(4).Label() != "" || ListKind(-1).Label() != "" {
		t.Error("a value that names no list has a label")
	}

	raw := rawPending()
	s := raw.Binding.Summary
	s.Binary = []record.Text{"img/a.png", "img/b.png"}
	s.Symlinks = []record.Text{"link"}
	s.ModeChanges = []record.Text{"run.sh"}
	s.Submodules = []record.Text{"vendor/x", "vendor/y", "vendor/z"}
	c = mustCard(t, seal(t, raw), Inputs{})
	want := [][]string{{"img/a.png", "img/b.png"}, {"link"}, {"run.sh"}, {"vendor/x", "vendor/y", "vendor/z"}}
	for i := range want {
		if got := lines(c.Lists[i].Paths); !reflect.DeepEqual(got, want[i]) {
			t.Errorf("list %d: got %q, want %q", i, got, want[i])
		}
	}
}

// The plan block is the record's plan text, word for word.
func TestThePlanBlockIsVerbatim(t *testing.T) {
	for _, plan := range []string{
		"one line",
		"one line\n",
		"two\nlines\n",
		"a blank line\n\nand a trailing blank\n\n",
		"  leading spaces and a tab\there\n",
		"a carriage return\r\nstays in its line\n",
		"\x1b[31mnot the host's\x1b[0m\n",
		"",
	} {
		raw := rawPending()
		raw.PlanText = record.Text(plan)
		c := mustCard(t, seal(t, raw), Inputs{})
		var got []string
		for _, l := range c.Plan.Lines {
			got = append(got, l.bytes())
		}
		if joined := strings.Join(got, "\n"); joined != strings.TrimSuffix(plan, "\n") {
			t.Errorf("plan %q: the lines join to %q", plan, joined)
		}
		if plan == "" && len(c.Plan.Lines) != 0 {
			t.Errorf("an empty plan has %d lines", len(c.Plan.Lines))
		}
		checkNothingRaw(t, c)
	}
}

// A prune has ref lines that delete, each with the branch it is merged
// into, and no feature, target, merge, diff or list.
func TestPruneCard(t *testing.T) {
	c := mustCard(t, prunePending(t), Inputs{})
	if c.Verb != (VerbLine{Name: "branch prune", Class: "ref-move"}) {
		t.Errorf("verb line: got %+v", c.Verb)
	}
	if c.Merge != nil || c.Diff != nil || c.Lists != nil || c.Flagged != nil || len(c.Evidence) != 0 {
		t.Errorf("a prune has a merge, a diff, lists, flags or evidence: %+v", c)
	}
	if len(c.Refs) != 2 {
		t.Fatalf("got %d ref lines, want 2", len(c.Refs))
	}
	for i, want := range []string{"refs/heads/feature/one", "refs/heads/feature/two"} {
		r := c.Refs[i]
		if line(r.Ref) != want || !r.Deletes || r.MergedInto == nil ||
			line(r.MergedInto.Ref) != "refs/heads/develop" || r.MergedInto.Tip != idM {
			t.Errorf("ref line %d: got %+v", i, r)
		}
	}
}

// A record whose feature name is outside the grammar gives feature-name
// and no card, whatever else is wrong with it.
func TestNewRefusesAFeatureNameOutsideTheGrammar(t *testing.T) {
	for _, argv := range [][]string{
		{"Sample"}, {"feature/x"}, {""}, {"a b"}, {"\x1b[2J"}, {"näme"},
		{strings.Repeat("a", 49)}, {"one", "two"}, {}, nil,
	} {
		raw := rawPending()
		raw.Binding.Argv = argv
		c, err := New(seal(t, raw), Inputs{})
		wantCode(t, fmt.Sprintf("argv %q", argv), err, record.CodeFeatureName)
		if !reflect.DeepEqual(c, Card{}) {
			t.Errorf("argv %q: a card came with the refusal", argv)
		}

		// Not sealed, and with a broken form as well: the name still
		// has its own code.
		raw.Schema = "other"
		raw.BindingSHA256 = "not a digest"
		_, err = New(raw, Inputs{})
		wantCode(t, fmt.Sprintf("argv %q in a broken record", argv), err, record.CodeFeatureName)
	}
}

// Names inside the grammar that are shaped to mislead make a card, and the
// name stands in the one field for it and nowhere else.
func TestMisleadingFeatureNamesStayNames(t *testing.T) {
	for _, name := range []string{"approve", "a-then-code", "develop", "sample-feature", "x", strings.Repeat("a", 48)} {
		raw := rawPending()
		raw.Binding.Argv = []string{name}
		raw.PlanText = "the plan\n"
		raw.Note = ""
		c := mustCard(t, seal(t, raw), Inputs{})
		if c.Verb.Feature != name {
			t.Errorf("name %q: the verb line has %q", name, c.Verb.Feature)
		}
		for path, value := range dump(c) {
			// The name stands in its own field. A name that is also
			// the target's leaves the target's field as it is.
			if path == "Verb.Feature" || (path == "Verb.Target" && name == "develop") {
				continue
			}
			if value == "string:"+name || value == "lead:"+name {
				t.Errorf("name %q: it is also the value of %s", name, path)
			}
		}
		if c.Item().Feature != name {
			t.Errorf("name %q: the list row has %q", name, c.Item().Feature)
		}
		// A name equal to the target changes neither the target nor
		// what the keys say.
		if c.Verb.Target != "develop" {
			t.Errorf("name %q: the target is %q", name, c.Verb.Target)
		}
		checkNothingRaw(t, c)
	}

	// Two cards may carry the same name: the id and the digest tell them
	// apart.
	first := mustCard(t, validPending(t), Inputs{})
	second := mustCard(t, seal(t, laterPending()), Inputs{})
	if first.Verb.Feature != second.Verb.Feature {
		t.Fatal("the two sample records are for two features")
	}
	if first.Title.ID == second.Title.ID || first.BindingSHA256 == second.BindingSHA256 {
		t.Error("two cards with the same feature name cannot be told apart")
	}
}

// The two digests of the record are checked again.
func TestNewChecksTheDigestsAgain(t *testing.T) {
	changes := map[string]func(p *record.Pending){
		"the project":         func(p *record.Pending) { p.Binding.Project = "project-b" },
		"the plan text":       func(p *record.Pending) { p.PlanText += "and one more thing\n" },
		"a path":              func(p *record.Pending) { p.Binding.Summary.Paths[0].Path = "lib/other.py" },
		"a flag":              func(p *record.Pending) { p.Binding.Summary.Flagged = []record.Flag{} },
		"the expiry":          func(p *record.Pending) { p.Binding.PendingExpiresAt = "2027-01-01T00:00:00Z" },
		"the truncation flag": func(p *record.Pending) { p.Binding.Summary.PathsTruncated = true },
		"the binding digest":  func(p *record.Pending) { p.BindingSHA256 = record.Digest([]byte("another binding")) },
		"the plan digest":     func(p *record.Pending) { p.Binding.PlanSHA256 = record.Digest([]byte("another plan")) },
		"the feature name":    func(p *record.Pending) { p.Binding.Argv = []string{"develop"} },
	}
	for name, change := range changes {
		p := validPending(t)
		change(&p)
		c, err := New(p, Inputs{})
		wantCode(t, "a record changed in "+name, err, record.CodeBindingMismatch)
		if !reflect.DeepEqual(c, Card{}) {
			t.Errorf("%s: a card came with the refusal", name)
		}
	}
	// The note is outside the binding: changing it is no mismatch.
	p := validPending(t)
	p.Note = "another note"
	if _, err := New(p, Inputs{}); err != nil {
		t.Errorf("a changed note is refused: %v", err)
	}
}

// New holds every rule of the decoder, for a record that never went
// through it.
func TestNewHoldsTheDecodersRules(t *testing.T) {
	cases := []struct {
		name   string
		change func(p *record.Pending)
		want   record.Code
	}{
		{"another schema", func(p *record.Pending) { p.Schema = "other/1" }, record.CodeRecordInvalid},
		{"an id outside the grammar", func(p *record.Pending) { p.ID = "ap-UPPER" }, record.CodeRecordInvalid},
		{"no holder", func(p *record.Pending) { p.Binding.Subject.Holder = nil }, record.CodeRecordInvalid},
		{"another verb", func(p *record.Pending) { p.Binding.Verb = "release" }, record.CodeVerbNotRequestable},
		{"no merge", func(p *record.Pending) { p.Binding.Merge = nil }, record.CodeRecordInvalid},
		{"no summary", func(p *record.Pending) { p.Binding.Summary = nil }, record.CodeRecordInvalid},
		{"no evidence", func(p *record.Pending) { p.Binding.Evidence = nil }, record.CodeRecordInvalid},
		{"a time with an offset", func(p *record.Pending) { p.Binding.RequestedAt = "2026-11-20T14:02:04+01:00" }, record.CodeRecordInvalid},
		{"an effect that is not the merge", func(p *record.Pending) { p.Binding.Effects[0].New = idF }, record.CodeRecordInvalid},
		{"a control character in the project", func(p *record.Pending) { p.Binding.Project = "project\x1b[2J" }, record.CodeRecordInvalid},
		{"a note over its cap", func(p *record.Pending) { p.Note = record.Text(strings.Repeat("n", record.MaxNoteBytes+1)) }, record.CodeRecordTooLarge},
		{"a record over its cap", func(p *record.Pending) {
			p.PlanText = record.Text(strings.Repeat("p", record.MaxPendingBytes))
		}, record.CodeRecordTooLarge},
		{"one line of 1 MiB as a path", func(p *record.Pending) {
			p.Binding.Summary.Paths[0].Path = record.Text(strings.Repeat("x", 1<<20))
		}, record.CodeRecordTooLarge},
	}
	for _, tc := range cases {
		raw := rawPending()
		tc.change(&raw)
		c, err := New(seal(t, raw), Inputs{})
		wantCode(t, tc.name, err, tc.want)
		if !reflect.DeepEqual(c, Card{}) {
			t.Errorf("%s: a card came with the refusal", tc.name)
		}
	}
}

// A string the host wrote stands on the model as a plain string, so a
// record in which one would need escaping makes no card. The record's own
// rules let these through: they refuse control characters only.
func TestNewRefusesHostStringsThatWouldNeedEscaping(t *testing.T) {
	const override = "\u202e" // a direction override: valid UTF-8, no control character
	fields := map[string]func(p *record.Pending, s string){
		"project":      func(p *record.Pending, s string) { p.Binding.Project = s },
		"role_name":    func(p *record.Pending, s string) { p.Binding.Subject.RoleName = s },
		"holder.kind":  func(p *record.Pending, s string) { p.Binding.Subject.Holder.Kind = s },
		"display_id":   func(p *record.Pending, s string) { p.Binding.Subject.Holder.DisplayID = s },
		"runtime":      func(p *record.Pending, s string) { p.Binding.Subject.Holder.Runtime = s },
		"plan_token":   func(p *record.Pending, s string) { p.Binding.PlanToken = s },
		"record_id":    func(p *record.Pending, s string) { p.Binding.Evidence[0].RecordID = s },
		"matcher":      func(p *record.Pending, s string) { p.Binding.Summary.ExecConfigSet.Matcher = s },
		"the target":   func(p *record.Pending, s string) { p.Binding.Effects[0].Ref = "refs/heads/" + s },
		"flag pattern": nil, // a pattern is lead text; see below
	}
	for name, set := range fields {
		if set == nil {
			continue
		}
		for _, hostile := range []string{"x" + override + "y", "zero\u200bwidth", `back\slash`, "sep\u2028arator"} {
			raw := rawPending()
			set(&raw, hostile)
			p := seal(t, raw)
			if _, err := record.EncodePending(p); err != nil {
				t.Fatalf("%s %q: the record's own rules refuse it: %v", name, hostile, err)
			}
			_, err := New(p, Inputs{})
			wantCode(t, fmt.Sprintf("%s %q", name, hostile), err, record.CodeRecordInvalid)
			if err != nil && (strings.Contains(err.Error(), hostile) || !hostText(err.Error())) {
				t.Errorf("%s: the refusal holds the text it refuses: %q", name, err.Error())
			}
		}
		// Text the escaping leaves alone is fine, non-ASCII included.
		raw := rawPending()
		set(&raw, "naïve-名前")
		if _, err := New(seal(t, raw), Inputs{}); err != nil {
			t.Errorf("%s: a printable name is refused: %v", name, err)
		}
	}

	// The same bytes in lead text make a card, and are escaped there.
	raw := rawPending()
	raw.Binding.Summary.Flagged[0].Pattern = "Make" + override + "file"
	raw.Binding.Summary.Flagged[0].Path = record.Text("Make" + override + "file")
	raw.Note = record.Text("note" + override)
	c := mustCard(t, seal(t, raw), Inputs{})
	if got := line(c.Flagged[0].Pattern); got != `Make\u{202E}file` {
		t.Errorf("pattern: got %q", got)
	}
	checkNothingRaw(t, c)
}

// A summary of 5,000 paths that the record holds only the first part of
// says so on the model.
func TestATruncatedSummarySaysSo(t *testing.T) {
	p, held := truncatedPending(t)
	c := mustCard(t, p, Inputs{})
	if !c.Diff.Truncated || c.Diff.Files != 5000 || len(c.Diff.Rows) != held {
		t.Fatalf("got truncated %v, %d files and %d rows", c.Diff.Truncated, c.Diff.Files, len(c.Diff.Rows))
	}
	want := fmt.Sprintf("the path list is cut: the card holds the first %d of 5000 paths, and the rest is read in pages", held)
	if got := c.Diff.TruncatedLine(); got != want {
		t.Errorf("the line is %q, want %q", got, want)
	}
	// The rows are the first of the list, in the record's order.
	if got := line(c.Diff.Rows[held-1].Path); got != fmt.Sprintf("pkg/dir-%02d/file-%04d.go", (held-1)%40, held-1) {
		t.Errorf("the last row is %q", got)
	}
}

// What came after the decision is on the card only through the inputs, and
// the verdicts only when the card is decided.
func TestInputsAfterTheDecision(t *testing.T) {
	p := validPending(t)
	verdicts := &VerdictBlock{}
	decidedStates := map[record.State]bool{
		record.StateApproved: true, record.StateApprovedByRule: true, record.StateApprovedByGrant: true,
		record.StateDenied: true, record.StateConsumed: true, record.StateApplied: true,
		record.StateNotApplied: true, record.StateUnknown: true,
	}
	all := []record.State{
		"", record.StatePending, record.StateApproved, record.StateApprovedByRule, record.StateApprovedByGrant,
		record.StateDenied, record.StateWithdrawn, record.StateExpired, record.StateVoid, record.StateConsumed,
		record.StateApplied, record.StateNotApplied, record.StateUnknown,
	}
	for _, state := range all {
		for _, held := range []bool{false, true} {
			c := mustCard(t, p, Inputs{State: state, Held: held, Line: "applied: develop 93c8d68 -> 0194daf", Verdicts: verdicts})
			want := After{State: state, Held: held, Line: "applied: develop 93c8d68 -> 0194daf"}
			if c.After != want {
				t.Errorf("state %q: got %+v, want %+v", state, c.After, want)
			}
			if c.Decided() != decidedStates[state] {
				t.Errorf("state %q: decided is %v", state, c.Decided())
			}
			if wantEnded := state != "" && state != record.StatePending; c.Ended() != wantEnded {
				t.Errorf("state %q: ended is %v", state, c.Ended())
			}
			if (c.Verdicts != nil) != decidedStates[state] {
				t.Errorf("state %q: the card has verdicts: %v", state, c.Verdicts != nil)
			}
			if c.Verdicts != nil && c.Verdicts != verdicts {
				t.Errorf("state %q: the verdicts are not the ones handed in", state)
			}
			if item := c.Item(); item.State != state || item.Held != held || item.Verdicts != c.Verdicts || item.Line != want.Line {
				t.Errorf("state %q: the list row has %+v", state, item)
			}
		}
	}
	// A held card is pending again: no verdict may steer the second
	// decision.
	if c := mustCard(t, p, Inputs{Held: true, Verdicts: verdicts}); c.Verdicts != nil || c.Ended() {
		t.Error("a held card shows verdicts or has ended")
	}
	if c := mustCard(t, p, Inputs{State: record.StateApplied}); c.Verdicts != nil {
		t.Error("a decided card has verdicts that nobody handed in")
	}

	_, err := New(p, Inputs{State: "held"})
	wantCode(t, "a state outside the list", err, record.CodeRecordInvalid)
	for _, bad := range []string{"applied\x1b[2J", "applied \u202e", "a\nb", `back\slash`, "\xff"} {
		_, err = New(p, Inputs{State: record.StateApplied, Line: bad})
		wantCode(t, fmt.Sprintf("the outcome line %q", bad), err, record.CodeRecordInvalid)
	}
}

func TestItem(t *testing.T) {
	p := validPending(t)
	got := mustCard(t, p, Inputs{}).Item()
	want := Item{
		ID: "ap-20261120-0007", BindingSHA256: p.BindingSHA256, Project: "project-a",
		Verb: "feature finish", Feature: "sample-feature", Asked: asked, Expires: expires,
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if prune := mustCard(t, prunePending(t), Inputs{}).Item(); prune.Feature != "" || prune.Verb != "branch prune" {
		t.Errorf("a prune's row: %+v", prune)
	}
}

// RowOf makes the row of a page's path as New makes the card's own rows.
func TestRowOf(t *testing.T) {
	p := validPending(t)
	c := mustCard(t, p, Inputs{})
	for i, path := range p.Binding.Summary.Paths {
		if got := RowOf(path); !reflect.DeepEqual(got, c.Diff.Rows[i]) {
			t.Errorf("row %d: got %+v, want %+v", i, got, c.Diff.Rows[i])
		}
	}
	checkNothingRaw(t, RowOf(record.PathChange{Path: "evil\x1b]0;title\x07\xff", Status: "A"}))
}

func TestPageKinds(t *testing.T) {
	if PagePaths == 0 || PagePatch == 0 || PagePaths == PagePatch {
		t.Error("the two kinds of page are not two values other than zero")
	}
	if PagePaths.String() != "paths" || PagePatch.String() != "patch" || PageKind(0).String() != "unknown" {
		t.Error("a kind of page has the wrong name")
	}
}

// The reason of a flag is the host's words for its kind, and nothing else.
func TestFlagReasons(t *testing.T) {
	seen := map[string]bool{}
	for _, kind := range []string{record.FlagExecConfig, record.FlagSymlink, record.FlagSubmodule, record.FlagAliased, record.FlagHistoryOnly} {
		raw := rawPending()
		raw.Binding.Summary.Flagged = []record.Flag{{Kind: kind, Path: "some/path", Pattern: ""}}
		c := mustCard(t, seal(t, raw), Inputs{})
		reason := c.Flagged[0].Reason
		if reason == "" || reason == "flagged" || seen[reason] || !hostText(reason) {
			t.Errorf("kind %s: the reason is %q", kind, reason)
		}
		seen[reason] = true
		if c.Flagged[0].Kind != kind {
			t.Errorf("kind %s: the card has %q", kind, c.Flagged[0].Kind)
		}
	}
}
