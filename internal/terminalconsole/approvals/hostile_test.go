package approvals

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

// longLine returns a line of at least n bytes that mixes words, escape
// sequences, wide characters, direction overrides and bytes that are not
// UTF-8.
func longLine(n int) string {
	var b strings.Builder
	b.Grow(n + 64)
	for i := 0; b.Len() < n; i++ {
		switch i % 6 {
		case 0:
			fmt.Fprintf(&b, "word%07d-", i)
		case 1:
			b.WriteString("\x1b[31mred\x1b[0m")
		case 2:
			b.WriteString("日本語é")
		case 3:
			b.WriteString("\u202eoverride\u200b")
		case 4:
			b.WriteString("\xff\xfe\x00\x07")
		case 5:
			b.WriteString("\x1b]0;title\x07 ")
		}
	}
	return b.String()
}

// hostileLines is the hostile set of the console-first build, as lines of
// lead text: escape sequences, control bytes, direction overrides and
// zero-width code points, bytes that are not UTF-8, one line of 1 MiB, and
// text shaped like the view's own chrome.
func hostileLines() []string {
	var c0, c1bytes, c1runes strings.Builder
	for b := 0; b < 0x20; b++ {
		c0.WriteByte(byte(b))
		c0.WriteString("c0")
	}
	c0.WriteString("\x7f")
	for b := 0x80; b <= 0x9f; b++ {
		c1bytes.WriteByte(byte(b))
		c1bytes.WriteString("c1")
		c1runes.WriteRune(rune(b))
		c1runes.WriteString("c1")
	}
	return []string{
		// Escape sequences: CSI, OSC ended by BEL and by ST, DCS.
		"\x1b[2J\x1b[H\x1b[31mred\x1b[0m\x1b[?1049h",
		"\x1b]0;a title\x07after the title",
		"\x1b]52;c;aGVsbG8=\x1b\\after the clipboard",
		"\x1b]8;;https://example.invalid/\x1b\\link\x1b]8;;\x1b\\",
		"\x1bP1$r0m\x1b\\after the dcs",
		"\x9b2J\x9d0;title\x9c\x90q\x9c",
		"\u009b2J\u009d0;title\u009c\u0090q\u009c",
		"\x1b", "\x1b[", "\x1b]", "\x07\x08\x0b\x0c\r", "a\tb\tc", "cr\rlf\nnul\x00end",
		c0.String(), c1bytes.String(), c1runes.String(),
		// Direction overrides and zero-width code points.
		"\u202eevorppa\u202c", "\u202a\u202b\u202d\u2066\u2067\u2068\u2069\u200e\u200f\u061c",
		"zero\u200bwidth\u200cjoin\u200dno\u2060break\ufeffbom\u00adsoft",
		"\u2028line\u2029paragraph\u0085next", "emoji\ufe0f\U000e0041tag\u2800blank\u3164filler\u00a0nbsp",
		"👩\u200d👩\u200d👧 family 🇦🇧 flags e\u0301 mark",
		// Bytes that are not UTF-8.
		"\xff\xfe\xfd", "\xc0\xaf\xe0\x80\xaf", "\xed\xa0\x80\xed\xbf\xbf", "\xf4\x90\x80\x80", "half \xe6\x97",
		// One line of 1 MiB.
		longLine(1 << 20),
		// Text shaped like the chrome.
		"[a] approve   [x] deny",
		"[space] sign off  [a] approve  [x] deny  [d] diff  [l] lead  [esc] back",
		"┏━━━━━━━━━━┓", "┃ APPROVED ┃", "┗━━━━━━━━━━┛",
		"APPROVAL ZZZZ  project-a / lead-a@lead-1 (runtime-a)",
		"approve is ready: press [a], then type the code from the title",
		"Approvals are given only here.",
		"Lead's note (lead-stated, not checked)",
		"code> ****   the code is in the title   [enter] send   [esc] cancel",
		`\x1b[31m already escaped \u{202E} \\`,
		"",
	}
}

