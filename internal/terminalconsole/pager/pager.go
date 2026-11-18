package pager

import (
	"errors"
	"image/color"

	tea "charm.land/bubbletea/v2"
)

// Colors are the colours the pager draws with. The parent fills them from
// its theme, so that the pager has no palette of its own. A nil colour is
// the terminal's default.
type Colors struct {
	// Fg and Bg are the colours of the patch's text and of the region.
	Fg, Bg color.Color
	// Dim is the colour of the pager's own words and of the wrap mark.
	Dim color.Color
	// Warn is the colour of the line that says why the reading ended.
	Warn color.Color
}

// Options configure a Model.
type Options struct {
	Colors Colors
	// GraphemeWidths says that the outer terminal measures whole grapheme
	// clusters (mode 2027). See SetGraphemeWidths.
	GraphemeWidths bool
}

// Action is what the parent is asked to do after an Update.
type Action int

const (
	// None asks for nothing.
	None Action = iota
	// Close asks the parent to close the pager: Esc was pressed. The pager
	// itself changes nothing and can still be drawn.
	Close
)

// The lines the pager shows when the reading ended early. They are its own
// words: the text of the source's error is never drawn.
const (
	failChanged   = "reading ended: the review changed"
	failPage      = "reading ended: a page could not be read"
	failMalformed = "reading ended: the pages do not fit together"
	failTooLong   = "reading ended: a line is too long to show"
)

// Model is the pager. Make one with New, hand it every message the parent
// receives while the pager is open (Update), and draw it with Draw after
// SetSize. A Model is used from one goroutine, as Bubble Tea uses a model.
type Model struct {
	src       Source
	colors    Colors
	graphemes bool
	w, h      int

	// held are the pages lo, lo+1, ... as the source returned them. pages
	// is the number of pages, or 0 until the first page was read.
	held  []page
	lo    int
	pages int
	// cache holds the joined and escaped lines that were looked at, by
	// their first piece.
	cache map[ref]*line

	// top is the row at the top of the screen. It is set once ready is.
	top   pos
	ready bool

	// seq numbers the page requests; waiting says that the answer to
	// request seq has not come yet. An answer with another number is from a
	// request the pager gave up.
	seq     int
	waiting bool

	reached bool
	fail    string
	err     error
}

// pageMsg is the answer to one page request.
type pageMsg struct {
	owner *Model
	seq   int
	n     int
	lines []string
	cont  []bool
	pages int
	err   error
}

// New returns a pager that reads from src. It has read nothing yet: Init
// returns the command that reads the first page.
func New(src Source, o Options) *Model {
	return &Model{src: src, colors: o.Colors, graphemes: o.GraphemeWidths, cache: map[ref]*line{}}
}

// Init returns the command that reads the first page.
func (m *Model) Init() tea.Cmd { return m.settle() }

// Reached reports whether the end was reached: the last piece of the last
// page was read and the last row of the patch was on the screen. It stays
// true when the reader scrolls back up, and it is false, for good, once the
// reading ended early.
func (m *Model) Reached() bool { return m.reached && m.fail == "" }

// Err returns the error that ended the reading, or nil. It is for the
// parent; the pager draws its own line and never the error's text. An
// error that the pager made itself (a page that does not fit) is returned
// as well.
func (m *Model) Err() error { return m.err }

// SetGraphemeWidths tells the pager whether the outer terminal measures
// whole grapheme clusters (mode 2027). Without it the pager gives each code
// point the cells that such a terminal gives it, so that the columns stay
// where the pager put them. The returned command is to be run, as Update's.
func (m *Model) SetGraphemeWidths(on bool) tea.Cmd {
	if on == m.graphemes {
		return nil
	}
	m.graphemes = on
	return m.rewrap()
}

// SetSize tells the pager the size of the region it is drawn in. The
// returned command is to be run, as Update's: a larger region may need
// another page.
func (m *Model) SetSize(width, height int) tea.Cmd {
	width, height = max(0, width), max(0, height)
	if width == m.w && height == m.h {
		return nil
	}
	m.w, m.h = width, height
	return m.rewrap()
}

