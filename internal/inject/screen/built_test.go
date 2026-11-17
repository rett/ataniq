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
	t.Run("a box without a prompt is unknown", func(t *testing.T) {
		for _, row := range []string{"hello", "> hello", "❯hello", "  ❯ hello", ""} {
			if got := rec.Read(claudeScreen([]string{row}, foot)); got.Known {
				t.Errorf("%q: %+v", row, got)
			}
		}
	})
	t.Run("cursor outside the box", func(t *testing.T) {
		s := claudeScreen([]string{"❯ "}, foot)
		s.CursorY = 5
		if got := rec.Read(s); got.CursorInComposer || !got.ComposerEmpty {
			t.Errorf("%+v", got)
		}
		s.CursorY, s.CursorHidden = 32, true
		if got := rec.Read(s); got.CursorInComposer {
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
