package sessionpane

import (
	"io"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
)

// Size limits. A size outside them is clamped: the pane never grows past
// MaxCols by MaxRows, whatever the caller or the session asks for, and never
// shrinks below MinCols by MinRows, the smallest grid that still holds one
// wide character.
const (
	MinCols = 2
	MinRows = 1
	MaxCols = 500
	MaxRows = 200
)

// replyBuffer is the size of the drain goroutine's read buffer. Any size
// works: the emulator's writer waits until the whole answer has been read.
const replyBuffer = 4096

// Options configures a pane. The zero value is a fixture pane: replies and
// input are discarded.
type Options struct {
	// RecentOutput keeps up to RecentRowLimit main-screen rows for local
	// paging. Only a live attach client enables it.
	RecentOutput bool

	// Replies receives each chunk of the emulator's answers to the session's
	// queries, as a copy the receiver may keep. It is called on the pane's
	// drain goroutine, never under the pane's lock. It must return promptly
	// and must not call the pane: the emulator cannot answer the next query
	// until it returns. Nil discards every reply.
	Replies func(p []byte)

	// GraphemeWidths tells the pane that the outer terminal measures grapheme
	// clusters (mode 2027); see Pane.Draw. It can be changed later with
	// Pane.SetGraphemeWidths.
	GraphemeWidths bool

	// Colours are the colours reported to the session's colour queries. A
	// nil colour is reported as the matching NeutralColours entry.
	Colours Colours

	// Input receives the keys, paste and mouse events meant for the session.
	// Nil discards them.
	Input InputSink

	// Clock returns the current time, for the synchronized output time
	// limit. Nil means time.Now; tests inject their own. It is called under
	// the pane's lock and must not call the pane.
	Clock func() time.Time
}

// Pane is an embedded session pane. Its methods are safe for concurrent use.
type Pane struct {
	mu sync.Mutex
	// emu is the unsafe emulator: every use is under mu, including the
	// callbacks it makes, which run inside emu.Write and emu.Resize.
	emu   *vt.Emulator
	clock func() time.Time
	input InputSink

	cols, rows int
	graphemes  bool

	dec       decModes
	syncOut   syncState
	recent    recentPage
	title     string
	clipboard int
	// titlesCut counts the titles that lost characters to the length cap.
	titlesCut uint64
	// altOn and altOff count the emulator's alternate-screen switches.
	altOn, altOff uint64

	ended       bool
	status      int
	statusKnown bool
	closed      bool

	// pipe is the writing end of the emulator's reply pipe, where the pane's
	// own handlers write their answers too, under mu. Closing it ends the
	// drain goroutine, which then closes drained.
	pipe    io.WriteCloser
	drained chan struct{}
}

// New returns a pane of the given size, clamped to the limits, and starts its
// drain goroutine. Call Close to stop it.
func New(cols, rows int, o Options) *Pane {
	cols, rows = clamp(cols, rows)
	p := &Pane{
		emu:       vt.NewEmulator(cols, rows),
		cols:      cols,
		rows:      rows,
		graphemes: o.GraphemeWidths,
		clock:     o.Clock,
		input:     o.Input,
		drained:   make(chan struct{}),
	}
	if p.clock == nil {
		p.clock = time.Now
	}
	if p.input == nil {
		p.input = Discard{}
	}
	rowsToKeep := 0
	if o.RecentOutput {
		rowsToKeep = RecentRowLimit
	}
	p.emu.SetScrollbackSize(rowsToKeep)
	// Closing the writer, rather than calling the emulator's Close, ends the
	// reader without writing the emulator's closed flag, which its Read
	// checks on the drain goroutine without a lock.
	pipe, ok := p.emu.InputPipe().(io.WriteCloser)
	if !ok {
		panic("sessionpane: the emulator's reply pipe cannot be closed")
	}
	p.pipe = pipe
	p.install()
	p.setColours(o.Colours)
	go drain(p.emu, o.Replies, p.drained)
	return p
}

