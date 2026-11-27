package leadctl

import (
	"strconv"
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Canvas is what the view draws into: a *uv.Buffer, or any uv.Screen such as
// a uv.ScreenBuffer.
type Canvas interface {
	Bounds() uv.Rectangle
	SetCell(x, y int, c *uv.Cell)
}

type style int

const (
	sText style = iota
	sDim
	sHead
	sWarn
	sAccent
)

// cellWidth returns how many cells r takes, one or two, and 0 for a rune the
// view does not draw as itself.
func cellWidth(r rune) int {
	if r >= 0x20 && r < 0x7f {
		return 1
	}
	c := string(r)
	w := ansi.StringWidth(c)
	if w < 1 || w > 2 || ansi.WcWidth.StringWidth(c) != w {
		return 0
	}
	return w
}

// The fixed rows of the view: three at the top and, when the window is tall
// enough, five at the bottom (the rule, two rows of key hints, the prompt and
// the receipt).
const (
	headRows  = 4
	footRows  = 5
	minFooter = headRows + footRows + 2
)

// tableRows is the number of rows of the table that scroll.
func (m *Model) tableRows() int {
	if m.h < minFooter {
		return max(0, m.h-headRows-1)
	}
	return m.h - headRows - footRows
}

// drawer writes cells of the view's region, by their place in the region,
// and nothing outside it.
type drawer struct {
	dst  Canvas
	area uv.Rectangle
	col  Colors
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

func (d *drawer) fill() {
	st := d.style(sText)
	for y := 0; y < d.area.Dy(); y++ {
		for x := 0; x < d.area.Dx(); x++ {
			d.set(x, y, " ", 1, st)
		}
	}
}

// put draws s from (x, y), at most maxw cells wide, and returns the width
// drawn. A text that does not fit ends in an ellipsis. It is the one place
// where text reaches the cells: each character becomes a cell of its own, and
// one that cellWidth does not accept is drawn as a question mark.
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
	for rest := s; rest != "" && total <= maxw && len(cells) <= 4*maxw; {
		r, size := utf8.DecodeRuneInString(rest)
		rest = rest[size:]
		w := cellWidth(r)
		if w == 0 {
			cells = append(cells, cell{"?", 1})
			total++
			continue
		}
		cells = append(cells, cell{string(r), w})
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

func (d *drawer) rule(y int) {
	st := d.style(sDim)
	for x := 0; x < d.area.Dx(); x++ {
		d.set(x, y, "─", 1, st)
	}
}

// pad cuts or pads s to n bytes. It is used on ASCII text only.
func pad(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

// Draw draws the view into the region of the canvas.
func (m *Model) Draw(dst Canvas, area uv.Rectangle) {
	d := &drawer{dst: dst, area: area.Intersect(dst.Bounds()), col: m.o.Colors}
	d.fill()
	w, h := d.area.Dx(), d.area.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	head := title
	switch {
	case !m.have && !m.listErr:
		head += "  (" + readingLine + ")"
	case m.have:
		head += "  " + strconv.Itoa(m.running) + " running of " + strconv.Itoa(m.capacity)
	}
	d.put(1, 0, w-1, head, d.style(sHead))
	if m.listErr {
		d.put(1, 1, w-1, noListLine+": the rows are the earlier ones", d.style(sWarn))
	} else if m.leftOut > 0 {
		d.put(1, 1, w-1, itoa(uint64(m.leftOut))+leftOutSuffix, d.style(sWarn))
	} else if m.have && len(m.leads) == 0 {
		d.put(1, 1, w-1, noneLine, d.style(sDim))
	} else {
		d.put(1, 1, w-1, "Each action is a command with a receipt. A lost answer is never sent again as a new action.", d.style(sDim))
	}
	d.rule(2)
	m.drawTable(d, w, h)
	m.drawFooter(d, w, h)
}

// column widths of the table.
const (
	colState = 11
	colEpoch = 7
	colMsg   = 21
	colLast  = 28
)

func (l Lead) label() string {
	if l.HomeID != "" {
		return safeID(l.HomeID, maxRole) + "/" + l.Key
	}
	return l.Key
}

func (m *Model) drawTable(d *drawer, w, h int) {
	keyW := 4
	for _, l := range m.leads {
		keyW = max(keyW, len(l.label()))
	}
	keyW = min(keyW, 24)
	hdr := pad("KEY", keyW+2) + pad("STATE", colState) + pad("EPOCH", colEpoch) + pad("MSG", colMsg) + pad("LAST RECEIPT", colLast) + "UNIT"
	d.put(3, 3, w-3, hdr, d.style(sDim))
	vh := m.tableRows()
	for y := 0; y < vh; y++ {
		i := m.top + y
		if i >= len(m.leads) {
			break
		}
		l := m.leads[i]
		row := 4 + y
		if i == m.cursor {
			d.set(1, row, ">", 1, d.style(sHead))
		}
		epoch := "-"
		if l.Epoch != 0 {
			epoch = itoa(l.Epoch)
		}
		state := l.State
		if state == "" {
			state = "-"
		}
		if l.Offline {
			state = "stale"
		} else if _, busy := m.busy[l.Role]; busy && l.HomeID == "" {
			state += "*"
		}
		x := 3
		x += d.put(x, row, w-x, pad(l.label(), keyW)+"  ", d.style(sText))
		x += d.put(x, row, w-x, pad(state, colState), d.style(sText))
		x += d.put(x, row, w-x, pad(epoch, colEpoch), d.style(sText))
		x += d.put(x, row, w-x, pad(msgWord(l, m.deliveryOf(l)), colMsg), d.style(sText))
		last, st := "-", sDim
		if l.Last != nil {
			last = opShort(l.Last.Op) + ": " + shortStatus(l.Last.Status)
			st = lastStyle(l.Last.Status)
		}
		x += d.put(x, row, w-x, pad(last, colLast), d.style(st))
		d.put(x, row, w-x, l.Unit, d.style(sDim))
	}
}

func opShort(o Op) string {
	switch o {
	case OpDefine, OpStart, OpStop, OpTicket:
		return string(o)
	case OpInject:
		return "message"
	}
	return "command"
}

func lastStyle(s Status) style {
	switch s {
	case Refused, Unavailable, Uncertain:
		return sWarn
	}
	return sText
}

func (m *Model) drawFooter(d *drawer, w, h int) {
	prompt, pst := m.promptLine()
	if h < minFooter {
		// Too short for the footer: one line says what matters most.
		if h > headRows {
			if m.edit != nil {
				m.drawEditor(d, h-1, w)
				return
			}
			line, st := prompt, pst
			if line == "" {
				line, st = m.receiptText(), sText
			}
			d.put(1, h-1, w-1, line, d.style(st))
		}
		return
	}
	d.rule(h - 5)
	hint1, hint2 := "[j/k] move  [r] read again  [esc] back", "[d] define  [s] start  [x] stop  [m] message  [w] attach as writer  [v] attach as viewer"
	if l, ok := m.under(); !ok || l.Offline || l.HomeID != "" {
		hint2 = ""
	}
	switch {
	case m.edit != nil:
		hint1, hint2 = "[enter] send once  [esc] drop it  [ctrl+u] clear", strconv.Itoa(len(m.edit.text))+" of "+strconv.Itoa(maxBody)+" bytes"
	case m.confirm != nil:
		hint1, hint2 = "[y] stop the lead  [any other key] keep it running", ""
	}
	d.put(1, h-4, w-1, hint1, d.style(sDim))
	d.put(1, h-3, w-1, hint2, d.style(sDim))
	last, lst := m.receiptText(), sText
	if m.status != nil {
		lst = lastStyle(m.status.Status)
	}
	if m.edit != nil {
		m.drawEditor(d, h-2, w)
		if prompt != "" {
			last, lst = prompt, pst
		}
	} else if prompt != "" {
		d.put(1, h-2, w-1, prompt, d.style(pst))
	} else if l, ok := m.under(); ok {
		d.put(1, h-2, w-1, stateLine(l), d.style(sDim))
	}
	d.put(1, h-1, w-1, last, d.style(lst))
}

// promptLine is the line of the confirmation or of the last note.
func (m *Model) promptLine() (string, style) {
	switch {
	case m.confirm != nil:
		which := "epoch " + itoa(m.confirm.Epoch)
		if m.confirm.Epoch == 0 {
			which = "the current epoch"
		}
		return "Stop " + m.confirm.Key + " (" + which + ")?", sWarn
	case m.note != "":
		return m.note, sWarn
	}
	return "", sText
}

// receiptText is the receipt line: the receipt of the last command, in the
// view's words, or nothing.
func (m *Model) receiptText() string {
	if m.status == nil {
		return ""
	}
	line := receiptLine(*m.status, m.heldFor(*m.status))
	if m.status.Status == Refused && m.status.Code == "max-running" {
		switch m.unknownLeads() {
		case unknownSettling:
			line += maxRunningUnknownTail
		case unknownOther:
			line += maxRunningPlainTail
		}
	}
	return line
}

// What the list shows of the leads that are unknown, which count against the
// limit of running leads.
type unknownKind int

const (
	// unknownNone: no lead is unknown.
	unknownNone unknownKind = iota
	// unknownSettling: every unknown lead is one the daemon settles by itself.
	unknownSettling
	// unknownOther: an unknown lead is unknown for another reason, so that
	// no promise of a self-settle holds for every slot.
	unknownOther
)

// unknownLeads says which kind of unknown lead the list shows.
func (m *Model) unknownLeads() unknownKind {
	k := unknownNone
	for _, l := range m.leads {
		switch {
		case l.State != "unknown":
		case !settlesItself(l):
			return unknownOther
		default:
			k = unknownSettling
		}
	}
	return k
}

// heldFor is what the list says of the epoch's message for a receipt: the row
// of the lead the command was for, once the list has been read since the
// answer (the list that was drawn before it says nothing of this message), and
// when the row is still at the epoch the receipt names (a receipt that names
// none takes the row's). A row at another epoch than the receipt's, or with
// none, says nothing of the receipt's message: the lead has moved on.
func (m *Model) heldFor(r Receipt) held {
	if !m.statusListed {
		return heldUnknown
	}
	for _, l := range m.leads {
		id := l.identity()
		id.Epoch = m.statusTarget.Epoch
		if l.Offline || id != m.statusTarget {
			continue
		}
		switch {
		case r.Epoch != 0 && l.Epoch != r.Epoch:
			return heldMoved
		case l.Epoch == 0:
			return heldUnknown
		case l.MessageSpent:
			return heldSpent
		}
		return heldFree
	}
	return heldUnknown
}

// drawEditor draws the message line: the target and the end of the text,
// with a mark where the next character goes.
func (m *Model) drawEditor(d *drawer, y, w int) {
	e := m.edit
	label := "message to " + e.Key + " (epoch " + itoa(e.Epoch) + ")> "
	x := 1 + d.put(1, y, w-1, label, d.style(sAccent))
	room := w - x - 1
	if room <= 0 {
		return
	}
	// Keep the end of the text that fits.
	rs := []rune(e.text)
	used, from := 0, len(rs)
	for from > 0 {
		cw := cellWidth(rs[from-1])
		if used+cw > room-1 {
			break
		}
		used += cw
		from--
	}
	shown := string(rs[from:])
	if from > 0 {
		x += d.put(x, y, 1, "…", d.style(sDim))
	}
	x += d.put(x, y, w-x, shown, d.style(sText))
	d.put(x, y, w-x, "_", d.style(sAccent))
}
