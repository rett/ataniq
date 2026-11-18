package pager

import (
	"strings"

	"ataniq/internal/exchange/leadtext"
)

// page is one page as the source returned it: raw pieces, not yet escaped.
type page struct {
	lines []string
	cont  []bool
}

// ref names a piece: piece i of page p. A ref whose page is the first page
// not held stands for "after everything held".
type ref struct{ p, i int }

// every is how many rows lie between two remembered row starts of a line.
const every = 32

// line is one whole line of the patch: its pieces joined and escaped.
type line struct {
	text string
	// next is the piece after the line's last piece, last the page of that
	// last piece.
	next ref
	last int
	// starts[k] is the offset in text of row k*every, for the width in use.
	// It is filled as far as rows were wrapped. rows is the number of rows,
	// or 0 while it is not known.
	starts []int
	rows   int
}

// pos is a row: row number row of the line that starts at the piece at,
// which begins at offset off of the line's text.
type pos struct {
	at       ref
	row, off int
}

// status is what a step from one row to the next found.
type status int

const (
	// found: the row is there.
	found status = iota
	// edge: there is no such row, the patch begins or ends here.
	edge
	// missing: the row is on a page that is not held.
	missing
)

// hi is the first page after the held ones.
func (m *Model) hi() int { return m.lo + len(m.held) }

// heldToEnd reports whether no piece lies after the held pages: they go up
// to the last page, or up to the pages at the end that hold no piece.
func (m *Model) heldToEnd() bool {
	return m.pages > 0 && (m.hi() == m.pages || (m.tailKnown && m.hi() >= m.tail))
}

func (m *Model) page(p int) *page { return &m.held[p-m.lo] }

// norm moves a ref that points after the last piece of its page to the
// first piece of the next page that has one.
func (m *Model) norm(r ref) ref {
	for r.p < m.hi() && r.i >= len(m.page(r.p).lines) {
		r = ref{r.p + 1, 0}
	}
	return r
}

// back returns the piece before r among the held pages.
func (m *Model) back(r ref) (ref, status) {
	for {
		if r.i > 0 {
			return ref{r.p, r.i - 1}, found
		}
		if r.p <= m.lo {
			if m.lo == 0 {
				return r, edge
			}
			return r, missing
		}
		r.p--
		r.i = len(m.page(r.p).lines)
	}
}

// lineAt returns the line whose first piece is r. It is false when the
// line's last piece is on a page that is not held. This is the one place
// where the lead's bytes become text that may be drawn: the pieces are
// joined, then escaped, once.
func (m *Model) lineAt(r ref) (*line, bool) {
	if ln, ok := m.cache[r]; ok {
		return ln, true
	}
	// Find the line's last piece first, so that a line that is not whole
	// yet costs a look at its pieces and nothing more.
	p, i, pieces, size := r.p, r.i, 0, 0
	for {
		if p >= m.hi() {
			return nil, false
		}
		pg := m.page(p)
		if i >= len(pg.lines) {
			p, i = p+1, 0
			continue
		}
		pieces, size = pieces+1, size+len(pg.lines[i])
		if !pg.cont[i] {
			break
		}
		i++
	}
	raw := m.page(r.p).lines[r.i]
	if pieces > 1 {
		var b strings.Builder
		b.Grow(size)
		for q, j := r.p, r.i; ; {
			pg := m.page(q)
			if j >= len(pg.lines) {
				q, j = q+1, 0
				continue
			}
			b.WriteString(pg.lines[j])
			if q == p && j == i {
				break
			}
			j++
		}
		raw = b.String()
	}
	ln := &line{text: leadtext.Escape(raw), next: ref{p, i + 1}, last: p}
	if len(m.cache) >= m.cacheRoom() {
		clear(m.cache)
	}
	m.cache[r] = ln
	return ln, true
}

// cacheRoom is how many joined lines are kept: enough for the rows the
// pager looks at around the screen. When the cache is full it is emptied
// and filled again from the held pages.
func (m *Model) cacheRoom() int { return 64 + 4*m.rows() }

// prevStart returns the first piece of the line before the one that starts
// at r.
func (m *Model) prevStart(r ref) (ref, status) {
	e, st := m.back(r)
	if st != found {
		return r, st
	}
	for {
		b, st := m.back(e)
		if st == missing {
			// Whether e begins a line is known only with the page before.
			return r, missing
		}
		if st == edge || !m.page(b.p).cont[b.i] {
			return e, found
		}
		e = b
	}
}

// extend wraps ln on from its last remembered row start until the start for
// row r is remembered or the line has ended.
func (m *Model) extend(ln *line, r int) {
	if len(ln.starts) == 0 {
		ln.starts = []int{0}
	}
	w := m.wrapper()
	for ln.rows == 0 && len(ln.starts) <= r/every {
		off := ln.starts[len(ln.starts)-1]
		n := (len(ln.starts) - 1) * every
		for j := 0; j < every; j++ {
			off = w.rowEnd(ln.text, off)
			n++
			if off >= len(ln.text) {
				ln.rows = n
				return
			}
		}
		ln.starts = append(ln.starts, off)
	}
}

