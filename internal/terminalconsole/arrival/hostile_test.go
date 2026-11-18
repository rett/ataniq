package arrival

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

var (
	red    = color.RGBA{0xff, 0, 0, 0xff}
	green  = color.RGBA{0, 0xff, 0, 0xff}
	blue   = color.RGBA{0, 0, 0xff, 0xff}
	yellow = color.RGBA{0xff, 0xff, 0, 0xff}
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

// hostileLines is the hostile set of the console-first build, as lines: the
// strip is fed them as if a caller had passed text of a lead by mistake.
// Escape sequences, control bytes, direction overrides and zero-width code
// points, bytes that are not UTF-8, one line of 1 MiB, text shaped like the
// console's own and like the strip's, and feature names shaped to mislead.
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
		"\a\a\a", "line one\nline two\n[2] approvals waiting 0",
		c0.String(), c1bytes.String(), c1runes.String(),
		// Direction overrides and zero-width code points.
		"\u202eevorppa\u202c", "\u202a\u202b\u202d\u2066\u2067\u2068\u2069\u200e\u200f\u061c",
		"zero\u200bwidth\u200cjoin\u200dno\u2060break\ufeffbom\u00adsoft",
		"\u2028line\u2029paragraph\u0085next", "emoji\ufe0f\U000e0041tag\u2800blank\u3164filler\u00a0nbsp",
		"👩\u200d👩\u200d👧 family 🇦🇧 flags e\u0301 mark",
		// Bytes that are not UTF-8.
		"\xff\xfe\xfd", "\xc0\xaf\xe0\x80\xaf", "\xed\xa0\x80\xed\xbf\xbf", "\xf4\x90\x80\x80", "half \xe6\x97",
		strings.Repeat("a", maxText-1) + "日本語", strings.Repeat("a", maxText-2) + "日本語",
		// Marks with nothing before them, and a character under a pile.
		"\u0301\u0301\u0301 marks first", "x" + strings.Repeat("\u0301\u0336", 400),
		// One line of 1 MiB.
		longLine(1 << 20),
		// Text shaped like the console's own, and like the strip's.
		"[a] approve   [x] deny",
		"┏━━━━━━━━━━┓", "┃ APPROVED ┃", "┗━━━━━━━━━━┛",
		"APPROVE feature-x · type ACDE and press Enter",
		"APPROVAL 7Q4K   project-a / lead-a",
		title + sep + hintFocused,
		"[1] alerts 0", "approvals 0 (oldest under 1 min)",
		`\x1b[31m already escaped \u{202E} \\`,
		// Feature names inside the grammar, shaped to mislead; one twice.
		"approve", "a-then-code", "develop", "feature-x", "feature-x",
		"", " ", "the last line",
	}
}

// checkCell fails when a cell holds something a terminal would act on or
// hide. A cell of the strip holds one printable character that stands
// alone, one or two cells wide.
func checkCell(t *testing.T, where string, c *uv.Cell) {
	t.Helper()
	if c == nil {
		t.Fatalf("%s: a cell of the area was not written", where)
	}
	s := c.Content
	if c.Width == 0 {
		// The second cell of a wide character.
		if s != "" {
			t.Fatalf("%s: a cell of no width holds %q", where, s)
		}
		return
	}
	if len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f {
		return
	}
	if !utf8.ValidString(s) {
		t.Fatalf("%s: a cell holds bytes that are not UTF-8: %q", where, s)
	}
	if utf8.RuneCountInString(s) != 1 {
		t.Fatalf("%s: a cell holds %d code points: %q", where, utf8.RuneCountInString(s), s)
	}
	if w := ansi.StringWidth(s); w != c.Width || w < 1 || w > 2 {
		t.Fatalf("%s: a cell of width %d holds %q, which measures %d", where, c.Width, s, w)
	}
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			t.Fatalf("%s: a cell holds the control %U: %q", where, r, s)
		case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Bidi_Control, r), unicode.Is(unicode.M, r),
			unicode.Is(unicode.Variation_Selector, r), unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r),
			unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Cs, r):
			t.Fatalf("%s: a cell holds the invisible or direction code point %U: %q", where, r, s)
		case r == utf8.RuneError:
			t.Fatalf("%s: a cell holds U+FFFD: %q", where, s)
		case !unicode.IsPrint(r):
			t.Fatalf("%s: a cell holds %U, which is not printable: %q", where, r, s)
		}
	}
}

