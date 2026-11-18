package terminalconsole

import (
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/sessionpane"
)

// Live mode. With Options.Live set the cockpit shows a live scratch session
// that a holder keeps, in place of the fixture replay. This package may not
// import net or os, so the attach client that talks to the holder lives
// elsewhere and is written against LiveSource, which is all the cockpit knows
// of it (the lead-hosting design, section 2). Nothing here reads a clock,
// polls or opens anything: the source tells the program when something
// changed, and the cockpit reads the source's status and notices then and on
// each redraw.
//
// Who owns what:
//
//   - The cockpit makes the session pane and gives it to the source in Start.
//     The source feeds it (Write, End) and owns its size: the cockpit never
//     calls Resize on a live pane. The source calls pane.Resize when the
//     holder says its screen has another size, or on a snapshot.
//   - The cockpit tells the source the size it wants with Resize, and only
//     as the writer with the connection up (see Resize). A viewer draws the
//     holder's size, cut or padded at the pane area's top left corner.
//   - What the pane sends (keys, paste, mouse) goes to the sink that Start
//     returned. A viewer's input is not sent at all.
//   - Mouse and paste requests to the outer terminal follow LiveStatus.Modes,
//     never the pane's own emulator. Focus reports are not asked for in live
//     mode: InputSink has no focus method, so there is nothing to forward
//     them to. That comes with a sink method later.

// LiveSource is a live session the cockpit shows. An implementation needs
// nothing from this package but the types named here, and sessionpane's
// Pane, InputSink, Colours and Modes.
//
// Status and Notices are called on the program's goroutine, Status on every
// redraw, so both must be cheap; every method is safe for concurrent use.
// The source reports a change by calling LiveStart.Wake.
type LiveSource interface {
	// Start hands over the pane and the initial size and colours, and returns
	// the sink the pane sends input to. It is called once, when the first
	// window size is known, before anything else. A nil sink discards input.
	// An error leaves the cockpit saying so. Start must not block on the
	// network: connecting happens afterwards and shows in the status.
	Start(LiveStart) (sessionpane.InputSink, error)
	// Resize asks for a pane of cols by rows, the size of the area the
	// cockpit has for the session, at most the pane's own limits. It must
	// not block. The holder decides the size: the source follows the
	// holder's answer in the pane.
	//
	// The rule: the cockpit calls Resize whenever it is the writer (Role is
	// LiveWriter), the connection is up (Conn is neither LiveDisconnected
	// nor LiveWaitingTicket), and the size it wants differs from the
	// holder's reported size (LiveStatus.Cols and Rows, when known) or from
	// the size it last asked for. It checks this when the window size
	// settles (after its 120 ms debounce), at once for zoom, when the role
	// becomes writer, and when the connection comes back up as the writer.
	// So the source may be called again with the size it was already given,
	// for example when a writer gets the role back and the holder has
	// another size; it must tolerate that, and may ignore a size the holder
	// already has. The cockpit never calls it for a viewer, a client with no
	// role or a connection that is down; a size wanted meanwhile is asked
	// for when the writer is back.
	Resize(cols, rows int)
	// Status is the current state, read on each redraw.
	Status() LiveStatus
	// Notices returns the notices that arrived since the last call, oldest
	// first, and forgets them. The cockpit calls it after every wake-up and
	// keeps at most LiveNoticeLimit; a source keeps a bounded backlog.
	Notices() []LiveNotice
	// Close ends the session's connection. It is called once, at the end of
	// the program, if Start was called; the cockpit closes the pane after.
	Close()
}

// LiveStart is what the cockpit gives a LiveSource in Start.
type LiveStart struct {
	// Pane is the session pane the source feeds. Its size is Cols by Rows.
	Pane *sessionpane.Pane
	// Cols and Rows are the size the cockpit wants for the session: its area
	// in the window, clamped to the pane's limits.
	Cols, Rows int
	// Colours are the colours of the cockpit's theme, for the writer's
	// Hello: what the holder answers to the session's colour queries.
	Colours sessionpane.Colours
	// Wake tells the program that the pane, the status or the notices changed.
	// It never blocks, may be called from any goroutine and any number of
	// times: calls that arrive before the program has looked coalesce, and
	// the program redraws at most about 60 times a second however often it is
	// called. It does nothing after Close.
	Wake func()
}

