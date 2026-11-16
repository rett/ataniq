package sessionpane

import (
	"io"
	"sync"

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

// Options configures a pane. The zero value is a fixture pane: replies are
// discarded.
type Options struct {
	// Replies receives each chunk of the emulator's answers to the session's
	// queries, as a copy the receiver may keep. It is called on the pane's
	// drain goroutine, never under the pane's lock. It must return promptly
	// and must not call the pane: the emulator cannot answer the next query
	// until it returns. Nil discards every reply.
	Replies func(p []byte)

	// Colours are the colours reported to the session's colour queries. A
	// nil colour is reported as the matching NeutralColours entry.
	Colours Colours
}

// Pane is an embedded session pane. Its methods are safe for concurrent use.
type Pane struct {
	mu sync.Mutex
	// emu is the unsafe emulator: every use is under mu, including the
	// callbacks it makes, which run inside emu.Write and emu.Resize.
	emu *vt.Emulator

	cols, rows int

	dec       decModes
	title     string
	clipboard int

	ended       bool
	status      int
	statusKnown bool
	closed      bool

	// pipe is the writing end of the emulator's reply pipe. Closing it ends
	// the drain goroutine, which then closes drained.
	pipe    io.Closer
	drained chan struct{}
}

// New returns a pane of the given size, clamped to the limits, and starts its
// drain goroutine. Call Close to stop it.
func New(cols, rows int, o Options) *Pane {
	cols, rows = clamp(cols, rows)
	p := &Pane{
		emu:     vt.NewEmulator(cols, rows),
		cols:    cols,
		rows:    rows,
		drained: make(chan struct{}),
	}
	// The pane keeps no scrollback; one line is the smallest x/vt accepts.
	p.emu.SetScrollbackSize(1)
	p.install()
	p.setColours(o.Colours)
	// Closing the writer, rather than calling the emulator's Close, ends the
	// reader without writing the emulator's closed flag, which its Read
	// checks on the drain goroutine without a lock.
	pipe, ok := p.emu.InputPipe().(io.Closer)
	if !ok {
		panic("sessionpane: the emulator's reply pipe cannot be closed")
	}
	p.pipe = pipe
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
	_, _ = p.emu.Write(b)
}

// Resize sets the pane's size, clamped to the limits. After End or Close it
// does nothing, so an ended session keeps its last screen as it was.
func (p *Pane) Resize(cols, rows int) {
	cols, rows = clamp(cols, rows)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || p.closed || (cols == p.cols && rows == p.rows) {
		return
	}
	p.emu.Resize(cols, rows)
	p.cols, p.rows = cols, rows
}

// End records that the session's output has ended, with its exit status when
// known is true. Later writes are ignored. Only the first call counts.
func (p *Pane) End(status int, known bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended || p.closed {
		return
	}
	p.ended = true
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
