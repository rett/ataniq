package inject

import (
	"strings"
	"testing"
	"time"

	"ataniq/internal/inject/idle"
	"ataniq/internal/inject/osc7501"
	"ataniq/internal/inject/screen"
)

// stubView answers newReading's three questions of a view.
type stubView struct{ View }

func (stubView) Term() TermState            { return TermState{ParserGround: true} }
func (stubView) OwnerQuiet() time.Duration  { return 3 * time.Second }
func (stubView) ScreenQuiet() time.Duration { return 4 * time.Second }

func TestLastRow(t *testing.T) {
	for name, c := range map[string]struct {
		text string
		want int
	}{
		"empty":                 {"", -1},
		"blanks only":           {"   \n\t \n", -1},
		"one row":               {"a", 0},
		"last row holds text":   {"a\n\nb", 2},
		"trailing blank rows":   {"a\n \n\n", 0},
		"text then blank lines": {"\n\nx   \n   \n", 2},
	} {
		if got := lastRow(c.text); got != c.want {
			t.Errorf("%s: lastRow = %d, want %d", name, got, c.want)
		}
	}
}

// The reading carries the shape of the screen as numbers and flags, and an
// unknown screen says the composer was not read instead of five false values.
func TestReadingStringCarriesTheScreenShape(t *testing.T) {
	snap := screen.Snapshot{Text: "SECRET-ROW\n\n   \nlast", Cols: 101, Rows: 24, CursorX: 2, CursorY: 20, AltScreen: false}

	unknown := newReading(stubView{}, snap, idle.Classification{State: idle.StateUnknown}, osc7501.Status{}, "").String()
	for _, want := range []string{", composer not read, screen 101x24 alt false cursor 2,20 last-row 3"} {
		if !strings.Contains(unknown, want) {
			t.Errorf("unknown screen: %q does not contain %q", unknown, want)
		}
	}
	if strings.Contains(unknown, "empty false") || strings.Contains(unknown, "cursor-in") || strings.Contains(unknown, "SECRET") {
		t.Errorf("unknown screen: %q", unknown)
	}

	var known idle.Classification
	known.State = idle.StateIdle
	known.Screen.Known, known.Screen.Composer, known.Screen.ComposerEmpty, known.Screen.CursorInComposer = true, true, true, true
	snap.AltScreen = true
	line := newReading(stubView{}, snap, known, osc7501.Status{}, "").String()
	want := ", composer true empty true cursor-in true header false marker false, screen 101x24 alt true cursor 2,20 last-row 3"
	if !strings.Contains(line, want) {
		t.Errorf("known screen: %q does not contain %q", line, want)
	}
	if strings.Contains(line, "composer not read") || strings.Contains(line, "SECRET") {
		t.Errorf("known screen: %q", line)
	}
}
