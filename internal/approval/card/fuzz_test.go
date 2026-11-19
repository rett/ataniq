package card

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/record"
)

// The hostile set, wherever a lead can write on a card that the record's
// caps let through: every text renders escaped, and the card can still be
// read and decided.
func TestHostileSetOnACard(t *testing.T) {
	for i, hostile := range hostileTexts() {
		raw := rawPending()
		s := raw.Binding.Summary
		raw.Note = record.Text(hostile)
		raw.PlanText = record.Text(hostile + "\n" + hostile)
		s.Paths = append(s.Paths, record.PathChange{Path: record.Text(hostile), Status: "A", OldMode: "000000", NewMode: "120000"})
		s.Flagged = append(s.Flagged, record.Flag{Kind: record.FlagSymlink, Path: record.Text(hostile), Pattern: ""})
		s.Binary = []record.Text{record.Text(hostile)}
		s.Symlinks = []record.Text{record.Text(hostile)}
		s.ModeChanges = []record.Text{record.Text(hostile)}
		s.Submodules = []record.Text{record.Text(hostile)}
		p := seal(t, raw)

		// The record carries it, as its own decoder reads it back.
		data, err := record.EncodePending(p)
		if err != nil {
			t.Fatalf("hostile text %d does not fit a record: %v", i, err)
		}
		decoded, err := record.DecodePending(data)
		if err != nil {
			t.Fatal(err)
		}
		earlier := seal(t, laterPending())
		c := mustCard(t, decoded, Inputs{Held: true, Earlier: &Earlier{Record: earlier, ReadAt: asked}})
		checkNothingRaw(t, c)
		checkNothingRaw(t, c.Item())
		checkNothingRaw(t, c.Keys())

		// The bytes are all still there, only escaped.
		if c.Note.bytes() != hostile || c.Flagged[1].Path.bytes() != hostile || c.Lists[ListSymlinks].Paths[0].bytes() != hostile {
			t.Errorf("hostile text %d: the card lost bytes", i)
		}
		// And the hostile path can be signed off and approved.
		r := Reading{Opened: asked, Paths: true, Lists: allLists, Differs: true}
		for _, f := range c.Flagged {
			r.Toggle(f.SignOff())
		}
		d, ok, reason := c.Approve(r, asked.Add(time.Minute))
		if !ok {
			t.Errorf("hostile text %d: approve is refused: %q", i, reason)
		}
		if !record.SignOffsEqual(decoded.Binding.Summary.Flagged, d.SignedOff) {
			t.Errorf("hostile text %d: the sign-offs are not the flagged list", i)
		}
	}
}

// One line of 1 MiB fits no record, so it reaches a card only as a page of
// the patch, which the pager escapes.
func TestOneLineOfOneMebibyteFitsNoRecord(t *testing.T) {
	long := longLine()
	for name, put := range map[string]func(p *record.Pending){
		"the note":      func(p *record.Pending) { p.Note = record.Text(long) },
		"the plan":      func(p *record.Pending) { p.PlanText = record.Text(long) },
		"a path":        func(p *record.Pending) { p.Binding.Summary.Paths[0].Path = record.Text(long) },
		"a flagged one": func(p *record.Pending) { p.Binding.Summary.Flagged[0].Path = record.Text(long) },
		"a list":        func(p *record.Pending) { p.Binding.Summary.Symlinks = []record.Text{record.Text(long)} },
	} {
		raw := rawPending()
		put(&raw)
		_, err := New(seal(t, raw), Inputs{})
		wantCode(t, "the line as "+name, err, record.CodeRecordTooLarge)
	}
}

// fuzzStates are the states the fuzz test picks from; the last is none of
// the closed list.
var fuzzStates = []record.State{
	"", record.StatePending, record.StateApplied, record.StateDenied, record.StateWithdrawn,
	record.StateExpired, record.StateVoid, record.StateUnknown, "held",
}

