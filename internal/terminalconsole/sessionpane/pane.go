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
	title     string
	clipboard int

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
	// The pane shows no scrollback, so the main screen keeps one line, the
	// smallest x/vt accepts. SetScrollbackSize reaches the main screen
	// only; the patched x/vt gives the alternate screen no scrollback.
	p.emu.SetScrollbackSize(1)
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

// Write feeds session output to the emulator. A chunk may end anywhere,
// inside an escape sequence or a character; the next chunk continues it.
// After End or Close, Write does nothing.
func (p *Pane) Write(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || p.closed || len(b) == 0 {
		return
	}
	p.write(b)
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
		_ = p.pipe.Close()
	}
	p.mu.Unlock()
	<-p.drained
}