// LiveRole is the role the holder granted.
type LiveRole string

// The roles. The zero value is no role yet: connecting, or disconnected.
const (
	LiveWriter LiveRole = "writer"
	LiveViewer LiveRole = "viewer"
)

// LiveConn is the state of the connection to the holder.
type LiveConn int

// The connection states. The zero value is the first one.
const (
	LiveConnecting LiveConn = iota
	LiveAttached
	LiveResyncing
	LiveWaitingTicket
	LiveDisconnected
)

func (c LiveConn) String() string {
	switch c {
	case LiveAttached:
		return "attached"
	case LiveResyncing:
		return "resyncing"
	case LiveWaitingTicket:
		return "waiting for a ticket"
	case LiveDisconnected:
		return "disconnected"
	}
	return "connecting"
}

// LiveFact is one of the end facts: yes, no or unknown (the zero value).
type LiveFact int

// The values of a LiveFact.
const (
	FactUnknown LiveFact = iota
	FactNo
	FactYes
)

// LiveEnd is the three end facts, as the holder reports them. They are
// separate: the terminal stream can end while the process tree lives on.
type LiveEnd struct {
	Stream    LiveFact // the terminal stream ended
	TUIExited LiveFact // the TUI exited, with ExitStatus when ExitStatusKnown
	TreeGone  LiveFact // the agent's process tree is gone

	ExitStatus      int
	ExitStatusKnown bool
}

// Any reports whether the holder reported that something has ended.
func (e LiveEnd) Any() bool {
	return e.Stream == FactYes || e.TUIExited == FactYes || e.TreeGone == FactYes
}

// LiveStatus is the state of the session as the source knows it.
type LiveStatus struct {
	// Lead is the lead's name; Epoch, Runtime and Version are the current
	// holder's, from its Welcome. All are empty or zero until then.
	Lead             string
	Epoch            uint64
	Runtime, Version string
	// Role is the role granted: LiveWriter, LiveViewer, or empty. The source
	// keeps the role as last granted while the connection is down
	// (LiveDisconnected, LiveWaitingTicket) and clears it only when the
	// holder says so; the cockpit takes no input and asks for no size while
	// the connection is down, whatever the role says.
	Role LiveRole
	// Cols and Rows are the holder's screen size; zero when unknown.
	Cols, Rows int
	// Modes are the modes of the session as the holder reports them (its
	// Modes frame). In live mode the cockpit takes its mouse and paste
	// decisions for the outer terminal from these, and not from the pane's
	// emulator; it does not ask for focus reports whatever FocusEvents says
	// (see the top of this file). An ended or read-only session asks for
	// none, whatever these say. The holder's full mode set is larger than
	// this type (cursor keys, keypad, alternate screen, synchronized output):
	// a source that encodes keys keeps its own copy of it.
	Modes sessionpane.Modes
	// Conn is the connection state. Reason says why when the state is
	// LiveWaitingTicket or LiveDisconnected.
	Conn   LiveConn
	Reason string
	// End holds the end facts.
	End LiveEnd
	// CheckMismatches counts snapshot checks that failed, Resyncs the
	// resynchronisations, RefusedInput the input the holder refused.
	CheckMismatches, Resyncs, RefusedInput uint64
	// Counters are the holder's other counters, in the order the holder
	// gives them, shown in the evidence pane as they are. The cockpit keeps
	// at most LiveCounterLimit: it cleans the names and cuts them to
	// maxLiveCounterName bytes, and says how many it did not keep.
	Counters []LiveCounter
	// Idle is the holder's idle classification, a short word. Empty and
	// "unknown" are shown as "unknown (not built)": the holder does not
	// classify yet. It is cleaned and cut to maxLiveIdle bytes.
	Idle string
	// Title is the session's title as the holder reports it. It is cleaned
	// and cut to maxLiveTitle bytes. Empty means the cockpit shows the title
	// the pane's own emulator saw, if any.
	Title string
}

