package terminalconsole

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// focusState says where keys go.
type focusState int

const (
	// consoleFocus sends keys to the console: panes, lists, help.
	consoleFocus focusState = iota
	// sessionFocus sends every key to the session except the return key and
	// the zoom key. That includes q and Ctrl+C, which never quit from here.
	sessionFocus
)

// The cockpit's panes, in Tab order.
const (
	paneTree = iota
	paneSession
	paneEvidence
	paneTimeline
	paneCount
)

// zoomKey toggles the session pane to the whole window, in either focus
// state. F12 is free in the terminals and window managers checked.
const zoomKey = tea.KeyF12

// maxPaste bounds a paste forwarded to the session.
const maxPaste = 1 << 20

// pasteEnd ends a bracketed paste. A pasted text that carried it could end the
// paste early and have the rest taken as typed input.
const pasteEnd = "\x1b[201~"

// cockpitState is the cockpit's part of the model.
type cockpitState struct {
	focus   focusState
	pane    int
	zoom    bool
	overlay string // "", "help" or "actions"
	// overlayScroll is the first line of the overlay shown.
	overlayScroll int
	treeCursor    int
	scrolls       [paneCount]int
	// graphemes is whether the outer terminal measures grapheme clusters
	// (mode 2027), as its answer to the query in Init says.
	graphemes bool
	rep       replay
	// now is the time of the latest tick: the cockpit's clock, which the
	// pane's frame time limit reads.
	now time.Time
	// after makes a command that sends a message once d has passed. It is
	// tea.Tick, and a test's own clock.
	after     func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd
	resizeSeq int
	sized     bool
	started   bool
	// sel is the text selected with the mouse in the session pane.
	sel selection
}

// resizeMsg applies a debounced window resize to the pane.
type resizeMsg struct{ seq int }

// resizeDelay is how long a window size must stay unchanged before the pane
// follows it. The value is provisional until the sandbox's resize behaviour
// with real agents is known.
const resizeDelay = 120 * time.Millisecond

