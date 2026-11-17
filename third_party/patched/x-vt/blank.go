package vt

import (
	"reflect"
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
)

// Lines that are one cell wide of the same cell.
//
// A cell of the buffer is 112 bytes, so a line of 500 cells is 56 KB and a
// screen of 500 by 200 cells 11 MB, and writing it takes about a millisecond
// whatever the code. A sequence that blanks the whole screen (erase display,
// the full reset, the switch of the alternate screen, a scroll by the whole
// region) used to write all of it, cell by cell, and 64 KiB of them kept the
// emulator, and the lock of whoever called it, for 10 to 70 seconds.
//
// A line now carries a mark, the cell that every one of its cells holds (a
// blank cell with the pen's background, for these sequences). A mark is
// either written (the cells of the line are that cell, which is all the mark
// says) or stale (the cells are not written yet and the line is that cell
// all the same). An erase of a line sets the mark and writes nothing, and an
// erase of a line whose mark is already that cell does nothing at all, so a
// loop that clears the screen costs the lines that changed since the last
// clear. A stale line is written when something reads or changes it (flush),
// and by the end of every Write (Emulator.flushScreens), so that nothing that
// reads the emulator between two writes, which may be several readers under
// SafeEmulator's read lock, ever finds a stale line, and no read has to
// change anything.
//
// The invariants, which the comparison tests of this package check against
// the cell-by-cell code:
//
//   - A mark with a cell says: once the line is flushed, every cell of it is
//     *cell (with part set, every cell from from up to to, written). A line
//     with no mark (cell nil) says nothing.
//   - A stale line has a mark, and its cells are not to be read.
//   - Whatever changes cells of a line writes it first (flush) and drops the
//     mark (forget), unless it sets the whole line, which sets the mark
//     (setLines) or drops it (overwritten).
//   - The marks move with their lines (moveLines).
//   - What reads the cells of a line from the buffer first writes it
//     (CellAt, the state, the text and the recount of the links, which skips
//     a marked line with no link, as none of its cells has one).
//
// A cell that is read through the pointer CellAt returns must not be written
// through it: the marks would not know. Nothing in the emulator does, and
// SetCell is the way to change a cell.

// lineMark is the mark of one line. With part set it says less: only the
// cells from from up to to are the cell, and they are written (a part is never
// stale), which is what an erase of a part of a line leaves behind (the
// erase of a scroll region narrower than the screen), so that the same erase
// of the same cells again does nothing.
type lineMark struct {
	cell     *uv.Cell
	stale    bool
	part     bool
	from, to int
}

// blankFill is the cell of an erase with no background, the one the buffer's
// functions use for a nil cell. It is never written to.
var blankFill = uv.EmptyCell

// marksTable returns the marks of the lines of the screen, a table of one
// mark per line made when the screen has none for its height.
func (s *Screen) marksTable() []lineMark {
	if len(s.marks) != len(s.buf.Lines) {
		// A screen is resized and restored only after a flush, so no line is
		// stale here.
		s.marks = make([]lineMark, len(s.buf.Lines))
		s.stale = 0
	}
	return s.marks
}

// fillCell returns the cell that marks the lines an erase or a fill with c
// sets, c being nil for a blank cell that has no background. Equal cells give
// the same pointer when they follow one another, which is what lets a line
// that already holds the cell be recognised by comparing pointers.
func (s *Screen) fillCell(c *uv.Cell) *uv.Cell {
	if c == nil {
		return &blankFill
	}
	if l := s.lastFill; l != nil && reflect.DeepEqual(l, c) {
		return l
	}
	n := *c
	s.lastFill = &n
	return &n
}

// flush writes line y if it is stale.
func (s *Screen) flush(y int) {
	if s.stale == 0 || y < 0 || y >= len(s.marks) {
		return
	}
	m := &s.marks[y]
	if !m.stale {
		return
	}
	line := s.buf.Lines[y]
	c := *m.cell
	for i := range line {
		line[i] = c
	}
	m.stale = false
	s.stale--
}

