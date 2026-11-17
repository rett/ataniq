package leadattach

import (
	"strconv"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/terminalconsole/sessionpane"
)

// Modes derives the modes the cockpit acts on for the outer terminal from
// the session's mode set: the strongest mouse tracking mode that is on (9,
// 1000 and 1001, 1002, 1003), SGR encoding when 1006 is on, bracketed paste
// (2004) and focus events (1004). It follows the order of the pane's own
// derivation.
func (m ModeSet) Modes() sessionpane.Modes {
	var out sessionpane.Modes
	switch {
	case m.Has(decMouseMotion, false):
		out.Mouse = uv.MouseModeMotion
	case m.Has(decMouseDrag, false):
		out.Mouse = uv.MouseModeDrag
	case m.Has(decMouseNormal, false) || m.Has(decMouseHilite, false):
		out.Mouse = uv.MouseModeClick
	case m.Has(decMousePress, false):
		out.Mouse = uv.MouseModePress
	}
	if m.Has(decMouseSGR, false) {
		out.MouseEncoding = uv.MouseEncodingSGR
	}
	out.BracketedPaste = m.Has(decPaste, false)
	out.FocusEvents = m.Has(decFocus, false)
	return out
}

// legacyMax is the largest zero-based coordinate the legacy mouse encoding
// can carry: a byte holds the coordinate plus 33.
const legacyMax = 255 - 33

// EncodeMouse returns the bytes a terminal sends for the mouse event ev (in
// the session's cells, zero-based) while the session has the modes ms on, or
// nil when the event is not reported in those modes. The encoding is SGR
// (CSI < b;x;y M or m) when mode 1006 is on, else the legacy one (CSI M and
// three bytes), which cannot carry a cell past column or row 223 (the event
// is dropped). The other encodings (1005, 1015, 1016) are not produced: a
// session that asks for one gets the legacy or the SGR form.
//
// What is reported follows the strongest tracking mode: presses and wheel
// events with mode 9 (without modifiers, as X10 did), and releases with
// 1000 and 1001, motion with a button held with 1002, and all motion with
// 1003. A release in the legacy encoding does not say which button was
// released (button 3). An SGR release of no known button is reported as the
// left button.
func EncodeMouse(ev uv.MouseEvent, ms ModeSet) []byte {
	if ev == nil {
		return nil
	}
	mode := ms.Modes().Mouse
	if mode == uv.MouseModeNone {
		return nil
	}
	mo := ev.Mouse()
	if mo.X < 0 || mo.Y < 0 {
		return nil
	}
	sgr := ms.Has(decMouseSGR, false)
	motion, release := false, false
	switch ev.(type) {
	case uv.MouseClickEvent, uv.MouseWheelEvent:
	case uv.MouseReleaseEvent:
		if mode < uv.MouseModeClick {
			return nil
		}
		release = true
	case uv.MouseMotionEvent:
		if mode < uv.MouseModeDrag || (mode == uv.MouseModeDrag && mo.Button == uv.MouseNone) {
			return nil
		}
		motion = true
	default:
		return nil
	}
	btn := mo.Button
	if release && !sgr {
		btn = uv.MouseNone
	}
	if release && sgr && btn == uv.MouseNone {
		btn = uv.MouseLeft
	}
	shift, alt, ctrl := mo.Mod&uv.ModShift != 0, mo.Mod&(uv.ModAlt|uv.ModMeta) != 0, mo.Mod&uv.ModCtrl != 0
	if mode == uv.MouseModePress {
		shift, alt, ctrl = false, false, false
	}
	code := ansi.EncodeMouseButton(btn, motion, shift, alt, ctrl)
	if code == 0xff {
		return nil
	}
	if sgr {
		end := byte('M')
		if release {
			end = 'm'
		}
		return []byte("\x1b[<" + strconv.Itoa(int(code)) + ";" + strconv.Itoa(mo.X+1) + ";" + strconv.Itoa(mo.Y+1) + string(end))
	}
	if mo.X > legacyMax || mo.Y > legacyMax || code > 255-32 {
		return nil
	}
	return []byte{0x1b, '[', 'M', code + 32, byte(mo.X + 33), byte(mo.Y + 33)}
}