func (m *Model) onSize() tea.Cmd {
	l := m.layout()
	if !l.cockpit && m.focus == sessionFocus {
		m.focus = consoleFocus
	}
	m.sel = selection{}
	var cmds []tea.Cmd
	if !m.started {
		m.started = true
		cmds = append(cmds, m.tickCmd())
	}
	if cmd := m.syncPane(m.sized); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// syncPane makes the pane follow the layout: at once, or after the debounce
// delay. A window that is too small for the cockpit leaves the pane alone.
func (m *Model) syncPane(debounce bool) tea.Cmd {
	l := m.layout()
	if !l.cockpit || m.rep.pane == nil {
		return nil
	}
	m.resizeSeq++
	if !debounce {
		m.sized = true
		m.rep.pane.Resize(l.inner.Dx(), l.inner.Dy())
		return nil
	}
	if cols, rows := m.rep.pane.Size(); cols == min(l.inner.Dx(), paneMaxCols) && rows == min(l.inner.Dy(), paneMaxRows) {
		return nil
	}
	seq := m.resizeSeq
	return m.after(resizeDelay, func(time.Time) tea.Msg { return resizeMsg{seq: seq} })
}

func (m *Model) onResize(v resizeMsg) {
	if v.seq == m.resizeSeq {
		m.syncPane(false)
	}
}

// onKey routes a key press: to the single-column pages below the cockpit's
// size, else to the cockpit.
func (m *Model) onKey(v tea.KeyPressMsg) tea.Cmd {
	if !m.layout().cockpit {
		return m.handleFallbackKey(v)
	}
	if v.Code == zoomKey && withoutLocks(v.Mod) == 0 {
		m.zoom = !m.zoom
		m.overlay = ""
		m.sel = selection{}
		return m.syncPane(false)
	}
	if m.focus == sessionFocus {
		return m.sessionKey(v)
	}
	return m.consoleKey(v)
}

// sessionKey handles a key while the session has focus. Nothing but the
// return key and the zoom key is the console's: q and Ctrl+C go to the
// session. On a session that has ended every key returns to the console.
func (m *Model) sessionKey(v tea.KeyPressMsg) tea.Cmd {
	if m.rep.pane == nil || m.sessionEnded() || m.opts.ReturnKey.matches(v) {
		m.focus = consoleFocus
		return nil
	}
	m.sel = selection{}
	m.rep.pane.Key(uv.KeyPressEvent(v))
	return nil
}

func (m *Model) sessionEnded() bool {
	if m.rep.pane == nil {
		return false
	}
	ended, _, _ := m.rep.pane.Ended()
	return ended
}

// consoleKey handles a key while the console has focus.
func (m *Model) consoleKey(v tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	key := v.String()
	if key == "q" || key == "ctrl+c" {
		return m.quit()
	}
	// Zoom draws no help or action list, so the keys that would open one do
	// nothing there. Without this an invisible overlay would take the next
	// Enter. Nor does a, whose notice explains a control of the action list.
	if m.zoom && (key == "?" || key == "ctrl+k" || key == "a") {
		return nil
	}
	if m.overlay != "" {
		switch key {
		case "esc", "enter", "?", "ctrl+k":
			m.overlay = ""
		case "up", "k", "down", "j":
			l := m.layout()
			_, ls := m.overlayLines(m.overlayBox(l).Dx() - 4)
			delta := 1
			if key == "up" || key == "k" {
				delta = -1
			}
			m.overlayScroll = min(max(0, m.overlayScroll+delta), max(0, len(ls)-1))
		}
		return nil
	}
	switch key {
	case "?":
		m.overlay, m.overlayScroll = "help", 0
	case "ctrl+k":
		m.overlay, m.overlayScroll = "actions", 0
	case "a":
		m.notice = m.unavailable("a")
	case "r":
		return m.restart()
	case "tab", "shift+tab":
		if !m.zoom {
			step := 1
			if key == "shift+tab" {
				step = paneCount - 1
			}
			m.pane = (m.pane + step) % paneCount
		}
	case "enter":
		m.enter()
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	}
	return nil
}

// enter acts on the focused pane: it selects in the tree and gives the
// session the focus.
func (m *Model) enter() {
	if m.zoom || m.pane == paneSession {
		if m.layout().cockpit {
			m.focus = sessionFocus
		}
		return
	}
	if m.pane != paneTree {
		return
	}
	rows := m.treeRows()
	if m.treeCursor < len(rows) && rows[m.treeCursor].selectable {
		r := rows[m.treeCursor]
		m.project, m.lead = r.project, r.lead
		m.projectCursor, m.leadCursor = r.project, r.lead
		m.leadSelections[r.project] = r.lead
	}
}

// move moves the tree cursor or scrolls the focused pane. A scroll of the
// timeline counts back from the newest entry.
func (m *Model) move(delta int) {
	if m.zoom {
		return
	}
	switch m.pane {
	case paneTree:
		// Delegates are shown but cannot be selected: the cursor skips them.
		rows := m.treeRows()
		for i := m.treeCursor + delta; i >= 0 && i < len(rows); i += delta {
			if rows[i].selectable {
				m.treeCursor = i
				break
			}
		}
	case paneEvidence:
		m.scrolls[paneEvidence] = min(max(0, m.scrolls[paneEvidence]+delta), max(0, len(m.evidenceLines(m.layout().evidence.Dx()-2))-1))
	case paneTimeline:
		m.scrolls[paneTimeline] = min(max(0, m.scrolls[paneTimeline]-delta), max(0, len(m.timeline)-1))
	}
}

func (m *Model) onPaste(text string) {
	if m.focus != sessionFocus || m.rep.pane == nil || !m.layout().cockpit {
		return
	}
	if len(text) > maxPaste {
		m.notice = "Paste not sent: it is longer than the console forwards."
		return
	}
	m.sel = selection{}
	m.rep.pane.Paste(stripPasteEnd(text))
}

// stripPasteEnd removes every ESC [ 2 0 1 ~ from text, including one that
// removing another makes.
func stripPasteEnd(text string) string {
	for strings.Contains(text, pasteEnd) {
		text = strings.ReplaceAll(text, pasteEnd, "")
	}
	return text
}

// onModeReport learns that the outer terminal measures grapheme clusters,
// from its answer to the mode 2027 query. Bubble Tea switches its own width
// method on the same answer.
func (m *Model) onModeReport(v tea.ModeReportMsg) {
	if v.Mode != ansi.ModeUnicodeCore {
		return
	}
	switch v.Value {
	case ansi.ModeSet, ansi.ModeReset, ansi.ModePermanentlySet:
		m.graphemes = true
		if m.rep.pane != nil {
			m.rep.pane.SetGraphemeWidths(true)
		}
	}
}
