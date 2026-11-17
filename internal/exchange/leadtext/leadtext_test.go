package leadtext

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLabel(t *testing.T) {
	if Label != "unverified lead data" {
		t.Fatalf("Label = %q", Label)
	}
}

var escapeCases = []struct {
	name string
	in   string
	want string
}{
	{"empty", "", ""},
	{"plain ascii", "feature/lead-mirror v1.2 ~ok", "feature/lead-mirror v1.2 ~ok"},
	{"space and punctuation", " !\"#$%&'()*+,-./:;<=>?@[]^_`{|}~", " !\"#$%&'()*+,-./:;<=>?@[]^_`{|}~"},
	{"backslash", `a\b`, `a\\b`},
	{"literal text that looks like an escape", `\x1b`, `\\x1b`},
	{"literal unicode escape text", `\u{200B}`, `\\u{200B}`},
	{"literal newline escape text", `\n`, `\\n`},
	{"newline", "a\nb", `a\nb`},
	{"carriage return", "a\rb", `a\rb`},
	{"crlf", "a\r\nb", `a\r\nb`},
	{"tab", "a\tb", `a\tb`},
	{"clear screen CSI", "\x1b[2J", `\x1b[2J`},
	{"colour CSI", "\x1b[31mred\x1b[0m", `\x1b[31mred\x1b[0m`},
	{"OSC 52 with BEL", "\x1b]52;c;aGk=\x07", `\x1b]52;c;aGk=\x07`},
	{"OSC with ST", "\x1b]0;title\x1b\\", `\x1b]0;title\x1b\\`},
	{"BEL", "\a", `\x07`},
	{"NUL", "a\x00b", `a\x00b`},
	{"backspace", "ab\b\bc", `ab\x08\x08c`},
	{"vertical tab and form feed", "\v\f", `\x0b\x0c`},
	{"DEL", "\x7f", `\x7f`},
	{"0x1f", "\x1f", `\x1f`},
	{"C1 NEL rune", "\u0085", `\u{85}`},
	{"C1 CSI rune", "\u009b", `\u{9B}`},
	{"C1 first and last", "\u0080\u009f", `\u{80}\u{9F}`},
	{"raw 0x9b byte", "\x9b", `\x9b`},
	{"raw 0x9b CSI form", "\x9b2J", `\x9b2J`},
	{"invalid UTF-8 byte 0xff", "a\xffb", `a\xffb`},
	{"lone continuation bytes", "\x80\xbf", `\x80\xbf`},
	{"truncated two-byte rune", "\xc3", `\xc3`},
	{"truncated three-byte rune", "\xe2\x82", `\xe2\x82`},
	{"overlong slash", "\xc0\xaf", `\xc0\xaf`},
	{"encoded surrogate", "\xed\xa0\x80", `\xed\xa0\x80`},
	{"above max rune", "\xf4\x90\x80\x80", `\xf4\x90\x80\x80`},
	{"bidi override RLO", "a\u202eb", `a\u{202E}b`},
	{"bidi all embeddings", "\u202a\u202b\u202c\u202d\u202e", `\u{202A}\u{202B}\u{202C}\u{202D}\u{202E}`},
	{"bidi isolates", "\u2066\u2067\u2068\u2069", `\u{2066}\u{2067}\u{2068}\u{2069}`},
	{"bidi marks", "\u061c\u200e\u200f", `\u{61C}\u{200E}\u{200F}`},
	{"zero width space", "a\u200bb", `a\u{200B}b`},
	{"zero width non-joiner and joiner", "\u200c\u200d", `\u{200C}\u{200D}`},
	{"word joiner", "\u2060", `\u{2060}`},
	{"BOM", "\ufeffx", `\u{FEFF}x`},
	{"line separator", "a\u2028b", `a\u{2028}b`},
	{"paragraph separator", "a\u2029b", `a\u{2029}b`},
	{"soft hyphen", "\u00ad", `\u{AD}`},
	{"no-break space", "\u00a0", `\u{A0}`},
	{"ideographic space", "\u3000", `\u{3000}`},
	{"hangul filler", "\u3164", `\u{3164}`},
	{"combining grapheme joiner", "\u034f", `\u{34F}`},
	{"tag character", "\U000e0041", `\u{E0041}`},
	{"private use BMP", "\ue000", `\u{E000}`},
	{"private use plane 15", "\U000f0000", `\u{F0000}`},
	{"private use plane 16 last", "\U0010ffff", `\u{10FFFF}`},
	{"unassigned", "\u0378", `\u{378}`},
	{"noncharacter plane 1", "\U0001fffe", `\u{1FFFE}`},
	{"replacement character itself", "\ufffd", "\ufffd"},
	{"latin accents", "caf\u00e9 \u00fc", "caf\u00e9 \u00fc"},
	{"cjk", "\u65e5\u672c\u8a9e", "\u65e5\u672c\u8a9e"},
	{"emoji", "\U0001f600", "\U0001f600"},
	{"combining mark", "e\u0301", "e\u0301"},
	{"variation selector", "\u2764\ufe0f", "\u2764" + `\u{FE0F}`},
	{"variation selector supplement", "main\U000e0100", `main\u{E0100}`},
	{"variation selector supplement last", "\U000e01ef", `\u{E01EF}`},
	{"mongolian free variation selectors", "\u180b\u180c\u180d\u180f", `\u{180B}\u{180C}\u{180D}\u{180F}`},
	{"khmer invisible vowels", "\u17b4\u17b5", `\u{17B4}\u{17B5}`},
	{"reserved default ignorable U+2065", "\u2065", `\u{2065}`},
	{"reserved default ignorable U+FFF0", "\ufff0\ufff8", `\u{FFF0}\u{FFF8}`},
	{"hidden bytes in a name", "main\U000e0100\U000e0101\U000e0102", `main\u{E0100}\u{E0101}\u{E0102}`},
	{"mixed", "ok\x1b[2J\u202e\xff\\", `ok\x1b[2J\u{202E}\xff\\`},
}

