package sessionpane

import (
	"slices"
)

// The ring of recent output. A holder's pane keeps the main-screen rows that
// scrolled off, for the whole epoch, so that a console that attaches later
// can be given the rows it missed. It is not the local paging of an attach
// client (Options.RecentOutput, at most RecentRowLimit rows kept as cells):
// the ring is bounded in rows and in bytes, keeps its rows encoded, and
// records an interval of rows that scrolled while the sensitive switch was
// on as a count with no content.
//
// How rows reach the ring. The emulator keeps a scrollback of its own as the
// window rows pass through: after every slice of output (and every resize)
// the pane takes the rows out of it, encodes them, appends them to the ring
// and empties the window, which also releases its memory. The window holds
// one row more than the ring does, so that a slice that scrolls more rows
// than the ring keeps is seen: the pane then knows that the count of rows it
// has seen is a lower bound (Recent.Approximate) and keeps the newest rows,
// which is what the ring would hold anyway. Rows of the alternate screen
// never reach the window: the patched x/vt keeps no scrollback for that
// screen (its notice, patch 3, explains the heap growth that the stock
// module had), so the ring gets none and the window is the only scrollback
// the pane asks the emulator for.
//
// What is not seen. A session that erases the scrollback (CSI 3 J) in the
// same slice that scrolled rows takes those rows out of the window before the
// pane reads it: they are in no ring, and no count. Erasing the scrollback
// does not empty the ring, which is a record of what the session wrote and
// not a display.

// DefaultRecentRows is the provisional bound of the holder's ring, in rows,
// to be settled at the attended rehearsal from heap use during scrolling and
// the size and latency of a full reattach. The holder takes it unless its
// configuration says another.
const DefaultRecentRows = 2000

// MaxRecentRows is the largest ring a pane keeps, whatever the option says.
const MaxRecentRows = 16384

// DefaultRecentBytes is the byte budget of a ring whose options give none:
// the encoded rows, and a fixed charge for each entry. Rows are evicted from
// the oldest when either bound is exceeded.
const DefaultRecentBytes = 16 << 20

// entryCharge is what each ring entry costs beyond its bytes: the slice
// header and the count.
const entryCharge = 32

// RecentEntry is one entry of the ring: one row, or an interval of rows that
// is recorded with no content. Exactly one of Row and Omitted is set.
type RecentEntry struct {
	// Row is the encoded row (EncodeRecentRow). It is shared with the ring
	// and never written: the caller must not change it.
	Row []byte
	// Omitted is the number of consecutive rows of an interval whose content
	// was never recorded.
	Omitted int
}

// Rows returns how many rows the entry stands for.
func (e RecentEntry) Rows() int {
	if e.Row != nil {
		return 1
	}
	return e.Omitted
}

// Recent is a copy of the ring at one moment. Rows are numbered from the
// first row the pane's main screen scrolled off in this epoch, which is
// row 0, and the numbers only grow.
type Recent struct {
	// Oldest is the number of the first row of Entries, and so the number of
	// rows before it that were evicted or never kept. It is the oldest
	// position of the ring: zero means that the ring is complete from the
	// start of the epoch.
	Oldest uint64
	// Next is the number the next row to scroll off will have: Oldest plus
	// the rows of Entries.
	Next uint64
	// Approximate is true when a slice of output scrolled more rows than the
	// ring keeps, so that the pane could only count that it was over: Oldest
	// and Next are then lower bounds of the true numbers. The rows and their
	// order are right.
	Approximate bool
	// Entries are the rows and omitted intervals, oldest first. It is empty
	// when nothing is kept (an empty ring, which is not an omitted interval).
	Entries []RecentEntry
}

// recentRing is the ring. It is used under the pane's lock.
type recentRing struct {
	maxRows, maxBytes int
	// window is the size the emulator's scrollback is given: one more than
	// maxRows.
	window int

	entries []RecentEntry
	head    int // entries[head:] are the live ones
	rows    int // rows the live entries stand for
	bytes   int // bytes the live entries are charged
	oldest  uint64
	next    uint64
	approx  bool
}

func newRecentRing(rows, bytes int) *recentRing {
	rows = min(rows, MaxRecentRows)
	if bytes <= 0 {
		bytes = DefaultRecentBytes
	}
	return &recentRing{maxRows: rows, maxBytes: bytes, window: rows + 1}
}

func (r *recentRing) live() []RecentEntry { return r.entries[r.head:] }

// evictFront removes the oldest row or, for an omitted interval, drops n of
// its rows.
func (r *recentRing) evictFront() {
	e := &r.entries[r.head]
	if e.Row == nil && e.Omitted > 1 {
		e.Omitted--
		r.rows--
		r.oldest++
		return
	}
	r.rows--
	r.bytes -= len(e.Row) + entryCharge
	r.oldest++
	*e = RecentEntry{}
	r.head++
	if r.head > 64 && r.head >= len(r.entries)/2 {
		r.entries = append(r.entries[:0], r.entries[r.head:]...)
		clear(r.entries[len(r.entries):cap(r.entries)])
		r.head = 0
	}
}

