package approvals

import (
	"strings"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/approval/record"
)

// Canvas is what the view draws into: a *uv.Buffer, or any uv.Screen such as
// a uv.ScreenBuffer.
type Canvas interface {
	Bounds() uv.Rectangle
	SetCell(x, y int, c *uv.Cell)
}

// replacement stands in a cell for a character that is not drawn.
const replacement = "?"

// maxText bounds the bytes of one string that the view lays out. A string
// that is longer is cut and ends in an ellipsis. Lead text is cut much
// shorter, by its own escaping; this bound is for every other string, so
// that no string of any source can make the view lay out a million rows.
const maxText = 1024

// style names how a piece of a row is drawn.
type style int

const (
	sText style = iota
	sDim
	sHead
	sWarn
	sAccent
)

// seg is a piece of a row in one style.
type seg struct {
	s  string
	st style
}

// row is one row of a scrolling area.
type row struct {
	segs []seg
	// cont says that the row goes on from the row above: a mark stands in
	// the margin.
	cont bool
	// item is 1 plus the index of the list item (in a list) or of the
	// flagged path (on a card) that the row belongs to, and 0 for none.
	item int
	// ends names the sections that end with this row.
	ends endMask
	// src numbers the lines the builder was given, so that a row can be
	// found again after the width changed and the same line wrapped
	// differently.
	src int
}

// endMask is a set of sections of a card.
type endMask uint16

const (
	endPaths endMask = 1 << iota
	endList0
	endList1
	endList2
	endList3
	endRefs
	endDiffers
	endPage
	// startPage marks the heading of the page of the whole path list, where
	// the reader starts when a page was turned to.
	startPage
)

