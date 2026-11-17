package vt

import (
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/exp/ordered"
)

// Screen represents a virtual terminal screen.
type Screen struct {
	// cb is the callbacks struct to use.
	cb *Callbacks
	// The buffer of the screen.
	buf *uv.RenderBuffer
	// The cur of the screen.
	cur, saved Cursor
	// scroll is the scroll region.
	scroll uv.Rectangle
	// scrollback is the scrollback buffer for lines scrolled off the top.
	scrollback *Scrollback
	// links bounds the distinct hyperlinks of the cells of the screen.
	links linkTable
	// marks has the mark of each line, which can say that the line is one
	// cell wide of the same cell, written or not (see blank.go); stale counts
	// the lines that are not written, and lastFill is the cell that the last
	// erase or fill marked its lines with.
	marks    []lineMark
	stale    int
	lastFill *uv.Cell
}

// NewScreen creates a new screen.
func NewScreen(w, h int) *Screen {
	s := Screen{
		buf:        uv.NewRenderBuffer(w, h),
		scrollback: NewScrollback(DefaultScrollbackSize),
	}
	s.scroll = s.buf.Bounds()
	// Every cell of a new screen is blank.
	s.marks = make([]lineMark, h)
	for y := range s.marks {
		s.marks[y].cell = &blankFill
	}
	return &s
}

// Reset resets the screen.
// It clears the screen, sets the cursor to the top left corner, reset the
// cursor styles, and resets the scroll region.
func (s *Screen) Reset() {
	s.fillBlank(nil, s.buf.Bounds())
	s.cur = Cursor{}
	s.saved = Cursor{}
	s.scroll = s.buf.Bounds()
	s.buf.Touched = nil
	// No cell references a link now.
	clear(s.links.set)
	s.links.wait = 0
}

// Bounds returns the bounds of the screen.
func (s *Screen) Bounds() uv.Rectangle {
	return s.buf.Bounds()
}

// Touched returns touched lines in the screen buffer.
func (s *Screen) Touched() []*uv.LineData {
	return s.buf.Touched
}

// ClearTouched clears the touched state.
func (s *Screen) ClearTouched() {
	s.buf.Touched = nil
}

// CellAt returns the cell at the given x, y position.
//
// The cell is not to be written through the pointer (see blank.go): SetCell
// changes a cell.
func (s *Screen) CellAt(x int, y int) *uv.Cell {
	s.flush(y)
	return s.buf.CellAt(x, y)
}

// SetCell sets the cell at the given x, y position.
func (s *Screen) SetCell(x, y int, c *uv.Cell) {
	s.forget(y)
	s.buf.SetCell(x, y, c)
}

// Height returns the height of the screen.
func (s *Screen) Height() int {
	return s.buf.Height()
}

// Resize resizes the screen.
func (s *Screen) Resize(width int, height int) {
	s.flushAll()
	s.marks, s.stale = nil, 0
	if s.buf == nil {
		s.buf = uv.NewRenderBuffer(width, height)
	} else {
		s.buf.Resize(width, height)
		s.buf.Touched = nil
	}
	s.scroll = s.buf.Bounds()
}

// Width returns the width of the screen.
func (s *Screen) Width() int {
	return s.buf.Width()
}

// Clear clears the screen with blank cells.
func (s *Screen) Clear() {
	s.ClearArea(s.Bounds())
}

// ClearWithScrollback saves all non-empty lines to scrollback before clearing.
// This is used for operations like ED 2 (erase screen) where content should
// be preserved in history.
func (s *Screen) ClearWithScrollback() {
	// A scrollback that keeps nothing is not given the lines, and a line
	// marked blank has no text: neither is read.
	if s.scrollback != nil && s.scrollback.maxLines > 0 {
		// Save all lines that have content before clearing
		for y := 0; y < s.buf.Height(); y++ {
			if s.markedBlank(y) {
				continue
			}
			s.flush(y)
			line := s.buf.Line(y)
			if line != nil && !s.isLineEmpty(line) {
				s.scrollback.Push(line)
			}
		}
	}
	s.Clear()
}

// isLineEmpty returns true if the line contains only empty/space cells.
func (s *Screen) isLineEmpty(line uv.Line) bool {
	for _, cell := range line {
		if cell.Content != "" && cell.Content != " " {
			return false
		}
	}
	return true
}

