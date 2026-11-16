package sessionpane

import (
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Placeholders drawn in place of a cluster the outer terminal would draw at
// another width. Both are a single code point whose width is the same under
// every width method: "?" is one cell, U+FF1F FULLWIDTH QUESTION MARK two.
const (
	narrowPlaceholder = "?"
	widePlaceholder   = "？"
)

// MaxCellBytes is the most bytes of content the pane draws in one cell. A
// cell holding more is drawn as a placeholder of its width, keeping its
// style. A grapheme cluster has no length limit, so a session can pile
// combining marks onto one character until a single cell holds megabytes;
// the limit keeps that from reaching the caller's buffer and the outer
// terminal. 64 bytes is almost twice the longest emoji sequence Unicode
// recommends (a kiss with two skin tones, 35 bytes), so no real text is
// replaced. The emulator itself still keeps the whole cluster.
const MaxCellBytes = 64

// cellSource is what Draw reads cells from: the emulator, or a held frame.
type cellSource interface {
	CellAt(x, y int) *uv.Cell
}

// Canvas is what the pane draws into: a *uv.Buffer, or any uv.Screen such as
// a uv.ScreenBuffer.
type Canvas interface {
	Bounds() uv.Rectangle
	SetCell(x, y int, c *uv.Cell)
}

// Draw draws the pane into area of dst, the pane's top left cell at
// area.Min.
//
// Only area is written, and all of it: a pane smaller than area leaves the
// rest blank, a larger one is cut, and a wide character that would cross the
// cut is drawn as blanks. Cells keep their content and style but never a
// hyperlink, so nothing the session wrote reaches the outer terminal except
// as plain cells.
//
// Unless the outer terminal measures grapheme clusters (SetGraphemeWidths),
// a cell whose content the outer terminal would measure at another width
// than the pane does, such as a ZWJ sequence or an emoji with a variation
// selector, is drawn as a placeholder of the cell's width, so that the
// columns after it stay where the pane put them. So is, in either mode, a
// cell whose content is longer than MaxCellBytes.
func (p *Pane) Draw(dst Canvas, area uv.Rectangle) {
	area = area.Intersect(dst.Bounds())
	if area.Empty() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	src, cols, rows := p.source()
	blank := uv.EmptyCell
	w, h := min(cols, area.Dx()), min(rows, area.Dy())
	for y := 0; y < area.Dy(); y++ {
		for x := 0; x < area.Dx(); {
			if x >= w || y >= h {
				dst.SetCell(area.Min.X+x, area.Min.Y+y, &blank)
				x++
				continue
			}
			c := src.CellAt(x, y)
			if c == nil || c.Width <= 0 || c.Content == "" {
				// Nothing, or the second column of a wide character whose
				// first column was not drawn.
				dst.SetCell(area.Min.X+x, area.Min.Y+y, &blank)
				x++
				continue
			}
			if x+c.Width > w {
				for ; x < w; x++ {
					dst.SetCell(area.Min.X+x, area.Min.Y+y, &blank)
				}
				continue
			}
			out := p.present(c)
			dst.SetCell(area.Min.X+x, area.Min.Y+y, &out)
			x += c.Width
		}
	}
}

// present returns the cell as the pane draws it.
func (p *Pane) present(c *uv.Cell) uv.Cell {
	out := *c
	out.Link = uv.Link{}
	if !drawable(out.Content, out.Width, p.graphemes) {
		out.Content = placeholder(out.Width)
	}
	return out
}

// drawable reports whether content can be drawn as it is in a cell of the
// given width: it is at most MaxCellBytes long, it holds no control
// character and, unless the outer terminal measures grapheme clusters, the
// outer terminal's per-character (wcwidth) measure agrees with the width.
func drawable(content string, width int, graphemes bool) bool {
	if len(content) > MaxCellBytes {
		return false
	}
	if len(content) == 1 && content[0] >= 0x20 && content[0] < 0x7f {
		return width == 1
	}
	for _, r := range content {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return graphemes || ansi.WcWidth.StringWidth(content) == width
}

func placeholder(width int) string {
	if width == 2 {
		return widePlaceholder
	}
	return strings.Repeat(narrowPlaceholder, max(width, 1))
}

// source returns what Draw shows, under p.mu: the held frame while the
// session is in a synchronized update, else the screen.
func (p *Pane) source() (cellSource, int, int) {
	if p.holding(p.clock()) {
		f := &p.syncOut.frame
		return f, f.cols, f.rows
	}
	return p.emu, p.cols, p.rows
}

// SetGraphemeWidths tells the pane whether the outer terminal measures
// grapheme clusters (mode 2027) and so draws any cluster at the width the
// pane gives it.
func (p *Pane) SetGraphemeWidths(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.graphemes = on
}

// Cursor returns the cursor's position in pane cells, and whether the cursor
// should be shown: not when the session hid it, nor once it has ended. While
// a frame is held it is the cursor of that frame. The
// caller adds the offset of the area it drew the pane in, and hides a cursor
// that falls outside that area.
func (p *Pane) Cursor() (pos uv.Position, visible bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.holding(p.clock()) {
		f := &p.syncOut.frame
		return f.cursor, !p.ended && !f.hidden
	}
	return p.emu.CursorPosition(), !p.ended && !p.emu.CursorHidden()
}
