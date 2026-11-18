// Package terminalconsole presents bounded, read-only cockpit projections.
//
// The console is a fixture: it shows a fictional snapshot and replays a
// recorded session into an embedded session pane. It has no live backend, no
// process and no network, and no key press, paste or mouse event leaves it
// except into the input sink it was given, which discards them by default.
// The one other thing it sends out is text the person selects in the session
// pane with the mouse, copied to the outer terminal's clipboard with OSC 52.
// An import guard test keeps the package away from every client, mutation,
// process and network package.
//
// With Options.Live set the same cockpit shows a live scratch session that a
// holder keeps, whose bytes come from a LiveSource: an interface this package
// defines and another package implements, because this one may not open a
// socket. See live.go.
//
// With Options.Label set the cockpit shows a labelled, read-only view of its
// source in place of the fixture: nothing in it is called a fixture, no
// recording is replayed, and every control stays unavailable with its reason.
// See view.go.
//
// With a window of at least MinCols by MinRows the console draws the cockpit:
// three panes, a timeline and a key bar. Below that it falls back to
// single-column pages.
package terminalconsole

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/terminalconsole/sessionpane"
	"ataniq/internal/terminalconsole/sessionreplay"
)

// Source supplies a snapshot, not an authority for commands or state changes.
// The snapshot is taken once, when the model is made: there is no refresh path
// yet.
type Source interface{ Snapshot() Projection }

// Projection describes display data and why live control is unavailable.
type Projection struct {
	Projects           []Project
	ControlUnavailable string
	// Operator names the operator the cockpit shows in the context block.
	Operator string
	// Gate is the pending gate, shown as data: no approval is possible.
	Gate Gate
	// Contract lists the attachment contract checks and their state.
	Contract []Check
	// Timeline lists events, oldest first.
	Timeline []TimelineEntry
	// Controls lists the controls the cockpit knows of. Each is unavailable,
	// and carries the reason.
	Controls []Control
	// Facts are facts about where the projection comes from, such as a
	// service's version, shown first in the evidence pane. The fixture has
	// none.
	Facts []Check
	// Problem, when it is not empty, says why the source has nothing to
	// show. The cockpit then draws it and NextStep, which says what a
	// person can do about it, where the projects and leads would be.
	Problem, NextStep string
}
type Project struct {
	Name  string
	Leads []Lead
	Tasks []string
}
type Lead struct {
	Name string
	// State is the lead's state in a word or two, shown beside its name in
	// the tree. Empty shows the name alone.
	State        string
	Subagents    []string
	Conversation []string
	Session      Session
}

// Session describes a lead's session and its binding, as data.
type Session struct {
	Provider, Runtime, Authority, Host, ID, Attachment string
	Binding, Execution                                 string
}

// Gate is a pending gate: what would be approved, by whom, and its state.
type Gate struct{ Action, Scope, Target, Approver, State string }

// Check is one item of the attachment contract.
type Check struct{ Name, State string }

// TimelineEntry is one event of the timeline.
type TimelineEntry struct{ Time, Text string }

// Control is a control the cockpit lists but cannot offer. Key is how it
// would be reached.
type Control struct{ Name, Key, Reason string }

// Limits on a projection.
const (
	maxProjects = 32
	maxLeads    = 32
	maxList     = 64
	maxContract = 16
	maxControls = 32
)

// Model holds its own sanitized, bounded copy of a source snapshot.
type Model struct {
	projects                                         []Project
	leadSelections                                   []int
	controlUnavailable                               string
	operator                                         string
	gate                                             Gate
	contract                                         []Check
	timeline                                         []TimelineEntry
	controls                                         []Control
	facts                                            []Check
	problem, nextStep                                string
	project, lead, projectCursor, leadCursor, scroll int
	page, theme, notice                              string
	// label is Options.Label, cleaned. Empty is the fixture.
	label         string
	width, height int

	opts Options
	cockpitState
}