// LiveCounter is one of the holder's counters: a name and a value.
type LiveCounter struct {
	Name  string
	Value uint64
}

// LiveNotice is a short message from the holder with a reason code.
type LiveNotice struct {
	// Code is a reason code: one of the Notice constants or another.
	Code string
	// Text is the message. Empty means the cockpit's own text for Code.
	Text string
	// Client is the client name the message is about: the new writer's.
	Client string
}

// The reason codes the cockpit has text for.
const (
	// NoticeHeldKey: a key was held back, not sent to the session (Ctrl+Z).
	NoticeHeldKey = "held-key"
	// NoticeViewerRefused: a viewer's input, paste or resize was refused.
	NoticeViewerRefused = "viewer-refused"
	// NoticeWriterChanged: the writer changed; Client is the new writer.
	NoticeWriterChanged = "writer-changed"
	// NoticeTooSlow: the client could not keep up and was resynced or cut.
	NoticeTooSlow = "too-slow"

	// noticeReadOnly is the cockpit's own: a key typed by a client that is
	// not the writer.
	noticeReadOnly = "read-only"
)

// LiveNoticeLimit is how many notices the cockpit keeps for the evidence pane.
const LiveNoticeLimit = 16

// LiveCounterLimit is how many of the holder's counters the cockpit keeps
// for the evidence pane. A longer list is cut, and the cut is counted and
// shown.
const LiveCounterLimit = 32

// Bounds on what a source reports. Longer text is cut.
const (
	maxLiveCode   = 32
	maxLiveText   = 160
	maxLiveClient = 64
	maxLiveBatch  = 4 * LiveNoticeLimit

	maxLiveCounterName = 48
	maxLiveIdle        = 32
	maxLiveTitle       = 160
)

// liveGap is the shortest time between two wake-ups the program handles: a
// redraw follows each, so this keeps continuous output under about 60 a
// second. A wake-up that arrives in the gap is handled when it ends.
const liveGap = 17 * time.Millisecond

// The pane's size before the window size is known to fit the cockpit.
const (
	liveDefaultCols = 80
	liveDefaultRows = 24
)

// liveUnavailable is the reason shown for every control in live mode.
const liveUnavailable = "A live scratch session is not managed by the daemon (ataniqd): there is nothing to attach, approve or control from here."

// liveMsg says the source called Wake. liveGapMsg says the gap after one has
// passed, so that the program listens again.
type (
	liveMsg    struct{}
	liveGapMsg struct{}
)

// liveNote is a notice as kept: cleaned, with its text.
type liveNote struct{ code, text string }

// liveState is the model's part in live mode.
type liveState struct {
	src LiveSource
	// begun is true once Start was called.
	begun  bool
	closed bool
	// startErr is why Start failed.
	startErr string
	sink     *liveSink
	wake     chan struct{}
	done     chan struct{}
	// clock is the real clock, which the pane's time limit for held frames
	// reads: output arrives on the source's goroutine, so the model's own
	// tick time cannot be it.
	clock func() time.Time

	st LiveStatus
	// countersCut is how many counters of the source's last status were over
	// LiveCounterLimit and not kept.
	countersCut int
	notices     []liveNote
	// sentCols and sentRows are the last size asked of the source.
	sentCols, sentRows int
	// paneCols and paneRows are the pane's size at the last wake-up.
	paneCols, paneRows int
	prevRole           LiveRole
	// prevUp is whether the connection was up at the last wake-up.
	prevUp bool
	// noted is true once the read-only notice was shown since the role last
	// changed.
	noted bool
	// noticeLive is true while m.notice is a live notice.
	noticeLive bool
}

// on reports whether the cockpit shows a live session.
func (s *liveState) on() bool { return s.src != nil }

// liveSink gives the pane an input sink before the source has returned its
// own, which it can only do once it has the pane.
type liveSink struct {
	mu sync.Mutex
	to sessionpane.InputSink
}

func (s *liveSink) set(to sessionpane.InputSink) {
	s.mu.Lock()
	s.to = to
	s.mu.Unlock()
}

func (s *liveSink) get() sessionpane.InputSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.to == nil {
		return sessionpane.Discard{}
	}
	return s.to
}

