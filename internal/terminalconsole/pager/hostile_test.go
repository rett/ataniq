package pager

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/exchange/leadtext"
)

// hostileLines is the hostile set of the console-first build, as lines of a
// patch: escape sequences, control bytes, direction overrides and
// zero-width code points, bytes that are not UTF-8, text shaped like the
// console's own, and feature names shaped to mislead. The set's line of
// 1 MiB is fed by TestALineOfOneMebibyte, which makes the same checks; here
// a line of 64 KiB of the same make stands for it, so that the set can be
// read at many sizes.
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
	lines := []string{
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
		"\u2028line\u2029paragraph\u0085next", "emoji\ufe0f\U000e0041tag\u2800blank",
		// Bytes that are not UTF-8.
		"\xff\xfe\xfd", "\xc0\xaf\xe0\x80\xaf", "\xed\xa0\x80\xed\xbf\xbf", "\xf4\x90\x80\x80", "half \xe6\x97",
		// Marks with nothing before them, and a character under a pile.
		"\u0301\u0301\u0301 marks first", "x" + strings.Repeat("\u0301\u0336", 400),
		// A long line.
		longLine(1 << 16),
		// Text shaped like the console's own.
		"[a] approve   [x] deny",
		"┏━━━━━━━━━━┓", "┃ APPROVED ┃", "┗━━━━━━━━━━┛",
		"APPROVE feature-x · type ACDE and press Enter",
		header,
		"end reached · end of the patch · Esc closes · j/k PgUp/PgDn Home/End",
		failChanged + " · end not reached",
		wrapMark + " a row that claims to go on",
		leadtext.Label,
		`\x1b[31m already escaped \u{202E} \\`,
		// Feature names inside the grammar, shaped to mislead; one twice.
		"approve", "a-then-code", "develop", "feature-x", "feature-x",
		"", " ", "the last line",
	}
	return lines
}

// checkCell fails when a cell holds something a terminal would act on or
// hide: no cell may hold a raw byte of the lead.
func checkCell(t *testing.T, where string, c *uv.Cell) {
	t.Helper()
	if c == nil {
		return
	}
	s := c.Content
	if len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f {
		return
	}
	if !utf8.ValidString(s) {
		t.Fatalf("%s: a cell holds bytes that are not UTF-8: %q", where, s)
	}
	if len(s) > maxCellBytes+1 {
		t.Fatalf("%s: a cell holds %d bytes", where, len(s))
	}
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			t.Fatalf("%s: a cell holds the control %U: %q", where, r, s)
		case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Bidi_Control, r),
			unicode.Is(unicode.Variation_Selector, r), unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r),
			unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r):
			t.Fatalf("%s: a cell holds the invisible or direction code point %U: %q", where, r, s)
		case r == utf8.RuneError:
			t.Fatalf("%s: a cell holds U+FFFD: %q", where, s)
		case r != ' ' && !unicode.IsPrint(r):
			t.Fatalf("%s: a cell holds %U, which is not printable: %q", where, r, s)
		}
	}
}

// footerForm is every footer the pager can write while it reads: its own
// words and numbers, nothing else.
var footerForm = regexp.MustCompile(`^end (not )?reached` +
	`( · (end of the patch|last page, more below|[0-9]+ of [0-9]+ pages below|reading the patch|the patch is empty|too small to show the patch))?` +
	`( · Esc closes)?( · j/k PgUp/PgDn Home/End)?( · reading a page)?$`)

// checkChrome checks the pager's own cells: the header, the footer and the
// margin hold the pager's words only, whatever the patch says.
func (r *rig) checkChrome(where string) {
	r.t.Helper()
	buf := r.draw()
	for y := 0; y < r.h; y++ {
		for x := 0; x < r.w; x++ {
			checkCell(r.t, where, buf.CellAt(x, y))
		}
	}
	if r.m.rows() == 0 {
		return
	}
	want := header
	if r.w < len([]rune(header)) {
		want = string([]rune(header)[:r.w-1]) + "…"
	}
	if got := strings.TrimRight(rowText(buf, 0, 0), " "); got != want {
		r.t.Fatalf("%s: header %q, want %q", where, got, want)
	}
	footer := strings.TrimRight(rowText(buf, r.h-1, 0), " ")
	if cut, ok := strings.CutSuffix(footer, "…"); ok {
		if !strings.HasPrefix("end not reached", cut) && !strings.HasPrefix("end reached", cut) {
			r.t.Fatalf("%s: footer %q", where, footer)
		}
	} else if !footerForm.MatchString(footer) {
		r.t.Fatalf("%s: footer %q", where, footer)
	}
	for y := 1; y < r.h-1; y++ {
		if c := buf.CellAt(0, y).Content; c != " " && c != wrapMark {
			r.t.Fatalf("%s: the margin of row %d holds %q", where, y, c)
		}
		if c := buf.CellAt(1, y).Content; c != " " {
			r.t.Fatalf("%s: the margin of row %d holds %q in its second cell", where, y, c)
		}
	}
}