// rowCount returns the number of rows ln takes. An empty line takes one.
func (m *Model) rowCount(ln *line) int {
	for ln.rows == 0 {
		m.extend(ln, (len(ln.starts)+1)*every)
	}
	return ln.rows
}

// rowStart returns the offset in ln's text where row r begins. r is a row
// of the line.
func (m *Model) rowStart(ln *line, r int) int {
	m.extend(ln, r)
	k := min(r/every, len(ln.starts)-1)
	off := ln.starts[k]
	w := m.wrapper()
	for j := k * every; j < r; j++ {
		off = w.rowEnd(ln.text, off)
	}
	return off
}

// next returns the row after c.
func (m *Model) next(c pos) (pos, status) {
	ln, ok := m.lineAt(c.at)
	if !ok {
		return c, missing
	}
	if end := m.wrapper().rowEnd(ln.text, c.off); end < len(ln.text) {
		return pos{c.at, c.row + 1, end}, found
	}
	n := m.norm(ln.next)
	if n.p >= m.hi() {
		if m.heldToEnd() {
			return c, edge
		}
		return c, missing
	}
	if _, ok := m.lineAt(n); !ok {
		return c, missing
	}
	return pos{at: n}, found
}

// prev returns the row before c.
func (m *Model) prev(c pos) (pos, status) {
	if c.row > 0 {
		ln, ok := m.lineAt(c.at)
		if !ok {
			return c, missing
		}
		return pos{c.at, c.row - 1, m.rowStart(ln, c.row-1)}, found
	}
	s, st := m.prevStart(c.at)
	if st != found {
		return c, st
	}
	ln, ok := m.lineAt(s)
	if !ok {
		return c, missing
	}
	n := m.rowCount(ln)
	return pos{s, n - 1, m.rowStart(ln, n-1)}, found
}

// lastRow returns the last row of the last whole line among the held pages.
func (m *Model) lastRow() (pos, bool) {
	r := ref{m.hi(), 0}
	for {
		b, st := m.back(r)
		if st != found {
			return pos{}, false
		}
		if !m.page(b.p).cont[b.i] {
			break
		}
		r = b
	}
	s, st := m.prevStart(r)
	if st != found {
		return pos{}, false
	}
	ln, ok := m.lineAt(s)
	if !ok {
		return pos{}, false
	}
	n := m.rowCount(ln)
	return pos{s, n - 1, m.rowStart(ln, n-1)}, true
}

// span is what the pager looks at around the screen: one screen of rows
// above the top row and two from it on.
type span struct {
	// low and high are the first and last page that these rows are on.
	low, high int
	// needBack and needFwd say that the rows above, or below, run into a
	// page that is not held; needNow that a row of the screen itself does.
	needBack, needFwd, needNow bool
	// atEnd says that the last row of the patch is on the screen.
	atEnd bool
}

func (m *Model) look() span {
	rows := m.rows()
	s := span{low: m.top.at.p, high: m.top.at.p}
	if ln, ok := m.lineAt(m.top.at); ok {
		s.high = ln.last
	}
	c := m.top
	for k := 0; k < rows; k++ {
		p, st := m.prev(c)
		if st != found {
			s.needBack = st == missing
			break
		}
		c = p
		s.low = min(s.low, c.at.p)
	}
	c = m.top
	for k := 1; k <= 2*rows; k++ {
		n, st := m.next(c)
		if st != found {
			s.needFwd = st == missing
			s.needNow = s.needFwd && k < rows
			s.atEnd = st == edge && k <= rows
			break
		}
		c = n
		if ln, ok := m.lineAt(c.at); ok {
			s.high = max(s.high, ln.last)
		}
	}
	return s
}

// keep lets go of the held pages outside [from, to) and of the joined lines
// that needed them.
func (m *Model) keep(from, to int) {
	from, to = max(from, m.lo), min(to, m.hi())
	if from >= to || (from == m.lo && to == m.hi()) {
		return
	}
	held := make([]page, to-from)
	copy(held, m.held[from-m.lo:to-m.lo])
	m.held, m.lo = held, from
	for r, ln := range m.cache {
		if r.p < from || ln.last >= to {
			delete(m.cache, r)
		}
	}
}

// tooLong reports whether a line among the held pages, whole or not yet
// whole, is longer than MaxLineBytes.
func (m *Model) tooLong() bool {
	n := 0
	for i := range m.held {
		pg := &m.held[i]
		for j, s := range pg.lines {
			n += len(s)
			if n > MaxLineBytes {
				return true
			}
			if !pg.cont[j] {
				n = 0
			}
		}
	}
	return false
}
