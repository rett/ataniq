package terminalconsole

import (
	"strconv"

	uv "github.com/charmbracelet/ultraviolet"
)

// The panels of live mode. They show what the source reports and nothing
// else: no project, lead list, gate or timeline of the fixture, and no
// invented data. Text from the source has been through clean.

// liveScreenOnly says what a client gets of the session.
const liveScreenOnly = "The holder sends the visible screen only, with no scrollback. Recent rows are kept locally after attach."

// drawLiveTree draws the lead and its holder where the fixture draws its
// projects and leads.
func (m *Model) drawLiveTree(p *painter, l layout) {
	p.box(l.tree, "Live session", "", m.pane == paneTree)
	st := m.live.st
	lead := st.Lead
	if lead == "" {
		lead = "unknown"
	}
	p.lines(inset(l.tree), []line{
		{"● " + lead, lnCursor},
		{"  runtime: " + m.liveRuntimeText(), lnText},
		{"  role: " + m.liveRoleText(), lnText},
		{"  epoch: " + m.liveEpochText(), lnText},
		{"  " + st.Conn.String(), stateKind(st.Conn.String())},
		{"", lnText},
		{"Not daemon-managed.", lnDim},
		{"No projects.", lnDim},
	})
}

func (m *Model) liveEpochText() string {
	if m.live.st.Epoch == 0 {
		return "unknown"
	}
	return strconv.FormatUint(m.live.st.Epoch, 10)
}

// drawLiveContext says plainly that there is nothing else to show.
func (m *Model) drawLiveContext(p *painter, l layout) {
	p.box(l.context, "Context", "", false)
	in := inset(l.context)
	var ls []line
	for _, w := range wrapLines("A live scratch session through a holder, not managed by the daemon. No project data here: only what the holder reports is shown.", in.Dx()) {
		ls = append(ls, line{w, lnDim})
	}
	p.lines(in, ls)
}

func (m *Model) drawLiveMeta(p *painter, l layout) {
	p.box(l.meta, "Session", "", false)
	st := m.live.st
	lead := st.Lead
	if lead == "" {
		lead = "unknown"
	}
	title, blocked := orNone(m.liveTitle()), 0
	paneSize := "none"
	if m.rep.pane != nil {
		blocked = m.rep.pane.ClipboardBlocked()
		c, r := m.rep.pane.Size()
		paneSize = sizeText(c, r)
	}
	conn := st.Conn.String()
	if st.Reason != "" {
		conn += ": " + st.Reason
	}
	p.lines(inset(l.meta), []line{
		{"Lead: " + lead + "   Role: " + m.liveRoleText() + "   Epoch: " + m.liveEpochText(), lnText},
		{"Runtime: " + m.liveRuntimeText() + "   Holder screen: " + m.liveSizeText() + "   Pane: " + paneSize, lnText},
		{"Connection: " + conn, stateKind(conn)},
		{m.liveCountersText(), lnDim},
		{"Title: " + title + "   Clipboard requests blocked: " + strconv.Itoa(blocked), lnDim},
	})
}

func (m *Model) drawLiveFocusBar(p *painter, in uv.Rectangle) {
	first := m.focusName() + " · return key " + m.opts.ReturnKey.String() + " · F12 zoom"
	second := "Enter focuses the session · " + m.liveRoleText()
	why, _ := m.liveInputStop()
	switch {
	case m.sessionEnded():
		second = "The session has ended"
	case why != "":
		second = "Keys typed in session focus are not sent: " + why
	case m.focus == sessionFocus:
		second = "Every key goes to the session; q and Ctrl+C do not quit"
	}
	p.lines(in, []line{{first, lnHead}, {second, lnDim}})
}

// liveEvidenceLines returns the lines of the evidence pane in live mode: the
// attachment contract as it holds here, the end facts and counters, and the
// notices, newest first.
func (m *Model) liveEvidenceLines(width int) []line {
	var out []line
	add := func(k lineKind, s string) {
		for _, w := range wrapLines(s, width) {
			out = append(out, line{w, k})
		}
	}
	add(lnHead, "ATTACHMENT CONTRACT")
	add(lnText, "One writer, no takeover.")
	add(lnText, "Input is the owner's direct words.")
	add(lnText, "A read-only viewer sends nothing: its keys, paste and size are not sent, and the holder refuses them.")
	add(lnText, "Role granted: "+m.liveRoleText())
	add(lnText, liveScreenOnly)
	add(lnText, "")
	add(lnHead, "HOLDER")
	st := m.live.st
	add(lnText, "Connection: "+st.Conn.String())
	if st.Reason != "" {
		add(lnWarn, "Reason: "+st.Reason)
	}
	add(lnText, "Check mismatches: "+strconv.FormatUint(st.CheckMismatches, 10))
	add(lnText, "Resyncs: "+strconv.FormatUint(st.Resyncs, 10))
	add(lnText, "Refused input: "+strconv.FormatUint(st.RefusedInput, 10))
	for _, c := range st.Counters {
		add(lnText, c.Name+": "+strconv.FormatUint(c.Value, 10))
	}
	if over := m.live.countersCut; over > 0 {
		add(lnWarn, strconv.Itoa(over)+" more counters not shown (the limit is "+strconv.Itoa(LiveCounterLimit)+")")
	}
	add(lnText, m.liveIdleText())
	if ended, _, _ := m.paneEnded(); ended {
		add(lnHead, "END, AS REPORTED")
		for _, f := range factsText(st.End) {
			add(lnText, f)
		}
	}
	add(lnText, "")
	add(lnHead, "NOTICES")
	if len(m.live.notices) == 0 {
		add(lnDim, "No notices")
	}
	for i := len(m.live.notices) - 1; i >= 0; i-- {
		n := m.live.notices[i]
		add(lnText, "["+n.code+"] "+n.text)
	}
	add(lnText, "")
	add(lnHead, "NOT MANAGED BY THE DAEMON")
	add(lnDim, "The daemon (ataniqd) does not manage this session: it has no project, gate or delegate data. Ctrl+K lists what is unavailable.")
	return out
}