// Fed the hostile set, the pager writes no raw byte of the lead into a
// cell, and its own rows and margin hold none of the lead's text.
func TestHostileSet(t *testing.T) {
	lines := hostileLines()
	for _, tc := range []struct {
		name                string
		w, h                int
		perPage, pieceBytes int
		graphemes           bool
	}{
		{"80x24, whole lines", 80, 24, 5, 0, false},
		{"80x24, whole lines, grapheme widths", 80, 24, 5, 0, true},
		{"80x24, pieces", 80, 24, 4, 1021, false},
		{"120x50, pieces, grapheme widths", 120, 50, 9, 4096, true},
		{"30x200, one piece a page", 30, 200, 1, 65536, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.w, tc.h, serve(paginate(lines, tc.perPage, tc.pieceBytes)))
			r.run(r.m.SetGraphemeWidths(tc.graphemes))
			for i := 0; ; i++ {
				r.checkChrome(fmt.Sprintf("screen %d", i))
				if r.m.Reached() {
					break
				}
				if i > 200000 {
					t.Fatal("the end is never reached")
				}
				r.press(keyPgDn)
			}
			if r.m.Err() != nil {
				t.Fatalf("err %v", r.m.Err())
			}
			for i := 0; i < 30; i++ {
				r.press(keyPgUp, keyK)
				r.checkChrome(fmt.Sprintf("screen %d going up", i))
			}
			r.press(keyHome)
			r.checkChrome("home")
			r.press(keyEnd)
			r.checkChrome("end")
		})
	}
}

// Every line of the hostile set is drawn as leadtext escapes it, once: the
// rows of a line, joined, are its escaped text, whole.
func TestHostileSetIsEscapedOnce(t *testing.T) {
	var lines []string
	for _, l := range hostileLines() {
		if len(l) < 1<<12 {
			lines = append(lines, l)
		}
	}
	for _, graphemes := range []bool{false, true} {
		for _, pieceBytes := range []int{0, 1, 5} {
			r := newRig(t, 80, 24, serve(paginate(lines, 6, pieceBytes)))
			r.run(r.m.SetGraphemeWidths(graphemes))
			got := r.readAll()
			if len(got) != len(lines) {
				t.Fatalf("pieces of %d bytes: %d lines drawn, want %d", pieceBytes, len(got), len(lines))
			}
			for i, l := range lines {
				// Blanks apart: a row may end in a blank cell before a wide
				// character, a line of marks is drawn after a blank, and
				// the blanks that fill a row are not told from the line's.
				if noBlanks(got[i]) != noBlanks(leadtext.Escape(l)) {
					t.Fatalf("pieces of %d bytes, line %d:\n%q is drawn as\n%q, want\n%q", pieceBytes, i, l, got[i], leadtext.Escape(l))
				}
			}
		}
	}
}

// At small sizes too.
func TestHostileSetInSmallRegions(t *testing.T) {
	var lines []string
	for _, l := range hostileLines() {
		if len(l) < 1<<12 {
			lines = append(lines, l)
		}
	}
	for _, size := range [][2]int{{4, 3}, {5, 4}, {7, 5}, {12, 3}, {20, 6}, {3, 3}, {80, 2}, {1, 1}} {
		for _, graphemes := range []bool{false, true} {
			r := newRig(t, size[0], size[1], serve(paginate(lines, 3, 7)))
			r.run(r.m.SetGraphemeWidths(graphemes))
			for i := 0; ; i++ {
				r.checkChrome(fmt.Sprintf("%dx%d, screen %d", size[0], size[1], i))
				if r.m.Reached() || r.m.rows() == 0 {
					break
				}
				if i > 100000 {
					t.Fatalf("%dx%d: the end is never reached", size[0], size[1])
				}
				r.press(keyPgDn)
			}
			if r.m.Reached() != (r.m.rows() > 0) {
				t.Fatalf("%dx%d: reached %v", size[0], size[1], r.m.Reached())
			}
		}
	}
}