func clean(s string) string {
	var b strings.Builder
	// Bound both inspection and output; do not emit partial UTF-8 sequences.
	for inspected := 0; len(s) > 0 && inspected < 4096; {
		r, n := utf8.DecodeRuneInString(s)
		if inspected+n > 4096 {
			break
		}
		s = s[n:]
		inspected += n
		if r == utf8.RuneError && n == 1 {
			continue
		}
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		if b.Len()+n > 512 {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
func bounded(ss []string) []string {
	if len(ss) > maxList {
		ss = ss[:maxList]
	}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, clean(s))
	}
	return out
}
func cleanSession(s Session) Session {
	return Session{Provider: clean(s.Provider), Runtime: clean(s.Runtime), Authority: clean(s.Authority), Host: clean(s.Host),
		ID: clean(s.ID), Attachment: clean(s.Attachment), Binding: clean(s.Binding), Execution: clean(s.Execution)}
}

// New returns a console for source with the default options and the given
// theme, dark or light.
func New(source Source, theme string) *Model { return NewWithOptions(source, Options{Theme: theme}) }

// NewWithOptions returns a console for source. Call Close when the program
// has ended.
func NewWithOptions(source Source, o Options) *Model {
	if o.ReturnKey == (ReturnKey{}) {
		o.ReturnKey = DefaultReturnKey
	}
	if o.Recording == "" {
		o.Recording = DefaultRecording
	}
	m := &Model{theme: o.Theme, page: "projects", opts: o}
	m.label = clean(o.Label)
	m.after = tea.Tick
	m.focus = consoleFocus
	if o.Live != nil {
		// A live session shows only what the source reports: the snapshot of
		// the fixture projection, if one was given, is not read.
		m.initLive(o.Live)
		return m
	}
	if source == nil {
		m.startSession()
		return m
	}
	m.apply(source.Snapshot())
	// A labelled view is not the fixture: no recording is replayed in it.
	if !m.labelled() {
		m.startSession()
	}
	return m
}

// apply replaces the model's copy of the source's data with a sanitized,
// bounded copy of in.
func (m *Model) apply(in Projection) {
	m.contract, m.timeline, m.controls, m.facts, m.projects, m.leadSelections = nil, nil, nil, nil, nil, nil
	m.controlUnavailable = clean(in.ControlUnavailable)
	if m.controlUnavailable == "" {
		m.controlUnavailable = "No live control capability is available."
	}
	m.operator = clean(in.Operator)
	m.gate = Gate{Action: clean(in.Gate.Action), Scope: clean(in.Gate.Scope), Target: clean(in.Gate.Target),
		Approver: clean(in.Gate.Approver), State: clean(in.Gate.State)}
	for _, c := range in.Contract[:min(len(in.Contract), maxContract)] {
		m.contract = append(m.contract, Check{Name: clean(c.Name), State: clean(c.State)})
	}
	for _, e := range in.Timeline[:min(len(in.Timeline), maxList)] {
		m.timeline = append(m.timeline, TimelineEntry{Time: clean(e.Time), Text: clean(e.Text)})
	}
	for _, c := range in.Controls[:min(len(in.Controls), maxControls)] {
		m.controls = append(m.controls, Control{Name: clean(c.Name), Key: clean(c.Key), Reason: clean(c.Reason)})
	}
	ps := in.Projects
	if len(ps) > maxProjects {
		ps = ps[:maxProjects]
	}
	for _, p := range ps {
		cp := Project{Name: clean(p.Name), Tasks: bounded(p.Tasks)}
		ls := p.Leads
		if len(ls) > maxLeads {
			ls = ls[:maxLeads]
		}
		for _, l := range ls {
			cp.Leads = append(cp.Leads, Lead{Name: clean(l.Name), State: clean(l.State), Subagents: bounded(l.Subagents), Conversation: bounded(l.Conversation), Session: cleanSession(l.Session)})
		}
		m.projects = append(m.projects, cp)
		m.leadSelections = append(m.leadSelections, 0)
	}
	for _, c := range in.Facts[:min(len(in.Facts), maxContract)] {
		m.facts = append(m.facts, Check{Name: clean(c.Name), State: clean(c.State)})
	}
	m.problem, m.nextStep = clean(in.Problem), clean(in.NextStep)
}

// Init sends tea.Raw(ansi.RequestModeUnicodeCore), which asks the outer
// terminal for mode 2027 (Unicode core) whatever the environment is, because
// Bubble Tea usually skips its own query when SSH_TTY is set. The answer comes
// back as a tea.ModeReportMsg, and Bubble Tea's own handling of it switches its
// width method. Init does not override the environment Bubble Tea reads, since
// that would also turn on synchronized output (2026) over SSH.
func (m *Model) Init() tea.Cmd { return tea.Raw(ansi.RequestModeUnicodeCore) }

// Recordings returns the names of the fixture recordings the console can
// replay, sorted.
func Recordings() []string { return sessionreplay.FixtureNames() }

// DefaultRecording is the fixture recording replayed unless Options says
// otherwise.
const DefaultRecording = "title.jsonl"

// Options configures the console.
type Options struct {
	// Theme is "dark" or "light".
	Theme string
	// ReturnKey is the key that leaves session focus. The zero value is
	// DefaultReturnKey.
	ReturnKey ReturnKey
	// Recording names the fixture recording to replay. Empty means
	// DefaultRecording. A name that is not a fixture leaves the pane empty.
	Recording string
	// Linger keeps the replayed session open this long after the recording's
	// last event before its end applies, so that a person can try the focus
	// and zoom keys on a session that is still running. Zero applies the end
	// at its recorded time.
	Linger time.Duration
	// Input receives what the person types, pastes or clicks while the
	// session has focus. Nil discards it, which is what the fixture console
	// does.
	Input sessionpane.InputSink
	// Live is a live session source. Nil means the fixture replay. With a
	// source the console shows a live scratch session through a holder: the
	// snapshot, Recording, Linger and Input are not used, the session's input
	// goes to the sink the source returns, and nothing of the fixture is
	// shown as if it were live data. See LiveSource.
	Live LiveSource
	// Label says what the cockpit shows, in the header and on the session
	// frame, such as "a service's view, read-only". Empty means the demo
	// fixture. With a label nothing is called a fixture and no recording is
	// replayed: the view has no session terminal.
	Label string
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.live.begun {
		m.refreshLive()
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = min(4096, max(0, v.Width))
		m.height = min(4096, max(0, v.Height))
		return m, m.onSize()
	case tea.KeyPressMsg:
		return m, m.onKey(v)
	case tea.PasteMsg:
		m.onPaste(v.Content)
	case tea.MouseMsg:
		return m, m.onMouse(v)
	case tea.ModeReportMsg:
		m.onModeReport(v)
	case tickMsg:
		return m, m.onTick(v)
	case holdMsg:
		m.onHold(v)
	case tea.FocusMsg, tea.BlurMsg:
		// The outer terminal's focus events are requested when the session asks
		// for them, but the input sink has no method for them yet: they are
		// dropped.
	case resizeMsg:
		m.onResize(v)
	case liveMsg:
		return m, m.onLive()
	case liveGapMsg:
		return m, m.waitLive()
	}
	return m, nil
}

// handleFallbackKey handles a key in the single-column pages, shown below the
// cockpit's minimum size.
func (m *Model) handleFallbackKey(v tea.KeyPressMsg) tea.Cmd {
	key := v.String()
	if m.live.on() {
		// The live session's one page has nothing to navigate.
		if key == "q" || key == "ctrl+c" {
			return m.quit()
		}
		return nil
	}
	switch key {
	case "q", "ctrl+c":
		return m.quit()
	case "?":
		m.page = "help"
		m.scroll = 0
	case "ctrl+k":
		m.page = "actions"
		m.scroll = 0
	case "esc", "backspace":
		switch m.page {
		case "conversation", "tasks":
			m.page = "leads"
		case "leads", "help", "actions":
			m.page = "projects"
		}
		m.scroll = 0
	case "up", "k":
		switch m.page {
		case "projects":
			m.projectCursor = max(0, m.projectCursor-1)
		case "leads":
			m.leadCursor = max(0, m.leadCursor-1)
		default:
			m.scroll = max(0, m.scroll-1)
		}
	case "down", "j":
		switch m.page {
		case "projects":
			m.projectCursor = min(max(0, len(m.projects)-1), m.projectCursor+1)
		case "leads":
			if len(m.projects) > 0 {
				m.leadCursor = min(max(0, len(m.projects[m.project].Leads)-1), m.leadCursor+1)
			}
		default:
			m.scroll = min(max(0, len(m.lines())-1), m.scroll+1)
		}
	case "enter":
		if m.page == "projects" && len(m.projects) > 0 {
			m.leadSelections[m.project] = m.leadCursor
			m.project = m.projectCursor
			m.leadCursor = m.leadSelections[m.project]
			m.lead = m.leadCursor
			m.page = "leads"
			m.scroll = 0
		} else if m.page == "leads" && len(m.projects) > 0 && len(m.projects[m.project].Leads) > 0 {
			m.lead = m.leadCursor
			m.page = "conversation"
			m.scroll = 0
		}
	case "t":
		if m.page == "leads" {
			m.page = "tasks"
			m.scroll = 0
		}
	case "a":
		m.notice = m.unavailable("a")
	}
	return nil
}

// unavailable returns the notice for a control reached by key: its reason, or
// the general one.
func (m *Model) unavailable(key string) string {
	for _, c := range m.controls {
		if c.Key == key {
			return "Unavailable: " + c.Name + ". " + c.Reason
		}
	}
	return "Unavailable: " + m.controlUnavailable
}

// quit stops the session replay and ends the program.
func (m *Model) quit() tea.Cmd {
	m.Close()
	return tea.Quit
}

func (m *Model) lines() []string {
	if m.live.on() {
		return m.liveLines()
	}
	lines := []string{"ATANIQ / DEMO FIXTURE", "Read-only fixture; no live backend", ""}
	// fixture marks the headings of the fixture's pages; a labelled view is
	// not one.
	fixture := " (fixture)"
	if m.labelled() {
		lines = []string{"ATANIQ / " + m.viewLabel(), "Read-only view; every control is unavailable", ""}
		fixture = ""
	}
	// The fixture shows nothing but this with no projects. A labelled view
	// keeps its help and its action list, whose reasons matter most then.
	if overlayPage := m.labelled() && (m.page == "help" || m.page == "actions"); !overlayPage {
		if m.problem != "" {
			return append(lines, m.problemLines(0)...)
		}
		if len(m.projects) == 0 {
			return append(lines, "No projects")
		}
	}
	var p Project
	if len(m.projects) > 0 {
		p = m.projects[m.project]
	}
	switch m.page {
	case "projects":
		lines = append(lines, "Projects")
		for i, p := range m.projects {
			mark := "  "
			if i == m.projectCursor {
				mark = "> "
			}
			lines = append(lines, mark+p.Name)
		}
	case "leads":
		lines = append(lines, "Project: "+p.Name, "Persistent leads"+fixture)
		if len(p.Leads) == 0 {
			lines = append(lines, "  No leads")
		}
		for i, l := range p.Leads {
			mark := "  "
			if i == m.leadCursor {
				mark = "> "
			}
			lines = append(lines, mark+leadText(l))
			for _, s := range l.Subagents {
				lines = append(lines, "    subagent: "+s)
			}
		}
	case "tasks":
		lines = append(lines, "Project: "+p.Name, "Tasks"+fixture)
		lines = append(lines, p.Tasks...)
	case "conversation":
		if len(p.Leads) > 0 {
			l := p.Leads[m.lead]
			lines = append(lines, "Project: "+p.Name+" / Lead: "+l.Name, "Conversation"+fixture)
			lines = append(lines, l.Conversation...)
		}
	case "help":
		lines = append(lines, "Help", "Up/down or j/k: navigate or scroll", "Enter: open project or conversation", "t: project tasks   Esc: back", "a: full attach (unavailable)   Ctrl+K: actions", "q or Ctrl-C: quit", "A window of "+sizeText(MinCols, MinRows)+" or more shows the cockpit.")
	case "actions":
		if m.labelled() {
			lines = append(lines, "Actions (all unavailable)")
		} else {
			lines = append(lines, "Actions (all unavailable, fixture)")
		}
		for _, c := range m.controls {
			lines = append(lines, c.Name+": "+c.Reason)
		}
		if len(m.controls) == 0 {
			lines = append(lines, m.controlUnavailable)
		}
	}
	return lines
}
func (m *Model) View() tea.View {
	if m.live.begun {
		m.refreshLive()
	}
	if m.width <= 0 || m.height <= 0 {
		return m.newView("")
	}
	if m.layout().cockpit {
		return m.cockpitView()
	}
	return m.fallbackView()
}

// newView returns a view on the alternate screen.
func (m *Model) newView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// fallbackView draws the single-column pages.
func (m *Model) fallbackView() tea.View {
	all := m.lines()
	lines := []string{all[0]}
	if m.height > 1 {
		body := all[1:]
		start := 0
		room := m.height - 1
		// Identity and selected row take precedence at height two. Otherwise
		// reserve a line for an unavailable-control notice.
		showNotice := m.notice != "" && (room > 1 || (m.page != "projects" && m.page != "leads"))
		if showNotice {
			room--
		}
		if m.page == "projects" {
			start = max(0, 3+m.projectCursor-(room-1))
		} else if m.page == "leads" {
			selected := 4
			if len(m.projects) > 0 {
				for i := 0; i < m.leadCursor; i++ {
					selected += 1 + len(m.projects[m.project].Leads[i].Subagents)
				}
			}
			start = max(0, selected-(room-1))
		} else {
			start = min(m.scroll, max(0, len(body)-1))
		}
		start = min(start, len(body))
		end := start + min(room, len(body)-start)
		lines = append(lines, body[start:end]...)
		if showNotice {
			lines = append(lines, m.notice)
		}
	}
	for i, s := range lines {
		lines[i] = ansi.Truncate(s, m.width, "")
	}
	content := strings.Join(lines, "\n")
	// Styling is generated locally, after untrusted text has been sanitized.
	if content != "" {
		fg, bg := "97", "40"
		if m.theme == "light" {
			fg, bg = "30", "107"
		}
		content = fmt.Sprintf("\x1b[%s;%sm%s\x1b[0m", fg, bg, content)
	}
	return m.newView(content)
}
