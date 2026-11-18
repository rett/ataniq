package vt

import (
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// nextTab moves the cursor to the next tab stop n times. This respects the
// horizontal scrolling region. This performs the same function as [ansi.CHT].
func (e *Emulator) nextTab(n int) {
	x, y := e.scr.CursorPosition()
	scroll := e.scr.ScrollRegion()
	for range n {
		ts := e.tabstops.Next(x)
		// No further tab stop: the other repeats would stay here too, and
		// a count of two billion made this loop for minutes.
		if ts <= x {
			break
		}
		x = ts
	}

	if x >= scroll.Max.X {
		x = min(scroll.Max.X-1, e.Width()-1)
	}

	// NOTE: We use t.scr.setCursor here because we don't want to reset the
	// phantom state.
	e.scr.setCursor(x, y, false)
}

// prevTab moves the cursor to the previous tab stop n times. This respects the
// horizontal scrolling region when origin mode is set. If the cursor would
// move past the leftmost valid column, the cursor remains at the leftmost
// valid column and the operation completes.
func (e *Emulator) prevTab(n int) {
	x, _ := e.scr.CursorPosition()
	leftmargin := 0
	scroll := e.scr.ScrollRegion()
	if e.isModeSet(ansi.DECOM) {
		leftmargin = scroll.Min.X
	}

	for range n {
		ts := e.tabstops.Prev(x)
		// As in nextTab, stop where the cursor can go no further.
		if ts >= x {
			break
		}
		x = ts
	}

	if x < leftmargin {
		x = leftmargin
	}

	// NOTE: We use t.scr.setCursorX here because we don't want to reset the
	// phantom state.
	e.scr.setCursorX(x, false)
}

// moveCursor moves the cursor by the given x and y deltas. If the cursor
// is at phantom, the state will reset and the cursor is back in the screen.
func (e *Emulator) moveCursor(dx, dy int) {
	e.scr.moveCursor(dx, dy)
	e.atPhantom = false
}

// setCursor sets the cursor position. This resets the phantom state.
func (e *Emulator) setCursor(x, y int) {
	e.scr.setCursor(x, y, false)
	e.atPhantom = false
}

// setCursorPosition sets the cursor position. This respects [ansi.DECOM],
// Origin Mode. This performs the same function as [ansi.CUP].
func (e *Emulator) setCursorPosition(x, y int) {
	mode, ok := e.modes[ansi.DECOM]
	margins := ok && mode.IsSet()
	e.scr.setCursor(x, y, margins)
	e.atPhantom = false
}

// carriageReturn moves the cursor to the leftmost column. If [ansi.DECOM] is
// set, the cursor is set to the left margin. If not, and the cursor is on or
// to the right of the left margin, the cursor is set to the left margin.
// Otherwise, the cursor is set to the leftmost column of the screen.
// This performs the same function as [ansi.CR].
func (e *Emulator) carriageReturn() {
	mode, ok := e.modes[ansi.DECOM]
	margins := ok && mode.IsSet()
	x, y := e.scr.CursorPosition()
	if margins {
		e.scr.setCursor(0, y, true)
	} else if region := e.scr.ScrollRegion(); uv.Pos(x, y).In(region) {
		e.scr.setCursor(region.Min.X, y, false)
	} else {
		e.scr.setCursor(0, y, false)
	}
	e.atPhantom = false
}

// repeatPreviousCharacter repeats the previous character n times. This is
// equivalent to typing the same character n times. This performs the same as
// [ansi.REP].
//
// A count is a parameter of up to two billion, and each print costs a cell
// write, and a print that wraps at the bottom of the region a scroll of the
// whole region: a count of the cells of a 500 by 200 screen cost 17 ms, and
// 100 of them with the cursor at the bottom 25 s. A count of up to two lines
// is printed one character at a time. A larger one is done a line at a time
// when the result is the same (see repeatInBulk): the lines that would only
// scroll over rows that are already full of the character are skipped, which
// leaves every field of [State] as the printing would. Any other state (a
// pen under a hyperlink, a region narrower than the screen, a cursor outside
// the region, a scrollback, a character that is not printable ASCII) cannot
// skip, and a count over two lines is cut to two lines. Either way
// `Counters().RepeatsCut` counts the sequence.
func (e *Emulator) repeatPreviousCharacter(n int) {
	if e.lastChar == 0 {
		return
	}
	if limit := 2*e.Width() + 2; n > limit {
		e.counters.RepeatsCut++
		if e.repeatInBulk(n) {
			return
		}
		n = limit
	}
	for range n {
		e.handlePrint(e.lastChar)
	}
}

// repeatInBulk prints lastChar n times, n at least 3, at a cost of a few
// screens whatever n is, and reports whether it did. It does not when the
// bulk would not give what the prints give, and then it changes nothing.
//
// The prints are: the first buffers the character, each of the others draws
// the buffered character and buffers the next, so n prints draw n-1
// characters. The bulk makes the first two prints (the first draw is the only
// one a pending single shift changes), then draws the others, which are all
// the same cell:
//
//   - With autowrap off every draw after the last column is a draw in the
//     last column again, so the draws are cut to the cells of the line.
//   - With autowrap on the draws fill the line to the end (a print each),
//     then every width draws are a whole line: the cursor wraps (down one
//     line, or the region scrolls one line when it is at the bottom of the
//     region) and the line is filled. The lines above the bottom of the region
//     are done one by one. Of the lines that scroll, as many as the region has
//     lines replace every line of the region, and so the rest change nothing:
//     the cursor is at the bottom, the region holds only the character. The
//     draws that are left over, less than a line, are printed.
//
// A scrollback would keep the lines that scroll off, and a link table would
// count the cells under a link it refuses, so a pen under a link and a
// screen with a scrollback print everything. The comparison tests of this
// package check the result against the prints on small screens.
func (e *Emulator) repeatInBulk(n int) bool {
	if !e.repeatBulkable() {
		return false
	}
	r := e.lastChar
	scr := e.scr
	w := scr.Width()
	region := scr.ScrollRegion()
	awm := e.isModeSet(ansi.ModeAutoWrap)

	// The first print buffers the character, the second draws it (with the
	// single shift, if one is pending) and buffers the next.
	e.handlePrint(r)
	e.handlePrint(r)
	d := n - 2
	draw := func() { e.handleGrapheme(string(r), 1) }

	if !awm {
		x, _ := scr.CursorPosition()
		for range min(d, w-x) {
			draw()
		}
		return true
	}

	for d > 0 && !e.atPhantom {
		draw()
		d--
	}
	lines, tail := d/w, d%w
	if lines > 0 {
		rc := e.repeatedCell()
		cell := scr.fillCell(&rc)
		_, y := scr.CursorPosition()
		for lines > 0 && y < region.Max.Y-1 {
			e.index()
			_, y = scr.CursorPosition()
			scr.fillLine(y, cell)
			scr.setCursor(w-1, y, false)
			e.atPhantom = true
			lines--
		}
		if lines > 0 {
			// At the bottom of the region: every line is a scroll of one line
			// and a full bottom line.
			size := region.Dy()
			if s := min(lines, size); s == size {
				for yy := region.Min.Y; yy < region.Max.Y; yy++ {
					scr.fillLine(yy, cell)
				}
			} else {
				scr.ScrollUp(s)
				for yy := region.Max.Y - s; yy < region.Max.Y; yy++ {
					scr.fillLine(yy, cell)
				}
			}
			scr.setCursor(w-1, region.Max.Y-1, false)
			e.atPhantom = true
		}
	}
	for range tail {
		draw()
	}
	return true
}

// repeatBulkable reports whether repeatInBulk can do a repeat: the character
// is printable ASCII, the pen has no link, no scrollback keeps the lines that
// scroll off, and with autowrap on the cursor is in a scroll region that is
// as wide as the screen.
func (e *Emulator) repeatBulkable() bool {
	scr := e.scr
	if !printableASCII(e.lastChar) || scr.cursorLink().URL != "" ||
		(scr.scrollback != nil && scr.scrollback.MaxLines() > 0) {
		return false
	}
	if e.isModeSet(ansi.ModeAutoWrap) {
		_, y := scr.CursorPosition()
		region := scr.ScrollRegion()
		return region.Min.X == 0 && region.Max.X == scr.Width() && y >= region.Min.Y && y < region.Max.Y
	}
	return true
}

// repeatedCell is the cell a print of lastChar draws when no single shift is
// pending, as handleGrapheme makes it for a pen that has no link (it may
// have parameters, which a cell keeps) and a printable ASCII character.
func (e *Emulator) repeatedCell() uv.Cell {
	c := uv.Cell{Content: string(e.lastChar), Width: 1, Style: e.scr.cursorPen(), Link: e.scr.cursorLink()}
	if cs := e.charsets[e.gl]; cs != nil {
		if r, ok := cs[byte(e.lastChar)]; ok {
			c.Content = r
		}
	}
	return c
}