func TestEscape(t *testing.T) {
	for _, c := range escapeCases {
		t.Run(c.name, func(t *testing.T) {
			got := Escape(c.in)
			if got != c.want {
				t.Fatalf("Escape(%q) = %q, want %q", c.in, got, c.want)
			}
			checkSafeLine(t, got)
			back, err := Unescape(got)
			if err != nil || back != c.in {
				t.Fatalf("Unescape(%q) = %q, %v; want %q", got, back, err, c.in)
			}
		})
	}
}

func TestEscapeAllBytes(t *testing.T) {
	for i := 0; i < 256; i++ {
		in := string([]byte{byte(i)})
		got := Escape(in)
		checkSafeLine(t, got)
		back, err := Unescape(got)
		if err != nil || back != in {
			t.Fatalf("byte %#x: Escape %q, Unescape %q, %v", i, got, back, err)
		}
	}
}

func TestEscapeEveryRune(t *testing.T) {
	// Every code point must round-trip, and a non-ASCII rune that passes
	// unchanged must be a safe one.
	for r := rune(0); r <= 0x10ffff; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		in := string(r)
		got := Escape(in)
		checkSafeLine(t, got)
		if got == in && r >= 0x80 && !safeRune(r) {
			t.Fatalf("U+%04X passed unescaped", r)
		}
		back, err := Unescape(got)
		if err != nil || back != in {
			t.Fatalf("U+%04X: Escape %q, Unescape %q, %v", r, got, back, err)
		}
	}
}

// checkSafeLine fails if s could do harm on a terminal or break a line.
func checkSafeLine(t *testing.T, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("output is not valid UTF-8: %q", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			t.Fatalf("output holds control byte %#x: %q", s[i], s)
		}
	}
	for _, r := range s {
		if r >= 0x80 && !safeRune(r) {
			t.Fatalf("output holds dangerous rune U+%04X: %q", r, s)
		}
	}
}

func TestEscapeKeepsPlainText(t *testing.T) {
	s := "feature/lead-mirror"
	if got := Escape(s); got != s {
		t.Fatal(got)
	}
}

