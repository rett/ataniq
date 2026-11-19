package terminalconsole

import (
	"crypto/rand"
	"errors"
	"io"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
	"ataniq/internal/terminalconsole/approvals"
	"ataniq/internal/terminalconsole/arrival"
	"ataniq/internal/terminalconsole/limitview"
)

// The approvals pieces of the cockpit.
//
// The cockpit places five components for the owner's approvals and owns none
// of their rules: the approvals view (the list and the card), the limit's
// screen, the arrival strip, the status element and the bell. This file is
// where they meet the cockpit, and it holds four rules.
//
//   - Keys. g opens approvals from console focus only: a key typed while the
//     session has the focus goes to the session and starts no decision. While
//     the approvals view is open (the list, a card, the limit's screen or a
//     field in them) the cockpit hands every key to the view before any key of
//     its own, so that a letter typed into the confirm field or the deny
//     reason reaches the field and is never q, a, j or k of the cockpit. Esc
//     walks back: field, card, list, cockpit.
//   - The backend. It reaches the cockpit as a value in Options.Backend. The
//     cockpit calls it only from commands (tea.Cmd), never from Update or
//     View. With no backend the approvals screen says there is no owner
//     channel in this build, and nothing can be decided.
//   - What the cockpit shows of the backend. It reads the listing on a timer
//     of its own: the oldest waiting card is the pending gate in the right
//     pane, the rows of recent decisions are the timeline's asked, decided and
//     applied entries, and the waiting cards are the approvals element of the
//     status line.
//   - Presence. One tracker is fed by every key and every pointer action. It
//     decides when the arrival strip is shown (once per arrival, never taking
//     the key that woke the console) and whether the bell rings for a card
//     that arrives, and it tells the backend of activity at most once a
//     minute.

// DefaultApprovalsRefresh is how often the cockpit reads the backend's
// listing when Options.ApprovalsRefresh is zero.
const DefaultApprovalsRefresh = 3 * time.Second

// SampleLabel is what the header says while the listing holds sample cards.
// It is the approvals view's own words for the same thing.
const SampleLabel = "sample cards: a decision changes nothing"

// NoOwnerChannel is what the approvals screen says when the console was given
// no backend.
const NoOwnerChannel = "no owner channel in this build"

// approvalsState is the cockpit's part of the approvals pieces.
type approvalsState struct {
	// backend is Options.Backend. Nil means there is no owner channel.
	backend approvals.Backend
	period  time.Duration
	random  io.Reader

	// view is the open approvals view, nil while it is closed. noChannel is
	// whether the screen without a backend is open.
	view      *approvals.Model
	noChannel bool
	// gate is the backend the view is given: it notes what the view's own
	// listing held. wantOldest is whether the view is to open the card the
	// pending gate named, oldestID, once it has its first list.
	gate       *gateBackend
	wantOldest bool
	oldestID   string
	// unread counts the ticks that found the drive armed and the view's
	// first list not read. A second one gives the drive up.
	unread int

	// limit is the limit's screen, open over the view for limitProject.
	limit        *limitview.Model
	limitProject string
	limitSeq     int

	// listing is the backend's listing as the cockpit last read it.
	listing    card.Listing
	have       bool
	listFailed bool
	seq        int
	pending    bool

	// tracker follows the owner's presence. It is nil only when no session id
	// could be made. ev is its answer for the key or pointer action that is
	// being handled.
	tracker *presence.Tracker
	ev      presence.Event
	strip   *arrival.Strip
	bell    *arrival.Bell
}

// gateBackend is the backend the approvals view is given. It wraps the
// cockpit's backend and notes the pending ids of every listing it hands the
// view, so that the cockpit can find the card of the pending gate in the list
// the view really holds, and not in its own copy of an earlier reading. It
// notes a failed reading as an empty one.
type gateBackend struct {
	approvals.Backend
	mu    sync.Mutex
	ids   []string
	reads int
}