// rewrap follows a change of the width or of the way text is measured. The
// top of the screen stays on the same text.
func (m *Model) rewrap() tea.Cmd {
	for _, ln := range m.cache {
		ln.starts, ln.rows = nil, 0
	}
	if !m.ready {
		return m.settle()
	}
	if ln, ok := m.lineAt(m.top.at); ok {
		w := m.wrapper()
		row, off := 0, 0
		for {
			end := w.rowEnd(ln.text, off)
			if end > m.top.off || end >= len(ln.text) {
				break
			}
			row, off = row+1, end
		}
		m.top.row, m.top.off = row, off
		m.fillScreen(false)
	}
	return m.settle()
}

// The region's rows are a header, the patch, and a footer. The patch has a
// margin of marginCells cells on the left, which holds the wrap mark.
const marginCells = 2

// rows is the number of rows the patch is shown in, 0 when the region is
// too small to show it.
func (m *Model) rows() int {
	if m.h < 3 || m.w-marginCells < 2 {
		return 0
	}
	return m.h - 2
}

func (m *Model) wrapper() wrapper {
	return wrapper{width: max(2, m.w-marginCells), graphemes: m.graphemes}
}

// lockMods are lock states a terminal may report with a key. They never
// change which key it is.
const lockMods = tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock

// Update handles a message. The pager acts on the answers to its own page
// requests and on its keys: j and k and the arrows scroll by a row, PgDn
// and PgUp by a screen, Home goes to the top, End to the end of what is
// read so far (and reads on), and Esc returns Close. Every other message
// and key is left alone. The command, when not nil, is to be run by the
// parent; it never ends the program.
func (m *Model) Update(msg tea.Msg) (tea.Cmd, Action) {
	switch v := msg.(type) {
	case pageMsg:
		m.onPage(v)
		return m.settle(), None
	case tea.KeyPressMsg:
		v.Mod &^= lockMods
		key := v.String()
		if key == "esc" {
			return nil, Close
		}
		if !m.ready || m.rows() == 0 {
			return nil, None
		}
		switch key {
		case "j", "down":
			m.down(1)
		case "k", "up":
			m.up(1)
		case "pgdown":
			m.down(max(1, m.rows()-1))
		case "pgup":
			m.up(max(1, m.rows()-1))
		case "home":
			m.home()
		case "end":
			if last, ok := m.lastRow(); ok {
				m.top = last
				m.fillScreen(true)
			}
		default:
			return nil, None
		}
		return m.settle(), None
	}
	return nil, None
}

// down scrolls n rows down, as far as rows that are read lie below the
// screen.
func (m *Model) down(n int) {
	bottom := m.top
	for k := 1; k < m.rows(); k++ {
		b, st := m.next(bottom)
		if st != found {
			return
		}
		bottom = b
	}
	for ; n > 0; n-- {
		b, st := m.next(bottom)
		if st != found {
			return
		}
		t, st := m.next(m.top)
		if st != found {
			return
		}
		bottom, m.top = b, t
	}
}

// up scrolls n rows up, as far as rows that are held lie above the screen.
func (m *Model) up(n int) {
	for ; n > 0; n-- {
		t, st := m.prev(m.top)
		if st != found {
			return
		}
		m.top = t
	}
}

// home goes to the first row. When the first page was let go, the pager
// lets go of everything and reads from the first page again.
func (m *Model) home() {
	if m.lo == 0 {
		if r := m.norm(ref{}); r.p < m.hi() {
			if _, ok := m.lineAt(r); ok {
				m.top = pos{at: r}
			}
		}
		return
	}
	if m.fail != "" {
		return
	}
	m.held, m.lo, m.ready, m.top = nil, 0, false, pos{}
	clear(m.cache)
	m.seq++
	m.waiting = false
}

