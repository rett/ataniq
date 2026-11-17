package terminalconsole

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// fixtureLabel is on the header and on the session frame in every state: the
// cockpit never shows a recording as a live session.
const fixtureLabel = "fixture recording, not a live session"

const tagline = "Provider-neutral leads · bounded delegates · supervised authority"

// cockpitView draws the cockpit and says what it needs of the outer terminal.
// It always asks for mouse reporting, so that a drag selects text in the
// session pane only (see onMouse): cell motion, or all motion in session
// focus when the session asked for all motion. Focus events are asked for
// only in session focus and only when the session asked for them, and the
// session's cursor is placed in session focus. A session that has ended may
// still hold the focus until the next key, but the pane then reports no
// modes and no visible cursor, so none is requested or placed.
func (m *Model) cockpitView() tea.View {
	l := m.layout()
	buf := uv.NewBuffer(l.canvas.Dx(), l.canvas.Dy())
	p := &painter{buf: buf, pal: paletteFor(m.theme), graphemes: m.graphemes}
	p.fill(l.canvas)
	if l.zoom {
		m.drawZoom(p, l)
	} else {
		m.drawHeader(p, l)
		m.drawTree(p, l)
		m.drawContext(p, l)
		m.drawMeta(p, l)
		m.drawSession(p, l)
		m.drawFocusBar(p, l)
		m.drawEvidence(p, l)
		m.drawTimeline(p, l)
		m.drawKeys(p, l)
		m.drawOverlay(p, l)
	}
	v := m.newView(buf.Render())
	v.MouseMode = tea.MouseModeCellMotion
	if m.focus == sessionFocus && m.rep.pane != nil {
		modes := m.rep.pane.Modes()
		if modes.Mouse >= uv.MouseModeMotion {
			v.MouseMode = tea.MouseModeAllMotion
		}
		v.ReportFocus = modes.FocusEvents
		if pos, ok := m.rep.pane.Cursor(); ok && pos.X < l.inner.Dx() && pos.Y < l.inner.Dy() {
			v.Cursor = tea.NewCursor(l.inner.Min.X+pos.X, l.inner.Min.Y+pos.Y)
		}
	}
	return v
}

func (m *Model) focusName() string {
	if m.focus == sessionFocus {
		return "SESSION FOCUS"
	}
	return "CONSOLE FOCUS"
}

func (m *Model) drawHeader(p *painter, l layout) {
	left := "ATANIQ / DEMO FIXTURE · cockpit · " + fixtureLabel
	n := p.text(0, 0, l.header.Dx(), left, p.style(lnHead))
	if room := l.header.Dx() - n - 2; room >= ansi.StringWidth(tagline) {
		p.text(l.header.Dx()-ansi.StringWidth(tagline), 0, room, tagline, p.style(lnDim))
	}
}

// drawZoom draws the zoomed session terminal under a one-line header: the
// focus, its keys and the fixture label. Zoom has no key bar, so a notice
// goes first in the header. The fixture label always stays; the keys stay
// when the line has room for them too.
func (m *Model) drawZoom(p *painter, l layout) {
	keys := m.focusName() + " · return " + m.opts.ReturnKey.String() + " · F12 leaves zoom"
	if m.focus == consoleFocus {
		keys = m.focusName() + " · Enter focuses the session · F12 leaves zoom"
	}
	label := " · " + fixtureLabel
	w := l.header.Dx()
	if m.notice == "" {
		p.text(0, 0, w, keys+label, p.style(lnHead))
	} else {
		x := p.text(0, 0, w-ansi.StringWidth(label), m.notice, p.style(lnWarn))
		rest := label
		if withKeys := " · " + keys + label; x+ansi.StringWidth(withKeys) <= w {
			rest = withKeys
		}
		p.text(x, 0, w-x, rest, p.style(lnHead))
	}
	m.drawPane(p, l.inner)
}

// treeRow is a row of the projects and leads tree.
type treeRow struct {
	text       string
	selectable bool
	project    int
	lead       int
	indent     int
	delegate   bool
}