// badCell returns what is wrong with a cell, or "" when it holds nothing a
// terminal would act on or hide. A cell of the view holds one printable
// character that stands alone, one or two cells wide.
func badCell(c *uv.Cell) string {
	if c == nil {
		return "a cell of the area was not written"
	}
	s := c.Content
	if c.Width == 0 {
		if s != "" {
			return fmt.Sprintf("a cell of no width holds %q", s)
		}
		return ""
	}
	if len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f {
		return ""
	}
	if !utf8.ValidString(s) {
		return fmt.Sprintf("a cell holds bytes that are not UTF-8: %q", s)
	}
	if utf8.RuneCountInString(s) != 1 {
		return fmt.Sprintf("a cell holds %d code points: %q", utf8.RuneCountInString(s), s)
	}
	if w := ansi.StringWidth(s); w != c.Width || w < 1 || w > 2 {
		return fmt.Sprintf("a cell of width %d holds %q, which measures %d", c.Width, s, w)
	}
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return fmt.Sprintf("a cell holds the control %U: %q", r, s)
		case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Bidi_Control, r), unicode.Is(unicode.M, r),
			unicode.Is(unicode.Variation_Selector, r), unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r),
			unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r):
			return fmt.Sprintf("a cell holds the invisible or direction code point %U: %q", r, s)
		case r == 0x2800, r == utf8.RuneError, !unicode.IsPrint(r):
			return fmt.Sprintf("a cell holds %U: %q", r, s)
		}
	}
	return ""
}

// checkCells checks every cell of the drawing.
func checkCells(t *testing.T, where string, buf *uv.Buffer) {
	t.Helper()
	for y := 0; y < buf.Bounds().Dy(); y++ {
		for x := 0; x < buf.Bounds().Dx(); x++ {
			if bad := badCell(buf.CellAt(x, y)); bad != "" {
				t.Fatalf("%s at (%d,%d): %s", where, x, y, bad)
			}
		}
	}
}

// mark is put into every lead string of the hostile card, so that a test
// finds where lead text was drawn.
const mark = "LEADMARK"

// hostileCard returns a card whose every lead-written string, and every
// host-written one that the backend could be made to put a line into, holds
// the line.
func hostileCard(id, line string) card.Card {
	c := testCard(id)
	lt := func(s string) card.LeadText { return card.NewLeadText(mark + s + line) }
	c.Plan.Lines = []card.LeadText{lt("plan1"), lt("plan2")}
	c.Refs = []card.RefLine{{Kind: "ref", Ref: lt("ref"), Old: hex(1), New: hex(2),
		MergedInto: &card.MergedInto{Ref: lt("merged"), Tip: hex(3)}}}
	c.Diff.Rows = []card.Row{
		card.RowOf(record.PathChange{Path: record.Text(mark + "row1" + line), Status: "M", OldMode: "100644", NewMode: "100755", Added: 1}),
		card.RowOf(record.PathChange{Path: record.Text(mark + "row2" + line), Status: "A", OldMode: "000000", NewMode: "100644", Added: 2}),
	}
	c.Lists = []card.List{
		{Kind: card.ListBinary, Paths: []card.LeadText{lt("bin")}},
		{Kind: card.ListSymlinks, Paths: []card.LeadText{lt("sym")}},
		{Kind: card.ListModeChanges, Paths: []card.LeadText{lt("mode")}},
		{Kind: card.ListSubmodules, Paths: []card.LeadText{lt("sub")}},
	}
	c.Flagged = []card.FlaggedPath{
		{Kind: record.FlagExecConfig, Path: lt("flag1"), Pattern: lt("pat1"), Reason: "matches an execution-config rule"},
		{Kind: record.FlagSymlink, Path: lt("flag2"), Reason: "a symbolic link"},
	}
	c.Note = card.NewLeadText(mark + "note1" + line + "\n" + mark + "note2" + line)
	c.Differs = &card.Differs{EarlierID: "p-0", ReadAt: t0, Feature: card.Change{Old: hex(3), New: hex(5)},
		PathsAdded: []card.LeadText{lt("added")}, PathsRemoved: []card.LeadText{lt("removed")},
		RefsAdded: []card.LeadText{lt("refadded")}}
	return c
}

var hostileSizes = [][2]int{{80, 24}, {120, 40}, {60, 15}, {30, 10}, {20, 8}, {12, 3}, {5, 2}, {1, 1}}

// chromeRows returns the row numbers of the card's chrome at 80 by 24: the
// title, the verb line, the rules, the hints, the message and the footer.
func chromeRows(h int) []int {
	foot := h - 5
	return []int{0, 1, 2, foot, foot + 1, foot + 2, foot + 3, foot + 4}
}

