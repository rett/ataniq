package card

import (
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

func TestDiffersFrom(t *testing.T) {
	earlier := validPending(t)
	now := seal(t, laterPending())
	readAt := asked.Add(time5min)

	d, err := DiffersFrom(earlier, now, readAt)
	if err != nil {
		t.Fatal(err)
	}
	data, err := record.EncodePending(earlier)
	if err != nil {
		t.Fatal(err)
	}
	want := Differs{
		EarlierID:     "ap-20261120-0007",
		EarlierSHA256: record.Digest(data),
		ReadAt:        readAt,
		Feature:       Change{Old: idF, New: idF2},
		Target:        Change{Old: idD, New: idD},
		Merge:         Change{Old: idM, New: idM2},
		PathsAdded:    []LeadText{NewLeadText("docs/seat.md")},
		PathsRemoved:  []LeadText{NewLeadText("Makefile")},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("got  %+v\nwant %+v", d, want)
	}
	if !d.Feature.Changed() || d.Target.Changed() || !d.Merge.Changed() {
		t.Errorf("changed: feature %v, target %v, merge %v", d.Feature.Changed(), d.Target.Changed(), d.Merge.Changed())
	}

	// New puts the same block on the card.
	c := mustCard(t, now, Inputs{Earlier: &Earlier{Record: earlier, ReadAt: readAt}})
	if c.Differs == nil || !reflect.DeepEqual(*c.Differs, want) {
		t.Errorf("the card's block is %+v", c.Differs)
	}
	if c := mustCard(t, now, Inputs{}); c.Differs != nil {
		t.Error("a card without an earlier one has the block")
	}
}

// The paths are compared by their bytes, each named once, in the order of
// the summary they stand in; a cut summary says that paths may be missing.
func TestDiffersFromPaths(t *testing.T) {
	path := func(s string) record.PathChange {
		return record.PathChange{Path: record.Text(s), Status: "M", OldMode: "100644", NewMode: "100644"}
	}
	old := rawPending()
	old.Binding.Summary.Flagged = []record.Flag{}
	old.Binding.Summary.Paths = []record.PathChange{path("z"), path("keep"), path("a\xff"), path("Name"), path("a")}
	cur := laterPending()
	cur.Binding.Summary.Paths = []record.PathChange{path("name"), path("keep"), path("a\xfe"), path("b\x1b[2J"), path("a")}

	d, err := DiffersFrom(seal(t, old), seal(t, cur), asked)
	if err != nil {
		t.Fatal(err)
	}
	raws := func(ts []LeadText) []string {
		var out []string
		for _, text := range ts {
			out = append(out, text.bytes())
		}
		return out
	}
	if got, want := raws(d.PathsAdded), []string{"name", "a\xfe", "b\x1b[2J"}; !reflect.DeepEqual(got, want) {
		t.Errorf("added: got %q, want %q", got, want)
	}
	if got, want := raws(d.PathsRemoved), []string{"z", "a\xff", "Name"}; !reflect.DeepEqual(got, want) {
		t.Errorf("removed: got %q, want %q", got, want)
	}
	if d.PathsPartial {
		t.Error("two whole summaries are called partial")
	}
	checkNothingRaw(t, d)

	// The same summary twice differs in no path.
	same := laterPending()
	same.Binding.Summary.Paths = old.Binding.Summary.Paths
	d, err = DiffersFrom(seal(t, old), seal(t, same), asked)
	if err != nil {
		t.Fatal(err)
	}
	if d.PathsAdded != nil || d.PathsRemoved != nil || d.RefsAdded != nil || d.RefsRemoved != nil {
		t.Errorf("the same paths differ: %+v", d)
	}

	for name, cut := range map[string]func(old, cur *record.Pending){
		"the earlier summary": func(old, cur *record.Pending) { old.Binding.Summary.PathsTruncated = true },
		"this summary":        func(old, cur *record.Pending) { cur.Binding.Summary.PathsTruncated = true },
	} {
		o, c := rawPending(), laterPending()
		cut(&o, &c)
		d, err := DiffersFrom(seal(t, o), seal(t, c), asked)
		if err != nil {
			t.Fatal(err)
		}
		if !d.PathsPartial {
			t.Errorf("%s is cut, and the block does not say that paths may be missing", name)
		}
	}
}

// Two prunes differ in the branches they delete.
func TestDiffersFromPrune(t *testing.T) {
	earlier := prunePending(t)
	cur := earlier
	cur.ID = "ap-20261120-0011"
	zeros := earlier.Binding.Effects[0].New
	cur.Binding.Effects = []record.Effect{
		earlier.Binding.Effects[1],
		{Kind: record.EffectRef, Ref: "refs/heads/feature/three\u202e", Old: idM2, New: zeros,
			MergedInto: &record.MergedInto{Ref: "refs/heads/develop", Tip: idM}},
	}
	cur = seal(t, cur)
	c := mustCard(t, cur, Inputs{Earlier: &Earlier{Record: earlier, ReadAt: asked}})
	d := c.Differs
	if d == nil {
		t.Fatal("no block")
	}
	if len(d.RefsAdded) != 1 || d.RefsAdded[0].bytes() != "refs/heads/feature/three\u202e" ||
		len(d.RefsRemoved) != 1 || d.RefsRemoved[0].bytes() != "refs/heads/feature/one" {
		t.Errorf("refs: added %v, removed %v", d.RefsAdded, d.RefsRemoved)
	}
	if d.Feature != (Change{}) || d.Target != (Change{}) || d.Merge != (Change{}) || d.PathsAdded != nil || d.PathsRemoved != nil || d.PathsPartial {
		t.Errorf("a prune's block has a merge or paths: %+v", d)
	}
	checkNothingRaw(t, c)
}

// A pair that is no "differs from" is refused, and so is an earlier record
// that is not a valid one.
func TestDiffersFromRefusals(t *testing.T) {
	now := seal(t, laterPending())
	cases := []struct {
		name   string
		change func(p *record.Pending)
		want   record.Code
	}{
		{"the record itself", func(p *record.Pending) { p.ID = now.ID }, record.CodeRecordInvalid},
		{"another project", func(p *record.Pending) { p.Binding.Project = "project-b" }, record.CodeRecordInvalid},
		{"another feature", func(p *record.Pending) { p.Binding.Argv = []string{"other-feature"} }, record.CodeRecordInvalid},
		{"another target", func(p *record.Pending) { p.Binding.Effects[0].Ref = "refs/heads/main" }, record.CodeRecordInvalid},
		{"a name outside the grammar", func(p *record.Pending) { p.Binding.Argv = []string{"Other"} }, record.CodeFeatureName},
		{"a broken form", func(p *record.Pending) { p.Schema = "other/1" }, record.CodeRecordInvalid},
		{"too large", func(p *record.Pending) { p.Note = record.Text(make([]byte, record.MaxNoteBytes+1)) }, record.CodeRecordTooLarge},
	}
	for _, tc := range cases {
		earlier := rawPending()
		tc.change(&earlier)
		earlier = seal(t, earlier)
		_, err := DiffersFrom(earlier, now, asked)
		wantCode(t, tc.name, err, tc.want)
		// A refused earlier record does not refuse the new card: the card
		// has no block and says so, with the code of the refusal.
		c, err := New(now, Inputs{Earlier: &Earlier{Record: earlier, ReadAt: asked}})
		if err != nil {
			t.Errorf("%s: New refused the card: %v", tc.name, err)
			continue
		}
		if c.Differs != nil {
			t.Errorf("%s: the card has a block", tc.name)
		}
		if want := DiffersRefusedLine + " (" + string(tc.want) + ")"; c.DiffersRefused != want {
			t.Errorf("%s: the card says %q, want %q", tc.name, c.DiffersRefused, want)
		}
		if !hostText(c.DiffersRefused) || strings.ContainsAny(c.DiffersRefused, "\n") {
			t.Errorf("%s: %q is not one line of the host's words", tc.name, c.DiffersRefused)
		}
		// The same card without the earlier record, but for this line.
		plain := mustCard(t, now, Inputs{})
		c.DiffersRefused = ""
		if !reflect.DeepEqual(c, plain) {
			t.Errorf("%s: the card differs from the card with no earlier record", tc.name)
		}
		if c.ShownDigest() != plain.ShownDigest() {
			t.Errorf("%s: the digest moved", tc.name)
		}
	}

	// A digest that does not fit the earlier record.
	earlier := validPending(t)
	earlier.Binding.Summary.Added++
	_, err := DiffersFrom(earlier, now, asked)
	wantCode(t, "an earlier record changed after it was sealed", err, record.CodeBindingMismatch)

	// Another verb.
	_, err = DiffersFrom(prunePending(t), now, asked)
	wantCode(t, "a prune before a feature finish", err, record.CodeRecordInvalid)
}