// ClearArea clears the given area.
func (s *Screen) ClearArea(area uv.Rectangle) {
	s.fillBlank(nil, area)
	if area.Min.X <= 0 && area.Min.Y <= 0 && area.Max.X >= s.Width() && area.Max.Y >= s.Height() {
		// Nothing of the screen references a link now.
		clear(s.links.set)
		s.links.wait = 0
	}
	s.touchArea(area)
}

// Fill fills the screen or part of it.
func (s *Screen) Fill(c *uv.Cell) {
	s.FillArea(c, s.Bounds())
}

// FillArea fills the given area with the given cell.
func (s *Screen) FillArea(c *uv.Cell, area uv.Rectangle) {
	s.forgetRows(area.Min.Y, area.Max.Y)
	s.buf.FillArea(c, area)
	s.touchArea(area)
}

// eraseArea fills the area with the cursor's blank cell, which is what the
// erase sequences do (erase in display and in line, erase characters).
func (s *Screen) eraseArea(area uv.Rectangle) {
	s.fillBlank(s.blankCell(), area)
	s.touchArea(area)
}

// setHorizontalMargins sets the horizontal margins.
func (s *Screen) setHorizontalMargins(left, right int) {
	s.scroll.Min.X = left
	s.scroll.Max.X = right
}

// setVerticalMargins sets the vertical margins.
func (s *Screen) setVerticalMargins(top, bottom int) {
	s.scroll.Min.Y = top
	s.scroll.Max.Y = bottom
}

// setCursorX sets the cursor X position. If margins is true, the cursor is
// only set if it is within the scroll margins.
func (s *Screen) setCursorX(x int, margins bool) {
	s.setCursor(x, s.cur.Y, margins)
}

// setCursor sets the cursor position. If margins is true, the cursor is only
// set if it is within the scroll margins. This follows how [ansi.CUP] works.
func (s *Screen) setCursor(x, y int, margins bool) {
	old := s.cur.Position
	if !margins {
		y = ordered.Clamp(y, 0, s.buf.Height()-1)
		x = ordered.Clamp(x, 0, s.buf.Width()-1)
	} else {
		y = ordered.Clamp(s.scroll.Min.Y+y, s.scroll.Min.Y, s.scroll.Max.Y-1)
		x = ordered.Clamp(s.scroll.Min.X+x, s.scroll.Min.X, s.scroll.Max.X-1)
	}
	s.cur.X, s.cur.Y = x, y

	if s.cb.CursorPosition != nil && (old.X != x || old.Y != y) {
		s.cb.CursorPosition(old, uv.Pos(x, y))
	}
}

// moveCursor moves the cursor by the given x and y deltas. If the cursor
// position is inside the scroll region, it is bounded by the scroll region.
// Otherwise, it is bounded by the screen bounds.
// This follows how [ansi.CUU], [ansi.CUD], [ansi.CUF], [ansi.CUB], [ansi.CNL],
// [ansi.CPL].
func (s *Screen) moveCursor(dx, dy int) {
	scroll := s.scroll
	old := s.cur.Position
	if old.X < scroll.Min.X {
		scroll.Min.X = 0
	}
	if old.X >= scroll.Max.X {
		scroll.Max.X = s.buf.Width()
	}

	pt := uv.Pos(s.cur.X+dx, s.cur.Y+dy)

	var x, y int
	if old.In(scroll) {
		y = ordered.Clamp(pt.Y, scroll.Min.Y, scroll.Max.Y-1)
		x = ordered.Clamp(pt.X, scroll.Min.X, scroll.Max.X-1)
	} else {
		y = ordered.Clamp(pt.Y, 0, s.buf.Height()-1)
		x = ordered.Clamp(pt.X, 0, s.buf.Width()-1)
	}

	s.cur.X, s.cur.Y = x, y

	if s.cb.CursorPosition != nil && (old.X != x || old.Y != y) {
		s.cb.CursorPosition(old, uv.Pos(x, y))
	}
}

// Cursor returns the cursor.
func (s *Screen) Cursor() Cursor {
	return s.cur
}

// CursorPosition returns the cursor position.
func (s *Screen) CursorPosition() (x, y int) {
	return s.cur.X, s.cur.Y
}

// ScrollRegion returns the scroll region.
func (s *Screen) ScrollRegion() uv.Rectangle {
	return s.scroll
}

