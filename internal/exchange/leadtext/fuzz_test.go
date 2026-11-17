package leadtext

import (
	"strings"
	"testing"
)

var fuzzSeeds = []string{
	"",
	"plain",
	`\x1b`,
	`\u{202E}`,
	`\`,
	`\\`,
	"\x1b[2J",
	"\x1b]52;c;aGk=\x07",
	"a\nb\r\nc\td",
	"\u0085\u009b\x9b",
	"\u202e\u2066\u200b\u200d\ufeff\u2028\u2029",
	"\xff\x80\xbf\xc3\xe2\x82\xf0\x9f\x98",
	"\xed\xa0\x80\xc0\xaf\xf4\x90\x80\x80",
	"caf\u00e9 \u65e5\u672c\u8a9e \U0001f600",
	"\ue000\U000f0000\U0010ffff\u0378\U00030000",
	"\x00\x7f\x1f",
}

func FuzzRoundTrip(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		e := Escape(s)
		checkSafeLine(t, e)
		back, err := Unescape(e)
		if err != nil {
			t.Fatalf("Unescape(Escape(%q)) failed: %v", s, err)
		}
		if back != s {
			t.Fatalf("Unescape(Escape(%q)) = %q", s, back)
		}
	})
}

func FuzzLine(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s, 5)
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max > 1<<16 || max < -(1<<16) {
			max %= 1 << 16
		}
		full := Escape(s)
		out, cut := Line(s, max)
		checkSafeLine(t, out)
		if max < 0 {
			max = 0
		}
		if len(out) > max || !strings.HasPrefix(full, out) || cut != (out != full) {
			t.Fatalf("Line(%q, %d) = %q, %v; full %q", s, max, out, cut, full)
		}
		if _, err := Unescape(out); err != nil {
			t.Fatalf("Line(%q, %d) = %q does not unescape: %v", s, max, out, err)
		}
	})
}

func FuzzWriter(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s, 1)
	}
	f.Fuzz(func(t *testing.T, s string, split int) {
		want := streamReference(s)
		if got := streamAll(t, s); got != want {
			t.Fatalf("one write of %q = %q, want %q", s, got, want)
		}
		i := 0
		if len(s) > 0 {
			i = split % (len(s) + 1)
			if i < 0 {
				i = -i
			}
		}
		if got := streamAll(t, s[:i], s[i:]); got != want {
			t.Fatalf("%q split at %d = %q, want %q", s, i, got, want)
		}
		for j := 0; j < len(want); j++ {
			c := want[j]
			if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
				t.Fatalf("output holds byte %#x", c)
			}
		}
	})
}
