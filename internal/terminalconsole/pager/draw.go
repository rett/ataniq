package pager

import (
	"strconv"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/exchange/leadtext"
)

// Canvas is what the pager draws into: a *uv.Buffer, or any uv.Screen such
// as a uv.ScreenBuffer.
type Canvas interface {
	Bounds() uv.Rectangle
	SetCell(x, y int, c *uv.Cell)
}

// wrapMark is drawn in the left margin of every row of a line after its
// first: the row goes on from the row above. It is the pager's own, one
// cell wide under every way of measuring, and the patch's text never stands
// in the margin.
const wrapMark = "↪"

// header is the first row. It says whose text the rows under it are, in the
// words leadtext gives for that.
const header = "PATCH · " + leadtext.Label

// Draw draws the pager into area of dst, its top left cell at area.Min. It
// draws at the size given to SetSize, which should be area's: where area is
// smaller the drawing is cut, and where it is larger the rest is blank.
// Only area is written, and all of it.
//
// Draw writes cells, never escape sequences, and it reads no page: what is
// not held yet is drawn once its page has come.
func (m *Model) Draw(dst Canvas, area uv.Rectangle) {
	area = area.Intersect(dst.Bounds())
	if area.Empty() {
		return
	}
	d := &drawer{dst: dst, area: area}
	text := uv.Style{Fg: m.colors.Fg, Bg: m.colors.Bg}
	dim := uv.Style{Fg: m.colors.Dim, Bg: m.colors.Bg}
	for y := 0; y < area.Dy(); y++ {
		for x := 0; x < area.Dx(); x++ {
			d.set(x, y, " ", 1, text)
		}
	}
	if m.w <= 0 || m.h <= 0 {
		return
	}
	rows := m.rows()
	// below is how many pages lie after the last row drawn, atEnd whether
	// that row is the last of the patch.
	below, atEnd := -1, false
	if rows > 0 {
		d.host(0, 0, m.w, header, dim)
	}
	if rows > 0 && m.ready {
		w := m.wrapper()
		c, st := m.top, found
		for y := 0; y < rows && st == found; y++ {
			ln, ok := m.lineAt(c.at)
			if !ok {
				break
			}
			below = m.pages - 1 - ln.last
			if c.row > 0 {
				d.set(0, 1+y, wrapMark, 1, dim)
			}
			end := w.rowEnd(ln.text, c.off)
			x := marginCells
			for off := c.off; off < end; {
				n, cells, carrier := w.unit(ln.text[off:])
				content := ln.text[off : off+n]
				if carrier {
					content = " " + content
				}
				if x+cells <= m.w {
					d.set(x, 1+y, content, cells, text)
				}
				x += cells
				off += n
			}
			c, st = m.next(c)
		}
		atEnd = st == edge
	}
	style := dim
	if m.fail != "" {
		style = uv.Style{Fg: m.colors.Warn, Bg: m.colors.Bg}
	}
	d.host(0, m.h-1, m.w, m.footer(below, atEnd), style)
}

// footer returns the last row's text. All of it is the pager's own words
// and numbers. The parts are in the order of their weight, and the parts
// that do not fit the width are left out, last first.
func (m *Model) footer(below int, atEnd bool) string {
	var parts []string
	if m.fail != "" {
		parts = []string{m.fail, "end not reached"}
	} else {
		if m.Reached() {
			parts = append(parts, "end reached")
		} else {
			parts = append(parts, "end not reached")
		}
		switch {
		case m.rows() == 0:
			parts = append(parts, "too small to show the patch")
		case m.empty():
			parts = append(parts, "the patch is empty")
		case below < 0:
			parts = append(parts, "reading the patch")
		case atEnd:
			parts = append(parts, "end of the patch")
		case below == 0:
			parts = append(parts, "last page, more below")
		default:
			parts = append(parts, strconv.Itoa(below)+" of "+strconv.Itoa(m.pages)+" pages below")
		}
	}
	parts = append(parts, "Esc closes", "j/k PgUp/PgDn Home/End")
	if m.waiting {
		parts = append(parts, "reading a page")
	}
	s := parts[0]
	for _, p := range parts[1:] {
		if ansi.StringWidth(s+" · "+p) > m.w {
			break
		}
		s += " · " + p
	}
	return s
}

// drawer writes cells of the pager's region, by their place in the region,
// and nothing outside area.
type drawer struct {
	dst  Canvas
	area uv.Rectangle
	// cell is the cell handed to the canvas, which copies it. One is used
	// for every cell, so that a drawing does not make a value for each.
	cell uv.Cell
}

// set writes one cell. A cell that does not fit area whole is left blank.
func (d *drawer) set(x, y int, content string, width int, st uv.Style) {
	if x < 0 || y < 0 || y >= d.area.Dy() || x+width > d.area.Dx() {
		return
	}
	d.cell = uv.Cell{Content: content, Width: width, Style: st}
	d.dst.SetCell(d.area.Min.X+x, d.area.Min.Y+y, &d.cell)
}

// host draws the pager's own words from (x, y), at most maxw cells wide. A
// text that does not fit ends in an ellipsis. It is never given text of the
// lead.
func (d *drawer) host(x, y, maxw int, s string, st uv.Style) {
	if maxw <= 0 {
		return
	}
	limit := maxw
	if ansi.StringWidth(s) > maxw {
		limit = maxw - 1
	}
	used := 0
	for rest := s; rest != ""; {
		c, w := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if c == "" || used+w > limit {
			break
		}
		rest = rest[len(c):]
		d.set(x+used, y, c, w, st)
		used += w
	}
	if limit < maxw {
		d.set(x+used, y, "…", 1, st)
	}
}
