package approvals

import (
	"strconv"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/exchange/leadtext"
)

// maxLeadBytes bounds the escaped bytes of one lead-written string on a
// card. A string that is longer is cut, and the row says so.
const maxLeadBytes = 600

// maxNoteLines bounds the lines of the lead's note that are drawn; a note
// has no other bound but the record's size.
const maxNoteLines = 60

// The view's own words for the sections of a card.
const (
	wordPlan     = "Plan"
	wordRefs     = "Refs"
	wordMerge    = "Merge"
	wordDiff     = "Diff"
	wordPaths    = "Changed paths"
	wordFlagged  = "Flagged paths (each needs its own sign-off)"
	wordEvidence = "Evidence"
	wordNote     = "Lead's note"
	wordDiffers  = "Differs from"
	wordAfter    = "Outcome"
	wordNone     = "none"
)

// clock returns t as the card shows a time: the time of day, and the date
// too when t is on another day than ref.
func clock(t, ref time.Time) string {
	if t.IsZero() {
		return "?"
	}
	if ref.IsZero() || t.YearDay() == ref.YearDay() && t.Year() == ref.Year() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

// short returns the first 12 characters of an object id or a digest.
func short(id string) string {
	if len(id) > 12 {
		return clip(id[:12])
	}
	return clip(id)
}

// sizeText writes a byte count the way a person reads it.
func sizeText(n int64) string {
	switch {
	case n < 1<<10:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1<<20:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	case n < 1<<30:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n>>30, 10) + " GiB"
}

func itoa(n int) string { return strconv.Itoa(n) }

// leadFlow lays out the escaped text of t after prefix. The row says when
// the text was cut.
func (b *builder) leadFlow(prefix []seg, indent int, t card.LeadText, tail []seg) (first, last int) {
	text, cut := t.Line(maxLeadBytes)
	if cut {
		tail = append([]seg{{" [cut]", sDim}}, tail...)
	}
	return b.flow(prefix, indent, text, sText, tail)
}

// head adds a heading row. The label of lead text, when the section holds
// some, and then any other label stand beside it in the dim colour.
func (b *builder) head(text string, labels ...string) int {
	segs := []seg{{text, sHead}}
	for _, l := range labels {
		segs = append(segs, seg{" · " + l, sDim})
	}
	return b.add(segs...)
}

// titleParts returns the two parts of the card's title: the left one, with
// the code when there is one, and the right one, with the times. The right
// one stands at the end of the first row when both fit, and is the first row
// of the body when they do not.
func (cs *cardState) titleParts(code string) (left, right string) {
	t := cs.c.Title
	left = "APPROVAL"
	if code != "" {
		left += " " + code
	}
	left += "  " + clip(t.Project) + " / " + clip(t.Subject.Role)
	if t.Subject.DisplayID != "" {
		left += "@" + clip(t.Subject.DisplayID)
	}
	if t.Subject.Runtime != "" {
		left += " (" + clip(t.Subject.Runtime) + ")"
	}
	right = "asked " + clock(t.Asked, cs.openedAt) + ", expires " + clock(t.Expires, t.Asked)
	return left, right
}

// whenInBody says whether the times do not fit the title row at width w.
func (cs *cardState) whenInBody(w int) bool {
	// A code that is there takes as many cells as the one used here, so
	// the answer does not change when the code comes or goes.
	l, r := cs.titleParts("0000")
	return width(l)+2+width(r) > w
}

// verbLine returns the card's second row: the verb, the feature's name
// where there is one, the target and the class. The feature name is the one
// string of a lead that stands here, and it is drawn as a name.
func (cs *cardState) verbLine() string {
	v := cs.c.Verb
	s := clip(v.Name)
	s += nameText(v.Feature)
	if v.Target != "" {
		s += "  ->  " + clip(v.Target)
	}
	if v.Class != "" {
		s += "    class: " + clip(v.Class)
	}
	return s
}

// buildBody lays out the body of the open card for the present width.
func (m *Model) buildBody() {
	cs := m.cs
	b := &builder{w: max(12, m.w-1)}
	// The cursor stays on the same line of the card when the rows wrap
	// differently.
	anchor := -1
	if cs.cursor >= 0 && cs.cursor < len(cs.body) {
		anchor = cs.body[cs.cursor].src
	}
	defer func() {
		cs.body, cs.bodyW = b.rows, m.w
		for i, r := range b.rows {
			if r.src == anchor {
				cs.cursor = i
				break
			}
		}
		m.clampCard()
	}()
	if cs.endedLine != "" {
		b.add(seg{cs.endedLine, sWarn})
		return
	}
	c := cs.c
	// A card that exists because a run refused says so first, in one line
	// that holds no verdict detail, also before the decision.
	if c.Refused {
		b.flow(nil, 0, refusedLine, sWarn, nil)
	}
	// The three kinds of content that the binding does not cover come
	// first where they matter: the block that says how this card differs
	// from an earlier one, and what came after the decision.
	if d := c.Differs; d != nil {
		m.differsRows(b, d)
	} else if c.DiffersRefused != "" {
		b.head(wordDiffers, card.DiffersLabel)
		b.flow([]seg{{"  ", sText}}, 2, c.DiffersRefused, sWarn, nil)
	}
	if cs.whenInBody(b.w) {
		_, right := cs.titleParts("")
		b.add(seg{"When  ", sHead}, seg{right, sText})
	}
	if c.After.State != "" || c.After.Line != "" || c.After.Held {
		b.head(wordAfter, card.AfterLabel)
		if c.After.State != "" {
			b.add(seg{"  state  " + clip(string(c.After.State)), sText})
		}
		if c.After.Held {
			b.add(seg{"  " + card.HeldLabel, sWarn})
		}
		if c.After.Line != "" {
			b.flow([]seg{{"  ", sText}}, 2, c.After.Line, sText, nil)
		}
		for _, l := range verdictLines(c, c.Decided()) {
			b.flow([]seg{{"  ", sText}}, 2, l, sText, nil)
		}
	}

	// The plan, verbatim.
	b.head(wordPlan+" (host, token "+clip(c.Plan.Token)+")", leadtext.Label)
	if len(c.Plan.Lines) == 0 {
		b.add(seg{"  " + wordNone, sDim})
	}
	for _, l := range c.Plan.Lines {
		b.leadFlow([]seg{{"  ", sText}}, 2, l, nil)
	}

	// The refs and the merge.
	for i, r := range c.Refs {
		lead := wordRefs
		if i > 0 {
			lead = ""
		}
		prefix := []seg{{pad(lead, 7), sHead}}
		var tail []seg
		if r.Deletes {
			tail = []seg{{"  " + short(r.Old) + " -> deleted", sText}}
		} else {
			tail = []seg{{"  " + short(r.Old) + " -> " + short(r.New), sText}}
		}
		_, last := b.leadFlow(prefix, 7, r.Ref, tail)
		if r.MergedInto != nil {
			_, last = b.leadFlow([]seg{{pad("", 7) + "merged into ", sDim}}, 7, r.MergedInto.Ref,
				[]seg{{" (" + short(r.MergedInto.Tip) + ")", sDim}})
		}
		if c.Diff == nil && i == len(c.Refs)-1 {
			b.rows[last].ends |= endRefs
		}
	}
	if len(c.Refs) == 0 {
		i := b.add(seg{pad(wordRefs, 7), sHead}, seg{wordNone, sDim})
		if c.Diff == nil {
			b.rows[i].ends |= endRefs
		}
	}
	if mg := c.Merge; mg != nil {
		b.add(seg{pad(wordMerge, 7), sHead}, seg{mg.Commit + "  built by " + mg.BuiltBy, sText})
		if t := mg.TargetTip(); t != "" {
			b.add(seg{"       target tip   " + t, sText})
		}
		if f := mg.FeatureTip(); f != "" {
			b.add(seg{"       feature tip  " + f, sText})
		}
	}

	if d := c.Diff; d != nil {
		m.diffRows(b, d)
	}

	// The four lists, each shown even when empty.
	for _, l := range c.Lists {
		k := l.Kind
		if k < 0 || int(k) >= card.NumLists {
			continue
		}
		mask := endList0 << uint(k)
		if len(l.Paths) == 0 {
			i := b.add(seg{k.Label() + " 0", sHead}, seg{"  " + wordNone, sDim})
			b.rows[i].ends |= mask
			continue
		}
		b.head(k.Label()+" "+itoa(len(l.Paths)), leadtext.Label)
		var last int
		for _, p := range l.Paths {
			_, last = b.leadFlow([]seg{{"  ", sText}}, 2, p, nil)
		}
		b.rows[last].ends |= mask
	}

	// The flagged paths, each with its own sign-off box.
	if c.Diff != nil || len(c.Flagged) > 0 {
		b.head(wordFlagged, leadtext.Label)
		if len(c.Flagged) == 0 {
			b.add(seg{"  " + wordNone, sDim})
		}
	}
	for i, f := range c.Flagged {
		box := "[ ] "
		if cs.reading.Signed(f.SignOff()) {
			box = "[x] "
		}
		first, last := b.leadFlow([]seg{{box, sAccent}, {f.Kind + "  ", sText}}, 6, f.Path, nil)
		b.mark(first, last, i+1, 0)
		_, l2 := b.flow([]seg{{"      " + f.Reason, sDim}}, 6, "", sText, nil)
		b.mark(l2, l2, i+1, 0)
		if f.Pattern.Len() > 0 {
			_, l3 := b.leadFlow([]seg{{"      rule  ", sDim}}, 12, f.Pattern, nil)
			b.mark(l3, l3, i+1, 0)
		}
	}

	// The evidence, which is the host's.
	b.head(wordEvidence)
	if len(c.Evidence) == 0 {
		b.add(seg{"  " + wordNone, sDim})
	}
	for _, e := range c.Evidence {
		b.flow([]seg{{"  ", sText}}, 2, clip(e.Kind)+"  record "+clip(e.RecordID)+"  "+short(e.RecordSHA256), sText, nil)
	}

	// The lead's note, labelled.
	b.head(wordNote+" ("+card.NoteLabel+")", leadtext.Label)
	lines := c.Note.Lines()
	if len(lines) == 0 {
		b.add(seg{"  " + wordNone, sDim})
	}
	for i, l := range lines {
		if i == maxNoteLines {
			b.add(seg{"  [" + itoa(len(lines)-i) + " more lines of the note are not shown]", sDim})
			break
		}
		b.leadFlow([]seg{{"  ", sText}}, 2, l, nil)
	}
}

// pad returns s blank-filled on the right to n cells, or s when it is
// longer.
func pad(s string, n int) string {
	for w := width(s); w < n; w++ {
		s += " "
	}
	return s
}

// differsRows lays out the "differs from" block, labelled as the card's
// exception. The last row ends the block: the card may be approved only
// when a row after it has been on the screen.
func (m *Model) differsRows(b *builder, d *card.Differs) {
	ref := m.cs.openedAt
	b.head(wordDiffers+" "+d.EarlierID+", which you read at "+clock(d.ReadAt, ref), card.DiffersLabel)
	change := func(label string, ch card.Change) {
		switch {
		case ch.Old == "" && ch.New == "":
		case ch.Changed():
			b.add(seg{"  " + pad(label, 13) + short(ch.Old) + " -> " + short(ch.New), sWarn})
		default:
			b.add(seg{"  " + pad(label, 13) + short(ch.New) + " (the same)", sText})
		}
	}
	change("feature tip", d.Feature)
	change("target tip", d.Target)
	change("merge", d.Merge)
	lists := func(label string, ts []card.LeadText) {
		if len(ts) == 0 {
			return
		}
		b.add(seg{"  " + label + " (" + itoa(len(ts)) + ")", sText}, seg{" · " + leadtext.Label, sDim})
		for _, t := range ts {
			b.leadFlow([]seg{{"    ", sText}}, 4, t, nil)
		}
	}
	lists("paths added", d.PathsAdded)
	lists("paths removed", d.PathsRemoved)
	lists("refs added", d.RefsAdded)
	lists("refs removed", d.RefsRemoved)
	if d.PathsPartial {
		b.flow([]seg{{"  ", sText}}, 2, "a path list is cut in one of the two cards, so paths may be missing above", sWarn, nil)
	}
	b.add(seg{"  end of what differs", sDim})
	b.rows[len(b.rows)-1].ends |= endDiffers
}

// diffRows lays out the diff summary, the changed paths and, when the card
// holds only the first part of the path list, the page of the whole list
// that is being read.
func (m *Model) diffRows(b *builder, d *card.Diff) {
	b.add(seg{pad(wordDiff, 7), sHead}, seg{short(d.Base) + ".." + short(d.Tip) + "   " +
		itoa(int(min(d.Files, 1<<30))) + " files, +" + strconv.FormatInt(d.Added, 10) +
		" -" + strconv.FormatInt(d.Removed, 10) + ", " + sizeText(d.NewBytes) + " new", sText})
	b.add(seg{"       flag rules " + d.ExecConfig.Matcher + ", " + strconv.FormatInt(d.ExecConfig.Additions, 10) +
		" added by the project", sDim})
	h := b.head(wordPaths+" ("+itoa(len(d.Rows))+" of "+strconv.FormatInt(d.Files, 10)+")", leadtext.Label)
	last := h
	for _, r := range d.Rows {
		last = m.pathRow(b, r)
	}
	if d.Truncated {
		b.flow(nil, 0, d.TruncatedLine(), sWarn, nil)
		m.pageRows(b)
		return
	}
	b.rows[last].ends |= endPaths
}

// pathRow lays out one changed path and returns its last row.
func (m *Model) pathRow(b *builder, r card.Row) int {
	prefix := []seg{{"  " + r.Status + " +" + strconv.FormatInt(r.Added, 10) + " -" + strconv.FormatInt(r.Removed, 10) + "  ", sText}}
	var tail []seg
	if r.OldMode != r.NewMode {
		tail = []seg{{"  mode " + r.OldMode + " -> " + r.NewMode, sDim}}
	}
	_, last := b.leadFlow(prefix, 4, r.Path, tail)
	return last
}

// pageRows lays out the page of the whole path list that was read last, for
// a card whose record holds only the first part of its path list.
func (m *Model) pageRows(b *builder) {
	cs := m.cs
	p := cs.page
	if p == nil {
		b.head("Whole path list: no page read yet", leadtext.Label)
		note := "  press ] to read the first page"
		if cs.pageLoading {
			note = "  reading the first page"
		}
		b.add(seg{note, sDim})
		return
	}
	h := b.head("Whole path list: page "+itoa(p.N+1)+" of "+itoa(p.Pages), leadtext.Label)
	b.rows[h].ends |= startPage
	last := len(b.rows) - 1
	if len(p.Paths) == 0 {
		last = b.add(seg{"  this page holds no path", sDim})
	}
	for _, pc := range p.Paths {
		last = m.pathRow(b, card.RowOf(pc))
	}
	b.rows[last].ends |= endPage
	if cs.pageLoading {
		b.add(seg{"  reading another page", sDim})
	}
}
