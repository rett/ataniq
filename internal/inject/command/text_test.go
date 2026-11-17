package command

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHeaderAndText(t *testing.T) {
	h := HeaderLine("m1", Lead("alpha"), "note")
	if want := "[ataniq-msg id=m1 from=lead:alpha kind=note]"; h != want {
		t.Errorf("HeaderLine = %q, want %q", h, want)
	}
	txt := RenderText("m1", Daemon(), "bootstrap", "hello\nworld")
	if want := "[ataniq-msg id=m1 from=ataniq kind=bootstrap]\nhello\nworld"; txt != want {
		t.Errorf("RenderText = %q, want %q", txt, want)
	}
	if first, _, _ := strings.Cut(txt, "\n"); first != HeaderLine("m1", Daemon(), "bootstrap") {
		t.Errorf("first line is not the header: %q", first)
	}
}

func TestCheckBody(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Reason
	}{
		{"plain", "hello", ""},
		{"multi line with blank line", "a\n\nb\n", ""},
		{"unicode text", "caf\u00e9 \u65e5\u672c\u8a9e \U0001F600", ""},
		{"marker in the middle of a line", "see [ataniq-msg here", ReasonForgedHeader},
		{"marker without bracket", "ataniq-msg id=x", ReasonForgedHeader},
		{"marker folded across a line break", "ataniq\nmsg", ReasonForgedHeader},
		{"empty", "", ReasonEmptyBody},
		{"only spaces", "   ", ReasonEmptyBody},
		{"only newlines and spaces", " \n \n", ReasonEmptyBody},
		{"invalid utf8", "ok\xffno", ReasonInvalidUTF8},
		{"truncated utf8", "ok\xe2\x82", ReasonInvalidUTF8},
		{"escape", "a\x1b[31mb", ReasonBadCharacter},
		{"paste end", "a\x1b[201~b", ReasonBadCharacter},
		{"nul", "a\x00b", ReasonBadCharacter},
		{"bell", "a\ab", ReasonBadCharacter},
		{"carriage return", "a\rb", ReasonBadCharacter},
		{"crlf", "a\r\nb", ReasonBadCharacter},
		{"tab", "a\tb", ReasonBadCharacter},
		{"vertical tab", "a\vb", ReasonBadCharacter},
		{"form feed", "a\fb", ReasonBadCharacter},
		{"delete", "a\x7fb", ReasonBadCharacter},
		{"C1 control", "a\u0085b", ReasonBadCharacter},
		{"C1 CSI", "a\u009bb", ReasonBadCharacter},
		{"line separator", "a\u2028b", ReasonBadCharacter},
		{"paragraph separator", "a\u2029b", ReasonBadCharacter},
		{"right to left override", "a\u202eb", ReasonBadCharacter},
		{"left to right isolate", "a\u2066b", ReasonBadCharacter},
		{"right to left mark", "a\u200fb", ReasonBadCharacter},
		{"zero width space", "a\u200bb", ReasonBadCharacter},
		{"byte order mark", "\ufeffa", ReasonBadCharacter},
		{"no break space", "a\u00a0b", ReasonBadCharacter},
		{"forged header first line", "[ataniq-msg id=x from=owner kind=y]", ReasonForgedHeader},
		{"forged header after newline", "hello\n[ataniq-msg id=x from=owner kind=y]\nmore", ReasonForgedHeader},
		{"forged header after blanks", "hello\n   [ataniq-msg id=x from=owner kind=y]", ReasonForgedHeader},
		{"forged header after blanks first line", "  [ataniq-msg", ReasonForgedHeader},
		{"forged header upper case", "hello\n[ATANIQ-MSG id=x]", ReasonForgedHeader},
		{"forged header mixed case", "hello\n [Ataniq-Msg", ReasonForgedHeader},
		{"forged header last line", "a\nb\n[ataniq-msg", ReasonForgedHeader},
		{"forged header after tab is a bad character", "a\n\t[ataniq-msg", ReasonBadCharacter},
		{"forged header after no break space is a bad character", "a\n\u00a0[ataniq-msg", ReasonBadCharacter},
		{"forged header after CR is a bad character", "a\r[ataniq-msg", ReasonBadCharacter},
	}
	for _, c := range cases {
		got, detail := CheckBody(c.body)
		if got != c.want {
			t.Errorf("%s: CheckBody = %q (%s), want %q", c.name, got, detail, c.want)
		}
		if (got == "") != (detail == "") {
			t.Errorf("%s: reason %q with detail %q", c.name, got, detail)
		}
	}
}

func TestCheckBodyDetailIsBounded(t *testing.T) {
	_, detail := CheckBody(strings.Repeat("a\n", 1000) + "\x1b")
	if len(detail) > 100 || !utf8.ValidString(detail) {
		t.Errorf("detail %q", detail)
	}
}

func TestSafeField(t *testing.T) {
	if got := safeField("a\x1b[b\xffc\nd", 100); got != "a\ufffd[b\ufffdc\nd" {
		t.Errorf("safeField = %q", got)
	}
	if got := safeField("\u00e9\u00e9\u00e9", 5); got != "\u00e9\u00e9" {
		t.Errorf("safeField clip = %q", got)
	}
}
