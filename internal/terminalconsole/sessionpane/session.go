package sessionpane

import (
	"bytes"
	"image/color"
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// MaxTitle is the most bytes of a title the pane keeps.
const MaxTitle = 256

// Colours are the default colours the pane reports when the session asks for
// them (OSC 10, 11 and 12): the outer terminal's colours, which the cockpit
// learns from the outer terminal or takes from its theme. A nil colour is
// unknown and reported as the matching NeutralColours entry.
type Colours struct {
	Foreground, Background, Cursor color.Color
}

// NeutralColours are reported when the outer terminal's colours are unknown:
// white on black, which a session reads as a dark background.
var NeutralColours = Colours{
	Foreground: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	Background: color.RGBA{A: 0xff},
	Cursor:     color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
}

// Modes are the terminal modes the session asked for that the cockpit must
// mirror on the outer terminal: mouse reporting, bracketed paste and focus
// events. Keyboard enhancement flags are not tracked.
type Modes struct {
	// Mouse is the strongest mouse tracking mode set: press (9), click (1000
	// or 1001), drag (1002) or motion (1003).
	Mouse uv.MouseMode
	// MouseEncoding is SGR when mode 1006 is set, else legacy.
	MouseEncoding  uv.MouseEncoding
	BracketedPaste bool // mode 2004
	FocusEvents    bool // mode 1004
}

// decModes are the DEC private modes the pane follows, as the emulator's
// mode callbacks report them.
type decModes struct {
	press, click, highlight, drag, motion bool
	sgr, focus, paste                     bool
}

func (d decModes) modes() Modes {
	var m Modes
	switch {
	case d.motion:
		m.Mouse = uv.MouseModeMotion
	case d.drag:
		m.Mouse = uv.MouseModeDrag
	case d.click || d.highlight:
		m.Mouse = uv.MouseModeClick
	case d.press:
		m.Mouse = uv.MouseModePress
	}
	if d.sgr {
		m.MouseEncoding = uv.MouseEncodingSGR
	}
	m.BracketedPaste, m.FocusEvents = d.paste, d.focus
	return m
}

// install sets the emulator's callbacks and the pane's own sequence
// handlers. Both run inside emu.Write, so under p.mu: they change pane state
// directly and never call a method that takes the lock.
func (p *Pane) install() {
	p.emu.SetCallbacks(vt.Callbacks{
		EnableMode:  func(m ansi.Mode) { p.setMode(m, true) },
		DisableMode: func(m ansi.Mode) { p.setMode(m, false) },
	})
	// x/vt drops a title that contains ";". These handlers, registered after
	// x/vt's own and so called first, keep everything after the first ";".
	for _, cmd := range []int{0, 2} {
		p.emu.RegisterOscHandler(cmd, func(data []byte) bool {
			if i := bytes.IndexByte(data, ';'); i >= 0 {
				p.title = cleanTitle(data[i+1:])
			}
			return true
		})
	}
	// A full reset resets mode 2026 too, but x/vt reports only the modes it
	// knows. Returning false lets x/vt's own handler do the reset.
	p.emu.RegisterEscHandler('c', func() bool {
		p.setSync(false)
		return false
	})
	// The icon name is not shown anywhere.
	p.emu.RegisterOscHandler(1, func([]byte) bool { return true })
	// Clipboard access is never passed on: a write or a query is counted and
	// dropped, and no reply is made.
	p.emu.RegisterOscHandler(52, func([]byte) bool {
		p.clipboard++
		return true
	})
}

func (p *Pane) setMode(m ansi.Mode, on bool) {
	d, ok := m.(ansi.DECMode)
	if !ok {
		return
	}
	switch d {
	case ansi.ModeMouseX10:
		p.dec.press = on
	case ansi.ModeMouseNormal:
		p.dec.click = on
	case ansi.ModeMouseHighlight:
		p.dec.highlight = on
	case ansi.ModeMouseButtonEvent:
		p.dec.drag = on
	case ansi.ModeMouseAnyEvent:
		p.dec.motion = on
	case ansi.ModeMouseExtSgr:
		p.dec.sgr = on
	case ansi.ModeFocusEvent:
		p.dec.focus = on
	case ansi.ModeBracketedPaste:
		p.dec.paste = on
	case ansi.ModeSynchronizedOutput:
		p.setSync(on)
	}
}

// setColours makes c the emulator's default colours, under p.mu.
func (p *Pane) setColours(c Colours) {
	p.emu.SetDefaultForegroundColor(orNeutral(c.Foreground, NeutralColours.Foreground))
	p.emu.SetDefaultBackgroundColor(orNeutral(c.Background, NeutralColours.Background))
	p.emu.SetDefaultCursorColor(orNeutral(c.Cursor, NeutralColours.Cursor))
}

func orNeutral(c, neutral color.Color) color.Color {
	if c == nil {
		return neutral
	}
	return c
}

// SetColours changes the colours the pane reports, for example when the
// cockpit learns the outer terminal's colours after the pane was made. A
// colour the session set itself (OSC 10, 11 or 12 with a value) is still
// reported in place of the default, as a terminal would.
func (p *Pane) SetColours(c Colours) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setColours(c)
}

// Modes returns the modes the session asked for. An ended session asks for
// none.
func (p *Pane) Modes() Modes {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended {
		return Modes{}
	}
	return p.dec.modes()
}

// Title returns the session's window title (OSC 0 or 2), without control or
// bidirectional formatting characters, at most MaxTitle bytes.
func (p *Pane) Title() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.title
}

// ClipboardBlocked returns how many clipboard requests (OSC 52, a write or a
// query) the session made. None was passed on.
func (p *Pane) ClipboardBlocked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.clipboard
}

// cleanTitle makes a title safe to show: invalid UTF-8 becomes U+FFFD,
// control characters, line separators and bidirectional formatting
// characters are dropped, and the result is cut, at a character boundary, to
// MaxTitle bytes. Only the first 4*MaxTitle bytes are read.
func cleanTitle(b []byte) string {
	if len(b) > 4*MaxTitle {
		b = b[:4*MaxTitle]
	}
	var s strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		b = b[n:]
		if dropFromTitle(r) {
			continue
		}
		if s.Len()+utf8.RuneLen(r) > MaxTitle {
			break
		}
		s.WriteRune(r)
	}
	return s.String()
}

func dropFromTitle(r rune) bool {
	switch {
	case r < 0x20, r >= 0x7f && r <= 0x9f:
		return true
	case r == 0x061c, r == 0x200e, r == 0x200f, r == 0x2028, r == 0x2029:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