func (s *liveSink) Key(k uv.KeyPressEvent) { s.get().Key(k) }
func (s *liveSink) Paste(text string)      { s.get().Paste(text) }
func (s *liveSink) Mouse(ev uv.MouseEvent) { s.get().Mouse(ev) }

func (m *Model) initLive(src LiveSource) {
	m.live = liveState{src: src, wake: make(chan struct{}, 1), done: make(chan struct{}), clock: time.Now, sink: &liveSink{}}
	m.controlUnavailable = liveUnavailable
}

// startLive makes the pane and starts the source, with the size the layout
// gives the session, or a default while the window is too small for the
// cockpit. It returns the command that waits for the first wake-up.
func (m *Model) startLive() tea.Cmd {
	cols, rows := liveDefaultCols, liveDefaultRows
	if l := m.layout(); l.cockpit {
		cols, rows = l.inner.Dx(), l.inner.Dy()
	}
	clock := m.live.clock
	pane := sessionpane.New(cols, rows, sessionpane.Options{
		GraphemeWidths: m.graphemes,
		Colours:        m.themeColours(),
		Input:          m.live.sink,
		Clock:          func() time.Time { return clock() },
	})
	cols, rows = pane.Size()
	m.live.begun, m.sized = true, true
	m.live.sentCols, m.live.sentRows = cols, rows
	wake := m.live.wake
	sink, err := m.live.src.Start(LiveStart{
		Pane: pane, Cols: cols, Rows: rows, Colours: m.themeColours(),
		Wake: func() {
			select {
			case wake <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		pane.Close()
		m.live.startErr = clean(err.Error())
		if m.live.startErr == "" {
			m.live.startErr = "the source did not start"
		}
		return nil
	}
	m.live.sink.set(sink)
	m.rep.pane = pane
	m.rep.write = pane.Write
	m.live.paneCols, m.live.paneRows = cols, rows
	m.refreshLive()
	m.live.prevRole, m.live.prevUp = m.live.st.Role, !m.liveDown()
	return m.waitLive()
}

// waitLive returns the command that waits for the source's next wake-up, and
// nothing else: while it waits no timer runs.
func (m *Model) waitLive() tea.Cmd {
	if m.live.closed || !m.live.begun || m.live.startErr != "" {
		return nil
	}
	wake, done := m.live.wake, m.live.done
	return func() tea.Msg {
		select {
		case <-wake:
			return liveMsg{}
		case <-done:
			return nil
		}
	}
}

// onLive handles a wake-up: it takes the status and the notices, checks what
// the pane's changes did to a selection, and listens again after liveGap.
func (m *Model) onLive() tea.Cmd {
	if m.live.closed || m.rep.pane == nil {
		return nil
	}
	m.refreshLive()
	for _, n := range m.liveNotices() {
		m.addNote(n)
	}
	cmds := []tea.Cmd{m.after(liveGap, func(time.Time) tea.Msg { return liveGapMsg{} })}
	if cols, rows := m.rep.pane.Size(); cols != m.live.paneCols || rows != m.live.paneRows {
		m.live.paneCols, m.live.paneRows = cols, rows
		m.sel = selection{}
	}
	m.checkSelection()
	if role, up := m.live.st.Role, !m.liveDown(); role != m.live.prevRole || up != m.live.prevUp {
		// A client that becomes the writer, or whose connection comes back
		// as the writer, asks for the size it wants at once, when that is
		// not the holder's: the window may have changed while it could not
		// ask, and another writer may have changed the holder's size.
		if role != m.live.prevRole {
			// The read-only notice is due again after each change of role:
			// the keys of a client that lost the writer role are dropped
			// again.
			m.live.noted = false
		}
		m.live.prevRole, m.live.prevUp = role, up
		if cmd := m.syncLive(false); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if cmd := m.holdCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// liveNotices takes the source's notices, bounded.
func (m *Model) liveNotices() []LiveNotice {
	ns := m.live.src.Notices()
	if len(ns) > maxLiveBatch {
		ns = ns[len(ns)-maxLiveBatch:]
	}
	return ns
}

// refreshLive takes a bounded, clean copy of the source's status.
func (m *Model) refreshLive() {
	st := m.live.src.Status()
	st.Lead, st.Runtime, st.Version = cut(clean(st.Lead), 64), cut(clean(st.Runtime), 64), cut(clean(st.Version), 64)
	st.Reason = cut(clean(st.Reason), maxLiveText)
	if st.Role != LiveWriter && st.Role != LiveViewer {
		st.Role = ""
	}
	st.Cols, st.Rows = min(max(st.Cols, 0), 4096), min(max(st.Rows, 0), 4096)
	if st.Conn < LiveConnecting || st.Conn > LiveDisconnected {
		st.Conn = LiveConnecting
	}
	st.Idle, st.Title = cut(clean(st.Idle), maxLiveIdle), cut(clean(st.Title), maxLiveTitle)
	// The source's list is read only up to the limit, and copied: the cockpit
	// keeps nothing of the source's memory.
	m.live.countersCut = max(0, len(st.Counters)-LiveCounterLimit)
	var counters []LiveCounter
	for _, c := range st.Counters[:min(len(st.Counters), LiveCounterLimit)] {
		c.Name = cut(clean(c.Name), maxLiveCounterName)
		if c.Name == "" {
			c.Name = "?"
		}
		counters = append(counters, c)
	}
	st.Counters = counters
	m.live.st = st
}

// cut returns s cut to at most n bytes, at a rune boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

// noticeText returns the text of a notice.
func noticeText(n LiveNotice) string {
	if n.Text != "" {
		return n.Text
	}
	switch n.Code {
	case NoticeHeldKey:
		return "A key was held back by the holder and not sent to the session."
	case NoticeViewerRefused:
		return "Read-only viewer: the holder refused the input."
	case NoticeWriterChanged:
		if n.Client != "" {
			return "Writer changed: " + n.Client + " is now the writer."
		}
		return "The writer changed."
	case NoticeTooSlow:
		return "Too slow: this client could not keep up with the output."
	case noticeReadOnly:
		return "Read-only viewer: keys are not sent."
	}
	return "Notice from the holder: " + n.Code + "."
}

// addNote shows a notice where notices are shown and keeps it, newest last,
// at most LiveNoticeLimit.
func (m *Model) addNote(n LiveNotice) {
	n.Code, n.Text, n.Client = cut(clean(n.Code), maxLiveCode), cut(clean(n.Text), maxLiveText), cut(clean(n.Client), maxLiveClient)
	if n.Code == "" {
		n.Code = "notice"
	}
	note := liveNote{code: n.Code, text: noticeText(n)}
	m.live.notices = append(m.live.notices, note)
	if over := len(m.live.notices) - LiveNoticeLimit; over > 0 {
		m.live.notices = append([]liveNote(nil), m.live.notices[over:]...)
	}
	m.notice, m.live.noticeLive = note.text, true
}

// noteReadOnly says that input is not sent, because the client is not the
// writer: once for each role the client holds.
func (m *Model) noteReadOnly() {
	if m.live.noted {
		return
	}
	m.live.noted = true
	n := LiveNotice{Code: noticeReadOnly}
	if m.live.st.Role != LiveViewer {
		n.Text = "Not attached as the writer: keys are not sent."
	}
	m.addNote(n)
}

// liveInputStop says why keys typed in session focus are not sent, and what
// a key does then: "" when they are sent. The key bar and the focus bar say
// it, in the order sessionKey acts: an ended session and a connection that is
// down return to the console on any key, a viewer or a client with no role
// is told once and stays.
func (m *Model) liveInputStop() (why string, anyKeyReturns bool) {
	switch {
	case m.sessionEnded():
		return "the session has ended", true
	case m.liveDown():
		return "not connected", true
	case m.live.st.Role == LiveViewer:
		return "read-only viewer", false
	case m.live.st.Role != LiveWriter:
		return "no role yet", false
	}
	return "", false
}

// liveWriter reports whether input and a size may go to the source: the role
// is writer and the connection is up.
func (m *Model) liveWriter() bool {
	return m.live.st.Role == LiveWriter && !m.liveDown()
}

// liveDown reports whether the holder connection is gone or waiting for a
// ticket: the last screen stays, dimmed.
func (m *Model) liveDown() bool {
	return m.live.st.Conn == LiveDisconnected || m.live.st.Conn == LiveWaitingTicket
}

// syncLive asks the source for the size the layout gives the session, while
// the role is writer and the connection is up, at once or after the debounce
// delay. A size is skipped only when it is the one last asked for and the
// holder reports it too (or reports no size): see LiveSource.Resize.
func (m *Model) syncLive(debounce bool) tea.Cmd {
	l := m.layout()
	if !l.cockpit || m.rep.pane == nil || !m.liveWriter() {
		return nil
	}
	m.resizeSeq++
	cols := min(max(l.inner.Dx(), sessionpane.MinCols), paneMaxCols)
	rows := min(max(l.inner.Dy(), sessionpane.MinRows), paneMaxRows)
	holder := m.live.st.Cols == 0 || m.live.st.Rows == 0 || (m.live.st.Cols == cols && m.live.st.Rows == rows)
	if cols == m.live.sentCols && rows == m.live.sentRows && holder {
		return nil
	}
	if !debounce {
		m.live.sentCols, m.live.sentRows = cols, rows
		m.live.src.Resize(cols, rows)
		return nil
	}
	seq := m.resizeSeq
	return m.after(resizeDelay, func(time.Time) tea.Msg { return resizeMsg{seq: seq} })
}

// liveModes returns the modes the cockpit acts on for the outer terminal and
// for forwarding: the holder's, while the session is the writer's and up.
func (m *Model) liveModes() sessionpane.Modes {
	if m.rep.pane == nil || !m.liveWriter() || m.sessionEnded() {
		return sessionpane.Modes{}
	}
	return m.live.st.Modes
}

// liveMouse sends a mouse event of the outer terminal, in window cells, to
// the source as pane cells, area being where the pane was drawn, when the
// holder's modes ask for that kind of event. It is the rule of the pane's
// own Mouse, with the holder's modes in place of the emulator's.
func (m *Model) liveMouse(ev uv.MouseEvent, area uv.Rectangle) {
	mode := m.liveModes().Mouse
	if mode == uv.MouseModeNone || ev == nil || m.rep.pane == nil {
		return
	}
	cols, rows := m.rep.pane.Size()
	mo := ev.Mouse()
	x, y := mo.X-area.Min.X, mo.Y-area.Min.Y
	if x < 0 || y < 0 || x >= min(cols, area.Dx()) || y >= min(rows, area.Dy()) {
		return
	}
	mo.X, mo.Y = x, y
	var out uv.MouseEvent
	switch ev.(type) {
	case uv.MouseClickEvent:
		out = uv.MouseClickEvent(mo)
	case uv.MouseWheelEvent:
		out = uv.MouseWheelEvent(mo)
	case uv.MouseReleaseEvent:
		if mode < uv.MouseModeClick {
			return
		}
		out = uv.MouseReleaseEvent(mo)
	case uv.MouseMotionEvent:
		if mode < uv.MouseModeDrag || (mode == uv.MouseModeDrag && mo.Button == uv.MouseNone) {
			return
		}
		out = uv.MouseMotionEvent(mo)
	default:
		return
	}
	m.live.sink.Mouse(out)
}

// paneEnded reports whether the session has ended, and its exit status when
// known: the pane's end, or an end fact the holder reported.
func (m *Model) paneEnded() (ended bool, status int, known bool) {
	if m.rep.pane == nil {
		return false, 0, false
	}
	ended, status, known = m.rep.pane.Ended()
	if m.live.on() && m.live.st.End.Any() {
		ended = true
		if !known && m.live.st.End.ExitStatusKnown {
			status, known = m.live.st.End.ExitStatus, true
		}
	}
	return ended, status, known
}

// liveRoleText names the role as the cockpit shows it.
func (m *Model) liveRoleText() string {
	switch m.live.st.Role {
	case LiveWriter:
		return "writer"
	case LiveViewer:
		return "read-only viewer"
	}
	return "no role yet"
}

// liveLabel replaces the fixture label in every place that carries one.
func (m *Model) liveLabel() string {
	lead, epoch := m.live.st.Lead, "?"
	if lead == "" {
		lead = "unknown"
	}
	if m.live.st.Epoch != 0 {
		epoch = strconv.FormatUint(m.live.st.Epoch, 10)
	}
	return "live scratch session · lead " + lead + " · " + m.liveRoleText() + " · epoch " + epoch
}

// factsText returns the end facts as the holder reported them, one phrase each.
func factsText(e LiveEnd) []string {
	pick := func(f LiveFact, yes, no, unknown string) string {
		switch f {
		case FactYes:
			return yes
		case FactNo:
			return no
		}
		return unknown
	}
	tui := pick(e.TUIExited, "TUI exited", "TUI has not exited", "TUI exit: unknown")
	if e.TUIExited == FactYes {
		tui += ", status unknown"
		if e.ExitStatusKnown {
			tui = "TUI exited with status " + strconv.Itoa(e.ExitStatus)
		}
	}
	return []string{
		pick(e.Stream, "terminal stream ended", "terminal stream has not ended", "terminal stream: unknown"),
		tui,
		pick(e.TreeGone, "process tree gone", "process tree still present", "process tree: unknown"),
	}
}

// liveCountersText returns the counters on one line.
func (m *Model) liveCountersText() string {
	st := m.live.st
	u := func(n uint64) string { return strconv.FormatUint(n, 10) }
	return "check mismatches " + u(st.CheckMismatches) + " · resyncs " + u(st.Resyncs) + " · refused input " + u(st.RefusedInput)
}

// liveRuntimeText returns the runtime and its version.
func (m *Model) liveRuntimeText() string {
	st := m.live.st
	return strings.TrimSpace(orNone(st.Runtime) + " " + st.Version)
}

// liveIdleText returns the holder's idle classification as it is shown.
func (m *Model) liveIdleText() string {
	idle := m.live.st.Idle
	if idle == "" || strings.EqualFold(idle, "unknown") {
		return "idle state: unknown (not built)"
	}
	return "idle state: " + idle
}

// liveTitle returns the session's title: the holder's, or the one the
// pane's emulator saw.
func (m *Model) liveTitle() string {
	if t := m.live.st.Title; t != "" {
		return t
	}
	if m.rep.pane != nil {
		return m.rep.pane.Title()
	}
	return ""
}

// liveSizeText returns the holder's screen size, or "unknown".
func (m *Model) liveSizeText() string {
	if m.live.st.Cols <= 0 || m.live.st.Rows <= 0 {
		return "unknown"
	}
	return sizeText(m.live.st.Cols, m.live.st.Rows)
}

// liveBanner returns the banner over the session pane for a session that has
// ended, is disconnected, waits for a ticket or has not connected yet.
func (m *Model) liveBanner(ended bool) (title string, ls []line, ok bool) {
	hint := "Nothing in the cockpit sends input to this session now."
	if m.focus == sessionFocus {
		hint = "Press any key to return to the console."
	}
	switch {
	case ended:
		for _, f := range factsText(m.live.st.End) {
			ls = append(ls, line{f, lnText})
		}
		ls = append(ls, line{"As the holder reported them: the three are separate facts. Nothing is replayed.", lnDim}, line{hint, lnWarn})
		return "SESSION ENDED", ls, true
	case m.live.st.Conn == LiveDisconnected, m.live.st.Conn == LiveWaitingTicket:
		title, what := "DISCONNECTED", "The connection to the holder is closed."
		if m.live.st.Conn == LiveWaitingTicket {
			title, what = "WAITING FOR A TICKET", "The console waits for an attach ticket."
		}
		return title, []line{{what, lnText}, {"Reason: " + orNone(m.live.st.Reason), lnText}, {"The last screen is kept, dimmed.", lnDim}, {hint, lnWarn}}, true
	case m.live.st.Conn == LiveConnecting:
		return "CONNECTING", []line{{"Connecting to the holder. Nothing has been received yet.", lnText}}, true
	}
	return "", nil, false
}
