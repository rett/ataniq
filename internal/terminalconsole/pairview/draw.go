package pairview

import (
	"image/color"
	"strconv"
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Canvas is what the view draws into: a *uv.Buffer, or any uv.Screen.
type Canvas interface {
	Bounds() uv.Rectangle
	SetCell(x, y int, c *uv.Cell)
}

// The QR is black on white whatever the theme is, and its quiet zone is
// white: a light code on a dark ground is the inverse code, which many
// scanners do not read.
var (
	qrDark  color.Color = color.Black
	qrLight color.Color = color.White
)

type lineKind int

const (
	lText lineKind = iota
	lDim
	lHead
	lWarn
	lWords
)

type dline struct {
	s string
	k lineKind
}

type drawer struct {
	dst  Canvas
	area uv.Rectangle
	cell uv.Cell
}

func (d *drawer) set(x, y int, content string, width int, st uv.Style) {
	if x < 0 || y < 0 || y >= d.area.Dy() || x+width > d.area.Dx() {
		return
	}
	d.cell = uv.Cell{Content: content, Width: width, Style: st}
	d.dst.SetCell(d.area.Min.X+x, d.area.Min.Y+y, &d.cell)
}

// text draws s from (x, y), at most maxw cells, an ellipsis where it is cut.
// It returns the cells used.
func (d *drawer) text(x, y, maxw int, s string, st uv.Style) int {
	if maxw <= 0 {
		return 0
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
		used++
	}
	return used
}

// qrSize returns the columns and rows the QR needs: its modules with the
// quiet zone, and two lines of text besides.
func qrSize(rows []string) (cols, lines int) {
	if len(rows) == 0 {
		return 0, 0
	}
	return utf8.RuneCountInString(rows[0]), len(rows) + 2
}

// secondsLeft returns the whole seconds until the code ends, at least zero.
func (m *Model) secondsLeft() int {
	if m.expires.IsZero() {
		return 0
	}
	d := m.expires.Sub(m.now())
	if d <= 0 {
		return 0
	}
	return int((d + 999_999_999) / 1_000_000_000)
}

// Draw draws the view into area of dst. It writes cells, never escape
// sequences, and every cell of area.
func (m *Model) Draw(dst Canvas, area uv.Rectangle) {
	area = area.Intersect(dst.Bounds())
	if area.Empty() {
		return
	}
	d := &drawer{dst: dst, area: area}
	base := uv.Style{Fg: m.o.Colors.Fg, Bg: m.o.Colors.Bg}
	for y := 0; y < area.Dy(); y++ {
		for x := 0; x < area.Dx(); x++ {
			d.set(x, y, " ", 1, base)
		}
	}
	w, h := min(m.w, area.Dx()), min(m.h, area.Dy())
	if w <= 0 || h <= 0 {
		return
	}
	if m.origin == "" {
		m.drawLines(d, w, h, []dline{{title, lHead}, {"", lText}, {noOrigin, lWarn}, {"", lText}, {"Esc: close", lDim}})
		return
	}
	if m.screen == scWaiting {
		m.drawWaiting(d, w, h)
		return
	}
	m.drawLines(d, w, h, m.textLines(w-2))
}

func (m *Model) style(k lineKind) uv.Style {
	c := m.o.Colors
	st := uv.Style{Fg: c.Fg, Bg: c.Bg}
	switch k {
	case lDim:
		st.Fg = c.Dim
	case lHead:
		st.Fg, st.Attrs = c.Accent, uv.AttrBold
	case lWarn:
		st.Fg = c.Warn
	case lWords:
		st.Fg, st.Attrs = c.Accent, uv.AttrBold
	}
	return st
}

// drawLines draws the lines from the top, one cell in from the left.
func (m *Model) drawLines(d *drawer, w, h int, lines []dline) {
	x, room := 1, w-2
	if w < 4 {
		x, room = 0, w
	}
	for y, l := range lines {
		if y >= h {
			break
		}
		d.text(x, y, room, l.s, m.style(l.k))
	}
}

// drawWaiting draws the QR between two lines of text, centred, or says that
// the terminal is too small for it. It never draws a part of the code.
func (m *Model) drawWaiting(d *drawer, w, h int) {
	rows := m.rows
	cols, need := qrSize(rows)
	if w < cols || h < need {
		// The least quiet zone, when the full one does not fit.
		rows = m.rowsMin
		cols, need = qrSize(rows)
	}
	if w < cols || h < need {
		var ls []dline
		for _, l := range wrap("terminal too small: need "+strconv.Itoa(cols)+" x "+strconv.Itoa(need), w-2) {
			ls = append(ls, dline{l, lWarn})
		}
		ls = append(ls, dline{"", lText})
		for _, l := range wrap("Esc: close (the code stays valid until it expires)", w-2) {
			ls = append(ls, dline{l, lDim})
		}
		m.drawLines(d, w, h, ls)
		return
	}
	head := "Scan with your phone's camera. Expires in " + strconv.Itoa(m.secondsLeft()) + " s."
	m.centre(d, w, 0, head, lText)
	foot, fk := waitingHint, lDim
	if m.note != "" {
		foot, fk = m.note, lWarn
	}
	m.centre(d, w, h-1, foot, fk)
	st := uv.Style{Fg: qrDark, Bg: qrLight}
	x0 := (w - cols) / 2
	y0 := 1 + (h-2-len(rows))/2
	for i, row := range rows {
		x := 0
		for _, r := range row {
			d.set(x0+x, y0+i, string(r), 1, st)
			x++
		}
	}
}

// centre draws s centred in the first w cells of row y.
func (m *Model) centre(d *drawer, w, y int, s string, k lineKind) {
	sw := ansi.StringWidth(s)
	x := 0
	if sw < w {
		x = (w - sw) / 2
	}
	d.text(x, y, w-x, s, m.style(k))
}

// wrap breaks s at spaces into lines of at most w cells (a word longer than
// that is cut by the drawing).
func wrap(s string, w int) []string {
	if w < 1 || ansi.StringWidth(s) <= w {
		return []string{s}
	}
	var out []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case ansi.StringWidth(cur)+1+ansi.StringWidth(word) <= w:
			cur += " " + word
		default:
			out = append(out, cur)
			cur = word
		}
	}
	return append(out, cur)
}

