package seatapp

import (
	"bytes"
	"encoding/json"
	"html/template"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestText(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"empty", "", 10, ""},
		{"plain", "A short note.", 100, "A short note."},
		{"line feed kept", "one\ntwo", 100, "one\ntwo"},
		{"tab escaped", "a\tb", 100, `a\u0009b`},
		{"carriage return escaped", "a\r\nb", 100, "a\\u000d\nb"},
		{"nul", "a\x00b", 100, `a\u0000b`},
		{"escape character", "\x1b[31mred\x1b[0m", 100, `\u001b[31mred\u001b[0m`},
		{"delete", "a\x7fb", 100, `a\u007fb`},
		{"C1 control", "a\u0085b", 100, `a\u0085b`},
		{"right-to-left override", "gpj.\u202eexe.txt", 100, `gpj.\u202eexe.txt`},
		{"bidi isolates", "a\u2066b\u2069c\u200fd\u200ee\u061cf", 100, `a\u2066b\u2069c\u200fd\u200ee\u061cf`},
		{"zero width", "pay\u200bment\u200c\u200d\ufeff", 100, `pay\u200bment\u200c\u200d\ufeff`},
		{"soft hyphen and word joiner", "a\u00adb\u2060c", 100, `a\u00adb\u2060c`},
		{"line and paragraph separators", "a\u2028b\u2029c", 100, `a\u2028b\u2029c`},
		{"look-alike spaces", "a\u00a0b\u3000c\u2003d", 100, `a\u00a0b\u3000c\u2003d`},
		{"language tag characters", "a\U000e0041b", 100, `a\U000e0041b`},
		{"private use", "a\ue000b", 100, `a\ue000b`},
		{"non-characters", "a\ufffeb\uffffc\ufdd0d", 100, `a\ufffeb\uffffc\ufdd0d`},
		{"blank-looking letters", "a\u3164b\u2800c", 100, `a\u3164b\u2800c`},
		{"invalid UTF-8", "a\xffb\xc3", 100, `a\xffb\xc3`},
		{"overlong encoding", "\xc0\xaf", 100, `\xc0\xaf`},
		{"a surrogate half in UTF-8", "\xed\xa0\x80", 100, `\xed\xa0\x80`},
		{"backslash is not escaped", `\u202e`, 100, `\u202e`},
		{"ordinary non-ASCII kept", "caf\u00e9 \u65e5\u672c\u8a9e \U0001f600", 100, "caf\u00e9 \u65e5\u672c\u8a9e \U0001f600"},
		{"markup is only text", `<a href="https://evil.example">tap here</a> <script>x()</script>`, 100, `<a href="https://evil.example">tap here</a> <script>x()</script>`},
		{"markdown is only text", "[approve](https://evil.example) **now**", 100, "[approve](https://evil.example) **now**"},

		{"exactly at the limit", "abcde", 5, "abcde"},
		{"cut", "abcdefgh", 5, "abcde (3 more characters not shown)"},
		{"cut by one", "abcdef", 5, "abcde (1 more character not shown)"},
		{"cut counts characters, not bytes", "\u65e5\u672c\u8a9e\u65e5\u672c", 3, "\u65e5\u672c\u8a9e (2 more characters not shown)"},
		{"cut counts what would be escapes", "ab\u202e\u202e\u202e", 2, "ab (3 more characters not shown)"},
		{"cut never falls inside an escape", "ab\u202ecd", 3, `ab\u202e (2 more characters not shown)`},
		{"invalid bytes count one each", "ab\xff\xff\xff", 2, "ab (3 more characters not shown)"},
		{"zero max", "abc", 0, "(3 more characters not shown)"},
		{"negative max", "abc", -4, "(3 more characters not shown)"},
		{"zero max, empty text", "", 0, ""},
	} {
		if got := Text(tc.in, tc.max); got != tc.want {
			t.Errorf("%s: Text(%q, %d) = %q, want %q", tc.name, tc.in, tc.max, got, tc.want)
		}
	}
}

// TestTextOutputIsSafeForEveryCharacter checks, for every code point, that the
// output holds no character a reader cannot see or that reorders text.
func TestTextOutputIsSafeForEveryCharacter(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue // not a character in a Go string
		}
		out := Text("a"+string(r)+"b", 10)
		for _, c := range out {
			if c == '\n' || c == ' ' {
				continue
			}
			if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) || unicode.Is(unicode.Zs, c) ||
				unicode.Is(unicode.Zl, c) || unicode.Is(unicode.Zp, c) || unicode.Is(unicode.Co, c) || blankLooking[c] {
				t.Fatalf("U+%04X: the output %q holds U+%04X", r, out, c)
			}
		}
		if !utf8.ValidString(out) {
			t.Fatalf("U+%04X: the output is not UTF-8", r)
		}
	}
}

func TestTextOutputIsBounded(t *testing.T) {
	// Ten characters of worst-case input are at most ten escapes of ten bytes.
	in := strings.Repeat("\U000e0041", 100000)
	if got := Text(in, 10); len(got) > 10*10+len(" (99990 more characters not shown)") {
		t.Errorf("%d bytes", len(got))
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\nb\u202e", 10); got != `a\u000ab\u202e` {
		t.Errorf("%q", got)
	}
}

// TestTextInATemplate shows the function in a page template: its output is
// HTML-escaped by the template, and the page makes no link and no element of
// what the text holds.
func TestTextInATemplate(t *testing.T) {
	tpl := template.Must(template.New("x").Funcs(template.FuncMap{"text": Text}).Parse(`<p>{{text .Note 200}}</p>`))
	var buf bytes.Buffer
	note := `<script>alert(1)</script> <a href="https://evil.example">tap</a> "q" & ` + "\u202e"
	if err := tpl.Execute(&buf, map[string]string{"Note": note}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, bad := range []string{"<script", "<a ", "\u202e"} {
		if strings.Contains(got, bad) {
			t.Errorf("the page holds %q: %s", bad, got)
		}
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;a href=", `\u202e`, "&amp;"} {
		if !strings.Contains(got, want) {
			t.Errorf("the page lacks %q: %s", want, got)
		}
	}
}

func TestPairShowsTheWordsThroughText(t *testing.T) {
	r := newRig(t)
	r.daemon.words = [6]string{"amber", "bi\u202erch", "ce\x00dar", "a very " + strings.Repeat("long", 40) + " word", "line\nbreak", "<b>x</b>"}
	a := newAuthenticator(t)
	w := r.postPair(a.register(t, r.getChallenge(t)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var out struct {
		Words []string `json:"words"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Words) != 6 {
		t.Fatalf("%q: %v", w.Body, err)
	}
	if out.Words[1] != `bi\u202erch` || out.Words[2] != `ce\u0000dar` || out.Words[4] != `line\u000abreak` || out.Words[5] != "<b>x</b>" {
		t.Errorf("words %q", out.Words)
	}
	if !strings.HasSuffix(out.Words[3], " more characters not shown)") || utf8.RuneCountInString(out.Words[3]) > maxWordLen+40 {
		t.Errorf("the long word %q", out.Words[3])
	}
	if strings.ContainsRune(w.Body.String(), '\u202e') {
		t.Errorf("a raw bidirectional character is in the answer: %q", w.Body)
	}
}
