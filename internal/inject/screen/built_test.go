package screen

import (
	"reflect"
	"strings"
	"testing"
)

// fixtureNamed returns the fixture of a runtime by name.
func fixtureNamed(t *testing.T, runtime, name string) loaded {
	t.Helper()
	for _, l := range loadAll(t) {
		if l.runtime == runtime && l.name == name {
			return l
		}
	}
	t.Fatalf("no fixture %s/%s", runtime, name)
	return loaded{}
}

// claudeScreen builds a 36x120 claude screen: a transcript row, the rule, the
// box rows, the rule and the footer at the bottom.
func claudeScreen(box []string, footer string) Snapshot {
	rule := strings.Repeat("─", 120)
	r := make([]string, 36)
	r[3] = "● hello"
	n := 36 - 3 - len(box)
	r[n-1] = rule
	copy(r[n:], box)
	r[n+len(box)] = rule
	r[n+len(box)+1] = footer
	return Snapshot{Text: strings.Join(r, "\n"), Cols: 120, Rows: 36, CursorX: 2, CursorY: n, AltScreen: true}
}

func TestClaudeFooters(t *testing.T) {
	rec := mustFor(t, "claude")
	for _, c := range []struct {
		footer string
		known  bool
		mode   string
		shells bool
		work   bool
	}{
		{"  ⏸ manual mode on", true, "manual", false, false},
		{"  ⏸ manual mode on · ? for shortcuts", true, "manual", false, false},
		{"  ⏸ manual mode on · esc to interrupt", true, "manual", false, true},
		{"  ⏸ manual mode on · 1 shell · ↓ to manage", true, "manual", true, false},
		{"  ⏸ manual mode on · 2 shells · ↓ to manage", true, "manual", true, false},
		{"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents", true, "auto", false, false},
		{"  ⏸ manual mode on · 0 shells", false, "", false, false},
		{"  ⏸ plan mode on", false, "", false, false},
		{"  ⏵⏵ accept edits on (shift+tab to cycle)", false, "", false, false},
		{"  ⏸ manual mode on · something new", false, "", false, false},
		{"  ⏸ manual mode on ·  ? for shortcuts", false, "", false, false},
		{"   ⏸ manual mode on", false, "", false, false},
		{"⏸ manual mode on", false, "", false, false},
		{"  shift+tab to cycle", false, "", false, false},
		// The paste hint is a footer only together with the marker in the
		// composer; this composer is empty.
		{"  paste again to expand", false, "", false, false},
	} {
		got := rec.Read(claudeScreen([]string{"❯ "}, c.footer))
		if got.Known != c.known || got.Mode != c.mode || got.BackgroundTask != c.shells || got.Working != c.work {
			t.Errorf("footer %q: %+v", c.footer, got)
		}
		if c.known && !got.ComposerEmpty {
			t.Errorf("footer %q: composer not empty: %+v", c.footer, got)
		}
	}
}

// TestStateHiddenOnlyUnderThePasteFooter: StateHidden is true exactly for the
// claude paste footer, on a built screen and on every recorded screen.
func TestStateHiddenOnlyUnderThePasteFooter(t *testing.T) {
	rec := mustFor(t, "claude")
	marker := "❯ x[Pasted text #1 +20 lines]"
	for _, c := range []struct {
		footer string
		hidden bool
	}{
		{"  paste again to expand", true},
		{"  ⏸ manual mode on", false},
		{"  ⏸ manual mode on · esc to interrupt", false},
		{"  ⏸ manual mode on · 1 shell · ↓ to manage", false},
	} {
		got := rec.Read(claudeScreen([]string{marker}, c.footer))
		if !got.Known || got.StateHidden != c.hidden || (len(got.Notes) > 0) != c.hidden {
			t.Errorf("%q: %+v", c.footer, got)
		}
	}
	// An unknown screen has no StateHidden: only the notes.
	if got := rec.Read(claudeScreen([]string{"❯ x"}, "  paste again to expand")); got.Known || got.StateHidden {
		t.Errorf("%+v", got)
	}
	n := 0
	for _, l := range loadAll(t) {
		got := recogniserFor(t, l).Read(l.snap())
		rs := strings.Split(strings.TrimRight(l.text, "\n"), "\n")
		paste := l.runtime == "claude" && got.Known && strings.TrimSpace(rs[len(rs)-1]) == "paste again to expand"
		if got.StateHidden != paste {
			t.Errorf("%s/%s: StateHidden=%v, paste footer=%v", l.runtime, l.name, got.StateHidden, paste)
		}
		if paste {
			n++
		}
	}
	if n == 0 {
		t.Error("no recorded screen with the paste footer")
	}
}