// OC10: hostile text in every lead field of the card. The cells hold no raw
// lead byte, and the chrome holds no lead text.
func TestOC10HostileCard(t *testing.T) {
	for i, line := range hostileLines() {
		c := hostileCard("p-1", line)
		b := backendWith(t, []card.Card{c}, nil)
		for _, size := range hostileSizes {
			r := newRig(t, b, size[0], size[1])
			r.key("enter")
			r.clk.Advance(time.Second)
			if r.m.cs == nil {
				t.Fatalf("line %d at %v: no card", i, size)
			}
			where := fmt.Sprintf("line %d at %dx%d", i, size[0], size[1])
			n := len(r.m.cs.body)
			if n > 2000 {
				t.Fatalf("%s: a line made %d rows", where, n)
			}
			// The card is read through at the larger sizes; at the small
			// ones the first screens are drawn.
			steps := n + 2
			if size[0] < 60 {
				steps = min(steps, 6)
			}
			for step := 0; step < steps; step++ {
				buf := r.draw()
				checkCells(t, where, buf)
				if size[0] == 80 && size[1] == 24 {
					checkChrome(t, where, buf, r)
				}
				r.key("down")
			}
		}
	}
}

// checkChrome checks the rows of a card drawn at 80 by 24 that are the
// view's own words.
func checkChrome(t *testing.T, where string, buf *uv.Buffer, r *rig) {
	t.Helper()
	for _, y := range chromeRows(buf.Bounds().Dy()) {
		if got := rowText(buf, y); strings.Contains(got, mark) {
			t.Fatalf("%s: chrome row %d holds lead text: %q", where, y, got)
		}
	}
	if got := rowText(buf, 0); got != " APPROVAL 7Q4K  project-a / lead-a@lead-1 (runtime-a)" {
		t.Fatalf("%s: title row %q", where, got)
	}
	if got := rowText(buf, 1); got != " feature finish name=feature-x  ->  develop    class: ref-move" {
		t.Fatalf("%s: verb row %q", where, got)
	}
	if got := rowText(buf, 20); got != " [space] sign off  [a] approve  [x] deny  [d] diff  [l] lead  [esc] back" {
		t.Fatalf("%s: hints row %q", where, got)
	}
	if got := rowText(buf, 23); !strings.HasSuffix(got, "Approvals are given only here.") {
		t.Fatalf("%s: footer row %q", where, got)
	}
	// The code is in the title, and a line shaped like another title or
	// like the confirm prompt changes nothing: nothing but row 0 holds it.
	for y := 1; y < buf.Bounds().Dy(); y++ {
		if strings.Contains(rowText(buf, y), "7Q4K") {
			t.Fatalf("%s: the code is on row %d", where, y)
		}
	}
	// Every row that holds lead text is a body row, and none of them starts
	// at the margin with a heading of the view's.
	for y := 3; y < 19; y++ {
		row := rowText(buf, y)
		if strings.Contains(row, mark) && strings.HasPrefix(strings.TrimLeft(row, ">↪"), "APPROVAL") {
			t.Fatalf("%s: a lead line stands as a title on row %d: %q", where, y, row)
		}
	}
}

func TestOC10HeadingsHoldNoLeadText(t *testing.T) {
	c := hostileCard("p-1", "\x1b[31m[a] approve")
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	for _, row := range r.m.cs.body {
		var line strings.Builder
		for _, s := range row.segs {
			line.WriteString(s.s)
			if s.st == sHead && strings.Contains(s.s, mark) {
				t.Fatalf("a heading holds lead text: %q", s.s)
			}
		}
		// Lead text is drawn in the text style, never in a heading's, and
		// only after a prefix of the view's own words.
		if strings.Contains(line.String(), mark) {
			for _, s := range row.segs {
				if strings.Contains(s.s, mark) && s.st != sText {
					t.Fatalf("lead text is drawn in style %v: %q", s.st, s.s)
				}
			}
		}
	}
}

func TestOC10FeatureNamesShapedToMislead(t *testing.T) {
	var cards []card.Card
	for i, name := range []string{"approve", "a-then-code", "develop", "feature-x", "feature-x"} {
		c := testCard(fmt.Sprintf("p-%d", i+1))
		c.Verb.Feature = name
		cards = append(cards, c)
	}
	b := backendWith(t, cards, nil)
	r := newRig(t, b, 80, 24)
	rows := r.rows()
	for _, name := range []string{"approve", "a-then-code", "develop", "feature-x"} {
		found := false
		for _, row := range rows {
			if regexp.MustCompile(`\b` + name + `\b`).MatchString(row) {
				found = true
				if !strings.Contains(row, "name="+name) {
					t.Fatalf("the name %q stands without its label: %q", name, row)
				}
			}
		}
		if !found {
			t.Fatalf("no row shows %q", name)
		}
	}
	for i, name := range []string{"approve", "a-then-code", "develop"} {
		r.key("enter")
		r.clk.Advance(time.Second)
		want := "feature finish name=" + name + "  ->  develop    class: ref-move"
		if got := strings.TrimSpace(r.rows()[1]); got != want {
			t.Fatalf("card %d: verb row %q, want %q", i, got, want)
		}
		r.key("esc")
		r.key("j")
	}
}