func (m *Model) treeRows() []treeRow {
	var rows []treeRow
	for pi, pr := range m.projects {
		rows = append(rows, treeRow{text: pr.Name, selectable: true, project: pi, lead: m.leadSelections[pi]})
		for li, ld := range pr.Leads {
			rows = append(rows, treeRow{text: ld.Name, selectable: true, project: pi, lead: li, indent: 1})
			for _, s := range ld.Subagents {
				rows = append(rows, treeRow{text: s, indent: 2, delegate: true})
			}
		}
	}
	return rows
}

func (m *Model) drawTree(p *painter, l layout) {
	focused := m.pane == paneTree
	p.box(l.tree, "Projects & leads", "", focused)
	in := inset(l.tree)
	rows := m.treeRows()
	if len(rows) == 0 {
		p.lines(in, []line{{"No projects", lnDim}})
		return
	}
	cursor := min(m.treeCursor, len(rows)-1)
	start := max(0, cursor-(in.Dy()-1))
	var ls []line
	for i := start; i < len(rows) && len(ls) < in.Dy(); i++ {
		r := rows[i]
		mark := "  "
		if i == cursor {
			mark = "> "
		}
		k := lnText
		switch {
		case r.delegate:
			k = lnDim
		case i == cursor && focused:
			k = lnCursor
		}
		name := r.text
		switch {
		case r.delegate:
			name = "└ " + name
		case r.indent == 0:
			name = "▾ " + name
		case r.project == m.project && r.lead == m.lead:
			name = "● " + name
		default:
			name = "○ " + name
		}
		ls = append(ls, line{mark + strings.Repeat("  ", r.indent) + name, k})
	}
	p.lines(in, ls)
}

// selected returns the selected project and lead names and the lead's
// session, empty when there are none.
func (m *Model) selected() (project, lead string, s Session) {
	if len(m.projects) == 0 {
		return
	}
	pr := m.projects[m.project]
	project = pr.Name
	if m.lead < len(pr.Leads) {
		lead, s = pr.Leads[m.lead].Name, pr.Leads[m.lead].Session
	}
	return
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (m *Model) drawContext(p *painter, l layout) {
	p.box(l.context, "Context", "", false)
	project, lead, s := m.selected()
	p.lines(inset(l.context), []line{
		{"Project: " + orNone(project), lnText}, {"Lead: " + orNone(lead), lnText}, {"Provider: " + orNone(s.Provider), lnText},
		{"Operator: " + orNone(m.operator), lnText}, {"Authority: " + orNone(s.Authority), lnText}, {"Runtime: " + orNone(s.Runtime), lnText},
	})
}

func (m *Model) drawMeta(p *painter, l layout) {
	p.box(l.meta, "Session", "", false)
	project, lead, s := m.selected()
	title, blocked := "none", 0
	if m.rep.pane != nil {
		title, blocked = orNone(m.rep.pane.Title()), m.rep.pane.ClipboardBlocked()
	}
	p.lines(inset(l.meta), []line{
		{"Project: " + orNone(project) + "   Lead: " + orNone(lead), lnText},
		{"Provider: " + orNone(s.Provider) + "   Host: " + orNone(s.Host), lnText},
		{"Session: " + orNone(s.ID), lnText},
		{"Attachment: " + orNone(s.Attachment), lnWarn},
		{"Title: " + title + "   Clipboard requests blocked: " + strconv.Itoa(blocked), lnDim},
	})
}

func (m *Model) drawSession(p *painter, l layout) {
	title := "SESSION TERMINAL"
	if m.focus == sessionFocus {
		title += " · SESSION FOCUS"
	}
	p.box(l.frame, title, fixtureLabel, m.pane == paneSession || m.focus == sessionFocus)
	m.drawPane(p, l.inner)
}

// drawPane draws the session pane in area, in the cockpit's colours. A session
// that has ended keeps its last screen, dimmed, under a banner. The pane is
// not resized after it has ended, so a zoom or a window resize shows the old
// size cut or padded under the banner.
func (m *Model) drawPane(p *painter, area uv.Rectangle) {
	if m.rep.pane == nil {
		p.centred(area, "NO RECORDING", []line{{"No fixture recording named " + clean(m.opts.Recording) + ".", lnWarn}})
		return
	}
	m.rep.pane.Draw(p.buf, area)
	ended, status, known := m.rep.pane.Ended()
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			c := p.buf.CellAt(x, y)
			if c == nil {
				continue
			}
			if c.Style.Fg == nil {
				c.Style.Fg = p.pal.fg
			}
			if c.Style.Bg == nil {
				c.Style.Bg = p.pal.bg
			}
			if ended {
				c.Style.Attrs |= uv.AttrFaint
			}
		}
	}
	m.drawSelection(p, area)
	if !ended {
		return
	}
	code := "unknown"
	if known {
		code = strconv.Itoa(status)
	}
	last := "r replays the recording."
	if m.focus == sessionFocus {
		last = "Press any key to return to the console."
	}
	p.centred(area, "TERMINAL STREAM ENDED", []line{
		{"The recorded terminal stream ended. Exit status: " + code + ".", lnText},
		{"That is the stream, not necessarily the agent: a Codex terminal can exit while its daemon keeps running.", lnDim},
		{last, lnWarn},
	})
}