// liveHelpLines returns the help of live mode.
func (m *Model) liveHelpLines(width int) (string, []line) {
	var out []line
	add := func(k lineKind, s string) {
		for _, w := range wrapLines(s, width) {
			out = append(out, line{w, k})
		}
	}
	add(lnHead, "Console focus")
	for _, s := range []string{
		"Tab, Shift+Tab: next or previous pane",
		"Up, Down, j, k: move in the pane",
		"Enter: focus the session",
		"Ctrl+K: actions, all unavailable with their reasons",
		"F12: zoom the session pane to the window",
		"?: this help   Esc: close   q, Ctrl+C: quit",
	} {
		add(lnText, s)
	}
	add(lnHead, "Session focus")
	add(lnText, "As the writer, every key goes to the session, q and Ctrl+C included. Ctrl+Z is not forwarded by the holder.")
	add(lnText, m.opts.ReturnKey.String()+" returns to the console; F12 zooms. As a read-only viewer no key is sent.")
	add(lnHead, "Mouse")
	add(lnText, "Drag in the session terminal to select its text; the release copies it to the clipboard (OSC 52, up to 1 MiB). A click clears the selection.")
	add(lnText, "In session focus as the writer, a session that asked for the mouse gets it instead.")
	add(lnHead, "Live scratch session")
	add(lnText, "This is a "+m.liveLabel()+", through a holder.")
	add(lnText, "The daemon (ataniqd) does not manage it, and there is no project data.")
	add(lnText, "There is no replay.")
	add(lnText, liveScreenOnly)
	return "HELP", out
}

// liveReselectTitle is what the header says when the client stopped because
// the lead's epoch changed: the pin no longer holds, so the owner has to
// select the lead again. The old lead and epoch stay in the status, and the
// header shows them.
const liveReselectTitle = "EPOCH CHANGED · select the lead again"

// liveReselect reports whether the client stopped for reselection.
func (m *Model) liveReselect() bool {
	return m.live.st.Conn == LiveStoppedReselect
}

// liveOldText names the lead and the epoch the client was pinned to.
func (m *Model) liveOldText() string {
	lead := m.live.st.Lead
	if lead == "" {
		lead = "unknown"
	}
	return "lead " + lead + " · epoch " + m.liveEpochText()
}

// liveCockpitHeader returns the header row of the cockpit layout for a
// reselect stop, or while resyncing or waiting for a renewed ticket, and ""
// for every other state, which keeps the usual header. The state comes
// before the variable-length text, so that a narrow window cuts the lead and
// the epoch first.
func (m *Model) liveCockpitHeader() string {
	st := m.live.st
	switch st.Conn {
	case LiveStoppedReselect:
		return "q detach · " + liveReselectTitle + " · " + m.liveOldText()
	case LiveResyncing, LiveWaitingTicket:
		state := st.Conn.String()
		if st.Reason != "" {
			state += ": " + st.Reason
		}
		return "ATANIQ / LIVE SCRATCH · " + m.liveRoleText() + " · " + state + " · " + m.liveOldText()
	}
	return ""
}

// liveLines reserves two rows above the session. Keep the detach hint,
// role and connection ahead of variable-length text on a phone display. A
// reselect stop puts its title on the first row, with the detach hint, and
// the old lead and epoch on the second.
func (m *Model) liveLines() []string {
	st := m.live.st
	header := "q detach · " + m.liveRoleText() + " · "
	if m.live.startErr != "" {
		return []string{header + "unavailable", "LIVE SESSION UNAVAILABLE: " + m.live.startErr}
	}
	header += st.Conn.String()
	lead := st.Lead
	if lead == "" {
		lead = "unknown"
	}
	detail := "Live scratch session · lead " + lead + " · epoch " + m.liveEpochText()
	switch {
	case m.liveReselect():
		header = "q detach · " + liveReselectTitle
		detail = "was " + m.liveOldText() + " · " + m.liveRoleText()
	case m.sessionEnded():
		detail = "SESSION ENDED · " + detail
	case (m.liveDown() || st.Conn == LiveResyncing) && st.Reason != "":
		detail = st.Reason
	}
	lines := []string{header, detail}
	area := m.liveArea()
	if area.Empty() || m.rep.pane == nil {
		return lines
	}
	buf := uv.NewBuffer(area.Dx(), area.Dy())
	if page, pages := m.rep.pane.DrawViewport(buf, buf.Bounds()); page != 0 {
		lines[1] = "Recent output since attach · page " + strconv.Itoa(page) + "/" + strconv.Itoa(pages)
	}
	for y := 0; y < buf.Height(); y++ {
		lines = append(lines, buf.Line(y).Render())
	}
	return lines
}