// SaveCursor saves the cursor.
func (s *Screen) SaveCursor() {
	s.saved = s.cur
}

// RestoreCursor restores the cursor.
func (s *Screen) RestoreCursor() {
	old := s.cur.Position
	s.cur = s.saved

	if s.cb.CursorPosition != nil && (old.X != s.cur.X || old.Y != s.cur.Y) {
		s.cb.CursorPosition(old, s.cur.Position)
	}
}

// setCursorHidden sets the cursor hidden.
func (s *Screen) setCursorHidden(hidden bool) {
	changed := s.cur.Hidden != hidden
	s.cur.Hidden = hidden
	if changed && s.cb.CursorVisibility != nil {
		s.cb.CursorVisibility(!hidden)
	}
}

// setCursorStyle sets the cursor style.
func (s *Screen) setCursorStyle(style CursorStyle, blink bool) {
	changed := s.cur.Style != style || s.cur.Steady != !blink
	s.cur.Style = style
	s.cur.Steady = !blink
	if changed && s.cb.CursorStyle != nil {
		s.cb.CursorStyle(style, !blink)
	}
}

// cursorPen returns the cursor pen.
func (s *Screen) cursorPen() uv.Style {
	return s.cur.Pen
}

// cursorLink returns the cursor link.
func (s *Screen) cursorLink() uv.Link {
	return s.cur.Link
}

// ShowCursor shows the cursor.
func (s *Screen) ShowCursor() {
	s.setCursorHidden(false)
}

// HideCursor hides the cursor.
func (s *Screen) HideCursor() {
	s.setCursorHidden(true)
}

// InsertCell inserts n blank characters at the cursor position pushing out
// cells to the right and out of the screen.
func (s *Screen) InsertCell(n int) {
	if n <= 0 {
		return
	}

	x, y := s.cur.X, s.cur.Y
	s.forget(y)
	s.buf.InsertCellArea(x, y, n, s.blankCell(), s.scroll)
}

// DeleteCell deletes n cells at the cursor position moving cells to the left.
// This has no effect if the cursor is outside the scroll region.
func (s *Screen) DeleteCell(n int) {
	if n <= 0 {
		return
	}

	x, y := s.cur.X, s.cur.Y
	s.forget(y)
	s.buf.DeleteCellArea(x, y, n, s.blankCell(), s.scroll)
}

// ScrollUp scrolls the content up n lines within the given region. Lines
// scrolled past the top margin are lost. This is equivalent to [ansi.SU] which
// moves the cursor to the top margin and performs a [ansi.DL] operation.
func (s *Screen) ScrollUp(n int) {
	x, y := s.CursorPosition()
	s.setCursor(s.cur.X, 0, true)
	s.DeleteLine(n)
	s.setCursor(x, y, false)
}

// ScrollDown scrolls the content down n lines within the given region. Lines
// scrolled past the bottom margin are lost. This is equivalent to [ansi.SD]
// which moves the cursor to top margin and performs a [ansi.IL] operation.
func (s *Screen) ScrollDown(n int) {
	x, y := s.CursorPosition()
	s.setCursor(s.cur.X, 0, true)
	s.InsertLine(n)
	s.setCursor(x, y, false)
}

// InsertLine inserts n blank lines at the cursor position Y coordinate.
// Only operates if cursor is within scroll region. Lines below cursor Y
// are moved down, with those past bottom margin being discarded.
// It returns true if the operation was successful.
func (s *Screen) InsertLine(n int) bool {
	if n <= 0 {
		return false
	}

	x, y := s.cur.X, s.cur.Y

	// Only operate if cursor Y is within scroll region
	if y < s.scroll.Min.Y || y >= s.scroll.Max.Y ||
		x < s.scroll.Min.X || x >= s.scroll.Max.X {
		return false
	}

	if s.scroll.Min.X == 0 && s.scroll.Max.X == s.buf.Width() {
		s.moveLines(y, n, false)
	} else {
		s.moveColumns(y, n, false)
	}

	return true
}