// fillScreen moves the top up when fewer rows than the screen has lie from
// it on, so that the screen is full. With always unset it does so only at
// the end of the patch, not where the rows below are merely not read yet.
func (m *Model) fillScreen(always bool) {
	n, c := 1, m.top
	for n < m.rows() {
		b, st := m.next(c)
		if st != found {
			if st != edge && !always {
				return
			}
			break
		}
		c, n = b, n+1
	}
	for ; n < m.rows(); n++ {
		t, st := m.prev(m.top)
		if st != found {
			return
		}
		m.top = t
	}
}

// onPage takes in the answer to a page request.
func (m *Model) onPage(v pageMsg) {
	if v.owner != m || v.seq != m.seq || !m.waiting {
		return
	}
	m.waiting = false
	if m.fail != "" {
		return
	}
	if v.err != nil {
		m.err = v.err
		m.fail = failPage
		if errors.Is(v.err, ErrReviewChanged) {
			m.fail = failChanged
		}
		return
	}
	last := len(v.lines) - 1
	if v.pages < 1 || v.n >= v.pages || len(v.cont) != len(v.lines) ||
		(m.pages != 0 && v.pages != m.pages) ||
		(v.n == v.pages-1 && last >= 0 && v.cont[last]) {
		m.malformed()
		return
	}
	pg := page{lines: append([]string(nil), v.lines...), cont: append([]bool(nil), v.cont...)}
	switch {
	case v.n == m.hi():
		m.held = append(m.held, pg)
	case v.n == m.lo-1:
		m.held = append([]page{pg}, m.held...)
		m.lo--
	default:
		// The pages around it were let go while it was read.
		return
	}
	m.pages = v.pages
	if m.hi() == m.pages {
		// The patch may not end in the middle of a line.
		if b, st := m.back(ref{m.hi(), 0}); st == found && m.page(b.p).cont[b.i] {
			m.malformed()
			return
		}
	}
	if m.tooLong() {
		m.fail, m.err = failTooLong, errTooLong
	}
}

var (
	errMalformed = errors.New("pager: the source returned a page that does not fit the others")
	errTooLong   = errors.New("pager: a line is longer than MaxLineBytes")
)

func (m *Model) malformed() { m.fail, m.err = failMalformed, errMalformed }

// empty reports whether every page was read and none holds a piece.
func (m *Model) empty() bool {
	return m.pages > 0 && m.lo == 0 && m.hi() == m.pages && m.norm(ref{}).p >= m.hi()
}

// settle runs after every change. It finds the first row once it is read,
// lets go of the pages that are far from the screen, notes whether the end
// is on the screen, and returns the command that reads the page the pager
// needs next, if it needs one and is not waiting for one.
func (m *Model) settle() tea.Cmd {
	want := -1
	switch {
	case m.pages == 0:
		want = 0
	case !m.ready:
		if r := m.norm(ref{}); m.lo == 0 && r.p < m.hi() {
			if _, ok := m.lineAt(r); ok {
				m.top, m.ready = pos{at: r}, true
			}
		}
		if !m.ready && m.hi() < m.pages {
			want = m.hi()
		}
	}
	switch {
	case m.fail != "":
		return nil
	case m.ready:
		s := m.look()
		// Keep a page on either side of what was looked at. Where the look
		// ran into a page that is not held, keep all there is on that side:
		// it may be the first pieces of a line that is not whole yet.
		from, to := s.low-1, s.high+2
		if s.needBack {
			from = m.lo
		}
		if s.needFwd {
			to = m.hi()
		}
		m.keep(from, to)
		if s.atEnd {
			m.reached = true
		}
		switch {
		case s.needNow:
			want = m.hi()
		case s.needBack:
			want = m.lo - 1
		case s.needFwd:
			want = m.hi()
		}
	case m.empty() && m.rows() > 0:
		m.reached = true
	}
	if want < 0 || m.waiting {
		return nil
	}
	m.seq++
	m.waiting = true
	src, owner, seq := m.src, m, m.seq
	return func() tea.Msg {
		lines, cont, pages, err := src.Page(want)
		return pageMsg{owner: owner, seq: seq, n: want, lines: lines, cont: cont, pages: pages, err: err}
	}
}