func (r *recentRing) trim() {
	for r.rows > r.maxRows || r.bytes > r.maxBytes {
		if r.head >= len(r.entries) {
			// Nothing is left to evict: a byte budget below the charge of
			// one entry.
			r.rows, r.bytes = 0, 0
			return
		}
		r.evictFront()
	}
}

// addRow appends one row.
func (r *recentRing) addRow(row []byte) {
	r.entries = append(r.entries, RecentEntry{Row: row})
	r.rows++
	r.bytes += len(row) + entryCharge
	r.next++
	r.trim()
}

// addOmitted records n rows with no content, joining the interval that ends
// the ring, if there is one.
func (r *recentRing) addOmitted(n int) {
	if n <= 0 {
		return
	}
	if l := len(r.entries); l > r.head && r.entries[l-1].Row == nil {
		r.entries[l-1].Omitted += n
	} else {
		r.entries = append(r.entries, RecentEntry{Omitted: n})
		r.bytes += entryCharge
	}
	r.rows += n
	r.next += uint64(n)
	r.trim()
}

// skip counts n rows that were never given to the ring because newer ones
// would evict them at once.
func (r *recentRing) skip(n int) {
	r.reset()
	r.oldest += uint64(n)
	r.next += uint64(n)
}

// reset empties the ring; the rows it held count as evicted.
func (r *recentRing) reset() {
	clear(r.entries)
	r.entries, r.head, r.rows, r.bytes = r.entries[:0], 0, 0, 0
	r.oldest = r.next
}

func (r *recentRing) copyOut() Recent {
	return Recent{
		Oldest:      r.oldest,
		Next:        r.next,
		Approximate: r.approx,
		Entries:     slices.Clone(r.live()),
	}
}

// sensitiveState is the sensitive switch. It is used under the pane's lock.
type sensitiveState struct {
	on bool
	// tail is how many more rows are recorded with no content after the
	// switch was turned off: the rows the screen held then, which may hold
	// cells written while it was on. They scroll off, in order, from the top.
	tail int
}

// harvest moves the rows the emulator has scrolled since the last call into
// the ring. It runs after each slice of output and each resize, under the
// pane's lock, so that the state of the sensitive switch is the one the rows
// scrolled under.
func (p *Pane) harvest() {
	r := p.ring
	if r == nil {
		return
	}
	n := p.emu.ScrollbackLen()
	if n == 0 {
		return
	}
	lines := p.emu.Scrollback().Lines()
	if n >= r.window {
		r.approx = true
	}
	s := &p.sens
	omit := func() bool {
		// Whether the next row is omitted, and the tail it uses up.
		if s.on {
			return true
		}
		if s.tail > 0 {
			s.tail--
			return true
		}
		return false
	}
	first := max(0, n-r.maxRows)
	if first > 0 {
		for range first {
			omit()
		}
		r.skip(first)
	}
	for _, line := range lines[first:] {
		if omit() {
			r.addOmitted(1)
		} else {
			r.addRow(EncodeRecentRow(line))
		}
	}
	// Setting the size to zero frees the rows, which clearing would keep
	// referenced from the backing array.
	p.emu.SetScrollbackSize(0)
	p.emu.SetScrollbackSize(r.window)
}

// Recent returns a copy of the ring, or the zero Recent for a pane that keeps
// none or has been closed. The rows are shared and must not be changed.
func (p *Pane) Recent() Recent {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ring == nil {
		return Recent{}
	}
	return p.ring.copyOut()
}

// SetSensitive turns the sensitive switch on or off. While it is on, the rows
// that scroll off the main screen are recorded as an omitted interval with no
// content, and the content of a row is never kept anywhere: not in the ring,
// not in the emulator's window (which is emptied after each slice).
//
// The cells that are on the screen when the switch is turned off may have
// been written while it was on, and the pane cannot tell which: the rows the
// screen then holds are recorded with no content too, as the next rows to
// scroll off. A screen that is later scrolled, resized, cleared or redrawn
// therefore never gives a cell written under the switch to the ring. (A
// session that moves lines down afterwards, with reverse index or insert
// line, can hold such a row on the screen longer than that count; the owner
// turns the switch on before a sensitive step, and clears the screen after
// it.) Turning it on again before the rows have scrolled keeps the earlier
// count. The switch does nothing on a pane that keeps no ring, and the
// alternate screen has no history to omit.
func (p *Pane) SetSensitive(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ring == nil || p.closed || p.sens.on == on {
		return
	}
	p.sens.on = on
	if !on {
		p.sens.tail = max(p.sens.tail, p.rows)
	}
}

// Sensitive reports the sensitive switch.
func (p *Pane) Sensitive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sens.on
}
