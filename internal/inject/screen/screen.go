package screen

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/inject/versions"
)

// The caps of the holder's emulator. A snapshot larger than these is not a
// screen the holder could have produced, so it is read as unknown.
const (
	MaxRows = 200
	MaxCols = 500

	minRows = 5
	minCols = 20

	// bottomRows is how many rows, counted up from the last row that holds
	// anything, are examined for dialog marks and status lines.
	bottomRows = 20

	// maxFaintSpans bounds the style spans that are looked at.
	maxFaintSpans = 4096
)

// Span marks a run of faint (dim) characters on one row of a Snapshot. Row is
// the zero-based row of Text; From and To are zero-based character (rune)
// offsets in that row of Text, From included and To excluded. They are not
// cell columns: Snapshot.CursorX is a cell column, and the two differ after
// any character that is wider than one cell, so the holder converts the
// columns of the emulator's cells to offsets in the row's text.
type Span struct {
	Row, From, To int
}

// Snapshot is a text snapshot of one agent screen, as the holder's emulator
// shows it.
type Snapshot struct {
	// Text is the plain screen, rows joined by "\n". Trailing blanks of a
	// row may be trimmed or not, and one final "\n" is accepted.
	Text string
	// Cols and Rows are the size of the screen.
	Cols, Rows int
	// CursorX and CursorY are the zero-based cursor column and row.
	CursorX, CursorY int
	CursorHidden     bool
	AltScreen        bool
	Title            string

	// Faint lists the runs of faint (dim, SGR 2) characters. Both agents
	// draw their hint text in the empty composer ("Ask Codex to do
	// anything", "Try ...", and the ghost of the last prompt) in faint
	// style, while typed text is drawn plain; the characters are the same,
	// so plain text alone cannot tell a hint from owner text. Without
	// Faint (nil) a composer that shows any text is read as holding text,
	// never as empty. The holder fills it from the cell attributes of the
	// emulator. The holder must fill it from the cell attributes of its
	// emulator on every snapshot: when it leaves it nil, no composer that
	// shows a hint reads as empty (every recorded codex idle screen shows
	// one), so nothing would ever be injected. The offsets of the spans are
	// offsets in the row's text, not cell columns (see Span).
	Faint []Span
}

// Reading is what a recogniser sees on one screen. Known is false for any
// screen the recogniser cannot place; then every other field except Notes is
// its zero value.
type Reading struct {
	// Known is true when the screen fits a layout the recogniser was
	// written from: a composer, or an open dialog.
	Known bool
	// Composer is true when the composer is at its place on the screen.
	Composer bool
	// ComposerEmpty is true when the composer holds nothing: no characters
	// but blanks with the cursor visible at the first input column of the
	// prompt row, or only the runtime's faint hint text. Blanks with the
	// cursor anywhere else are text the owner typed. It implies Composer,
	// CursorInComposer and not Dialog. Known limit: blanks the owner typed,
	// with the cursor then moved back to the first input column (Home), read
	// as empty too, because the screen shows the same as an empty composer.
	ComposerEmpty bool
	// ComposerText is what the composer holds, rows joined by "\n", without
	// the prompt glyph and without a faint hint. Empty for an empty
	// composer. A consumer must test ComposerEmpty and never
	// ComposerText == "": the text is empty too when the composer holds only
	// blanks that the owner typed (or a box of blank rows), which is not an
	// empty composer.
	ComposerText string
	// CursorInComposer is true when the cursor is visible and on a row of
	// the composer.
	CursorInComposer bool
	// Dialog is true when the bottom rows carry a mark of an open dialog;
	// DialogMarks names the marks found. An open dialog replaces the
	// composer, so Composer is false then.
	Dialog      bool
	DialogMarks []string
	// BackgroundTask is true when the screen shows a running background
	// task (claude only). It is not observed when StateHidden is true: the
	// shell count of the footer is not shown then, so false does not mean
	// that no shell runs.
	BackgroundTask bool
	// Mode is the permission mode the footer shows, as a short name (claude
	// only; empty for codex).
	Mode string
	// PastedMarker is true when the composer holds the runtime's marker
	// for pasted text.
	PastedMarker bool
	// Notes says why a screen is unknown, or what was noticed.
	Notes []string
	// StateHidden is true exactly when the screen hides the working hint
	// and the footer's shell count: claude's footer "paste again to
	// expand" (a collapsed paste is in the composer) replaces the whole
	// footer line, so neither can appear. Then Working and the footer part
	// of BackgroundTask are not observed, and their false values are not
	// observations. A consumer must take the working state from the
	// runtime's own report (OSC 7501) and not from Working then. A Note
	// says so as well.
	StateHidden bool

	// Working is true when the screen shows the runtime's working line:
	// claude's footer "esc to interrupt", codex's "Working (... esc to
	// interrupt)" line. It is a mark, not a verdict on idleness. It is not
	// observed when StateHidden is true: false then does not mean that the
	// agent is idle, and the consumer must take the working state from the
	// OSC 7501 report.
	Working bool
	// Placeholder is true when the composer shows only the runtime's faint
	// hint text, which ComposerEmpty counts as empty.
	Placeholder bool
}