func (m *Model) textLines(w int) []dline {
	var ls []dline
	add := func(k lineKind, s string) {
		for _, l := range wrap(s, w) {
			ls = append(ls, dline{l, k})
		}
	}
	blank := func() { add(lText, "") }
	add(lHead, title)
	blank()
	switch m.screen {
	case scIdle:
		add(lText, "Pair a phone so that it can approve cards. The console draws a QR code; the phone scans it, then the phone and this screen show the same eight words. You compare them and confirm here. Nothing is trusted until you do.")
		blank()
		if m.opening {
			add(lDim, checkingLine)
		}
		m.noteLine(&ls, w)
		blank()
		add(lDim, "Enter: start   Esc: close")
	case scStarting:
		add(lText, startingLine)
		blank()
		add(lDim, "Esc: close")
	case scOpen:
		add(lText, msgOpen)
		if m.expires.After(m.now()) {
			add(lText, "It ends in "+strconv.Itoa(m.secondsLeft())+" s.")
		}
		blank()
		m.noteLine(&ls, w)
		add(lDim, "Enter: new code (ends the open one)   Esc: close")
	case scPending, scConfirm, scConfirming, scRejecting:
		if (m.screen == scPending || m.screen == scConfirm) && m.wordsOK && !m.wordsFit() {
			add(lWarn, msgTooSmallWords)
			blank()
			add(lDim, keysTooSmall)
			return ls
		}
		switch m.screen {
		case scConfirming:
			ls = m.wordsScreen(w, false, false)
			add(lText, confirmingTxt)
		case scRejecting:
			ls = m.wordsScreen(w, false, false)
			add(lText, rejectingTxt)
		default:
			return m.wordsScreen(w, m.screen == scConfirm, true)
		}
	case scPaired:
		kind := kindUnverif
		if m.kind != "" && m.kind != "unverified" {
			kind = "Kind: " + m.kind + "."
		}
		add(lText, msgPaired+" "+kind)
		id := m.id
		if id == "" {
			id = "(none)"
		}
		add(lText, "Device id: "+id)
		blank()
		add(lDim, "Esc: close")
	case scExpired, scGone:
		msg := msgExpired
		if m.screen == scGone {
			msg = msgGone
		}
		add(lText, msg)
		blank()
		add(lDim, "Enter: new code   Esc: close")
	case scFailed:
		add(lWarn, m.note)
		blank()
		add(lDim, "Enter: try again   Esc: close")
	}
	return ls
}