// drain reads the emulator's replies until the pipe is closed. It touches no
// pane state, so a writer holding the pane's lock can always hand it a reply.
func drain(r io.Reader, replies func([]byte), done chan<- struct{}) {
	defer close(done)
	buf := make([]byte, replyBuffer)
	for {
		n, err := r.Read(buf)
		if n > 0 && replies != nil {
			replies(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			return
		}
	}
}

func clamp(cols, rows int) (int, int) {
	return min(max(cols, MinCols), MaxCols), min(max(rows, MinRows), MaxRows)
}

// writeSlice is the most output the pane feeds to the emulator under one
// hold of its lock. A whole-screen erase of the largest pane costs the lines
// it changes (see the patched x/vt) and a scroll in a region narrower than the
// screen moves up to 11 MB, a millisecond, so a chunk of 64 KiB of such
// sequences would keep the lock, and with it every draw, digest and snapshot,
// for seconds. In slices of 4 KiB the lock is held for a sixteenth of that at
// most, and a call that waits for it waits for one slice.
const writeSlice = 4 << 10

// Write feeds session output to the emulator. A chunk may end anywhere,
// inside an escape sequence or a character; the next chunk continues it. The
// chunk is fed in slices of at most 4 KiB, and the pane's lock is free between
// two slices, so a draw or a digest never waits for a whole chunk; a slice
// ends anywhere too, which the parser does not mind. Another call that
// writes may come in between, as it may between two chunks. After End or
// Close, Write does nothing; a chunk that is in the middle of being fed is
// dropped from the slice on that finds the pane ended or closed.
func (p *Pane) Write(b []byte) {
	for len(b) > 0 {
		var ok bool
		if b, ok = p.feed(b); !ok {
			return
		}
	}
}

// feed feeds the first slice of b to the emulator under the lock and returns
// the rest, and whether the pane took the slice (it does not once it has
// ended or closed). The frame logic of synchronized output (write) sees one
// slice at a time: a frame that opens and closes inside the slice is never
// shown, as before; a frame that is still open at the end of a slice is
// held, so a reader that comes in between two slices sees the last complete
// frame, not half of the next.
func (p *Pane) feed(b []byte) (rest []byte, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || p.closed {
		return nil, false
	}
	n := min(len(b), writeSlice)
	p.write(b[:n])
	return b[n:], true
}

// Resize sets the pane's size, clamped to the limits. A frame held for
// synchronized output no longer fits and is dropped. After End or Close
// Resize does nothing, so an ended session keeps its last screen as it was.
func (p *Pane) Resize(cols, rows int) {
	cols, rows = clamp(cols, rows)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || p.closed || (cols == p.cols && rows == p.rows) {
		return
	}
	p.emu.Resize(cols, rows)
	p.cols, p.rows = cols, rows
	p.syncOut.held = false
}

// End records that the session's output has ended, with its exit status when
// known is true. Later writes are ignored. Only the first call counts. A
// frame held for synchronized output at that moment stays the last screen.
func (p *Pane) End(status int, known bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || p.closed {
		return
	}
	p.ended = true
	if p.holding(p.clock()) {
		p.syncOut.kept = true
	} else {
		p.syncOut.held = false
	}
	if known {
		p.status, p.statusKnown = status, true
	}
}

// Ended reports whether End was called, and the exit status it gave. When
// known is false the status is unknown and reported as 0.
func (p *Pane) Ended() (ended bool, status int, known bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ended, p.status, p.statusKnown
}

// Size returns the pane's size in cells.
func (p *Pane) Size() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cols, p.rows
}

// Close stops the drain goroutine and waits for it to return. Write, Resize
// and End do nothing afterwards; the last screen can still be drawn. Close
// may be called more than once.
func (p *Pane) Close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		p.recent = recentPage{}
		p.emu.SetScrollbackSize(0)
		_ = p.pipe.Close()
	}
	p.mu.Unlock()
	<-p.drained
}
