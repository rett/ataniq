//go:build linux && ataniq_canary

package standin

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/sessionpane"
)

// A row is checked in characters, so a row of wide characters can hold more
// cells than the screen has columns. It is clipped at the edge, and the rows
// that follow are where the file puts them.
func TestDrawClipsARowOfWideCharactersInsteadOfWrappingIt(t *testing.T) {
	s := &snapshot{Cols: 20, Rows: 6, CursorX: 0, CursorY: 3, Title: "wide"}
	// Fifteen wide characters are 30 cells on a screen of 20 columns.
	wide := strings.Repeat("漢", 15)
	if err := s.check(wide + "\nsecond\nthird\n"); err != nil {
		t.Fatal(err)
	}
	pane := sessionpane.New(s.Cols, s.Rows, sessionpane.Options{})
	defer pane.Close()
	pane.SetGraphemeWidths(true)
	pane.Write(s.draw(false))
	buf := uv.NewBuffer(s.Cols, s.Rows)
	pane.Draw(buf, buf.Bounds())
	row := func(y int) string {
		var b strings.Builder
		for x := 0; x < s.Cols; x++ {
			if c := buf.CellAt(x, y); c.Width > 0 {
				b.WriteString(c.Content)
			}
		}
		return strings.TrimRight(b.String(), " ")
	}
	if got := row(0); !strings.HasPrefix(got, "漢漢") || strings.ContainsAny(got, "sct") {
		t.Errorf("row 0 %q", got)
	}
	if got := row(1); got != "second" {
		t.Errorf("row 1 %q, want the second row of the file", got)
	}
	if got := row(2); got != "third" {
		t.Errorf("row 2 %q, want the third row of the file", got)
	}
	if got := row(3); got != "" {
		t.Errorf("row 3 %q, want it empty", got)
	}
}

// Autowrap is off while the rows are drawn and on after them, so a terminal
// that is drawn on keeps the mode it had.
func TestDrawTurnsAutowrapOffForTheRowsAndBackOnAfter(t *testing.T) {
	s := &snapshot{Cols: 20, Rows: 6, CursorX: 1, CursorY: 2, Title: "t"}
	if err := s.check("ab\ncd\n"); err != nil {
		t.Fatal(err)
	}
	out := string(s.draw(false))
	off, on := strings.Index(out, "\x1b[?7l"), strings.LastIndex(out, "\x1b[?7h")
	first, last := strings.Index(out, "ab"), strings.Index(out, "cd")
	if off < 0 || off > first {
		t.Fatalf("autowrap is not turned off before the first row: %q", out)
	}
	if on < last || strings.Count(out, "\x1b[?7h") != 1 || strings.Count(out, "\x1b[?7l") != 1 {
		t.Fatalf("autowrap is not turned on once, after the last row: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b[3;2H\x1b[?25h") {
		t.Fatalf("the draw does not end with the cursor: %q", out)
	}

	// On a terminal, text that runs past the last column wraps again.
	pane := sessionpane.New(s.Cols, s.Rows, sessionpane.Options{})
	defer pane.Close()
	pane.Write([]byte(out))
	pane.Write([]byte("\x1b[5;1H" + strings.Repeat("x", s.Cols) + "yz"))
	buf := uv.NewBuffer(s.Cols, s.Rows)
	pane.Draw(buf, buf.Bounds())
	if c := buf.CellAt(0, 5); c.Content != "y" {
		t.Fatalf("cell 0,5 is %q: the text did not wrap to the next row", c.Content)
	}
}
