package sessionpane

import (
	"image/color"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// Drained returns the channel the drain goroutine closes when it returns.
func Drained(p *Pane) <-chan struct{} { return p.drained }

// ScreenText returns the emulator's screen as plain text, with trailing
// blank lines removed.
func ScreenText(p *Pane) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimRight(p.emu.String(), "\n")
}

// CleanTitle is cleanTitle, for tests.
func CleanTitle(b []byte) string {
	s, _ := cleanTitle(b)
	return s
}

// ScrollbackLen returns how many lines the emulator's main screen keeps in
// its scrollback.
func ScrollbackLen(p *Pane) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emu.ScrollbackLen()
}

// SetCell puts c in the emulator's screen at x, y, as no output can: the
// emulator stores no control character in a cell, so a test of what Text
// does with one sets it here.
func SetCell(p *Pane, x, y int, c *uv.Cell) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emu.SetCell(x, y, c)
}

// DeepStateData is the whole state of a pane, as data a test can compare: the
// emulator's state with what the snapshot does not tell apart made equal (a
// link with no URL is no link, and the colours the session set are compared
// by value, as the pane reports them), and the pane's own state.
type DeepStateData struct {
	VT         vt.State
	Title      string
	Cols, Rows int
	Dec        decModes
	SyncOn     bool
}

// DeepState returns the state of p.
func DeepState(p *Pane) DeepStateData {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := DeepStateData{
		VT: p.emu.State(), Title: p.title, Cols: p.cols, Rows: p.rows,
		Dec: p.dec, SyncOn: p.syncOut.on,
	}
	for i := range d.VT.Screens {
		sc := &d.VT.Screens[i]
		for j := range sc.Cells {
			sc.Cells[j].Link = normLink(sc.Cells[j].Link)
		}
		sc.Cursor.Link = normLink(sc.Cursor.Link)
		sc.Saved.Link = normLink(sc.Saved.Link)
	}
	norm := func(c color.Color) color.Color {
		if c == nil {
			return nil
		}
		r, g, b, a := c.RGBA()
		return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
	}
	d.VT.Foreground = norm(d.VT.Foreground)
	d.VT.Background = norm(d.VT.Background)
	d.VT.CursorColor = norm(d.VT.CursorColor)
	return d
}

// SetEmulatorState replaces the emulator's state without going through a
// snapshot, and the pane's size to match, as a test of what a snapshot of a
// state that output cannot make does.
func SetEmulatorState(p *Pane, st vt.State) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.emu.SetState(st); err != nil {
		return err
	}
	p.cols, p.rows = st.Width, st.Height
	return nil
}

// CellAt returns a copy of the emulator's cell at x, y.
func CellAt(p *Pane, x, y int) uv.Cell {
	p.mu.Lock()
	defer p.mu.Unlock()
	return *p.emu.CellAt(x, y)
}

// HeldFrameShown reports whether Draw shows a frame held for synchronized
// output rather than the screen.
func HeldFrameShown(p *Pane) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.holding(p.clock())
}

// EmulatorState returns the emulator's state, as the snapshot encoder reads it.
func EmulatorState(p *Pane) vt.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.emu.State()
}

// AppendSnapshotLimit is AppendSnapshot with another limit than
// MaxSnapshotBytes, for the test of the error and of the worst case.
func AppendSnapshotLimit(p *Pane, dst []byte, limit int) ([]byte, error) {
	return p.appendSnapshot(dst, limit)
}