func TestClaudeBackgroundStatusLine(t *testing.T) {
	rec := mustFor(t, "claude")
	s := claudeScreen([]string{"❯ "}, "  ⏸ manual mode on · ? for shortcuts")
	if rec.Read(s).BackgroundTask {
		t.Fatal("a background task without a mark")
	}
	r := strings.Split(s.Text, "\n")
	r[20] = "✻ Sautéed for 1s · done 1:32 PM · 1 shell still running"
	s.Text = strings.Join(r, "\n")
	if got := rec.Read(s); !got.BackgroundTask || !got.ComposerEmpty {
		t.Errorf("status line not read: %+v", got)
	}
	// Above the bottom rows the status line is history.
	r[20] = ""
	r[1] = "✻ Sautéed for 1s · done 1:32 PM · 1 shell still running"
	s.Text = strings.Join(r, "\n")
	if rec.Read(s).BackgroundTask {
		t.Error("a status line above the bottom rows counted")
	}
}

func TestClaudeComposerShapes(t *testing.T) {
	rec := mustFor(t, "claude")
	foot := "  ⏸ manual mode on · ? for shortcuts"
	t.Run("two blank rows is not an empty composer", func(t *testing.T) {
		got := rec.Read(claudeScreen([]string{"❯ ", "  "}, foot))
		if got.ComposerEmpty || !got.Composer {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a bare prompt glyph is empty", func(t *testing.T) {
		if got := rec.Read(claudeScreen([]string{"❯"}, foot)); !got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
		if got := rec.Read(claudeScreen([]string{"❯   "}, foot)); !got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
	})
	t.Run("typed blanks are text, not an empty composer", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			x, y   int
			hidden bool
			want   bool
		}{
			{"cursor at the first input column", 2, 32, false, true},
			{"cursor after the typed blanks", 6, 32, false, false},
			{"cursor before the first input column", 1, 32, false, false},
			{"cursor on another row", 2, 20, false, false},
			{"cursor hidden", 2, 32, true, false},
		} {
			s := claudeScreen([]string{"❯     "}, foot)
			s.CursorX, s.CursorY, s.CursorHidden = c.x, c.y, c.hidden
			got := rec.Read(s)
			if !got.Known || !got.Composer || got.ComposerEmpty != c.want {
				t.Errorf("%s: %+v", c.name, got)
			}
			if !c.want && got.Holds(" ") {
				t.Errorf("%s: blanks held as a header: %+v", c.name, got)
			}
		}
	})
	t.Run("a collapsed paste and the paste footer", func(t *testing.T) {
		marker := "❯ paste one line[Pasted text #1 +20 lines]"
		hint := "  paste again to expand"
		got := rec.Read(claudeScreen([]string{marker}, hint))
		if !got.Known || !got.Composer || got.ComposerEmpty || !got.PastedMarker || got.Mode != "" ||
			got.ComposerText != "paste one line[Pasted text #1 +20 lines]" || got.Working || got.BackgroundTask {
			t.Errorf("%+v", got)
		}
		// The footer hides the working hint and the shell count: the
		// reading says so, and the false values are not observations.
		if !got.StateHidden || len(got.Notes) == 0 || !strings.Contains(got.Notes[0], "OSC 7501") {
			t.Errorf("paste footer: state not marked hidden: %+v", got)
		}
		// The marker on a continuation row counts as well.
		got = rec.Read(claudeScreen([]string{"❯ paste one line", "  [Pasted text #2]"}, hint))
		if !got.Known || got.ComposerEmpty || !got.PastedMarker || got.Mode != "" {
			t.Errorf("marker on a second row: %+v", got)
		}
		// Without the marker in the composer the footer is not known.
		for name, box := range map[string][]string{
			"empty composer":    {"❯ "},
			"bare glyph":        {"❯"},
			"typed text":        {"❯ paste one line"},
			"wrong marker":      {"❯ [Pasted text 1]"},
			"two rows, no mark": {"❯ one", "  two"},
			"marker as a glyph": {"[Pasted text #1 +2 lines]"},
			"codex marker":      {"❯ one", "  two [Pasted Content 12 chars]"},
		} {
			if got := rec.Read(claudeScreen(box, hint)); got.Known || got.ComposerEmpty || got.Mode != "" {
				t.Errorf("%s: %+v", name, got)
			}
		}
		// A faint marker is a hint, and a hint never makes the footer known.
		s := claudeScreen([]string{marker}, hint)
		s.CursorX, s.CursorY = 2, 32
		s.Faint = []Span{{Row: 32, From: 2, To: 44}}
		if got := rec.Read(s); got.Known || got.ComposerEmpty || got.Placeholder {
			t.Errorf("faint marker: %+v", got)
		}
		// Only the whole hint is the footer: no other part, no other blanks.
		for _, f := range []string{
			"  paste again to expand · ? for shortcuts",
			"  ⏸ manual mode on · paste again to expand",
			"   paste again to expand",
			"paste again to expand",
			"  Paste again to expand",
			"  paste again to expand now",
			"  paste again",
		} {
			if got := rec.Read(claudeScreen([]string{marker}, f)); got.Known {
				t.Errorf("footer %q: %+v", f, got)
			}
		}
	})
	t.Run("a box without a prompt is unknown", func(t *testing.T) {
		for _, row := range []string{"hello", "> hello", "❯hello", "  ❯ hello", ""} {
			if got := rec.Read(claudeScreen([]string{row}, foot)); got.Known {
				t.Errorf("%q: %+v", row, got)
			}
		}
	})
	t.Run("cursor outside the box", func(t *testing.T) {
		// Blanks are an empty composer only with the cursor at the first
		// input column of the prompt row; anywhere else the owner typed them.
		s := claudeScreen([]string{"❯ "}, foot)
		s.CursorY = 5
		if got := rec.Read(s); got.CursorInComposer || got.ComposerEmpty || !got.Composer {
			t.Errorf("%+v", got)
		}
		s.CursorY, s.CursorHidden = 32, true
		if got := rec.Read(s); got.CursorInComposer || got.ComposerEmpty {
			t.Errorf("hidden cursor counted: %+v", got)
		}
	})
	t.Run("rows below the footer are unknown", func(t *testing.T) {
		s := claudeScreen([]string{"❯ "}, foot)
		r := strings.Split(s.Text, "\n")
		r[35] = "  something below"
		s.Text = strings.Join(r, "\n")
		if got := rec.Read(s); got.Known {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a short rule is not a rule", func(t *testing.T) {
		s := claudeScreen([]string{"❯ "}, foot)
		s.Text = strings.Replace(s.Text, strings.Repeat("─", 120), strings.Repeat("─", 119), 1)
		if got := rec.Read(s); got.Known {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a tall box", func(t *testing.T) {
		box := []string{"❯ line 1"}
		for i := 2; i <= 12; i++ {
			box = append(box, "  line "+string(rune('a'+i)))
		}
		got := rec.Read(claudeScreen(box, foot))
		if !got.Known || got.ComposerEmpty || strings.Count(got.ComposerText, "\n") != 11 {
			t.Errorf("%+v", got)
		}
	})
	t.Run("pasted marker", func(t *testing.T) {
		got := rec.Read(claudeScreen([]string{"❯ look at [Pasted text #1 +20 lines]"}, foot))
		if !got.PastedMarker || got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
		if got := rec.Read(claudeScreen([]string{"❯ look at [Pasted text 1"}, foot)); got.PastedMarker {
			t.Errorf("%+v", got)
		}
	})
}

// codexScreen builds a 36x120 codex screen with the composer rows at row 30.
func codexScreen(box []string, below ...string) Snapshot {
	r := make([]string, 36)
	r[3] = "• hello"
	copy(r[30:], box)
	copy(r[30+len(box)+1:], below)
	return Snapshot{Text: strings.Join(r, "\n"), Cols: 120, Rows: 36, CursorX: 2, CursorY: 30, AltScreen: true}
}

// codexTall builds a codex screen of h rows (a screen of at most MaxRows)
// whose bottom is a transcript row, a blank row, the composer rows, a blank row
// and the below rows. When they do not fit, the top is scrolled off. The cursor
// is on the prompt row, column 2, when that row is on the screen.
func codexTall(h int, box []string, below ...string) Snapshot {
	full := append([]string{"• hello", ""}, box...)
	full = append(full, "")
	full = append(full, below...)
	skipped := 0
	if len(full) > h {
		skipped = len(full) - h
		full = full[skipped:]
	}
	return Snapshot{
		Text: strings.Join(full, "\n"), Cols: 120, Rows: h,
		CursorX: 2, CursorY: 2 - skipped, AltScreen: true,
	}
}

// tallBox is a composer of n rows: a prompt row and n-1 continuation rows.
func tallBox(n int, prompt, cont string) []string {
	box := []string{prompt}
	for len(box) < n {
		box = append(box, cont)
	}
	return box
}

func TestCodexShapes(t *testing.T) {
	rec := mustFor(t, "codex")
	status := "  GPT-6-Luna medium · /home/user/work"
	t.Run("empty prompt, no hint", func(t *testing.T) {
		for _, p := range []string{"›", "› ", "› "} {
			got := rec.Read(codexScreen([]string{p}, status))
			if !got.ComposerEmpty || got.Placeholder || got.Mode != "" || got.BackgroundTask {
				t.Errorf("%q: %+v", p, got)
			}
		}
	})
	t.Run("hint text without style is owner text", func(t *testing.T) {
		got := rec.Read(codexScreen([]string{"› Ask Codex to do anything"}, status))
		if got.ComposerEmpty || got.ComposerText != "Ask Codex to do anything" {
			t.Errorf("%+v", got)
		}
	})
	t.Run("hint text with style is empty", func(t *testing.T) {
		s := codexScreen([]string{"› Ask Codex to do anything"}, status)
		s.Faint = []Span{{Row: 30, From: 2, To: 26}}
		got := rec.Read(s)
		if !got.ComposerEmpty || !got.Placeholder || got.ComposerText != "" {
			t.Errorf("%+v", got)
		}
	})
	t.Run("no status row is unknown", func(t *testing.T) {
		if got := rec.Read(codexScreen([]string{"› hi"})); got.Known {
			t.Errorf("%+v", got)
		}
	})
	t.Run("composer far from the bottom is unknown", func(t *testing.T) {
		got := rec.Read(codexScreen([]string{"› hi"}, "", "", "", "", "  status"))
		if got.Known {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a choice list is a dialog, not a composer", func(t *testing.T) {
		got := rec.Read(codexScreen([]string{"› 1. Yes, proceed (y)", "  2. No"}, status))
		if !got.Dialog || got.Composer || got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
	})
	t.Run("pasted marker on a continuation row", func(t *testing.T) {
		got := rec.Read(codexScreen([]string{"› look", "  [Pasted Content 5000 chars]"}, status))
		if !got.PastedMarker || got.ComposerEmpty || !got.Holds("look[PastedContent5000chars]") {
			t.Errorf("%+v", got)
		}
	})
	t.Run("echoed prompts above are not the composer", func(t *testing.T) {
		s := codexScreen([]string{"›"}, status)
		r := strings.Split(s.Text, "\n")
		r[20] = "› an earlier prompt"
		s.Text = strings.Join(r, "\n")
		if got := rec.Read(s); !got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
	})
	t.Run("working line", func(t *testing.T) {
		s := codexScreen([]string{"›"}, status)
		r := strings.Split(s.Text, "\n")
		r[27] = "• Working (4s • esc to interrupt)"
		s.Text = strings.Join(r, "\n")
		if got := rec.Read(s); !got.Working || !got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
	})
	t.Run("the hint must be the recorded text", func(t *testing.T) {
		for _, text := range []string{
			"Ask Codex to do anything else", "Ask Codex", "ask codex to do anything",
			"Type a message", "queued: do the thing", "x",
		} {
			s := codexScreen([]string{"› " + text}, status)
			s.Faint = []Span{{Row: 30, From: 2, To: 2 + len([]rune(text))}}
			got := rec.Read(s)
			if got.ComposerEmpty || got.Placeholder || got.ComposerText != text || !got.Composer {
				t.Errorf("faint %q read as a hint: %+v", text, got)
			}
		}
		// The recorded hint, with blanks after it, is still the hint.
		s := codexScreen([]string{"› Ask Codex to do anything   "}, status)
		s.Faint = []Span{{Row: 30, From: 2, To: 26}}
		if got := rec.Read(s); !got.ComposerEmpty || !got.Placeholder {
			t.Errorf("%+v", got)
		}
	})
	t.Run("typed blanks are text, not an empty composer", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			x, y   int
			hidden bool
			want   bool
		}{
			{"cursor at the first input column", 2, 30, false, true},
			{"cursor after the typed blanks", 6, 30, false, false},
			{"cursor before the first input column", 1, 30, false, false},
			{"cursor on another row", 2, 20, false, false},
			{"cursor hidden", 2, 30, true, false},
		} {
			s := codexScreen([]string{"›     "}, status)
			s.CursorX, s.CursorY, s.CursorHidden = c.x, c.y, c.hidden
			got := rec.Read(s)
			if !got.Known || !got.Composer || got.ComposerEmpty != c.want {
				t.Errorf("%s: %+v", c.name, got)
			}
			if !c.want && got.Holds(" ") {
				t.Errorf("%s: blanks held as a header: %+v", c.name, got)
			}
		}
	})
	t.Run("a tall composer is found from the bottom", func(t *testing.T) {
		for _, n := range []int{2, 17, 18, 19, 20, 21, 22, 40, 100, 190} {
			box := tallBox(n, "› first", "  line")
			box[n-1] = "   [Pasted Content 5000 chars]"
			got := rec.Read(codexTall(200, box, status, "  more status"))
			if !got.Known || !got.Composer || got.ComposerEmpty || !got.PastedMarker ||
				strings.Count(got.ComposerText, "\n") != n-1 || !got.CursorInComposer {
				t.Errorf("%d rows: %+v", n, got)
			}
			if !got.Holds("first") {
				t.Errorf("%d rows: header not held: %+v", n, got)
			}
		}
	})
	t.Run("a composer that does not fit the screen is never empty", func(t *testing.T) {
		// A message of 4 KiB of one-character lines is 2048 rows: the
		// screen shows the end of it. The prompt row is off the screen
		// when the rows do not fit, and the screen is unknown then; so it
		// is when the prompt row is the top row, because the blank row
		// above it cannot be seen.
		for _, n := range []int{150, 190, 195, 196, 197, 198, 199, 200, 300, 2048} {
			got := rec.Read(codexTall(200, tallBox(n, "› x", "  x"), status))
			if got.ComposerEmpty || got.Placeholder {
				t.Errorf("%d rows: %+v", n, got)
			}
			fits := n+3 <= 200 // the blank row above, the composer, the blank row and the status row
			if got.Known != fits {
				t.Errorf("%d rows: known=%v, want %v: %+v", n, got.Known, fits, got.Notes)
			}
		}
		// The tallest screen and a one-row composer on a small screen.
		if got := rec.Read(codexTall(5, []string{"› x"}, status)); !got.Known || got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
	})
	t.Run("a broken walk is unknown", func(t *testing.T) {
		box := tallBox(21, "› first", "  line")
		for name, mod := range map[string]func([]string){
			"blank row inside":       func(b []string) { b[10] = "" },
			"blanks only inside":     func(b []string) { b[10] = "   " },
			"row without indent":     func(b []string) { b[10] = " line" },
			"unindented text":        func(b []string) { b[10] = "line" },
			"no prompt row":          func(b []string) { b[0] = "  first" },
			"prompt glyph, no blank": func(b []string) { b[0] = "›first" },
			"prompt is a choice":     func(b []string) { b[0] = "› 1. first" },
		} {
			b := append([]string(nil), box...)
			mod(b)
			got := rec.Read(codexTall(36, b, status))
			if got.Known && !got.Dialog && got.ComposerEmpty {
				t.Errorf("%s: %+v", name, got)
			}
			if name != "prompt is a choice" && got.Known {
				t.Errorf("%s: read as known: %+v", name, got)
			}
			if name == "prompt is a choice" && (got.Composer || got.ComposerEmpty) {
				t.Errorf("%s: %+v", name, got)
			}
		}
		// The prompt row must be on the screen: with the prompt row
		// scrolled off, the transcript above it is not the composer.
		got := rec.Read(codexTall(20, box, status))
		if got.Known {
			t.Errorf("prompt row scrolled off: %+v", got)
		}
	})
	t.Run("a blank row must be directly above the prompt row", func(t *testing.T) {
		// The walk must not join transcript rows to the composer: what is
		// above the prompt row is a blank row, as in every recorded screen.
		box := tallBox(5, "› first", "  line")
		if got := rec.Read(codexTall(36, box, status)); !got.Known || !got.Composer {
			t.Fatalf("with the blank row: %+v", got)
		}
		for name, above := range map[string]string{
			"transcript row":      "• earlier answer",
			"echoed continuation": "  earlier line",
			"echoed prompt":       "› earlier prompt",
			"rule":                "────",
		} {
			s := codexTall(36, box, status)
			r := strings.Split(s.Text, "\n")
			r[1] = above
			s.Text = strings.Join(r, "\n")
			if got := rec.Read(s); got.Known || len(got.Notes) == 0 {
				t.Errorf("%s directly above: %+v", name, got)
			}
		}
		// A one-row composer is held to the same rule.
		s := codexScreen([]string{"› hi"}, status)
		r := strings.Split(s.Text, "\n")
		r[29] = "• answer"
		s.Text = strings.Join(r, "\n")
		if got := rec.Read(s); got.Known {
			t.Errorf("one row, no blank above: %+v", got)
		}
		// A prompt row at the top row: what is above it cannot be seen.
		s = Snapshot{Text: strings.Join([]string{"› hi", "", "  GPT-6-Luna medium · /home/user/work", "", ""}, "\n"), Cols: 120, Rows: 5, CursorX: 2, AltScreen: true}
		if got := rec.Read(s); got.Known {
			t.Errorf("prompt row at row 0: %+v", got)
		}
		// The same screen with a row above it is known.
		s = Snapshot{Text: strings.Join([]string{"", "› hi", "", "  GPT-6-Luna medium · /home/user/work", ""}, "\n"), Cols: 120, Rows: 5, CursorX: 2, CursorY: 1, AltScreen: true}
		if got := rec.Read(s); !got.Known || !got.Composer {
			t.Errorf("prompt row at row 1: %+v", got)
		}
	})
	t.Run("working line above a tall composer", func(t *testing.T) {
		working := "• Working (5s • esc to interrupt)"
		for _, n := range []int{1, 2, 10, 21} {
			s := codexTall(60, tallBox(n, "› first", "  line"), status)
			r := strings.Split(s.Text, "\n")
			// The working row is directly above the blank row that is
			// above the composer.
			r[0] = working
			s.Text = strings.Join(r, "\n")
			got := rec.Read(s)
			if !got.Known || !got.Composer || !got.Working {
				t.Errorf("%d rows: %+v", n, got)
			}
			s.Text = strings.Replace(s.Text, working, "• Worked for 5s", 1)
			if got := rec.Read(s); !got.Known || got.Working {
				t.Errorf("%d rows, no working row: %+v", n, got)
			}
		}
		// Rows farther above than the bottom window above the prompt row
		// are not looked at.
		s := codexTall(80, tallBox(21, "› first", "  line"), status)
		rows := []string{working}
		for i := 0; i < 30; i++ {
			rows = append(rows, "• earlier answer")
		}
		s.Text = strings.Join(rows, "\n") + "\n" + s.Text
		if got := rec.Read(s); !got.Known || got.Working {
			t.Errorf("working row far above: %+v", got)
		}
		// The text in the composer rows is not a working row.
		box := tallBox(21, "› first", "  line")
		box[10] = "  esc to interrupt)"
		if got := rec.Read(codexTall(60, box, status)); got.Working {
			t.Errorf("composer text read as the working row: %+v", got)
		}
	})
	t.Run("the nearest prompt row is the composer", func(t *testing.T) {
		// The echoed prompt of an earlier turn sits above the transcript
		// of the answer; only the prompt row at the top of the walk counts.
		s := codexTall(36, []string{"› now", "  and more"}, status)
		r := strings.Split(s.Text, "\n")
		r[0] = "› an earlier prompt"
		s.Text = strings.Join(r, "\n")
		got := rec.Read(s)
		if !got.Known || got.ComposerText != "now\nand more" {
			t.Errorf("%+v", got)
		}
	})
	t.Run("dialog marks in the bottom rows win over a tall composer", func(t *testing.T) {
		box := tallBox(21, "› first", "  line")
		s := codexTall(36, box, status)
		r := strings.Split(s.Text, "\n")
		// A tall composer starts above the bottom rows; this row is in them.
		last := len(r) - 1
		r[last-3] = "  Press enter to confirm or esc to cancel"
		s.Text = strings.Join(r, "\n")
		if got := rec.Read(s); !got.Dialog || got.Composer || got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
		// The same text high up in the composer is not in the bottom rows.
		r[last-3] = "  line"
		r[last-21] = "  Press enter to confirm or esc to cancel"
		s.Text = strings.Join(r, "\n")
		if got := rec.Read(s); got.Dialog || !got.Composer {
			t.Errorf("%+v", got)
		}
	})

}

// TestDialogAfterPasteStaysDialog: the dumps taken after the fence pasted into
// an open dialog still read as a dialog, with no composer and nothing held.
func TestDialogAfterPasteStaysDialog(t *testing.T) {
	for _, c := range [][2]string{{"claude", "dialog-write-after-paste"}, {"codex", "dialog-command-after-paste"}} {
		l := fixtureNamed(t, c[0], c[1])
		got := mustFor(t, c[0]).Read(l.snap())
		if !got.Known || !got.Dialog || got.Composer || got.ComposerEmpty || got.CursorInComposer || got.Holds("ataniq") {
			t.Errorf("%s: %+v", c[0], got)
		}
	}
}

// TestKnownFalseKeepsOnlyNotes is checked over every unknown fixture.
func TestKnownFalseKeepsOnlyNotes(t *testing.T) {
	n := 0
	for _, l := range loadAll(t) {
		got := recogniserFor(t, l).Read(l.snap())
		if got.Known {
			continue
		}
		n++
		got.Notes = nil
		if !reflect.DeepEqual(got, Reading{}) {
			t.Errorf("%s/%s: %+v", l.runtime, l.name, got)
		}
	}
	if n == 0 {
		t.Fatal("no unknown fixture")
	}
}
