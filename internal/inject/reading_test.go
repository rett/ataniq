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
	// An unknown screen on the alternate screen (a later rule failed) reads
	// alt true, and a known screen off it reads alt false: the flag is the
	// snapshot's, not the recogniser's verdict.
	snap.AltScreen = true
	if got := newReading(stubView{}, snap, idle.Classification{State: idle.StateUnknown}, osc7501.Status{}, "").String(); !strings.Contains(got, ", composer not read, screen 101x24 alt true cursor 2,20 last-row 3") {
		t.Errorf("unknown screen on the alternate screen: %q", got)
	}
	snap.AltScreen = false
	if got := newReading(stubView{}, snap, known, osc7501.Status{}, "").String(); !strings.Contains(got, "marker false, screen 101x24 alt false cursor 2,20 last-row 3") {
		t.Errorf("known screen off the alternate screen: %q", got)
	}
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

// The shape follows the failed conditions, so the conditions that decided the
// result keep their place at the front of a line that the wire cuts.
func TestReadingStringPutsTheShapeAfterTheFailedList(t *testing.T) {
	snap := screen.Snapshot{Text: "x", Cols: 101, Rows: 24, CursorX: 2, CursorY: 20}
	r := newReading(stubView{}, snap, idle.Classification{State: idle.StateIdle}, osc7501.Status{}, "")
	r.fail("the owner typed too recently")
	line := r.String()
	failed, shape := strings.Index(line, ", failed: the owner typed too recently"), strings.Index(line, ", screen 101x24 ")
	if failed < 0 || shape < 0 || failed > shape {
		t.Errorf("failed at %d, shape at %d: %q", failed, shape, line)
	}
	if !strings.HasSuffix(line, ", screen 101x24 alt false cursor 2,20 last-row 0") {
		t.Errorf("the shape does not end the line: %q", line)
	}
}

// The line of an unknown screen on a live claude, the one the wire carries in
// its first 512 bytes (SanitizeDetail), fits with room to spare, also with
// three-digit sizes. The bound is the wire's, written as a literal: the test
// must not move with the constant that bounds the reading itself.
func TestUnknownScreenLineFitsTheWireCut(t *testing.T) {
	sig := []idle.Signal{
		{Source: idle.SourceReport, Says: "idle", Detail: "app claude-code"},
		{Source: idle.SourceTitle, Says: "no mark"},
		{Source: idle.SourceScreen, Says: "unknown", Detail: "no composer box: no rule line directly above the last row"},
		{Source: idle.SourceProcess, Says: "running"},
	}
	for name, snap := range map[string]screen.Snapshot{
		"live":        {Text: strings.Repeat("x\n", 22) + "x", Cols: 101, Rows: 24, CursorX: 2, CursorY: 20},
		"three digit": {Text: strings.Repeat("x\n", 100) + "x", Cols: 120, Rows: 120, CursorX: 100, CursorY: 100},
	} {
		r := newReading(stubView{}, snap, idle.Classification{State: idle.StateUnknown, Signals: sig}, osc7501.Status{Has: true}, "")
		for _, f := range []string{"state unknown", "composer not empty", "cursor outside the composer", "the screen changed too recently"} {
			r.fail(f)
		}
		res := Result{Outcome: OutcomeNotIdle, Written: WrittenNothing, Detail: "state unknown", Before: r}
		line := res.Readings()
		if len(line) > 512-10 {
			t.Errorf("%s: the line is %d bytes, over the wire cut of 512 less a margin of 10: %q", name, len(line), line)
		}
		if !strings.Contains(line, "last-row ") || !strings.HasSuffix(line, "; after: not read") {
			t.Errorf("%s: %q", name, line)
		}
	}
}