// DeleteLine deletes n lines at the cursor position Y coordinate.
// Only operates if cursor is within scroll region. Lines below cursor Y
// are moved up, with blank lines inserted at the bottom of scroll region.
// If scrollback is enabled and cursor is at top of scroll region, lines
// are saved to the scrollback buffer before deletion.
// It returns true if the operation was successful.
func (s *Screen) DeleteLine(n int) bool {
	if n <= 0 {
		return false
	}

	scroll := s.scroll
	x, y := s.cur.X, s.cur.Y

	// Only operate if cursor Y is within scroll region
	if y < scroll.Min.Y || y >= scroll.Max.Y ||
		x < scroll.Min.X || x >= scroll.Max.X {
		return false
	}

	// Save lines to scrollback if we're at the top of the scroll region
	// and the scroll region uses the full width (typical terminal scroll).
	// This captures lines that would be lost during scroll up operations.
	if s.scrollback != nil && y == scroll.Min.Y &&
		scroll.Min.X == 0 && scroll.Max.X == s.buf.Width() {
		// Save lines that will be deleted
		linesToSave := min(n, scroll.Max.Y-y)
		if s.scrollback.maxLines > 0 {
			// A scrollback that keeps nothing reads no line.
			s.flushRows(y, y+linesToSave)
		}
		s.scrollback.PushN(s.buf, y, linesToSave)
	}

	if scroll.Min.X == 0 && scroll.Max.X == s.buf.Width() {
		s.moveLines(y, n, true)
	} else {
		s.moveColumns(y, n, true)
	}

	return true
}

// moveLines is InsertLine or, with up set, DeleteLine for a scroll region as
// wide as the screen: it moves the lines of the region, not their cells, and
// blanks the n lines that come in. The cells of a line are copied one at a
// time by the buffer's own insert and delete, which on a screen of 500 by 200
// cells is 11 MB for every line feed at the bottom (1.1 ms; 64 KiB of line
// feeds took over a minute). The result is the same: the lines of the region
// from y on move n lines up (or down), with their marks, the n lines that
// leave are lost (they were saved to the scrollback before), and the n lines
// that come in are blank, in the cell the buffer would have used, which is
// marked and not written (see blank.go): a scroll by the whole region, which
// blanks every line, writes nothing. The lines of the region are all touched,
// as the buffer's own functions do.
func (s *Screen) moveLines(y, n int, up bool) {
	region := s.scroll
	n = min(n, region.Max.Y-y)
	lines := s.buf.Lines[y:region.Max.Y]
	marks := s.marksTable()[y:region.Max.Y]
	from, to := len(lines)-n, len(lines)
	if !up {
		from, to = 0, n
	}
	k := n // rotate the lines left by k: up moves them n lines, down len-n
	if !up {
		k = len(lines) - n
	}
	rotateLeft(lines, k)
	rotateLeft(marks, k)
	s.setLines(y+from, y+to, s.fillCell(s.blankCell()))
	for i := region.Min.Y; i < region.Max.Y; i++ {
		s.buf.TouchLine(0, i, s.buf.Width())
	}
}

// moveColumns is InsertLine or, with up set, DeleteLine for a scroll region
// that is narrower than the screen: the buffer's own insert and delete move
// the cells of the region, one at a time.
//
//   - A count of the lines left in the region or more moves nothing, and is
//     the erase of the area of the region from line y down, which fillBlank
//     does (the buffer's functions fill it with the blank cell, the cell at a
//     time).
//   - A region that leaves few cells of a line outside it (a few columns, as
//     margins do that only cut off the edges), and a count of at most a tenth
//     of the lines left, moves the lines of the region and puts back the
//     cells outside it: 11 MB for a scroll of one line of the largest screen
//     with margins in the second and the last but one column, 1 ms, becomes
//     the cells outside the region and the lines that come in, 50 us a line
//     (a larger count would copy more than it saves: 20 lines cost the 1 ms).
//     The blank lines are
//     then filled by the buffer's function, which repairs a wide cell one
//     cell after another (a wide cell at the edge makes it write outside the
//     region), so the lines that come in get their own cells back first.
//   - Any other region is moved by the buffer's functions.
//
// Nothing is touched but what the buffer's functions touch: the lines of the
// region, and the lines n above (or below) them.
func (s *Screen) moveColumns(y, n int, up bool) {
	area := s.scroll
	x0, x1 := area.Min.X, area.Max.X
	switch {
	case n >= area.Max.Y-y:
		s.fillBlank(s.blankCell(), uv.Rect(x0, y, area.Dx(), area.Max.Y-y))
		s.touchColumns(n, up)
	case (s.buf.Width()-(x1-x0))*8 <= x1-x0 && n*10 <= area.Max.Y-y:
		s.forgetRows(y, area.Max.Y)
		s.shiftColumns(y, n, up)
		s.touchColumns(n, up)
	default:
		s.forgetRows(y, area.Max.Y)
		if up {
			s.buf.DeleteLineArea(y, n, s.blankCell(), area)
		} else {
			s.buf.InsertLineArea(y, n, s.blankCell(), area)
		}
	}
}