// noteLine adds the note, if there is one: a refusal in the warning colour,
// the rejection's confirmation plainly.
func (m *Model) noteLine(ls *[]dline, w int) {
	if m.note == "" {
		return
	}
	k := lWarn
	if m.note == msgRejected {
		k = lText
	}
	for _, l := range wrap(m.note, w) {
		*ls = append(*ls, dline{l, k})
	}
}

// wordLines lays the numbered words out in four columns, or two, or one when
// the width does not hold more (the count divides by each), spaced apart, in
// rows of their own.
func (m *Model) wordLines(w int) []dline {
	const gap = 4
	cells := make([]string, WordCount)
	cw := 0
	for i, s := range m.words {
		cells[i] = strconv.Itoa(i+1) + " " + s
		cw = max(cw, ansi.StringWidth(cells[i]))
	}
	cols := 1
	for _, c := range []int{4, 2} {
		if c*cw+(c-1)*gap <= w {
			cols = c
			break
		}
	}
	var out []dline
	for r := 0; r < WordCount/cols; r++ {
		var b strings.Builder
		for c := 0; c < cols; c++ {
			cell := cells[r*cols+c]
			b.WriteString(cell)
			if c < cols-1 {
				b.WriteString(strings.Repeat(" ", cw-ansi.StringWidth(cell)+gap))
			}
		}
		out = append(out, dline{b.String(), lWords})
		if cols > 1 && r < WordCount/cols-1 {
			out = append(out, dline{"", lText})
		}
	}
	return out
}

// wordsScreen is the whole words screen: the title, the words and the
// owner's prompt, which is the second question when confirm is set. With keys
// unset the prompt is left out (the screens of a call in flight add their
// own line).
func (m *Model) wordsScreen(w int, confirm, keys bool) []dline {
	var ls []dline
	add := func(k lineKind, s string) {
		for _, l := range wrap(s, w) {
			ls = append(ls, dline{l, k})
		}
	}
	blank := func() { add(lText, "") }
	add(lHead, title)
	blank()
	add(lText, "The phone and this screen show eight words.")
	blank()
	ls = append(ls, m.wordLines(w)...)
	blank()
	if !keys {
		return ls
	}
	if confirm {
		add(lText, confirmLine1)
		add(lHead, confirmLine2)
		blank()
		add(lDim, "Esc: close")
		return ls
	}
	add(lText, helpWords)
	if m.expires.After(m.now()) {
		add(lDim, strconv.Itoa(m.secondsLeft())+" s left")
	}
	m.noteLine(&ls, w)
	blank()
	k := "c: confirm   x: reject   Esc: close"
	if !m.wordsOK {
		k = keysTooSmall
	}
	add(lDim, k)
	return ls
}

// wordsFit reports whether the whole words screen, with the prompt, fits the
// stored size with no line cut or wrapped off the bottom: the words in
// full, and both the pending screen and the second question. The owner may
// confirm only while it is true.
func (m *Model) wordsFit() bool {
	room := m.w - 2
	if m.w < 4 {
		room = m.w
	}
	if room < 1 {
		return false
	}
	fits := func(ls []dline) bool {
		if len(ls) > m.h {
			return false
		}
		for _, l := range ls {
			if ansi.StringWidth(l.s) > room {
				return false
			}
		}
		return true
	}
	return fits(m.wordsScreen(m.w-2, false, true)) && fits(m.wordsScreen(m.w-2, true, true))
}