// The forms of the strip's own part of each row, whatever follows it.
var (
	alertsForm = regexp.MustCompile(`^\[1\] alerts [0-9]+ ·( |$)`)
	heldForm   = regexp.MustCompile(`^\[[1-4]\] held notices [0-9]+ ·( |$)`)
)

// TestHostileLines feeds every line of the hostile set as the first alert
// and as the first held notice, draws the strip at many sizes, and checks
// that no cell holds a raw control byte or anything else a terminal would
// act on or hide, that the strip's own words stand where they belong, and
// that a line cannot make a row of its own.
func TestHostileLines(t *testing.T) {
	sizes := [][2]int{{0, 0}, {1, 1}, {2, 5}, {3, 5}, {5, 5}, {17, 3}, {40, 5}, {80, 5}, {80, 2}, {200, 5}, {400, 9}}
	for i, line := range hostileLines() {
		sum := Summary{Alerts: []string{line, "second"}, Waiting: 1, ByGrant: 1, Held: []string{line}}
		s := NewStrip(Colors{})
		if !s.Arrive(arrived, sum) {
			t.Fatalf("line %d: no strip", i)
		}
		if s.Rows() != 5 {
			t.Fatalf("line %d: Rows() = %d, want 5", i, s.Rows())
		}
		for _, r := range s.rows {
			if len(r.text) > maxText+64 {
				t.Fatalf("line %d: the strip keeps %d bytes of a line", i, len(r.text))
			}
		}
		for _, size := range sizes {
			for _, focused := range []bool{false, true} {
				w, h := size[0], size[1]
				where := fmt.Sprintf("line %d at %dx%d", i, w, h)
				buf := draw(s, w, h, focused)
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						checkCell(t, where, buf.CellAt(x, y))
					}
				}
				if w < 80 || h < 5 {
					continue
				}
				wantTitle := title
				if focused {
					wantTitle += sep + hintFocused
				}
				if got := rowText(buf, 0); got != wantTitle {
					t.Fatalf("%s: title row %q, want %q", where, got, wantTitle)
				}
				if got := rowText(buf, 1); !alertsForm.MatchString(got) {
					t.Fatalf("%s: row 1 is %q", where, got)
				}
				if got := rowText(buf, 2); got != "[2] approvals waiting 1 · oldest under 1 min" {
					t.Fatalf("%s: row 2 is %q", where, got)
				}
				if got := rowText(buf, 3); got != "[3] approved without you 1 · 1 by grant" {
					t.Fatalf("%s: row 3 is %q", where, got)
				}
				if got := rowText(buf, 4); !heldForm.MatchString(got) {
					t.Fatalf("%s: row 4 is %q", where, got)
				}
				for y := 5; y < h; y++ {
					if got := rowText(buf, y); got != "" {
						t.Fatalf("%s: row %d below the strip holds %q", where, y, got)
					}
				}
			}
		}
	}
}

// TestHostileTextIsReplacedNotDropped shows what a reader sees of such a
// line: its printable characters, and a question mark for each other one.
func TestHostileTextIsReplacedNotDropped(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x1b[31mred\x1b[0m", "?[31mred?[0m"},
		{"a\tb\nc\rd\x00e\x7f", "a b?c?d?e?"},
		{"\u202eevorppa\u202c", "?evorppa?"},
		{"e\u0301 zero\u200bwidth", "e? zero?width"},
		{"\xff\xfe ok \xe6\x97", "?? ok ??"},
		{"日本語 wide", "日本語 wide"},
		{"\u009b2J", "?2J"},
	} {
		_, buf := shown(t, Summary{Held: []string{tc.in}}, 80, 2)
		if got, want := rowText(buf, 1), "[1] held notices 1 · "+tc.want; got != want {
			t.Errorf("%q is drawn as %q, want %q", tc.in, got, want)
		}
	}
}

func TestClipCutsBeforeACharacter(t *testing.T) {
	for _, s := range hostileLines() {
		got := clip(s)
		if len(got) > maxText {
			t.Fatalf("clip keeps %d bytes", len(got))
		}
		if !strings.HasPrefix(s, got) {
			t.Fatalf("clip(%.20q) is no beginning of it", s)
		}
	}
	// A cut inside a character moves before it.
	s := strings.Repeat("a", maxText-1) + "日本語"
	if got := clip(s); got != strings.Repeat("a", maxText-1) {
		t.Errorf("clip left %d bytes, want %d", len(got), maxText-1)
	}
}