// FuzzNew builds a pending record around text a lead could have written,
// seals it as a host would, and hands it to New. Whatever the text is, New
// must not panic; a refusal must be a refusal of the record package, in the
// host's words; and a card must render no raw byte of the text anywhere.
// The seeds run as an ordinary test.
func FuzzNew(f *testing.F) {
	hostile := hostileTexts()
	for i, text := range hostile {
		other := hostile[(i+1)%len(hostile)]
		// The text in every place a lead writes, with a good name.
		f.Add([]byte(text), []byte(other), []byte(text), []byte(other), "sample-feature", text, "project-a", "", uint16(i))
		// The same with marks, states and an earlier card switched on.
		f.Add([]byte(other), []byte(text), []byte(other), []byte(text), "sample-feature", other, "project-a",
			"applied: develop 93c8d68 -> 0194daf", uint16(0xff00|i))
		// The text where only the host may write: New must refuse, or
		// find nothing to escape.
		f.Add([]byte("note"), []byte("plan"), []byte("path"), []byte("link"), "sample-feature", "Makefile", text, text, uint16(i))
		// The text as the feature name.
		f.Add([]byte("note"), []byte("plan"), []byte("path"), []byte("link"), text, "Makefile", "project-a", "", uint16(i))
	}
	// Names inside the grammar that are shaped to mislead, and the name
	// of the earlier card.
	for i, name := range []string{"approve", "a-then-code", "develop", "sample-feature", "x", strings.Repeat("a", 48), strings.Repeat("a", 49), "Approve", ""} {
		f.Add([]byte("[a] approve   [x] deny"), []byte(samplePlan), []byte(name), []byte("develop"), name, name, name, name, uint16(i<<4))
	}
	// One line of 1 MiB, in each place in turn.
	long := []byte(longLine())
	short := []byte("x")
	f.Add(long, short, short, short, "sample-feature", "Makefile", "project-a", "", uint16(0))
	f.Add(short, long, short, short, "sample-feature", "Makefile", "project-a", "", uint16(0))
	f.Add(short, short, long, short, "sample-feature", "Makefile", "project-a", "", uint16(0))
	f.Add(short, short, short, long, "sample-feature", "Makefile", "project-a", "", uint16(0))
	f.Add(short, short, short, short, string(long[:1<<16]), string(long), string(long[:4096]), string(long[:4096]), uint16(0))

	f.Fuzz(func(t *testing.T, note, plan, path, link []byte, feature, pattern, project, outcome string, flags uint16) {
		raw := rawPending()
		b := &raw.Binding
		s := b.Summary
		raw.Note = record.Text(note)
		raw.PlanText = record.Text(plan)
		b.Argv = []string{feature}
		b.Project = project
		s.Paths = append(s.Paths, record.PathChange{Path: record.Text(path), Status: "T", OldMode: "100644", NewMode: "120000"})
		s.Flagged = append(s.Flagged,
			record.Flag{Kind: record.FlagExecConfig, Path: record.Text(path), Pattern: pattern},
			record.Flag{Kind: record.FlagSymlink, Path: record.Text(link), Pattern: ""})
		s.Symlinks = []record.Text{record.Text(link)}
		s.Binary = []record.Text{record.Text(path), record.Text(link)}
		s.PathsTruncated = flags&1 != 0
		in := Inputs{
			Held:  flags&2 != 0,
			State: fuzzStates[int(flags>>4&0xf)%len(fuzzStates)],
			Line:  outcome,
		}
		if flags&4 != 0 {
			in.Verdicts = &VerdictBlock{}
		}
		if flags&8 != 0 {
			// An earlier card for the same feature, with the same
			// lead text in other places.
			earlier := laterPending()
			earlier.Binding.Argv = []string{feature}
			earlier.Binding.Project = project
			earlier.Note = record.Text(path)
			earlier.Binding.Summary.Paths = append(earlier.Binding.Summary.Paths,
				record.PathChange{Path: record.Text(link), Status: "A", OldMode: "000000", NewMode: "100644"})
			in.Earlier = &Earlier{Record: seal(t, earlier), ReadAt: asked}
		}
		p := seal(t, raw)

		c, err := New(p, in)
		if err != nil {
			var refusal *record.Error
			if !errors.As(err, &refusal) {
				t.Fatalf("New failed with something that is no refusal: %v", err)
			}
			if refusal.Code == "" || !hostText(string(refusal.Code)) || !hostText(refusal.Detail) {
				t.Errorf("the refusal is not in the host's words: %q", err.Error())
			}
			if !record.ValidFeatureName(feature) && refusal.Code != record.CodeFeatureName {
				t.Errorf("the name %q is outside the grammar, and the refusal is %s", feature, refusal.Code)
			}
			if c.BindingSHA256 != "" || c.Title.ID != "" || c.Note.Len() != 0 {
				t.Error("a card came with the refusal")
			}
			return
		}

		// A card: the name passed the grammar, and nothing renders raw.
		if !record.ValidFeatureName(feature) || c.Verb.Feature != feature {
			t.Fatalf("a card for the feature name %q, shown as %q", feature, c.Verb.Feature)
		}
		if c.BindingSHA256 != p.BindingSHA256 || c.Title.ID != p.ID {
			t.Error("the card is not the record's")
		}
		checkNothingRaw(t, c)
		item := c.Item()
		checkNothingRaw(t, item)
		checkNothingRaw(t, c.Keys())
		if c.Verdicts != nil && !c.Decided() {
			t.Error("a card that is not decided shows verdicts")
		}
		if (c.Differs != nil) != (in.Earlier != nil) {
			t.Error("the block is there without an earlier card, or the other way round")
		}

		// The rules answer, in the host's words, whatever the reading.
		now := asked.Add(time.Hour)
		if len(c.ShownDigest()) != 64 {
			t.Error("the digest of what was shown is not 64 digits")
		}
		if ok, reason := c.MayApprove(Reading{}, now); ok || reason == "" || !hostText(reason) {
			t.Errorf("nothing read: %v, %q", ok, reason)
		}
		r := Reading{Opened: asked, Paths: true, Lists: allLists, Differs: true}
		for _, fl := range c.Flagged {
			r.Toggle(fl.SignOff())
		}
		r.ReadPage(Page{Kind: PagePaths, N: 0, Pages: 1, ReviewDigest: c.Diff.ReviewDigest})
		d, ok, reason := c.Approve(r, now)
		if ok == c.Ended() || !hostText(reason) {
			t.Errorf("everything read: %v, %q, and ended is %v", ok, reason, c.Ended())
		}
		if ok && (!record.SignOffsEqual(p.Binding.Summary.Flagged, d.SignedOff) || d.BindingSHA256 != p.BindingSHA256) {
			t.Error("the decision does not fit the record")
		}
		if _, ok, reason := c.Deny(string(note), now); !hostText(reason) || (ok && c.Ended()) {
			t.Errorf("a deny: %v, %q", ok, reason)
		}
	})
}
