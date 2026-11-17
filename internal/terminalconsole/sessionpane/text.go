package sessionpane

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
)

// Text returns the text of the pane from one cell to another, both included,
// read as a terminal reads a stream selection: from `from` to the end of its
// row, every row in between whole, and `to`'s row up to `to`. The cells may be
// given in either order and are clamped to the pane.
//
// Each character is copied once, however many columns it covers: a wide
// character or a grapheme cluster is taken from its first column, and the
// columns it covers after that add nothing. A cell with nothing in it counts
// as a space. Blanks at the end of each row are dropped, and rows are joined
// with "\n". That includes a row the session's text wrapped onto by itself,
// because the emulator keeps no record of where a line wrapped. A cell whose
// content holds a control character is copied as a placeholder of its width.
//
// Text reads what Draw shows: the frame held for synchronized output while it
// is shown, else the screen. It stops and reports false as soon as the text
// would be longer than limit bytes.
func (p *Pane) Text(from, to uv.Position, limit int) (string, bool) {
	if to.Y < from.Y || (to.Y == from.Y && to.X < from.X) {
		from, to = to, from
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	src, cols, rows := p.source()
	from, to = clampPos(from, cols, rows), clampPos(to, cols, rows)
	var b strings.Builder
	for y := from.Y; y <= to.Y; y++ {
		if y > from.Y {
			if b.Len()+1 > limit {
				return "", false
			}
			b.WriteByte('\n')
		}
		x0, x1 := 0, cols-1
		if y == from.Y {
			x0 = from.X
		}
		if y == to.Y {
			x1 = to.X
		}
		// blanks counts the blank cells not yet written: they are written
		// only when something follows them on the row.
		blanks := 0
		for x := x0; x <= x1; x++ {
			c := src.CellAt(x, y)
			if c == nil || c.Width <= 0 {
				// A column a wide character covers after its first.
				continue
			}
			s := c.Content
			if s == "" || s == " " {
				blanks++
				continue
			}
			if hasControl(s) {
				s = placeholder(c.Width)
			}
			if b.Len()+blanks+len(s) > limit {
				return "", false
			}
			b.WriteString(strings.Repeat(" ", blanks))
			b.WriteString(s)
			blanks = 0
		}
	}
	return b.String(), true
}

// clampPos clamps pos to a grid of cols by rows.
func clampPos(pos uv.Position, cols, rows int) uv.Position {
	return uv.Pos(min(max(pos.X, 0), cols-1), min(max(pos.Y, 0), rows-1))
}

// hasControl reports whether s holds a C0 or C1 control character.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}