// Recogniser reads the screen of one runtime at one version.
type Recogniser interface {
	// Read says what the snapshot shows. It never panics and keeps no state.
	Read(Snapshot) Reading
}

// For returns the recogniser for a runtime and version. Only the tested pairs
// of the table in package versions exist (claude 2.1.325, and codex 0.169,
// whose recorded build 0.169.0 the table lists as an alias). Any other pair,
// any empty value and any runtime without a recogniser here is not found.
func For(runtime, version string) (Recogniser, bool) {
	e, ok := versions.Lookup(runtime, version)
	if !ok {
		return nil, false
	}
	switch e.Runtime {
	case "claude":
		return claude{}, true
	case "codex":
		return codex{}, true
	}
	return nil, false
}

// Holds reports whether the composer text contains header, with blanks and
// line breaks removed from both (the composer wraps long lines). An empty
// header, or a header of only blanks, is never held.
func (r Reading) Holds(header string) bool {
	want := squash(header)
	if want == "" || !r.Composer {
		return false
	}
	return strings.Contains(squash(r.ComposerText), want)
}

// squash removes every blank and line break.
func squash(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsSpace(c) {
			return -1
		}
		return c
	}, s)
}

// unknown is the reading of a screen that cannot be placed.
func unknown(note string) Reading {
	return Reading{Notes: []string{note}}
}

// grid is a snapshot split into rows of characters.
type grid struct {
	rows [][]rune
	last int // index of the last row that holds a non-blank, -1 if none
	cols int
}