func TestUnescape(t *testing.T) {
	good := []struct{ in, want string }{
		{``, ""},
		{`abc`, "abc"},
		{`\\`, `\`},
		{`\n\r\t`, "\n\r\t"},
		{`\x1b`, "\x1b"},
		{`\x1B`, "\x1b"},
		{`\xff`, "\xff"},
		{`\u{85}`, "\u0085"},
		{`\u{0085}`, "\u0085"},
		{`\u{1f600}`, "\U0001f600"},
		{`\u{10FFFF}`, "\U0010ffff"},
		{`\\x1b`, `\x1b`},
	}
	for _, c := range good {
		got, err := Unescape(c.in)
		if err != nil || got != c.want {
			t.Errorf("Unescape(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	bad := []string{
		`\`, `a\`, `\q`, `\x`, `\x1`, `\xzz`, `\x1g`, `\u`, `\u0085`, `\u{`, `\u{}`,
		`\u{85`, `\u{zz}`, `\u{110000}`, `\u{D800}`, `\u{DFFF}`, `\u{1234567}`,
		`\\\`,
	}
	for _, in := range bad {
		if got, err := Unescape(in); err == nil {
			t.Errorf("Unescape(%q) = %q, want an error", in, got)
		}
	}
}

func TestLine(t *testing.T) {
	inputs := []string{
		"",
		"abc",
		"plain ascii text with some length",
		"a\\b\\c",
		"a\nb\tc\rd",
		"\x1b[2J\x1b]52;c;aGk=\x07",
		"caf\u00e9 \u65e5\u672c\u8a9e \U0001f600",
		"\u202e\u200b\ufeff\u0085",
		"\xff\x80\xc3\xe2\x82",
		"mix\x1b\u202e\\\xffend\U0010ffff",
		"\U0010ffff\U0010ffff",
	}
	for _, s := range inputs {
		full := Escape(s)
		for max := 0; max <= len(full)+1; max++ {
			out, cut := Line(s, max)
			checkSafeLine(t, out)
			if len(out) > max {
				t.Fatalf("Line(%q, %d) = %q: %d bytes, over max", s, max, out, len(out))
			}
			if !strings.HasPrefix(full, out) {
				t.Fatalf("Line(%q, %d) = %q is not a prefix of %q", s, max, out, full)
			}
			if cut != (out != full) {
				t.Fatalf("Line(%q, %d): cut = %v, out %q, full %q", s, max, cut, out, full)
			}
			if max >= len(full) && (cut || out != full) {
				t.Fatalf("Line(%q, %d) cut though it fits: %q", s, max, out)
			}
			// The longest escape is ten bytes, so a cut that left ten or more
			// bytes unused stopped too early.
			if cut && max-len(out) >= 10 {
				t.Fatalf("Line(%q, %d) = %q stopped too early", s, max, out)
			}
			// The cut text must still unescape: no escape was split.
			back, err := Unescape(out)
			if err != nil {
				t.Fatalf("Line(%q, %d) = %q does not unescape: %v", s, max, out, err)
			}
			if !strings.HasPrefix(s, back) {
				t.Fatalf("Line(%q, %d): Unescape(%q) = %q is not a prefix of the input", s, max, out, back)
			}
		}
	}
}

func TestLineExactBoundaries(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
		cut  bool
	}{
		{"abc", 3, "abc", false},
		{"abc", 2, "ab", true},
		{"abc", 0, "", true},
		{"", 0, "", false},
		{"\x1b", 3, "", true},
		{"\x1b", 4, `\x1b`, false},
		{"a\x1b", 4, "a", true},
		{"\u202e", 7, "", true},
		{"\u202e", 8, `\u{202E}`, false},
		{"\u00e9a", 1, "", true},
		{"\u00e9a", 2, "\u00e9", true},
		{"\\", 1, "", true},
		{"\\", 2, `\\`, false},
		{"abc", -5, "", true},
		{"", -1, "", false},
	}
	for _, c := range cases {
		out, cut := Line(c.in, c.max)
		if out != c.want || cut != c.cut {
			t.Errorf("Line(%q, %d) = %q, %v; want %q, %v", c.in, c.max, out, cut, c.want, c.cut)
		}
	}
}

func streamAll(t *testing.T, chunks ...string) string {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, ch := range chunks {
		n, err := w.Write([]byte(ch))
		if err != nil || n != len(ch) {
			t.Fatalf("Write(%q) = %d, %v", ch, n, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// streamReference is the output a Writer must give for s: Escape on each part
// between newlines and tabs, with those two passing.
func streamReference(s string) string {
	var b strings.Builder
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\t' {
			b.WriteString(Escape(s[start:i]))
			b.WriteByte(s[i])
			start = i + 1
		}
	}
	b.WriteString(Escape(s[start:]))
	return b.String()
}

var writerInputs = []string{
	"",
	"one line\n",
	"a diff\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-old\n+new\n",
	"tab\there\n",
	"cr\rhere\r\n",
	"\x1b[2J\x1b]52;c;aGk=\x07done\n",
	"caf\u00e9 \u65e5\u672c\u8a9e \U0001f600\n",
	"\u202e\u200b\ufeff\u0085\u2028\n",
	"\xff\x80\xc3\xe2\x82",
	"truncated at the end \xe2\x82",
	"back\\slash \\x1b \\n\n",
	"\U0010ffff\ue000\n",
	"\x00\x7f\x9b\n",
}

func TestWriterMatchesReference(t *testing.T) {
	for _, in := range writerInputs {
		want := streamReference(in)
		if got := streamAll(t, in); got != want {
			t.Errorf("one write of %q = %q, want %q", in, got, want)
		}
	}
}

func TestWriterSplitAtEveryPosition(t *testing.T) {
	for _, in := range writerInputs {
		want := streamAll(t, in)
		for i := 0; i <= len(in); i++ {
			if got := streamAll(t, in[:i], in[i:]); got != want {
				t.Fatalf("input %q split at %d = %q, want %q", in, i, got, want)
			}
		}
		for i := 0; i <= len(in); i++ {
			for j := i; j <= len(in); j++ {
				if got := streamAll(t, in[:i], in[i:j], in[j:]); got != want {
					t.Fatalf("input %q split at %d and %d = %q, want %q", in, i, j, got, want)
				}
			}
		}
		var chunks []string
		for i := 0; i < len(in); i++ {
			chunks = append(chunks, in[i:i+1])
		}
		if got := streamAll(t, chunks...); got != want {
			t.Fatalf("input %q byte by byte = %q, want %q", in, got, want)
		}
	}
}

func TestWriterOutputIsSafe(t *testing.T) {
	var all strings.Builder
	for i := 0; i < 256; i++ {
		all.WriteByte(byte(i))
	}
	for r := rune(0x80); r < 0x3000; r++ {
		if r < 0xd800 || r > 0xdfff {
			all.WriteRune(r)
		}
	}
	inputs := append([]string{all.String()}, writerInputs...)
	for _, in := range inputs {
		out := streamAll(t, in)
		if !utf8.ValidString(out) {
			t.Fatalf("output for %q is not valid UTF-8", in)
		}
		for i := 0; i < len(out); i++ {
			c := out[i]
			if (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f {
				t.Fatalf("output for %q holds byte %#x", in, c)
			}
		}
		for _, r := range out {
			if r >= 0x80 && !safeRune(r) {
				t.Fatalf("output for %q holds dangerous rune U+%04X", in, r)
			}
		}
	}
}

func TestWriterCarriageReturnIsText(t *testing.T) {
	if got := streamAll(t, "a\rb\r\nc"); got != "a\\rb\\r\nc" {
		t.Fatalf("got %q", got)
	}
}

func TestWriterHoldsPartialRune(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if n, err := w.Write([]byte("a\xe2\x82")); n != 3 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if buf.String() != "a" {
		t.Fatalf("written so far %q, want %q", buf.String(), "a")
	}
	if n, err := w.Write([]byte("\xac!")); n != 2 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if buf.String() != "a\u20ac!" {
		t.Fatalf("written %q", buf.String())
	}
	if _, err := w.Write([]byte("\xf0\x9f")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	want := "a\u20ac!" + `\xf0\x9f`
	if buf.String() != want {
		t.Fatalf("after Close %q, want %q", buf.String(), want)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if buf.String() != want {
		t.Fatalf("second Close wrote: %q", buf.String())
	}
}

func TestWriterBrokenSequenceDoesNotStall(t *testing.T) {
	// A start byte followed by a byte that cannot continue it is released at
	// once, not held until Close.
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.Write([]byte("\xe2")); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("held byte was written: %q", buf.String())
	}
	if _, err := w.Write([]byte("A")); err != nil {
		t.Fatal(err)
	}
	if buf.String() != `\xe2A` {
		t.Fatalf("got %q", buf.String())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterReportsCallerCounts(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	in := []byte("\x1b[2J\x1b[2J\x1b[2J")
	n, err := w.Write(in)
	if n != len(in) || err != nil {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(in))
	}
	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty Write = %d, %v", n, err)
	}
}

func TestWriterWriteAfterClose(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("x")); n != 0 || err == nil {
		t.Fatalf("Write after Close = %d, %v", n, err)
	}
}

type failWriter struct {
	err   error
	short bool
	calls int
}

func (f *failWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.short {
		return len(p) - 1, nil
	}
	return 0, f.err
}

func TestWriterPassesErrors(t *testing.T) {
	boom := errors.New("boom")
	fw := &failWriter{err: boom}
	w := NewWriter(fw)
	if n, err := w.Write([]byte("abc")); n != 0 || !errors.Is(err, boom) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if _, err := w.Write([]byte("abc")); !errors.Is(err, boom) {
		t.Fatalf("second Write error = %v", err)
	}
	if fw.calls != 1 {
		t.Fatalf("wrapped writer called %d times", fw.calls)
	}
	if err := w.Close(); !errors.Is(err, boom) {
		t.Fatalf("Close = %v", err)
	}

	w = NewWriter(&failWriter{short: true})
	if _, err := w.Write([]byte("abc")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}

	w = NewWriter(&failWriter{err: boom})
	if _, err := w.Write([]byte("\xe2")); err != nil { // held back, nothing written yet
		t.Fatal(err)
	}
	if err := w.Close(); !errors.Is(err, boom) {
		t.Fatalf("Close of held byte = %v", err)
	}
}

func ExampleEscape() {
	fmt.Println(Escape("ok\x1b[2J\u202e"))
	// Output: ok\x1b[2J\u{202E}
}

func ExampleLine() {
	out, cut := Line("a\x1bb", 3)
	fmt.Println(out, cut)
	// Output: a true
}

func ExampleNewWriter() {
	w := NewWriter(os.Stdout)
	w.Write([]byte("line\x1b[2J\n"))
	w.Close()
	// Output: line\x1b[2J
}

// defaultIgnorableRanges lists the Default_Ignorable_Code_Point ranges of the
// Unicode Character Database, written out here so that the test does not
// depend on the tables the code uses. Code points in these ranges print as
// nothing.
var defaultIgnorableRanges = [][2]rune{
	{0x00ad, 0x00ad}, {0x034f, 0x034f}, {0x061c, 0x061c},
	{0x115f, 0x1160}, {0x17b4, 0x17b5}, {0x180b, 0x180f},
	{0x200b, 0x200f}, {0x202a, 0x202e}, {0x2060, 0x206f},
	{0x3164, 0x3164}, {0xfe00, 0xfe0f}, {0xfeff, 0xfeff},
	{0xffa0, 0xffa0}, {0xfff0, 0xfff8}, {0x1bca0, 0x1bca3},
	{0x1d173, 0x1d17a}, {0xe0000, 0xe0fff},
}

func TestEscapeDefaultIgnorable(t *testing.T) {
	for _, rg := range defaultIgnorableRanges {
		for r := rg[0]; r <= rg[1]; r++ {
			in := "a" + string(r) + "b"
			got := Escape(in)
			if strings.ContainsRune(got, r) {
				t.Fatalf("U+%04X passed unescaped: %q", r, got)
			}
			back, err := Unescape(got)
			if err != nil || back != in {
				t.Fatalf("U+%04X: Escape %q, Unescape %q, %v", r, got, back, err)
			}
		}
	}
}
