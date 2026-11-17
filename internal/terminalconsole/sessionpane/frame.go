package sessionpane

import (
	"bytes"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
)

// MaxFrameHold is the longest the pane keeps showing the last complete frame
// while the session holds synchronized output (mode 2026) set. After it the
// pane shows the screen as it is, as a terminal would.
const MaxFrameHold = 150 * time.Millisecond

// The literal forms of setting and resetting mode 2026. Write splits its
// input after each set, and looks for a reset in what is left, to learn
// whether a frame that opens in a chunk also closes in it (see Write). Other
// forms, such as a set combined with other modes, are still followed through
// the mode callbacks; they only miss that shortcut.
var (
	syncSet   = []byte("\x1b[?2026h")
	syncReset = []byte("\x1b[?2026l")
)

// frame is a copy of the screen and cursor taken when a synchronized update
// began: the last complete frame.
type frame struct {
	cells      []uv.Cell
	cols, rows int
	cursor     uv.Position
	hidden     bool
}

// CellAt returns the cell at x, y, or nil outside the frame.
func (f *frame) CellAt(x, y int) *uv.Cell {
	if x < 0 || y < 0 || x >= f.cols || y >= f.rows {
		return nil
	}
	return &f.cells[y*f.cols+x]
}

// syncState follows mode 2026. All of it is used under p.mu.
type syncState struct {
	on bool // the session has mode 2026 set
	// held is true when frame is the last complete frame, to be shown while
	// the mode stays set, for at most MaxFrameHold after at.
	held  bool
	at    time.Time
	frame frame
	// kept is true when the session ended while a frame was held: the frame
	// is then its last screen, shown with no time limit.
	kept bool
	// closesLater is true while Write feeds a part of its input that is
	// followed, in the same input, by a reset of the mode.
	closesLater bool
}

// setSync is the mode callback for 2026. It runs inside emu.Write, where a
// frame boundary can fall anywhere in a chunk, so the frame is taken here,
// from the unsafe emulator, under the lock Write holds.
func (p *Pane) setSync(on bool) {
	s := &p.syncOut
	switch {
	case on && !s.on:
		s.on = true
		// A frame that also closes later in this chunk is never shown, so
		// copying the screen for it would be wasted.
		if s.closesLater {
			s.held = false
		} else {
			p.capture()
		}
	case !on:
		s.on, s.held = false, false
	}
}

// capture copies the screen and cursor into the frame, reusing its cells.
func (p *Pane) capture() {
	f := &p.syncOut.frame
	n := p.cols * p.rows
	if cap(f.cells) < n {
		f.cells = make([]uv.Cell, n)
	}
	f.cells = f.cells[:n]
	for y := 0; y < p.rows; y++ {
		for x := 0; x < p.cols; x++ {
			if c := p.emu.CellAt(x, y); c != nil {
				f.cells[y*p.cols+x] = *c
			} else {
				f.cells[y*p.cols+x] = uv.Cell{}
			}
		}
	}
	f.cols, f.rows = p.cols, p.rows
	f.cursor, f.hidden = p.emu.CursorPosition(), p.emu.CursorHidden()
	p.syncOut.held, p.syncOut.at = true, p.clock()
}

// holding reports whether the held frame is shown at now.
func (p *Pane) holding(now time.Time) bool {
	s := &p.syncOut
	return s.on && s.held && (s.kept || now.Sub(s.at) < MaxFrameHold)
}

// write feeds b to the emulator in parts, each ending after a literal set of
// mode 2026, and tells the callback whether a literal reset follows the part
// in b. Such a reset is a reset unless the parser swallows its ESC, which
// only malformed input makes it do (an ESC inside a UTF-8 character or just
// after a DCS introducer). Then the frame stays open with no copy, and the
// pane shows the screen as it is, as it does after MaxFrameHold.
//
// The work is linear in len(b): one search for the last reset, and one for
// each set, each starting where the last one ended.
func (p *Pane) write(b []byte) {
	lastReset := bytes.LastIndex(b, syncReset)
	for start := 0; start < len(b); {
		end := len(b)
		if i := bytes.Index(b[start:], syncSet); i >= 0 {
			end = start + i + len(syncSet)
		}
		p.syncOut.closesLater = lastReset >= end
		_, _ = p.emu.Write(b[start:end])
		start = end
	}
	p.syncOut.closesLater = false
}

// HoldDeadline returns the time at which the frame the pane is holding stops
// being shown, if it is holding one that can time out. The cockpit draws the
// pane again then.
func (p *Pane) HoldDeadline() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.holding(p.clock()) || p.syncOut.kept {
		return time.Time{}, false
	}
	return p.syncOut.at.Add(MaxFrameHold), true
}