// List reads the backend's listing and notes its pending ids.
func (g *gateBackend) List() (card.Listing, error) {
	l, err := g.Backend.List()
	var ids []string
	if err == nil {
		ids = make([]string, len(l.Pending))
		for i, it := range l.Pending {
			ids[i] = it.ID
		}
	}
	g.mu.Lock()
	g.ids, g.reads = ids, g.reads+1
	g.mu.Unlock()
	return l, err
}

// noted returns the ids of the last listing handed to the view and how many
// listings that makes.
func (g *gateBackend) noted() ([]string, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ids, g.reads
}

// The cockpit's own messages for the approvals pieces.
type (
	// apprTickMsg is the timer that reads the listing again.
	apprTickMsg struct{ at time.Time }
	// apprListMsg is the answer to the listing request seq.
	apprListMsg struct {
		seq int
		l   card.Listing
		err error
	}
	// apprLimitMsg is the answer to a limit request for project: the limit's
	// view, or the refusal. set is whether it answers a change.
	apprLimitMsg struct {
		seq     int
		project string
		v       limits.View
		err     error
		set     bool
	}
)

// initApprovals makes the approvals pieces from the options.
func (m *Model) initApprovals(o Options) {
	a := &m.ap
	a.backend, a.period, a.random = o.Backend, o.ApprovalsRefresh, o.Random
	if a.period == 0 {
		a.period = DefaultApprovalsRefresh
	}
	if a.random == nil {
		a.random = rand.Reader
	}
	a.bell = arrival.NewBell(!o.BellOff)
	a.strip = arrival.NewStrip(m.arrivalColors())
	if session, err := presence.NewSession(a.random); err == nil {
		a.tracker, _ = presence.New(presence.DefaultWindow, presence.SurfaceConsole, session)
	}
}

// clock returns the time the approvals pieces go by: Options.Now, or the
// system's.
func (m *Model) clock() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

func (m *Model) apprColors() approvals.Colors {
	p := paletteFor(m.theme)
	return approvals.Colors{Fg: p.fg, Bg: p.bg, Dim: p.dim, Accent: p.accent, Warn: p.warn}
}

func (m *Model) limitColors() limitview.Colors {
	p := paletteFor(m.theme)
	return limitview.Colors{Fg: p.fg, Bg: p.bg, Dim: p.dim, Warn: p.warn}
}

func (m *Model) arrivalColors() arrival.Colors {
	p := paletteFor(m.theme)
	return arrival.Colors{Fg: p.fg, Bg: p.bg, Dim: p.dim, Warn: p.warn}
}

// apprOpen reports whether an approvals screen has the keys: the view (its
// list, a card or a field in it), the limit's screen, or the screen that says
// there is no owner channel.
func (m *Model) apprOpen() bool { return m.ap.view != nil || m.ap.noChannel }

// apprSize is the size of the approvals screens: the whole window, up to the
// largest canvas.
func (m *Model) apprSize() (int, int) {
	return min(m.width, maxCanvasCols), min(m.height, maxCanvasRows)
}

// afterFn is the view's timer: the cockpit's own, which a test replaces.
func (m *Model) afterFn(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
	return m.after(d, fn)
}

// startApprovals schedules the first reading of the listing, at once. It
// returns nil when there is no backend or the setting is off.
func (m *Model) startApprovals() tea.Cmd {
	if m.ap.backend == nil || m.ap.period < 0 {
		return nil
	}
	return m.apprTickCmd(0)
}

func (m *Model) apprTickCmd(d time.Duration) tea.Cmd {
	return m.after(d, func(t time.Time) tea.Msg { return apprTickMsg{at: t} })
}