// TestOC10HostileHostStrings feeds hostile text to every string that the
// view draws and that the host is meant to have written. The view still
// writes no raw byte to a cell.
func TestOC10HostileHostStrings(t *testing.T) {
	for i, line := range hostileLines() {
		c := testCard("p-1")
		c.After = card.After{State: record.StateApplied, Line: line}
		c.DiffersRefused = line
		c.Evidence = []card.Evidence{{Kind: line, RecordID: line, RecordSHA256: line}}
		c.Flagged[0].Reason = line
		c.Title.Subject.DisplayID = line
		c.Title.Project = line
		c.Verb.Class = line
		c.Diff.ExecConfig.Matcher = line
		b := backendWith(t, []card.Card{c}, []card.Item{{ID: "p-9", Verb: line, Feature: line, Project: line, Line: line, State: record.StateApplied}})
		b.listing.Pending[0].Project = line
		b.listing.Pending[0].Line = line
		b.listing.Rules = []card.Rule{{Project: line, Name: line, Text: line}}
		b.deciderO, b.deciderW = false, line
		for _, size := range hostileSizes {
			r := newRig(t, b, size[0], size[1])
			where := fmt.Sprintf("host line %d at %v", i, size)
			checkCells(t, where+" (list)", r.draw())
			r.key("enter")
			r.clk.Advance(time.Second)
			if r.m.cs == nil {
				continue
			}
			for step := 0; step < len(r.m.cs.body)+2; step++ {
				checkCells(t, where+" (card)", r.draw())
				r.key("down")
			}
			r.key("x")
			checkCells(t, where+" (deny)", r.draw())
		}
	}
}

func TestOC10HostileBackendErrorsAndOutcome(t *testing.T) {
	for i, line := range hostileLines() {
		r := readyRig(t, testCard("p-1"))
		r.b.decide = func(card.Decision) (card.Outcome, error) {
			return card.Outcome{State: record.StateApplied, Line: line}, nil
		}
		r.key("a")
		r.typ("7Q4K")
		r.key("enter")
		checkCells(t, fmt.Sprintf("outcome line %d", i), r.draw())
		r2 := readyRig(t, testCard("p-1"))
		r2.b.decide = func(card.Decision) (card.Outcome, error) {
			return card.Outcome{}, &record.Error{Code: record.Code(line), Detail: line}
		}
		r2.key("a")
		r2.typ("7Q4K")
		r2.key("enter")
		checkCells(t, fmt.Sprintf("error line %d", i), r2.draw())
	}
}

func TestOC10HostileReasonTyped(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("x")
	for _, c := range "[a] approve \u202e" {
		r.key(string(c))
	}
	r.feed(tea2(0x1b))
	checkCells(t, "typed reason", r.draw())
}

func TestOC10HostilePatchInThePager(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	var lines []string
	for _, l := range hostileLines() {
		// The pager's own cells are clusters; a flag is two code points.
		if !strings.Contains(l, "\U0001f1e6") {
			lines = append(lines, l)
		}
	}
	b.pages = func(_ string, _ card.PageKind, n int) (card.Page, error) {
		return card.Page{Kind: card.PagePatch, N: n, Pages: 1, ReviewDigest: "review-a", Patch: lines, Cont: make([]bool, len(lines))}, nil
	}
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("d")
	for i := 0; i < 80; i++ {
		checkCells(t, "pager", r.draw())
		r.key("pgdown")
	}
}

func TestOC10HostileSizesDoNotPanic(t *testing.T) {
	c := hostileCard("p-1", "\x1b[31m")
	b := backendWith(t, []card.Card{c}, recentItems())
	for w := 0; w <= 90; w += 7 {
		for h := 0; h <= 30; h += 3 {
			r := newRig(t, b, w, h)
			checkCells(t, fmt.Sprintf("list %dx%d", w, h), r.draw())
			r.key("enter")
			r.clk.Advance(time.Second)
			checkCells(t, fmt.Sprintf("card %dx%d", w, h), r.draw())
			r.key("pgdown")
			r.key("x")
			r.key("a")
			checkCells(t, fmt.Sprintf("field %dx%d", w, h), r.draw())
		}
	}
}
