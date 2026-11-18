package consoleclient

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// shownSafely reports why s is not safe to show, or "". It is written apart
// from clean, by listing what must not pass, so that the two can disagree.
func shownSafely(s string) string {
	if len(s) > MaxText {
		return "over MaxText"
	}
	if !utf8.ValidString(s) {
		return "not UTF-8"
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return "a control character"
		case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs, unicode.Zl, unicode.Zp):
			return "a format, private or separator character"
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
			return "a direction character"
		case r >= 0x200b && r <= 0x200d, r == 0x2060, r == 0xfeff:
			return "a zero-width character"
		case r >= 0xe0000 && r <= 0xe0fff, r >= 0xfe00 && r <= 0xfe0f:
			return "a tag or variation selector"
		case r != ' ' && unicode.IsSpace(r):
			return "a space that is not the ASCII one"
		}
	}
	if r, _ := utf8.DecodeRuneInString(s); unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me) {
		return "a combining mark at the start"
	}
	return ""
}

func TestClean(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"plain", "demo/lead", "demo/lead"},
		{"letters of other scripts stay", "naïve 名前 ж", "naïve 名前 ж"},
		{"an escape sequence loses its escape", "\x1b[31mred\x1b[0m", "[31mred[0m"},
		{"an OSC title", "\x1b]0;title\x07x", "]0;titlex"},
		{"C1 controls", "a\u009bb\u0085c", "ab c"},
		{"direction overrides and isolates", "a\u202eb\u202dc\u2066d\u2069e\u200ff", "abcdef"},
		{"zero width and joiners", "a\u200bb\u200dc\ufeffd\u2060e", "abcde"},
		{"line breaks become spaces", "a\nb\r\tc\u2028d\u2029e", "a b  c d e"},
		{"other spaces become the ASCII one", "a\u00a0b\u3000c", "a b c"},
		{"invalid UTF-8", "a\xff\xfeb\xc3", "ab"},
		{"tags and variation selectors", "a\U000e0041b\ufe0fc", "abc"},
		{"private use and the blank braille", "a\ue000b\u2800c", "abc"},
		{"a combining mark at the start", "\u0301x", "x"},
		{"marks of every kind at the start", "\u0301\u20dd\u0903\u0300x\u0301y", "x\u0301y"},
		{"a mark after dropped characters is at the start too", "\x1b\u200b\u0301x", "x"},
		{"a mark after the value's own space stays", " \u0301x", " \u0301x"},
		{"only marks", "\u0301\u0300", ""},
		{"empty", "", ""},
	} {
		got := clean(c.in)
		if got != c.want {
			t.Errorf("%s: clean(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if why := shownSafely(got); why != "" {
			t.Errorf("%s: clean(%q) = %q holds %s", c.name, c.in, got, why)
		}
	}
}

func TestCleanBoundsLength(t *testing.T) {
	// A string that fits the bound is kept whole, also when what follows it
	// is dropped; one byte more and it is cut to the bound with its mark.
	exact := strings.Repeat("a", MaxText)
	for _, in := range []string{exact, exact + "\x1b\u202e", strings.Repeat("名", MaxText/3) + "a"} {
		if got := clean(in); got != strings.TrimRight(in, "\x1b\u202e") {
			t.Errorf("a string of %d bytes that fits the bound came out as %d bytes", len(in), len(got))
		}
	}
	if got, want := clean(exact+"a"), exact[:MaxText-len(cutMark)]+cutMark; got != want {
		t.Errorf("a string one byte over the bound = %d bytes %q, want %d bytes", len(got), got, len(want))
	}
	// The room for the mark is made a whole character at a time.
	if got, want := clean(strings.Repeat("名", MaxText/3)+"ab"), strings.Repeat("名", MaxText/3-1)+cutMark; got != want {
		t.Errorf("a cut after a wide character = %q, want %q", got, want)
	}
	for _, in := range []string{
		strings.Repeat("a", MaxText+1),
		strings.Repeat("a", 1<<20),
		strings.Repeat("名", 1<<16),          // cut between characters, never inside one
		strings.Repeat("\x1b[0m", 1<<16),    // mostly dropped, still bounded
		strings.Repeat("\u202e", 1<<16),     // all dropped
		strings.Repeat("\xff", 1<<16) + "a", // inspection is bounded too
	} {
		got := clean(in)
		if why := shownSafely(got); why != "" {
			t.Errorf("clean of %d bytes holds %s", len(in), why)
		}
		if !strings.HasSuffix(got, cutMark) {
			t.Errorf("clean of %d bytes = %q, want it to end in the cut mark", len(in), got)
		}
	}
}
