package arrival

import (
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Canvas is what the strip draws into: a *uv.Buffer, or any uv.Screen such
// as a uv.ScreenBuffer.
type Canvas interface {
	Bounds() uv.Rectangle
	SetCell(x, y int, c *uv.Cell)
}

// replacement stands in a cell for a character that is not drawn.
const replacement = "?"

// Draw draws the strip into area of dst, its top left cell at area.Min: the
// title row, then one row for each line, each with the digit that opens it.
// The cockpit gives it [Strip.Rows] rows. In a lower area the last lines are
// left out, and those cannot be opened, nor can a line in an area too
// narrow for its digit; a line too long for the width ends
// in an ellipsis. With focused set, the title row also says which keys are
// the strip's.
//
// While the strip is showing, only area is written, and all of it. While it
// is not, nothing is written. Draw writes cells, never escape sequences.
func (s *Strip) Draw(dst Canvas, area uv.Rectangle, focused bool) {
	if !s.visible {
		return
	}
	s.drawn = 0
	area = area.Intersect(dst.Bounds())
	if area.Empty() {
		return
	}
	d := &drawer{dst: dst, area: area}
	text := uv.Style{Fg: s.colors.Fg, Bg: s.colors.Bg}
	dim := uv.Style{Fg: s.colors.Dim, Bg: s.colors.Bg}
	warn := uv.Style{Fg: s.colors.Warn, Bg: s.colors.Bg}
	head := text
	head.Attrs = uv.AttrBold
	for y := 0; y < area.Dy(); y++ {
		for x := 0; x < area.Dx(); x++ {
			d.set(x, y, " ", 1, text)
		}
	}
	w := area.Dx()
	used := d.put(0, 0, w, title, head)
	if focused && used == ansi.StringWidth(title) {
		// The hint is drawn whole or not at all.
		if hint := sep + hintFocused; used+ansi.StringWidth(hint) <= w {
			d.put(used, 0, w-used, hint, dim)
		}
	}
	for i, r := range s.rows {
		y := 1 + i
		if y >= area.Dy() || i >= 9 {
			break
		}
		mark := "[" + string(rune('1'+i)) + "] "
		x := d.put(0, y, w, mark, dim)
		st := text
		if r.alert {
			st = warn
		}
		d.put(x, y, w-x, r.text, st)
		// A line whose digit could not be drawn was not shown.
		if x == len(mark) {
			s.drawn = i + 1
		}
	}
}

// drawer writes cells of the strip's region, by their place in the region,
// and nothing outside area.
type drawer struct {
	dst  Canvas
	area uv.Rectangle
	// cell is the cell handed to the canvas, which copies it.
	cell uv.Cell
}

// set writes one cell. A cell that does not fit area whole is left as it is.
func (d *drawer) set(x, y int, content string, width int, st uv.Style) {
	if x < 0 || y < 0 || y >= d.area.Dy() || x+width > d.area.Dx() {
		return
	}
	d.cell = uv.Cell{Content: content, Width: width, Style: st}
	d.dst.SetCell(d.area.Min.X+x, d.area.Min.Y+y, &d.cell)
}

// put draws s from (x, y), at most maxw cells wide, and returns the width
// drawn. A text that does not fit ends in an ellipsis.
//
// It is the one place where text reaches the cells, and it trusts none of
// it: each character becomes a cell of its own through cellOf, so nothing a
// terminal would act on or hide is written, whoever wrote s. It reads no
// more of s than fits the width.
func (d *drawer) put(x, y, maxw int, s string, st uv.Style) int {
	if maxw <= 0 {
		return 0
	}
	type cell struct {
		content string
		w       int
	}
	// Characters are taken until one more than fits was seen: then the
	// text is known not to fit, and the rest is not read.
	var cells []cell
	total := 0
	for rest := s; rest != "" && total <= maxw; {
		r, size := utf8.DecodeRuneInString(rest)
		rest = rest[size:]
		c, w := cellOf(r)
		cells = append(cells, cell{c, w})
		total += w
	}
	limit := maxw
	if total > maxw {
		limit = maxw - 1
	}
	used := 0
	for _, c := range cells {
		if used+c.w > limit {
			break
		}
		d.set(x+used, y, c.content, c.w, st)
		used += c.w
	}
	if limit < maxw {
		d.set(x+used, y, "…", 1, st)
		used++
	}
	return used
}

// cellOf returns what is drawn for one character and how many cells it
// takes, one or two. A character is drawn as itself when it is printable,
// stands alone and has the same width however a terminal measures it. Every
// other one is drawn as a question mark: a control byte or code point, a
// byte that is not UTF-8, a direction override, a code point without width
// (a combining mark, a joiner, a variation selector), a space other than the
// ASCII space. A tab is drawn as a space.
func cellOf(r rune) (string, int) {
	switch {
	case r == '\t':
		return " ", 1
	case r >= 0x20 && r < 0x7f:
		return string(r), 1
	case r == utf8.RuneError, !unicode.IsPrint(r), unicode.Is(unicode.M, r),
		unicode.Is(unicode.Bidi_Control, r), unicode.Is(unicode.Join_Control, r),
		unicode.Is(unicode.Variation_Selector, r),
		unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r):
		return replacement, 1
	}
	c := string(r)
	w := ansi.StringWidth(c)
	if w < 1 || w > 2 || ansi.WcWidth.StringWidth(c) != w {
		return replacement, 1
	}
	return c, w
}