// cellOf returns what is drawn for one character and how many cells it
// takes, one or two. A character is drawn as itself when it is printable,
// stands alone and has the same width however a terminal measures it. Every
// other one is drawn as a question mark: a control byte or code point, a
// byte that is not UTF-8, a direction override, a code point without width
// (a combining mark, a joiner, a variation selector), a space other than the
// ASCII space, and the blank braille character. A tab is drawn as a space.
func cellOf(r rune) (string, int) {
	switch {
	case r == '\t':
		return " ", 1
	case r >= 0x20 && r < 0x7f:
		return string(r), 1
	case r == utf8.RuneError, r == 0x2800, !unicode.IsPrint(r), unicode.Is(unicode.M, r),
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

// badFeatureName is what is drawn in place of a feature name that is outside
// the grammar of feature names.
const badFeatureName = "(a feature name outside the grammar)"

// nameText returns the text drawn after the verb for a feature name: " name="
// and the name when it is within the grammar of feature names, and a
// placeholder written here when it is not. The view checks the name itself,
// since the card it is given may come from any backend.
func nameText(feature string) string {
	if feature == "" {
		return ""
	}
	if !record.ValidFeatureName(feature) {
		return " " + badFeatureName
	}
	return " name=" + feature
}

// clip cuts s to maxText bytes at a character boundary, with an ellipsis
// where it was cut.
func clip(s string) string {
	if len(s) <= maxText {
		return s
	}
	cut := maxText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// width returns how many cells s takes as the view draws it.
func width(s string) int {
	n := 0
	for rest := clip(s); rest != ""; {
		r, size := utf8.DecodeRuneInString(rest)
		rest = rest[size:]
		_, w := cellOf(r)
		n += w
	}
	return n
}

func segsWidth(ss []seg) int {
	n := 0
	for _, s := range ss {
		n += width(s.s)
	}
	return n
}

// wrapCells cuts s into chunks that take at most first cells for the first
// chunk and rest cells for each one after it. It never splits a character,
// and it breaks after a space when there is one in the last two thirds of
// the chunk, so that words stay whole where they can.
func wrapCells(s string, first, rest int) []string {
	first, rest = max(2, first), max(2, rest)
	s = clip(s)
	var out []string
	limit, used, start := first, 0, 0
	// afterSpace is the index just after the last space of the chunk, and
	// usedAtSpace the cells the chunk took up to there.
	afterSpace, usedAtSpace := -1, 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		_, w := cellOf(r)
		for used+w > limit && i > start {
			cut, carry := i, 0
			if afterSpace > start && usedAtSpace*3 >= limit*2 {
				cut, carry = afterSpace, used-usedAtSpace
			}
			out = append(out, s[start:cut])
			start, used, limit, afterSpace = cut, carry, rest, -1
		}
		used += w
		i += size
		if r == ' ' {
			afterSpace, usedAtSpace = i, used
		}
	}
	return append(out, s[start:])
}

// builder lays out rows for a scrolling area w cells wide.
type builder struct {
	w     int
	rows  []row
	lines int
}

func (b *builder) add(segs ...seg) int {
	b.rows = append(b.rows, row{segs: segs, src: b.lines})
	b.lines++
	return len(b.rows) - 1
}

// flow lays out prefix and then text, wrapped to the width: the rows after
// the first start with indent blank cells and carry the continuation mark.
// tail follows the last chunk, on a row of its own when it does not fit. It
// returns the first and the last row made.
func (b *builder) flow(prefix []seg, indent int, text string, st style, tail []seg) (first, last int) {
	pw := segsWidth(prefix)
	if pw > b.w-8 && len(prefix) > 0 {
		first = b.add(prefix...)
		prefix, pw = nil, 0
	} else {
		first = len(b.rows)
	}
	src := b.lines
	b.lines++
	indent = min(indent, max(0, b.w/2))
	chunks := wrapCells(text, b.w-pw, b.w-indent)
	for i, c := range chunks {
		var segs []seg
		if i == 0 {
			segs = append(append(segs, prefix...), seg{c, st})
		} else {
			if indent > 0 {
				segs = append(segs, seg{strings.Repeat(" ", indent), sText})
			}
			segs = append(segs, seg{c, st})
		}
		b.rows = append(b.rows, row{segs: segs, cont: i > 0, src: src})
	}
	last = len(b.rows) - 1
	if len(tail) > 0 {
		if segsWidth(b.rows[last].segs)+segsWidth(tail) <= b.w {
			b.rows[last].segs = append(b.rows[last].segs, tail...)
		} else {
			segs := append([]seg{{strings.Repeat(" ", indent), sText}}, tail...)
			b.rows = append(b.rows, row{segs: segs, cont: true, src: src})
			last++
		}
	}
	return first, last
}

// mark sets item on rows first to last, and ends on the last.
func (b *builder) mark(first, last, item int, ends endMask) {
	for i := first; i <= last && i < len(b.rows); i++ {
		b.rows[i].item = item
	}
	b.rows[last].ends |= ends
}

// drawer writes cells of the view's region, by their place in the region,
// and nothing outside it.
type drawer struct {
	dst  Canvas
	area uv.Rectangle
	col  Colors
	// cell is the cell handed to the canvas, which copies it.
	cell uv.Cell
}

func (d *drawer) style(s style) uv.Style {
	st := uv.Style{Fg: d.col.Fg, Bg: d.col.Bg}
	switch s {
	case sDim:
		st.Fg = d.col.Dim
	case sHead:
		st.Fg, st.Attrs = d.col.Accent, uv.AttrBold
	case sWarn:
		st.Fg = d.col.Warn
	case sAccent:
		st.Fg = d.col.Accent
	}
	return st
}

// set writes one cell. A cell that does not fit the region whole is left as
// it is.
func (d *drawer) set(x, y int, content string, w int, st uv.Style) {
	if x < 0 || y < 0 || y >= d.area.Dy() || x+w > d.area.Dx() {
		return
	}
	d.cell = uv.Cell{Content: content, Width: w, Style: st}
	d.dst.SetCell(d.area.Min.X+x, d.area.Min.Y+y, &d.cell)
}

// fill blanks the whole region.
func (d *drawer) fill() {
	st := d.style(sText)
	for y := 0; y < d.area.Dy(); y++ {
		for x := 0; x < d.area.Dx(); x++ {
			d.set(x, y, " ", 1, st)
		}
	}
}

// put draws s from (x, y), at most maxw cells wide, and returns the width
// drawn. A text that does not fit ends in an ellipsis.
//
// It is the one place where text reaches the cells, and it trusts none of
// it: each character becomes a cell of its own through cellOf, so nothing a
// terminal would act on or hide is written, whoever wrote s. It reads no
// more of s than fits the width, and none past maxText bytes.
func (d *drawer) put(x, y, maxw int, s string, st uv.Style) int {
	if maxw <= 0 {
		return 0
	}
	type cell struct {
		content string
		w       int
	}
	var cells []cell
	total := 0
	for rest := clip(s); rest != "" && total <= maxw; {
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

// segs draws the pieces of a row from (x, y), at most maxw cells wide, and
// returns the width drawn.
func (d *drawer) segs(x, y, maxw int, ss []seg) int {
	used := 0
	for _, s := range ss {
		if used >= maxw {
			break
		}
		n := d.put(x+used, y, maxw-used, s.s, d.style(s.st))
		used += n
		if n < width(s.s) {
			break
		}
	}
	return used
}

// rule draws a dim line across row y.
func (d *drawer) rule(y int) {
	st := d.style(sDim)
	for x := 0; x < d.area.Dx(); x++ {
		d.set(x, y, "─", 1, st)
	}
}

// marginMark is drawn in the margin of a row that goes on from the row
// above. It is one cell wide under every way of measuring.
const marginMark = "↪"

// cursorMark is drawn in the margin of the row under the cursor.
const cursorMark = ">"

// bodyRow draws one row of a scrolling area at row y: a margin cell, and the
// row from the second cell on.
func (d *drawer) bodyRow(y int, r row, cursor bool) {
	switch {
	case cursor:
		d.set(0, y, cursorMark, 1, d.style(sHead))
	case r.cont:
		d.set(0, y, marginMark, 1, d.style(sDim))
	}
	d.segs(1, y, d.area.Dx()-1, r.segs)
}

// putWrapped draws s from (x, y) over at most rows rows, each at most maxw
// cells wide, breaking after a space where it can. What does not fit the
// rows ends in an ellipsis.
func (d *drawer) putWrapped(x, y, maxw, rows int, s string, st uv.Style) {
	if maxw <= 0 || rows <= 0 {
		return
	}
	chunks := wrapCells(s, maxw, maxw)
	for i, c := range chunks {
		if i >= rows {
			break
		}
		if i == rows-1 && len(chunks) > rows {
			c += "…"
		}
		d.put(x, y+i, maxw, strings.TrimRight(c, " "), st)
	}
}