// touchColumns marks what the buffer's insert and delete of lines mark in the
// scroll region: its lines, and the lines n above or below them.
func (s *Screen) touchColumns(n int, up bool) {
	area := s.scroll
	for i := area.Min.Y; i < area.Max.Y; i++ {
		s.buf.TouchLine(area.Min.X, i, area.Dx())
		if up {
			s.buf.TouchLine(area.Min.X, i+n, area.Dx())
		} else {
			s.buf.TouchLine(area.Min.X, i-n, area.Dx())
		}
	}
}

// shiftColumns moves the lines of the scroll region from line y n lines up
// (or down), puts the cells outside the region back where they were, and
// blanks the n lines that come in as the buffer's functions do, for a count
// below the lines left. The lines of the region from y are written and
// unmarked (forgetRows).
func (s *Screen) shiftColumns(y, n int, up bool) {
	area := s.scroll
	x0, x1 := area.Min.X, area.Max.X
	lines := s.buf.Lines[y:area.Max.Y]
	from, k := len(lines)-n, n
	if !up {
		from, k = 0, len(lines)-n
	}
	// What is outside the region, line by line, and what is inside the lines
	// that come in: the fill of a line reads the cells it overwrites (the
	// repair of a wide cell goes by what a cell was, one cell after another),
	// so it has to find its own.
	outside := make([]uv.Cell, 0, len(lines)*(s.buf.Width()-(x1-x0)))
	for _, line := range lines {
		outside = append(outside, line[:x0]...)
		outside = append(outside, line[x1:]...)
	}
	own := make([][]uv.Cell, n)
	for i := range own {
		own[i] = slices.Clone(lines[from+i][x0:x1])
	}
	rotateLeft(lines, k)
	o := 0
	for _, line := range lines {
		o += copy(line[:x0], outside[o:])
		o += copy(line[x1:], outside[o:])
	}
	blank := s.blankCell()
	for i := range own {
		copy(lines[from+i][x0:x1], own[i])
		s.buf.Buffer.FillArea(blank, uv.Rect(x0, y+from+i, x1-x0, 1))
	}
}

// blankCell returns the cursor blank cell with the background color set to the
// current pen background color. If the pen background color is nil, the return
// value is nil.
func (s *Screen) blankCell() *uv.Cell {
	if s.cur.Pen.Bg == nil {
		return nil
	}

	c := uv.EmptyCell
	c.Style.Bg = s.cur.Pen.Bg
	return &c
}

// fillLine sets every cell of line y to the cell c, which fillCell returned
// and is one cell wide: no wide cell of the line is left half over. The cells
// are not written (see blank.go).
func (s *Screen) fillLine(y int, c *uv.Cell) {
	s.setLines(y, y+1, c)
	s.buf.TouchLine(0, y, s.buf.Width())
}

// touchArea marks all lines in the given area as touched.
func (s *Screen) touchArea(area uv.Rectangle) {
	for y := area.Min.Y; y < area.Max.Y; y++ {
		s.buf.TouchLine(area.Min.X, y, area.Max.X-area.Min.X)
	}
}

// Scrollback returns the screen's scrollback buffer.
func (s *Screen) Scrollback() *Scrollback {
	return s.scrollback
}

// SetScrollback sets the screen's scrollback buffer.
// Pass nil to disable scrollback.
func (s *Screen) SetScrollback(sb *Scrollback) {
	s.scrollback = sb
}

// SetScrollbackSize sets the maximum number of lines in the scrollback buffer.
// Zero keeps no scrollback. A negative number is ignored, except that a screen
// without a scrollback buffer gets one of the default size.
func (s *Screen) SetScrollbackSize(maxLines int) {
	if s.scrollback == nil {
		if maxLines == 0 {
			s.scrollback = &Scrollback{}
			return
		}
		s.scrollback = NewScrollback(maxLines)
	} else {
		s.scrollback.SetMaxLines(maxLines)
	}
}