// flushRows writes the stale lines from y0 up to y1.
func (s *Screen) flushRows(y0, y1 int) {
	if s.stale == 0 {
		return
	}
	for y := max(y0, 0); y < min(y1, len(s.marks)); y++ {
		s.flush(y)
	}
}

// flushAll writes every stale line.
func (s *Screen) flushAll() {
	if s.stale == 0 {
		return
	}
	for y := range s.marks {
		s.flush(y)
		if s.stale == 0 {
			return
		}
	}
}

// forget is what a change of some cells of line y does first: the line is
// written, and the mark, which the change would make wrong, is dropped.
func (s *Screen) forget(y int) {
	if y < 0 || y >= len(s.marks) {
		return
	}
	s.flush(y)
	s.marks[y].cell = nil
}

// forgetRows is forget for the lines from y0 up to y1.
func (s *Screen) forgetRows(y0, y1 int) {
	for y := max(y0, 0); y < min(y1, len(s.marks)); y++ {
		s.forget(y)
	}
}

// overwritten is what a change of every cell of line y does: whatever the
// line held is gone, so it needs no write and no mark.
func (s *Screen) overwritten(y int) {
	if y < 0 || y >= len(s.marks) {
		return
	}
	m := &s.marks[y]
	if m.stale {
		m.stale = false
		s.stale--
	}
	m.cell = nil
}

// setLines makes the lines from y0 up to y1 a line of the cell c, which
// fillCell returned, without writing them. A line that already holds it is
// left as it is.
func (s *Screen) setLines(y0, y1 int, c *uv.Cell) {
	marks := s.marksTable()
	for y := max(y0, 0); y < min(y1, len(marks)); y++ {
		m := &marks[y]
		if m.cell == c && !m.part {
			continue
		}
		m.cell, m.part = c, false
		if !m.stale {
			m.stale = true
			s.stale++
		}
	}
}

// markedBlank reports whether line y is marked as a line of blank cells,
// which are cells of one space or none, so that its text is empty.
func (s *Screen) markedBlank(y int) bool {
	if y < 0 || y >= len(s.marks) {
		return false
	}
	m := &s.marks[y]
	return m.cell != nil && !m.part && (m.cell.Content == "" || m.cell.Content == " ")
}

// fillBlank sets the cells of area to the cell c, which is a blank cell of
// one column or nil, as the buffer's FillArea does, one cell at a time with
// the repair of the wide cells the area cuts. A line that the area covers
// from edge to edge is set whole, as every cell of it is c whatever it held,
// and a line that already holds c is left as it is. The cells of a part of a
// line are written as they were, after the line is written.
func (s *Screen) fillBlank(c *uv.Cell, area uv.Rectangle) {
	w, h := s.buf.Width(), s.buf.Height()
	y0, y1 := max(area.Min.Y, 0), min(area.Max.Y, h)
	if y0 >= y1 {
		return
	}
	fill := s.fillCell(c)
	if area.Min.X <= 0 && area.Max.X >= w {
		s.setLines(y0, y1, fill)
		return
	}
	marks := s.marksTable()
	from, to := max(area.Min.X, 0), min(area.Max.X, w)
	for y := y0; y < y1; y++ {
		m := &marks[y]
		if m.cell == fill && (!m.part || (m.from <= from && to <= m.to)) {
			// Every cell of the part is c already, whole cells of one column
			// that the fill would leave as they are.
			continue
		}
		s.forget(y)
		s.buf.FillArea(c, uv.Rect(area.Min.X, y, area.Dx(), 1))
		if from < to {
			*m = lineMark{cell: fill, part: true, from: from, to: to}
		}
	}
}

// flushScreens writes the lines of both screens that are stale. Write calls
// it on the way out.
func (e *Emulator) flushScreens() {
	e.scrs[0].flushAll()
	e.scrs[1].flushAll()
}

// rotateLeft moves the elements of s k places to the left, the first k going
// to the end.
func rotateLeft[T any](s []T, k int) {
	slices.Reverse(s[:k])
	slices.Reverse(s[k:])
	slices.Reverse(s)
}