func (m *Model) drawFocusBar(p *painter, l layout) {
	p.box(l.focus, "", "", false)
	in := inset(l.focus)
	first := m.focusName() + " · return key " + m.opts.ReturnKey.String() + " · F12 zoom"
	second := "Enter focuses the session · a full attach (unavailable)"
	if m.focus == sessionFocus {
		second = "Every key goes to the session; q and Ctrl+C do not quit"
	}
	if m.sessionEnded() {
		second = "The terminal stream has ended"
	}
	p.lines(in, []line{{first, lnHead}, {second, lnDim}})
}

// stateKind styles a state: one that says unqualified, unavailable or pending
// is flagged.
func stateKind(state string) lineKind {
	s := strings.ToLower(state)
	if strings.Contains(s, "unqualified") || strings.Contains(s, "unavailable") || strings.Contains(s, "pending") {
		return lnWarn
	}
	return lnText
}

// evidenceLines returns the lines of the Evidence & approvals pane wrapped to
// width: the attachment contract, the pending gate and the lead and its
// delegates.
func (m *Model) evidenceLines(width int) []line {
	var out []line
	add := func(k lineKind, s string) {
		for _, w := range wrapLines(s, width) {
			out = append(out, line{w, k})
		}
	}
	add(lnHead, "ATTACHMENT CONTRACT")
	if len(m.contract) == 0 {
		add(lnDim, "No contract data")
	}
	for _, c := range m.contract {
		add(stateKind(c.State), c.Name+": "+c.State)
	}
	add(lnText, "")
	add(lnHead, "PENDING GATE")
	for _, f := range []struct{ label, value string }{{"Action", m.gate.Action}, {"Scope", m.gate.Scope}, {"Target", m.gate.Target}, {"Approver", m.gate.Approver}} {
		add(lnText, f.label+": "+orNone(f.value))
	}
	add(stateKind(m.gate.State), "State: "+orNone(m.gate.State))
	add(lnDim, "Approve and deny are unavailable. Ctrl+K lists each control with its reason.")
	add(lnText, "")
	add(lnHead, "LEAD & DELEGATE")
	_, _, s := m.selected()
	add(stateKind(s.Binding), "Binding: "+orNone(s.Binding))
	add(stateKind(s.Execution), "Execution: "+orNone(s.Execution))
	if len(m.projects) > 0 && m.lead < len(m.projects[m.project].Leads) {
		subs := m.projects[m.project].Leads[m.lead].Subagents
		if len(subs) == 0 {
			add(lnDim, "Delegates: none")
		}
		for _, d := range subs {
			add(lnText, "Delegate: "+d)
		}
	}
	return out
}

func (m *Model) drawEvidence(p *painter, l layout) {
	p.box(l.evidence, "Evidence & approvals", "", m.pane == paneEvidence)
	in := inset(l.evidence)
	ls := m.evidenceLines(in.Dx())
	start := min(m.scrolls[paneEvidence], max(0, len(ls)-1))
	p.lines(in, ls[start:])
}

