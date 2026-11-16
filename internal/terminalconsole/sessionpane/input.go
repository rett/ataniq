package sessionpane

import (
	uv "github.com/charmbracelet/ultraviolet"
)

// InputSink receives the input meant for the session: keys, pasted text and
// mouse events, the mouse in pane cells. It is a separate interface so that a
// read-only console can be built without one. The fixture console discards
// everything; later a holder will encode the input for the session. The pane
// calls it outside its lock.
type InputSink interface {
	Key(k uv.KeyPressEvent)
	Paste(text string)
	Mouse(m uv.MouseEvent)
}

// Discard is an InputSink that drops all input.
type Discard struct{}

func (Discard) Key(uv.KeyPressEvent) {}
func (Discard) Paste(string)         {}
func (Discard) Mouse(uv.MouseEvent)  {}

// live reports, under p.mu, whether the session can still take input.
func (p *Pane) live() bool { return !p.ended && !p.closed }

// Key passes a key press to the input sink. Which keys reach the pane (the
// focus, the return key) is the cockpit's decision. After End or Close the
// key is dropped.
func (p *Pane) Key(k uv.KeyPressEvent) {
	p.mu.Lock()
	live := p.live()
	p.mu.Unlock()
	if live {
		p.input.Key(k)
	}
}

// Paste passes pasted text to the input sink. After End or Close it is
// dropped.
func (p *Pane) Paste(text string) {
	p.mu.Lock()
	live := p.live()
	p.mu.Unlock()
	if live {
		p.input.Paste(text)
	}
}

// Mouse translates an event of the outer terminal, in window cells, to pane
// cells, area being where the pane was drawn, and passes it to the input
// sink when the session asked for that kind of event. It reports whether it
// did.
//
// An event outside the part of area the pane covers is dropped. So is every
// event while the session has not asked for mouse reporting, and after End
// or Close. With press tracking (mode 9) only presses and wheel events pass,
// with click tracking (1000, 1001) releases too, with drag tracking (1002)
// also motion with a button held, and with motion tracking (1003) all motion.
func (p *Pane) Mouse(ev uv.MouseEvent, area uv.Rectangle) bool {
	p.mu.Lock()
	mode, cols, rows, live := p.dec.modes().Mouse, p.cols, p.rows, p.live()
	p.mu.Unlock()
	if !live || ev == nil {
		return false
	}
	m := ev.Mouse()
	x, y := m.X-area.Min.X, m.Y-area.Min.Y
	if x < 0 || y < 0 || x >= min(cols, area.Dx()) || y >= min(rows, area.Dy()) {
		return false
	}
	m.X, m.Y = x, y
	var out uv.MouseEvent
	switch ev.(type) {
	case uv.MouseClickEvent:
		out = uv.MouseClickEvent(m)
	case uv.MouseWheelEvent:
		out = uv.MouseWheelEvent(m)
	case uv.MouseReleaseEvent:
		if mode < uv.MouseModeClick {
			return false
		}
		out = uv.MouseReleaseEvent(m)
	case uv.MouseMotionEvent:
		if mode < uv.MouseModeDrag || (mode == uv.MouseModeDrag && m.Button == uv.MouseNone) {
			return false
		}
		out = uv.MouseMotionEvent(m)
	default:
		return false
	}
	if mode == uv.MouseModeNone {
		return false
	}
	p.input.Mouse(out)
	return true
}