// onApprTick reads the listing, unless a request is out, refreshes the view
// when it is open, and schedules the next tick.
func (m *Model) onApprTick(apprTickMsg) tea.Cmd {
	a := &m.ap
	if a.backend == nil {
		return nil
	}
	cmds := []tea.Cmd{m.apprTickCmd(a.period)}
	if a.wantOldest && a.gate != nil {
		// A first list that does not come must not keep the view from being
		// refreshed for good: the drive is given up at the second tick that
		// finds no read, and the view refreshes from then on.
		if _, reads := a.gate.noted(); reads == 0 {
			if a.unread++; a.unread > 1 {
				a.wantOldest = false
			}
		}
	}
	if a.view != nil && !a.wantOldest {
		// While the gate's card is still to be opened the view is not
		// refreshed: its first list is then the only one read, so that the
		// list the gate noted is the list the view holds. (The drive's own
		// length check below catches a differing list only when the lengths
		// differ; it does not tell two lists of the same length apart.)
		// Refresh asks the view for a new listing on every tick, whether or
		// not its last request is answered, and the view drops an answer to
		// an older request. A backend that is slower than the period (the
		// daemon client of a later part) would so never get a listing into
		// the open view. The cockpit's own request below has a guard; this
		// one has none, and a later part is to add one (for example, a
		// wrapper that counts the calls in flight and skips Refresh).
		cmds = append(cmds, a.view.Refresh())
	}
	if !a.pending {
		a.seq++
		a.pending = true
		seq, b := a.seq, a.backend
		cmds = append(cmds, func() tea.Msg {
			l, err := b.List()
			return apprListMsg{seq: seq, l: l, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// onApprList takes in the listing. The cards that were waiting at the first
// reading are known to the bell and never ring it; a card that comes later
// rings it when the owner is not present.
func (m *Model) onApprList(v apprListMsg) tea.Cmd {
	a := &m.ap
	if !a.pending || v.seq != a.seq {
		return nil
	}
	a.pending = false
	if v.err != nil {
		a.listFailed = true
		return nil
	}
	a.listFailed = false
	first := !a.have
	a.listing, a.have = v.l, true
	now := m.clock()
	present := a.tracker != nil && a.tracker.Present(now)
	var cmds []tea.Cmd
	for _, it := range v.l.Pending {
		if first {
			a.bell.Known(it.ID)
		} else if cmd, ok := a.bell.CardArrived(it.ID, present); ok {
			cmds = append(cmds, cmd)
		}
	}
	a.strip.Refresh(m.arrivalSummary(now))
	m.scrolls[paneTimeline] = min(m.scrolls[paneTimeline], max(0, len(m.allTimeline())-1))
	return tea.Batch(cmds...)
}

// sample reports whether the listing says its cards are samples.
func (m *Model) sample() bool { return m.ap.have && m.ap.listing.Sample }

// oldestWaiting returns the oldest waiting card of the listing, and its place
// among the pending cards.
func (m *Model) oldestWaiting() (card.Item, int, bool) {
	var best card.Item
	at := -1
	for i, it := range m.ap.listing.Pending {
		if at < 0 || it.Asked.Before(best.Asked) {
			best, at = it, i
		}
	}
	return best, at, at >= 0
}

// lives are the lives of the waiting cards, for the status element and the
// strip.
func (m *Model) lives() []arrival.Life {
	var out []arrival.Life
	for _, it := range m.ap.listing.Pending {
		out = append(out, arrival.Life{Asked: it.Asked, Expires: it.Expires})
	}
	return out
}

// approvalsStatus is the approvals element of the status line: empty when no
// card waits or there is no backend.
func (m *Model) approvalsStatus() arrival.Status {
	if m.ap.backend == nil || !m.ap.have {
		return arrival.Status{}
	}
	return arrival.StatusElement(m.lives(), m.clock())
}

// arrivalSummary is what the arrival strip says: the cards that wait and the
// ones decided past the limit. It is empty before the first reading.
func (m *Model) arrivalSummary(now time.Time) arrival.Summary {
	a := &m.ap
	if !a.have {
		return arrival.Summary{}
	}
	var s arrival.Summary
	s.Waiting, s.OldestAge = arrival.Waiting(m.lives(), now)
	for _, it := range a.listing.Pending {
		if it.Held {
			s.Held = append(s.Held, card.HeldLabel+": "+clean(it.Project))
		}
	}
	return s
}

// activity feeds the presence tracker with a key or pointer action of the
// owner. It shows the arrival strip when the action is an arrival, and it
// returns the command that tells the backend when a beat is due. It takes
// nothing of the action: the caller delivers it where it was aimed.
func (m *Model) activity() tea.Cmd {
	a := &m.ap
	a.ev = presence.Event{}
	if a.tracker == nil {
		return nil
	}
	now := m.clock()
	a.ev = a.tracker.Activity(now)
	if !m.apprOpen() {
		// An arrival inside an approvals screen is not announced: he is
		// looking at approvals, and the strip would otherwise show behind the
		// view and appear when he leaves it.
		a.strip.Arrive(a.ev, m.arrivalSummary(now))
	}
	if !a.ev.BeatDue || a.backend == nil {
		return nil
	}
	b := a.backend
	return func() tea.Msg {
		b.Activity(now)
		return nil
	}
}

// stripFocused says whether the arrival strip may take a key: only in console
// focus, with the cockpit drawn, and never while the approvals view, an
// overlay or zoom has the keys. The strip is never given the focus by
// itself: it takes the digits and Esc in console focus while it shows.
func (m *Model) stripFocused() bool {
	return m.layout().cockpit && !m.zoom && m.focus == consoleFocus && m.overlay == "" && !m.apprOpen()
}

// openApprovals opens the approvals screen, and with oldest set the oldest
// waiting card in it. It closes the arrival strip. Without a backend it opens
// the screen that says there is no owner channel.
func (m *Model) openApprovals(oldest bool) tea.Cmd {
	a := &m.ap
	a.strip.Dismiss()
	m.sel = selection{}
	if a.backend == nil {
		a.noChannel = true
		return nil
	}
	if a.view != nil {
		return nil
	}
	a.gate = &gateBackend{Backend: a.backend}
	a.view = approvals.New(approvals.Options{
		Backend:        a.gate,
		Colors:         m.apprColors(),
		Now:            m.clock,
		Random:         a.random,
		After:          m.afterFn,
		GraphemeWidths: m.graphemes,
	})
	a.limit = nil
	a.wantOldest, a.unread = false, 0
	if it, _, ok := m.oldestWaiting(); oldest && ok {
		a.wantOldest, a.oldestID = true, it.ID
	}
	w, h := m.apprSize()
	return tea.Batch(a.view.SetSize(w, h), a.view.Init())
}

// closeApprovals leaves the approvals screens for the cockpit.
func (m *Model) closeApprovals() {
	a := &m.ap
	a.view, a.limit, a.noChannel, a.wantOldest = nil, nil, false, false
}

// apprKey handles a key press while an approvals screen is open. Every key
// goes to the screen on top (the limit's screen, or the view) and none to the
// cockpit's own commands, ctrl+c aside: it quits from every screen.
func (m *Model) apprKey(v tea.KeyPressMsg) tea.Cmd {
	a := &m.ap
	if v.String() == "ctrl+c" {
		return m.quit()
	}
	if a.noChannel {
		if v.String() == "esc" {
			m.closeApprovals()
		}
		return nil
	}
	if a.limit != nil {
		return m.limitKey(v)
	}
	return m.apprUpdate(v)
}

// apprUpdate hands msg to the view and carries out what it asks of the
// cockpit.
func (m *Model) apprUpdate(msg tea.Msg) tea.Cmd {
	a := &m.ap
	if a.view == nil {
		return nil
	}
	cmd, act := a.view.Update(msg)
	cmds := []tea.Cmd{cmd}
	switch act.Kind {
	case approvals.Close:
		m.closeApprovals()
		return tea.Batch(cmds...)
	case approvals.Quit:
		return m.quit()
	case approvals.Limit:
		cmds = append(cmds, m.openLimit(act.Project))
	case approvals.Lead:
		m.closeApprovals()
		m.selectProject(act.Project)
		return tea.Batch(cmds...)
	}
	cmds = append(cmds, m.driveOldest())
	return tea.Batch(cmds...)
}

// driveOldest opens the card of the pending gate once the view has its first
// list: it moves the view's cursor to the card's place in the view's own list
// with j and presses Enter. These are the only keys the cockpit ever sends the
// view on its own, and they open a card for reading: a decision is made on the
// card with its typed code. The keys are sent at most once, for the view's
// first list. When that list does not hold the card (it was decided or
// expired meanwhile), or the view is no longer at its list, nothing is opened
// and nothing stays armed to fire later.
func (m *Model) driveOldest() tea.Cmd {
	a := &m.ap
	if !a.wantOldest || a.view == nil || a.gate == nil {
		return nil
	}
	ids, reads := a.gate.noted()
	if reads == 0 {
		return nil // the view has not read its list yet
	}
	if a.view.Pending() == 0 && len(ids) > 0 {
		return nil // the view has not taken in the list that was read
	}
	a.wantOldest = false
	at := slices.Index(ids, a.oldestID)
	if at < 0 || len(ids) != a.view.Pending() || a.limit != nil || a.view.CardOpen() {
		return nil
	}
	for i := 0; i < at; i++ {
		a.view.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	}
	cmd, _ := a.view.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return cmd
}

// forward hands a message that is not the cockpit's own to the approvals
// view: its answers to its commands, and what its pager asks for.
func (m *Model) forward(msg tea.Msg) tea.Cmd {
	if m.ap.view == nil {
		return nil
	}
	return m.apprUpdate(msg)
}

// apprGraphemes tells an open approvals view whether the outer terminal
// measures grapheme clusters, after the answer to the mode 2027 query.
func (m *Model) apprGraphemes() tea.Cmd {
	if m.ap.view == nil {
		return nil
	}
	return m.ap.view.SetGraphemeWidths(m.graphemes)
}

// selectProject selects the project of a card in the tree, when it is there.
func (m *Model) selectProject(name string) {
	for i, p := range m.projects {
		if p.Name != name {
			continue
		}
		m.project, m.projectCursor = i, i
		m.lead, m.leadCursor = m.leadSelections[i], m.leadSelections[i]
		for ri, r := range m.treeRows() {
			if r.selectable && r.project == i && r.indent == 0 {
				m.treeCursor = ri
			}
		}
		return
	}
}

// openLimit opens the limit's screen for project over the view, and asks the
// backend for the limit's view from a command.
func (m *Model) openLimit(project string) tea.Cmd {
	a := &m.ap
	a.limit = limitview.New(limitview.Options{Colors: m.limitColors()})
	a.limitProject = project
	w, h := m.apprSize()
	a.limit.SetSize(w, h)
	return m.limitCmd(project, nil)
}

// limitCmd asks the backend for the limit's view of project, or with a change
// asks it to make the change and give the new view.
func (m *Model) limitCmd(project string, change *limits.Change) tea.Cmd {
	a := &m.ap
	a.limitSeq++
	seq, b := a.limitSeq, a.backend
	return func() tea.Msg {
		if change == nil {
			v, err := b.Limit(project)
			return apprLimitMsg{seq: seq, project: project, v: v, err: err}
		}
		v, err := b.SetLimit(project, *change)
		return apprLimitMsg{seq: seq, project: project, v: v, err: err, set: true}
	}
}

// limitKey hands a key to the limit's screen. A change it proposes goes to
// the backend from a command; Esc goes back to the list.
func (m *Model) limitKey(v tea.KeyPressMsg) tea.Cmd {
	a := &m.ap
	act := a.limit.Update(v)
	switch act.Kind {
	case limitview.Close:
		a.limit = nil
	case limitview.Propose:
		c := act.Change
		return m.limitCmd(a.limitProject, &c)
	}
	return nil
}

// onApprLimit gives the limit's screen the backend's answer.
func (m *Model) onApprLimit(v apprLimitMsg) {
	a := &m.ap
	if a.limit == nil || v.seq != a.limitSeq || v.project != a.limitProject {
		return
	}
	if v.err != nil {
		a.limit.SetError(limitErrLine(v.err, v.set))
		return
	}
	a.limit.SetView(v.v)
}

// limitErrLine is one line in the cockpit's words for a refusal of the
// backend. A *record.Error carries a detail the host wrote, and it is shown
// after the cockpit's words; the text of any other error is not.
func limitErrLine(err error, set bool) string {
	what := "the limit could not be read"
	if set {
		what = "the change was refused"
	}
	if code, ok := record.CodeOf(err); ok {
		what += " (" + string(code) + ")"
		var re *record.Error
		if errors.As(err, &re) && re.Detail != "" {
			what += ": " + re.Detail
		}
	}
	return what
}

// resizeApprovals gives the open approvals screens the size of the window.
func (m *Model) resizeApprovals() tea.Cmd {
	a := &m.ap
	w, h := m.apprSize()
	if a.limit != nil {
		a.limit.SetSize(w, h)
	}
	if a.view != nil {
		return a.view.SetSize(w, h)
	}
	return nil
}

// approvalsView draws the open approvals screen over the whole window.
func (m *Model) approvalsView() tea.View {
	a := &m.ap
	w, h := m.apprSize()
	buf := uv.NewBuffer(w, h)
	p := &painter{buf: buf, pal: paletteFor(m.theme), graphemes: m.graphemes}
	p.fill(buf.Bounds())
	switch {
	case a.noChannel:
		p.lines(uv.Rect(1, 0, max(0, w-1), h), []line{
			{"APPROVALS", lnHead},
			{"", lnText},
			{NoOwnerChannel, lnWarn},
			{"Nothing can be approved or denied here.", lnText},
			{"", lnText},
			{"[esc] back", lnDim},
		})
	case a.limit != nil:
		a.limit.Draw(buf, buf.Bounds())
	case a.view != nil:
		a.view.Draw(buf, buf.Bounds())
	}
	return m.newView(buf.Render())
}

// drawStrip draws the arrival strip under the header, over the top rows of
// the panes, while it shows. It is not drawn in session focus: every key goes
// to the session there, so the strip could neither take a key nor be closed,
// and it would cover the first rows of the session's terminal. It shows again
// when the owner returns to console focus.
func (m *Model) drawStrip(p *painter, l layout) {
	s := m.ap.strip
	if !s.Visible() || l.zoom || m.focus == sessionFocus {
		return
	}
	rows := min(s.Rows(), max(0, l.canvas.Dy()-2))
	if rows == 0 {
		return
	}
	s.Draw(p.buf, uv.Rect(0, 1, l.canvas.Dx(), rows), m.stripFocused())
}

// gateLines are the lines of the pending gate in the right pane when there is
// a backend: the oldest waiting card, from the listing.
func (m *Model) gateLines() []line {
	a := &m.ap
	switch {
	case !a.have && a.listFailed:
		return []line{{"State: the list could not be read", lnWarn}}
	case !a.have:
		return []line{{"State: reading the list", lnDim}}
	}
	it, _, ok := m.oldestWaiting()
	if !ok {
		return []line{{"No card waits.", lnText}, {"g opens approvals.", lnDim}}
	}
	action := clean(it.Verb)
	if it.Feature != "" {
		action += " name=" + clean(it.Feature)
	}
	n, oldest := arrival.Waiting(m.lives(), m.clock())
	out := []line{
		{"Action: " + orNone(action), lnText},
		{"Scope: " + orNone(clean(it.Project)), lnText},
		{"Asked: " + it.Asked.Local().Format("15:04") + ", " + arrival.Age(m.clock().Sub(it.Asked)) + " ago", lnText},
		{"State: pending, " + strconv.Itoa(n) + " waiting (oldest " + arrival.Age(oldest) + ")", lnWarn},
	}
	if it.Held {
		out = append(out, line{card.HeldLabel, lnWarn})
	}
	if a.listFailed {
		out = append(out, line{"The list could not be read the last time; this is the earlier one.", lnWarn})
	}
	return append(out, line{"Enter opens the oldest waiting card. g opens approvals.", lnDim})
}

// approvalsTimeline returns the timeline's entries from the listing: for each
// request, when it was asked, and for a request that was decided, what was
// decided and what was applied. The listing's rows carry the time the request
// was asked and no time for the decision, so a decision is shown with
// "--:--". Entries are in the order the requests were asked, oldest first.
func (m *Model) approvalsTimeline() []TimelineEntry {
	a := &m.ap
	if a.backend == nil || !a.have {
		return nil
	}
	type group struct {
		at      time.Time
		entries []TimelineEntry
	}
	var groups []group
	add := func(it card.Item, decided bool) {
		what := clean(it.Verb)
		if it.Feature != "" {
			what += " name=" + clean(it.Feature)
		}
		g := group{at: it.Asked}
		g.entries = append(g.entries, TimelineEntry{Time: it.Asked.Local().Format("15:04"), Text: "asked: " + what + " (" + clean(it.Project) + ")"})
		if decided {
			g.entries = append(g.entries, decidedEntries(it)...)
		}
		groups = append(groups, g)
	}
	for _, it := range a.listing.Recent {
		add(it, true)
	}
	for _, it := range a.listing.Pending {
		add(it, false)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].at.Before(groups[j].at) })
	var out []TimelineEntry
	for _, g := range groups {
		out = append(out, g.entries...)
	}
	if len(out) > maxList {
		out = out[len(out)-maxList:]
	}
	return out
}

// decidedEntries are the timeline entries after a request was asked: what
// became of it, and for a request that was applied or not applied, that too.
// Only the states that are decisions are worded "decided". A request that
// ended with no decision is "ended", and a request that was applied, not
// applied or consumed was approved, though the listing does not say by whom
// (the owner, a rule or a grant), so it says "approved" alone.
func decidedEntries(it card.Item) []TimelineEntry {
	const unknown = "--:--"
	state := clean(string(it.State))
	if it.Line != "" {
		state += " · " + clean(it.Line)
	}
	switch it.State {
	case record.StateApplied, record.StateNotApplied, record.StateConsumed:
		return []TimelineEntry{
			{Time: unknown, Text: "approved"},
			{Time: unknown, Text: "applied: " + state},
		}
	case "", record.StatePending:
		return nil
	case record.StateExpired, record.StateWithdrawn, record.StateVoid:
		return []TimelineEntry{{Time: unknown, Text: "ended: " + state}}
	case record.StateUnknown:
		return []TimelineEntry{{Time: unknown, Text: "state: " + state}}
	}
	return []TimelineEntry{{Time: unknown, Text: "decided: " + state}}
}

// allTimeline is the timeline the cockpit shows: the source's entries, then
// the ones the approvals listing gives.
func (m *Model) allTimeline() []TimelineEntry {
	extra := m.approvalsTimeline()
	if len(extra) == 0 {
		return m.timeline
	}
	out := make([]TimelineEntry, 0, len(m.timeline)+len(extra))
	return append(append(out, m.timeline...), extra...)
}

// approvalsLive reports whether the action list has live controls: there is a
// backend and the source lists a control that decides a gate.
func (m *Model) approvalsLive() bool {
	if m.ap.backend == nil {
		return false
	}
	for _, c := range m.controls {
		if c.Approval {
			return true
		}
	}
	return false
}

// controlReason is what the action list says of a control after its name: its
// reason, or for a live control that it opens the approvals view.
func (m *Model) controlReason(c Control) string {
	if c.Approval && m.ap.backend != nil {
		return "available: g opens the approvals view; a decision is made there, on the card"
	}
	return c.Reason
}

// approvalsHelp are the help lines about approvals.
func (m *Model) approvalsHelp() []string {
	if m.ap.backend == nil {
		return []string{"g: approvals (" + NoOwnerChannel + ")"}
	}
	return []string{
		"g: approvals, from console focus only. Enter on the pending gate opens the oldest waiting card",
		"In approvals: Esc goes back, one step at a time. A decision is made on the card, with its typed code",
	}
}

// keyBar picks the key bar's text: the full one, or the compact one when the
// approvals element needs the room.
func (m *Model) keyBar(full, compact string, width int) string {
	st := m.approvalsStatus()
	if st.Text == "" || ansi.StringWidth(full)+2+ansi.StringWidth(st.Text) <= width {
		return full
	}
	return compact
}
