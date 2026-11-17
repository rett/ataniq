package terminalconsole

import (
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// maxCopy bounds the text a selection copies to the outer terminal's
// clipboard. A longer selection is not copied at all.
const maxCopy = 1 << 20

// copyRefused is the notice shown for a selection longer than maxCopy.
const copyRefused = "Selection not copied: it is longer than 1 MiB."

// selection is text selected with the mouse in the session pane, while the
// session does not take the mouse itself. anchor is the cell where the left
// button was pressed and head the cell the pointer was last seen at, both in
// pane cells and clamped to the part of the pane that is drawn. The zero
// value is no selection.
type selection struct {
	// drag is true while the left button is held.
	drag bool
	// moved is true once the pointer has left the cell it was pressed in.
	// Only then is there a selection to draw and copy: a click alone
	// selects nothing.
	moved        bool
	anchor, head uv.Position
	// text is the selected text as copied, or as read at the last check. A
	// change in it clears the selection.
	text string
}

// shown reports whether there is a selection to draw.
func (s selection) shown() bool { return s.moved }

// span returns the selection's first and last cells, in reading order.
func (s selection) span() (from, to uv.Position) {
	from, to = s.anchor, s.head
	if to.Y < from.Y || (to.Y == from.Y && to.X < from.X) {
		from, to = to, from
	}
	return from, to
}

// covers reports whether the selection includes the pane cell x, y: a
// stream from its first cell to its last, whole rows in between.
func (s selection) covers(x, y int) bool {
	if !s.shown() {
		return false
	}
	from, to := s.span()
	switch {
	case y < from.Y || y > to.Y:
		return false
	case y == from.Y && x < from.X:
		return false
	case y == to.Y && x > to.X:
		return false
	}
	return true
}

// paneArea returns the part of the session pane's area, in window cells,
// that the pane covers: the inner area, cut to the pane's size.
func (m *Model) paneArea(l layout) uv.Rectangle {
	cols, rows := m.rep.pane.Size()
	return uv.Rect(l.inner.Min.X, l.inner.Min.Y, min(cols, l.inner.Dx()), min(rows, l.inner.Dy()))
}

// sessionTakesMouse reports whether mouse events go to the session: in
// session focus, when the session asked for mouse reporting. An ended
// session asks for none.
func (m *Model) sessionTakesMouse() bool {
	return m.focus == sessionFocus && m.rep.pane != nil && m.rep.pane.Modes().Mouse != uv.MouseModeNone
}

// onMouse handles a mouse event in the cockpit. Any press clears the
// selection, and the notice of a selection that was not copied. When the
// session takes the mouse the event goes to the pane, as the session asked;
// otherwise it makes a selection in the pane.
func (m *Model) onMouse(msg tea.MouseMsg) tea.Cmd {
	l := m.layout()
	if !l.cockpit || m.rep.pane == nil {
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); ok {
		m.sel = selection{}
		m.dropRefusal()
	}
	if m.sessionTakesMouse() {
		if m.sel.drag {
			// The session took the mouse in the middle of a drag.
			m.sel = selection{}
		}
		if ev := uvMouse(msg); ev != nil {
			m.rep.pane.Mouse(ev, l.inner)
		}
		return nil
	}
	return m.selectMouse(msg, l)
}

// uvMouse converts a Bubble Tea mouse message to the pane's event type.
func uvMouse(msg tea.MouseMsg) uv.MouseEvent {
	switch e := msg.(type) {
	case tea.MouseClickMsg:
		return uv.MouseClickEvent(e)
	case tea.MouseReleaseMsg:
		return uv.MouseReleaseEvent(e)
	case tea.MouseWheelMsg:
		return uv.MouseWheelEvent(e)
	case tea.MouseMotionMsg:
		return uv.MouseMotionEvent(e)
	}
	return nil
}

// selectMouse makes a selection: a left press inside the session pane's area
// starts it, motion extends it, clamped to the drawn pane however far the
// pointer goes, and the release ends it and copies the text. A press
// elsewhere, another button, the wheel, and a press while help or the action
// list is open start nothing.
func (m *Model) selectMouse(msg tea.MouseMsg, l layout) tea.Cmd {
	area := m.paneArea(l)
	at := func(x, y int) uv.Position {
		return uv.Pos(min(max(x, area.Min.X), area.Max.X-1)-area.Min.X, min(max(y, area.Min.Y), area.Max.Y-1)-area.Min.Y)
	}
	switch e := msg.(type) {
	case tea.MouseClickMsg:
		if e.Button != tea.MouseLeft || m.overlay != "" || area.Empty() || !uv.Pos(e.X, e.Y).In(l.inner) {
			return nil
		}
		p := at(e.X, e.Y)
		m.sel = selection{drag: true, anchor: p, head: p}
	case tea.MouseMotionMsg:
		if m.sel.drag {
			m.extend(at(e.X, e.Y))
		}
	case tea.MouseReleaseMsg:
		if !m.sel.drag {
			return nil
		}
		m.extend(at(e.X, e.Y))
		m.sel.drag = false
		if !m.sel.moved {
			m.sel = selection{}
			return nil
		}
		return m.copySelection()
	}
	return nil
}

func (m *Model) extend(p uv.Position) {
	m.sel.head = p
	if p != m.sel.anchor {
		m.sel.moved = true
	}
}

// copySelection reads the selected text and copies it to the outer terminal's
// clipboard with OSC 52. Empty text is not copied, and text longer than
// maxCopy is not copied at all; a notice says so until the next press.
func (m *Model) copySelection() tea.Cmd {
	from, to := m.sel.span()
	text, ok := m.rep.pane.Text(from, to, maxCopy)
	if !ok {
		m.notice = copyRefused
		return nil
	}
	m.sel.text = text
	if text == "" {
		return nil
	}
	return tea.Raw(ansi.SetSystemClipboard(text))
}

// dropRefusal removes the notice of a selection that was not copied, and
// leaves any other notice.
func (m *Model) dropRefusal() {
	if m.notice == copyRefused {
		m.notice = ""
	}
}

// checkSelection clears a finished selection when the text it covers has
// changed since it was copied: the session wrote over it, scrolled or
// cleared the screen. It runs after output reaches the pane and when a
// frame held for synchronized output stops being shown. A selection being
// dragged is not checked: its text is read when it is released.
func (m *Model) checkSelection() {
	if !m.sel.shown() || m.sel.drag || m.rep.pane == nil {
		return
	}
	from, to := m.sel.span()
	if text, ok := m.rep.pane.Text(from, to, maxCopy); !ok || text != m.sel.text {
		m.sel = selection{}
	}
}

// drawSelection marks the selected cells of the pane drawn at area in the
// palette's selection colours.
func (m *Model) drawSelection(p *painter, area uv.Rectangle) {
	if !m.sel.shown() {
		return
	}
	from, to := m.sel.span()
	for y := max(from.Y, 0); y <= to.Y && y < area.Dy(); y++ {
		for x := 0; x < area.Dx(); x++ {
			if !m.sel.covers(x, y) {
				continue
			}
			if c := p.buf.CellAt(area.Min.X+x, area.Min.Y+y); c != nil {
				c.Style.Fg, c.Style.Bg = p.pal.selFg, p.pal.selBg
				c.Style.Attrs &^= uv.AttrReverse
			}
		}
	}
}