func (m *Model) drawTimeline(p *painter, l layout) {
	p.box(l.timeline, "Timeline", "", m.pane == paneTimeline)
	in := inset(l.timeline)
	if len(m.timeline) == 0 {
		p.lines(in, []line{{"No events", lnDim}})
		return
	}
	// The newest entry is last; scrolling goes back in time.
	end := len(m.timeline) - min(m.scrolls[paneTimeline], len(m.timeline)-1)
	start := max(0, end-in.Dy())
	var ls []line
	for _, e := range m.timeline[start:end] {
		ls = append(ls, line{e.Time + "  " + e.Text, lnText})
	}
	p.lines(in, ls)
}

func (m *Model) drawKeys(p *painter, l layout) {
	if m.notice != "" {
		p.text(0, l.keys.Min.Y, l.keys.Dx(), m.notice, p.style(lnWarn))
		return
	}
	s := "Tab pane · j/k move · Enter focus · a attach · Ctrl+K actions · r replay · ? help · q quit"
	if m.focus == sessionFocus {
		s = "SESSION FOCUS · every key goes to the session · " + m.opts.ReturnKey.String() + " returns · F12 zoom"
	}
	p.text(0, l.keys.Min.Y, l.keys.Dx(), s, p.style(lnDim))
}

// overlayLines returns the title and wrapped lines of the open overlay.
func (m *Model) overlayLines(width int) (string, []line) {
	var out []line
	add := func(k lineKind, s string) {
		for _, w := range wrapLines(s, width) {
			out = append(out, line{w, k})
		}
	}
	if m.overlay == "actions" {
		if len(m.controls) == 0 {
			add(lnWarn, m.controlUnavailable)
		}
		for _, c := range m.controls {
			head := c.Name
			if c.Key != "" {
				head += " [" + c.Key + "]"
			}
			add(lnHead, head)
			add(lnWarn, "Unavailable: "+c.Reason)
		}
		return "ACTIONS (all unavailable)", out
	}
	add(lnHead, "Console focus")
	for _, s := range []string{
		"Tab, Shift+Tab: next or previous pane",
		"Up, Down, j, k: move in the pane",
		"Enter: select in the tree, or focus the session",
		"a: full attach (unavailable)",
		"Ctrl+K: actions, all unavailable with their reasons",
		"r: replay the recording from its start",
		"F12: zoom the session pane to the window",
		"?: this help   Esc: close   q, Ctrl+C: quit",
	} {
		add(lnText, s)
	}
	add(lnHead, "Session focus")
	add(lnText, "Every key goes to the session, q and Ctrl+C included.")
	add(lnText, m.opts.ReturnKey.String()+" returns to the console; F12 zooms. In a terminal without the kitty keyboard protocol, Ctrl+4 sends the same byte as Ctrl+\\ and returns too.")
	add(lnHead, "Mouse")
	add(lnText, "Drag in the session terminal to select its text; the release copies it to the clipboard (OSC 52, up to 1 MiB). A click clears the selection.")
	add(lnText, "In session focus a session that asked for the mouse gets it instead.")
	add(lnText, "Shift+drag selects across the window in most terminals.")
	add(lnHead, "Fixture")
	add(lnText, "This is "+fixtureLabel+". Nothing typed reaches a process.")
	return "HELP", out
}

// overlayBox is where the open overlay is drawn: the middle of the window,
// as high as its text needs up to the window less four rows.
func (m *Model) overlayBox(l layout) uv.Rectangle {
	w := min(l.canvas.Dx()-4, 78)
	_, ls := m.overlayLines(w - 4)
	h := min(l.canvas.Dy()-4, len(ls)+2)
	return uv.Rect((l.canvas.Dx()-w)/2, (l.canvas.Dy()-h)/2, w, h)
}

func (m *Model) drawOverlay(p *painter, l layout) {
	if m.overlay == "" {
		return
	}
	box := m.overlayBox(l)
	title, ls := m.overlayLines(box.Dx() - 4)
	p.box(box, title, "Esc closes", true)
	in := uv.Rect(box.Min.X+2, box.Min.Y+1, box.Dx()-4, box.Dy()-2)
	start := min(m.overlayScroll, max(0, len(ls)-1))
	p.lines(in, ls[start:])
}