// parse validates a snapshot and splits it into rows. It returns a note and a
// nil grid when the snapshot is not a screen the holder could have produced.
func parse(s Snapshot) (*grid, string) {
	if s.Cols < minCols || s.Cols > MaxCols || s.Rows < minRows || s.Rows > MaxRows {
		return nil, "screen size outside 20x5 to 500x200"
	}
	if len(s.Text) > MaxRows*(4*MaxCols+1) {
		return nil, "text larger than the caps"
	}
	if !utf8.ValidString(s.Text) {
		return nil, "text is not valid UTF-8"
	}
	for _, c := range s.Text {
		if c != '\n' && (unicode.IsControl(c) || c == utf8.RuneError) {
			return nil, "text holds a control character"
		}
	}
	lines := strings.Split(s.Text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > s.Rows {
		return nil, "more rows of text than the screen has"
	}
	g := &grid{rows: make([][]rune, len(lines)), last: -1, cols: s.Cols}
	for i, l := range lines {
		rs := []rune(l)
		if len(rs) > s.Cols {
			return nil, "a row is wider than the screen"
		}
		g.rows[i] = rs
		if !blank(rs) {
			g.last = i
		}
	}
	if g.last < 0 {
		return nil, "screen is empty"
	}
	return g, ""
}

func blank(rs []rune) bool {
	for _, c := range rs {
		if !unicode.IsSpace(c) {
			return false
		}
	}
	return true
}

// trimRight drops trailing blanks (the no-break space included).
func trimRight(rs []rune) []rune {
	n := len(rs)
	for n > 0 && unicode.IsSpace(rs[n-1]) {
		n--
	}
	return rs[:n]
}

// leadingSpaces counts the plain spaces at the start of a row.
func leadingSpaces(rs []rune) int {
	n := 0
	for n < len(rs) && rs[n] == ' ' {
		n++
	}
	return n
}

// windowStart is the first row of the bottom rows.
func (g *grid) windowStart() int {
	return max(0, g.last-bottomRows+1)
}

// isRule reports whether a row is one full-width rule line of '─'.
func (g *grid) isRule(i int) bool {
	rs := trimRight(g.rows[i])
	if len(rs) != g.cols {
		return false
	}
	for _, c := range rs {
		if c != '─' {
			return false
		}
	}
	return true
}

// hasPrefix reports whether the row starts with the given text.
func hasPrefix(rs []rune, p string) bool {
	return strings.HasPrefix(string(rs), p)
}

// choiceLine reports whether a row, with its leading blanks removed, is an
// option of a numbered list that starts with glyph: "<glyph> <digits>. ".
func choiceLine(rs []rune, glyph rune) bool {
	if len(rs) < 5 || rs[0] != glyph || rs[1] != ' ' {
		return false
	}
	i := 2
	for i < len(rs) && rs[i] >= '0' && rs[i] <= '9' {
		i++
	}
	return i > 2 && i+1 < len(rs) && rs[i] == '.' && rs[i+1] == ' '
}

// cursorIn reports whether the cursor is visible and on rows top to bottom.
func cursorIn(s Snapshot, top, bottom int) bool {
	return !s.CursorHidden && s.CursorY >= top && s.CursorY <= bottom &&
		s.CursorX >= 0 && s.CursorX < s.Cols
}

// emptyCursor reports whether the cursor is where it sits in an empty
// composer: visible, on the prompt row, at the first input column (after the
// prompt glyph and its blank). A composer that shows nothing but blanks is
// empty only then; with the cursor anywhere else the owner typed the blanks,
// and they are text. Every recorded empty composer has the cursor at column 2.
func emptyCursor(s Snapshot, row int) bool {
	return !s.CursorHidden && s.CursorY == row && s.CursorX == 2
}

// hintOnly reports whether the characters of row, from the offset textFrom,
// are all faint, there is at least one, and the cursor sits at the first of
// them. That is how both agents draw the hint in an empty composer.
func hintOnly(s Snapshot, row int, rs []rune, textFrom int) bool {
	if s.CursorHidden || s.CursorY != row || s.CursorX != textFrom {
		return false
	}
	spans := s.Faint
	if len(spans) > maxFaintSpans {
		spans = spans[:maxFaintSpans]
	}
	seen := false
	for i := textFrom; i < len(rs); i++ {
		if unicode.IsSpace(rs[i]) {
			continue
		}
		seen = true
		covered := false
		for _, sp := range spans {
			if sp.Row == row && sp.From <= i && i < sp.To {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return seen
}

// addMark appends a mark once.
func addMark(marks []string, m string) []string {
	for _, x := range marks {
		if x == m {
			return marks
		}
	}
	return append(marks, m)
}

// composerText joins the rows of a composer: the first row without its prompt,
// the others without their two blanks of indent, each without trailing blanks.
func composerText(rows [][]rune, promptLen int) string {
	var parts []string
	for i, rs := range rows {
		drop := 0
		if i == 0 {
			drop = min(promptLen, len(rs))
		} else {
			drop = min(leadingSpaces(rs), 2)
		}
		parts = append(parts, string(trimRight(rs[drop:])))
	}
	return strings.TrimRight(strings.Join(parts, "\n"), "\n")
}
