package terminalconsole

import (
	"image/color"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// palette holds the colours the cockpit paints with, and reports to the
// session's colour queries.
type palette struct{ fg, bg, dim, accent, warn color.RGBA }

func paletteFor(theme string) palette {
	if theme == "light" {
		return palette{
			fg: color.RGBA{0x1a, 0x1a, 0x1a, 0xff}, bg: color.RGBA{0xfa, 0xfa, 0xfa, 0xff},
			dim: color.RGBA{0x5c, 0x63, 0x70, 0xff}, accent: color.RGBA{0x0b, 0x5c, 0xad, 0xff}, warn: color.RGBA{0x9a, 0x5b, 0x00, 0xff},
		}
	}
	return palette{
		fg: color.RGBA{0xe6, 0xe6, 0xe6, 0xff}, bg: color.RGBA{0x0e, 0x0f, 0x12, 0xff},
		dim: color.RGBA{0x8a, 0x90, 0x9a, 0xff}, accent: color.RGBA{0x4f, 0xc3, 0xf7, 0xff}, warn: color.RGBA{0xf0, 0xb4, 0x29, 0xff},
	}
}

// Placeholders drawn in place of a cluster that an outer terminal without mode
// 2027 would draw at another width. Each is one code point that every width
// method measures the same: "?" takes one cell and U+FF1F two.
const (
	narrowPlaceholder = "?"
	widePlaceholder   = "？"
)

// lineKind is how a line of a panel is styled.
type lineKind int

const (
	lnText lineKind = iota
	lnDim
	lnHead
	lnWarn
	lnCursor
)

type line struct {
	s string
	k lineKind
}

// painter draws into a cell buffer. It writes cells, never escape sequences,
// and the text it is given has been through clean.
type painter struct {
	buf       *uv.Buffer
	pal       palette
	graphemes bool
}

func (p *painter) style(k lineKind) uv.Style {
	st := uv.Style{Fg: p.pal.fg, Bg: p.pal.bg}
	switch k {
	case lnDim:
		st.Fg = p.pal.dim
	case lnHead:
		st.Fg, st.Attrs = p.pal.accent, uv.AttrBold
	case lnWarn:
		st.Fg = p.pal.warn
	case lnCursor:
		st.Fg, st.Attrs = p.pal.accent, uv.AttrBold
	}
	return st
}

// fill blanks r in the base colours.
func (p *painter) fill(r uv.Rectangle) {
	cell := uv.Cell{Content: " ", Width: 1, Style: p.style(lnText)}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p.buf.SetCell(x, y, &cell)
		}
	}
}

// text draws s from (x, y), at most maxw cells wide, and returns the width
// drawn. A text that does not fit ends in an ellipsis. A cluster that an outer
// terminal without mode 2027 would measure differently is drawn as a
// placeholder of its cell width.
func (p *painter) text(x, y, maxw int, s string, st uv.Style) int {
	if maxw <= 0 {
		return 0
	}
	type cluster struct {
		content string
		w       int
	}
	var cs []cluster
	total := 0
	for rest := s; rest != ""; {
		c, w := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if c == "" {
			break
		}
		rest = rest[len(c):]
		if w <= 0 {
			continue
		}
		if !p.graphemes && ansi.WcWidth.StringWidth(c) != w {
			c = narrowPlaceholder
			if w > 1 {
				c = widePlaceholder
			}
		}
		cs = append(cs, cluster{c, w})
		total += w
	}
	limit, ellipsis := maxw, false
	if total > maxw {
		limit, ellipsis = maxw-1, true
	}
	used := 0
	for _, c := range cs {
		if used+c.w > limit {
			break
		}
		p.buf.SetCell(x+used, y, &uv.Cell{Content: c.content, Width: c.w, Style: st})
		used += c.w
	}
	if ellipsis && limit >= 0 {
		p.buf.SetCell(x+used, y, &uv.Cell{Content: "…", Width: 1, Style: st})
		used++
	}
	return used
}

// lines draws ls in the rows of r, from the first, each cut to r's width.
func (p *painter) lines(r uv.Rectangle, ls []line) {
	for i, l := range ls {
		if i >= r.Dy() {
			return
		}
		p.text(r.Min.X, r.Min.Y+i, r.Dx(), l.s, p.style(l.k))
	}
}

// box draws a border around r with a title in its top edge and a label in its
// bottom edge. A focused box has a heavy border in the accent colour.
func (p *painter) box(r uv.Rectangle, title, label string, focused bool) {
	if r.Dx() < 2 || r.Dy() < 2 {
		return
	}
	p.fill(r)
	tl, tr, bl, br, h, v := "┌", "┐", "└", "┘", "─", "│"
	st := p.style(lnText)
	if focused {
		tl, tr, bl, br, h, v = "┏", "┓", "┗", "┛", "━", "┃"
		st = p.style(lnCursor)
	}
	set := func(x, y int, c string) { p.buf.SetCell(x, y, &uv.Cell{Content: c, Width: 1, Style: st}) }
	x0, y0, x1, y1 := r.Min.X, r.Min.Y, r.Max.X-1, r.Max.Y-1
	for x := x0 + 1; x < x1; x++ {
		set(x, y0, h)
		set(x, y1, h)
	}
	for y := y0 + 1; y < y1; y++ {
		set(x0, y, v)
		set(x1, y, v)
	}
	set(x0, y0, tl)
	set(x1, y0, tr)
	set(x0, y1, bl)
	set(x1, y1, br)
	if title != "" {
		p.text(x0+2, y0, r.Dx()-4, " "+title+" ", st)
	}
	if label != "" {
		p.text(x0+2, y1, r.Dx()-4, " "+label+" ", p.style(lnDim))
	}
}

// centred draws a bordered message in the middle of r, over what is there.
// When r is too small for a border it draws the first line alone.
func (p *painter) centred(r uv.Rectangle, title string, ls []line) {
	w := min(r.Dx(), 64)
	inner := max(0, w-4)
	var wrapped []line
	for _, l := range ls {
		for _, s := range wrapLines(l.s, inner) {
			wrapped = append(wrapped, line{s, l.k})
		}
	}
	rows := min(len(wrapped), max(0, r.Dy()-2))
	h := rows + 2
	if w < 8 || h < 3 {
		if r.Dx() > 0 && r.Dy() > 0 && len(ls) > 0 {
			p.text(r.Min.X, r.Min.Y, r.Dx(), ls[0].s, p.style(lnWarn))
		}
		return
	}
	box := uv.Rect(r.Min.X+(r.Dx()-w)/2, r.Min.Y+(r.Dy()-h)/2, w, h)
	p.box(box, title, "", true)
	p.lines(uv.Rect(box.Min.X+2, box.Min.Y+1, inner, rows), wrapped)
}

// wrapLines wraps s to lines of at most w cells.
func wrapLines(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	if s == "" {
		return []string{""}
	}
	return strings.Split(ansi.Wrap(s, w, ""), "\n")
}
