package title

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

type titleCase struct {
	runtime string
	busy    bool
	action  bool
	title   string
	line    int
}

func loadTitles(t *testing.T) []titleCase {
	t.Helper()
	f, err := os.Open("testdata/titles.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cases []titleCase
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 3 {
			t.Fatalf("testdata/titles.txt:%d: want at least 3 tab-separated fields", n)
		}
		c := titleCase{runtime: parts[0], line: n}
		if len(parts) == 4 {
			c.title = parts[3]
		}
		for i, dst := range []*bool{&c.busy, &c.action} {
			switch parts[i+1] {
			case "true":
				*dst = true
			case "false":
			default:
				t.Fatalf("testdata/titles.txt:%d: field %d is %q, want true or false", n, i+2, parts[i+1])
			}
		}
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

var versions = map[string]string{"claude": "2.1.325", "codex": "0.169"}

func TestRecordedTitles(t *testing.T) {
	cases := loadTitles(t)
	seen := map[string]int{}
	for _, c := range cases {
		seen[c.runtime]++
		version, ok := versions[c.runtime]
		if !ok {
			t.Fatalf("testdata/titles.txt:%d: unknown runtime %q", c.line, c.runtime)
		}
		rules, ok := For(c.runtime, version)
		if !ok {
			t.Fatalf("For(%q, %q) = false", c.runtime, version)
		}
		got := rules.Read(c.title)
		if got.Busy != c.busy || got.ActionRequired != c.action {
			t.Errorf("testdata/titles.txt:%d: %s %q: got busy=%v action=%v, want busy=%v action=%v",
				c.line, c.runtime, c.title, got.Busy, got.ActionRequired, c.busy, c.action)
		}
	}
	for rt := range versions {
		if seen[rt] == 0 {
			t.Errorf("testdata/titles.txt has no %s titles", rt)
		}
	}
}

func TestForKnowsOnlyRecordedVersions(t *testing.T) {
	for _, c := range []struct {
		runtime, version string
		ok               bool
	}{
		{"claude", "2.1.325", true},
		{"codex", "0.169", true},
		{"claude", "2.1.326", false},
		{"claude", "2.1", false},
		{"claude", "", false},
		{"claude", "0.169", false},
		{"codex", "0.169.0", false},
		{"codex", "0.170", false},
		{"codex", "2.1.325", false},
		{"", "", false},
		{"Claude", "2.1.325", false},
		{"gemini", "1", false},
	} {
		rules, ok := For(c.runtime, c.version)
		if ok != c.ok {
			t.Errorf("For(%q, %q) ok = %v, want %v", c.runtime, c.version, ok, c.ok)
		}
		if !ok && rules.Runtime() != "" {
			t.Errorf("For(%q, %q) returned rules for %q with ok false", c.runtime, c.version, rules.Runtime())
		}
	}
}

func mustRules(t *testing.T, runtime string) Rules {
	t.Helper()
	r, ok := For(runtime, versions[runtime])
	if !ok {
		t.Fatalf("For(%q) = false", runtime)
	}
	return r
}

func TestCodexRules(t *testing.T) {
	r := mustRules(t, "codex")
	for _, c := range []struct {
		title        string
		busy, action bool
		name         string
	}{
		{"⠀ x", true, false, "first braille pattern U+2800"},
		{"⣿ x", true, false, "last braille pattern U+28FF"},
		{"⟿ x", false, false, "rune before the braille block"},
		{"⤀ x", false, false, "rune after the braille block"},
		{" ⠸ x", false, false, "braille not first"},
		{"◐ x", false, false, "a claude spinner is not a codex spinner"},
		{"[ . ] Action Required | x", false, true, "blink phase dot"},
		{"⠸ Action Required | x", true, true, "busy and action required"},
		{"action required", false, false, "the words are case sensitive"},
		{"Action  Required", false, false, "the words are exact"},
		{"x Action Required", false, true, "the words anywhere"},
	} {
		got := r.Read(c.title)
		if got.Busy != c.busy || got.ActionRequired != c.action {
			t.Errorf("%s: Read(%q) = busy %v action %v, want %v %v", c.name, c.title, got.Busy, got.ActionRequired, c.busy, c.action)
		}
	}
}

func TestClaudeRules(t *testing.T) {
	r := mustRules(t, "claude")
	for _, c := range []struct {
		title string
		busy  bool
		notes bool
		name  string
	}{
		{"✳ Claude Code", false, false, "idle glyph sets nothing"},
		{"✳", false, false, "idle glyph alone"},
		{"◐ x", true, false, "recorded spinner"},
		{"◑ x", true, false, "recorded spinner"},
		{"◒ x", true, true, "unrecorded symbol is busy, with a note"},
		{"⠸ x", true, true, "a braille glyph is an unrecorded symbol"},
		{"✻ x", true, true, "another star is not the idle glyph"},
		{"Claude Code", false, true, "no glyph: no reading, with a note"},
		{"", false, true, "empty: no reading, with a note"},
		{"a ◐", false, true, "spinner not first"},
		{" ✳", false, true, "leading space is no glyph"},
		{"\xff x", true, true, "invalid first byte cannot be told: busy, with a note"},
		{"\xe2\x97", true, true, "cut glyph cannot be told: busy, with a note"},
	} {
		got := r.Read(c.title)
		if got.Busy != c.busy {
			t.Errorf("%s: Read(%q).Busy = %v, want %v", c.name, c.title, got.Busy, c.busy)
		}
		if got.ActionRequired {
			t.Errorf("%s: Read(%q).ActionRequired is set; claude never sets it", c.name, c.title)
		}
		if (len(got.Notes) > 0) != c.notes {
			t.Errorf("%s: Read(%q).Notes = %q, want notes: %v", c.name, c.title, got.Notes, c.notes)
		}
	}
}

func TestClaudeNeverActionRequired(t *testing.T) {
	r := mustRules(t, "claude")
	for _, title := range []string{"[ ! ] Action Required | x", "✳ Action Required", "◐ Action Required"} {
		if r.Read(title).ActionRequired {
			t.Errorf("Read(%q).ActionRequired is set", title)
		}
	}
}

func TestBoundedRead(t *testing.T) {
	codex := mustRules(t, "codex")
	pad := strings.Repeat("a", MaxTitleBytes)

	// Text past the cap is not read.
	if got := codex.Read(pad + "Action Required"); got.ActionRequired {
		t.Error("Action Required past the cap was read")
	}
	if got := codex.Read(pad[:MaxTitleBytes-len(actionRequired)] + actionRequired); !got.ActionRequired {
		t.Error("Action Required ending at the cap was not read")
	}
	got := codex.Read(pad + "x")
	if len(got.Notes) == 0 {
		t.Error("a title over the cap has no note")
	}
	if got := codex.Read(pad); len(got.Notes) != 0 {
		t.Errorf("a title at the cap has notes %q", got.Notes)
	}

	// A glyph cut by the cap is not a panic and not a reading.
	cut := strings.Repeat("a", MaxTitleBytes-1) + "◐"
	if got := mustRules(t, "claude").Read(cut); got.Busy {
		t.Error("a spinner past the cap made the title busy")
	}
}

func TestHostileTitles(t *testing.T) {
	huge := strings.Repeat("⠸", 1<<20/3+1) // over 1 MiB
	if len(huge) <= 1<<20 {
		t.Fatalf("huge title is %d bytes", len(huge))
	}
	invalid := strings.Repeat("\xff\xfe\xc0\x80\xed\xa0\x80", 100)
	for runtime := range versions {
		r := mustRules(t, runtime)
		for name, title := range map[string]string{
			"empty":             "",
			"1 MiB":             huge,
			"1 MiB ascii":       strings.Repeat("x", 1<<20),
			"invalid":           invalid,
			"lone byte":         "\xff",
			"cut rune":          "\xe2\xa0",
			"NUL":               "\x00\x00",
			"glyph only":        "⠸",
			"claude glyph only": "◐",
			"idle glyph only":   "✳",
			"escape":            "\x1b]0;x\x07",
		} {
			got := r.Read(title)
			if len(got.Notes) > 0 {
				for _, n := range got.Notes {
					if !utf8.ValidString(n) {
						t.Errorf("%s %s: note is not valid UTF-8", runtime, name)
					}
				}
			}
		}
	}
	// The glyph-only titles read as the rules say.
	if !mustRules(t, "codex").Read("⠸").Busy {
		t.Error("codex: a braille glyph alone is not busy")
	}
	if !mustRules(t, "claude").Read("◐").Busy {
		t.Error("claude: a spinner glyph alone is not busy")
	}
	if mustRules(t, "claude").Read("✳").Busy {
		t.Error("claude: the idle glyph alone is busy")
	}
}

func TestZeroRules(t *testing.T) {
	var r Rules
	if got := r.Read("⠸ Action Required"); got.Busy || got.ActionRequired || got.Notes != nil {
		t.Errorf("zero Rules read %+v", got)
	}
	if r.Runtime() != "" {
		t.Errorf("zero Rules runtime = %q", r.Runtime())
	}
}

func FuzzRead(f *testing.F) {
	titles := loadFuzzSeeds(f)
	for _, s := range titles {
		f.Add(s)
	}
	f.Add("")
	f.Add("\xff")
	f.Add("[ ! ] Action Required | work")
	f.Fuzz(func(t *testing.T, title string) {
		for runtime, version := range versions {
			r, ok := For(runtime, version)
			if !ok {
				t.Fatalf("For(%q, %q) = false", runtime, version)
			}
			got := r.Read(title)
			if runtime == "claude" && got.ActionRequired {
				t.Fatalf("claude set ActionRequired for %q", title)
			}
			// The reading depends on the first MaxTitleBytes bytes only.
			head := title
			if len(head) > MaxTitleBytes {
				head = head[:MaxTitleBytes]
			}
			again := r.Read(head)
			if again.Busy != got.Busy || again.ActionRequired != got.ActionRequired {
				t.Fatalf("%s: reading of %q differs from its first %d bytes", runtime, title, MaxTitleBytes)
			}
		}
	})
}

func loadFuzzSeeds(f *testing.F) []string {
	file, err := os.Open("testdata/titles.txt")
	if err != nil {
		f.Fatal(err)
	}
	defer file.Close()
	var out []string
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		if parts := strings.SplitN(line, "\t", 4); len(parts) == 4 {
			out = append(out, parts[3])
		}
	}
	return out
}
