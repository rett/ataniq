package terminalconsole

import (
	"context"
	"slices"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"ataniq/internal/terminalconsole/leadctl"
)

// The refresh. A cockpit whose source can change asks it again on a tick.
//
// How it works:
//
//   - A tick arrives every period, as a message that carries its time. That
//     time is the refresh's only clock: nothing here reads one.
//   - On a tick the model starts one request, unless one is still out. The
//     request is a command (tea.Cmd): Bubble Tea runs it on a goroutine of
//     its own and brings the answer back as a message. The source is never
//     called inside Update or while drawing, so a slow source cannot stop a
//     key from being handled.
//   - Bubble Tea hands Update one message at a time, so a tick, an answer and
//     a key are each handled whole, in the order they arrived. An answer
//     replaces the data and then puts the selection back by identity (see
//     applyKeeping), so a key means the same row whether it arrived before
//     the answer or after it.
//   - An answer with an error, or none within two periods, keeps the view as
//     it is and makes the header say how old it is. The error's text is not
//     shown: only text written here is.

// DefaultRefresh is how often a Refresher is asked again when Options.Refresh
// is zero.
const DefaultRefresh = 3 * time.Second

// Refresher is a Source that can be asked again and can say that it failed.
// A source that is only a Source is asked again with Snapshot, and only when
// Options.Refresh is positive.
//
// Refresh is called on a goroutine that is not the program's, at most one
// call at a time, so it may block: on a socket, for example. The context ends
// one period after the call began and when the console is closed; a Refresh
// that returns when it ends is never counted as late. An error keeps the
// last view on the screen with its age. A source that knows why it has
// nothing to show returns a Projection with Problem and NextStep set, and no
// error: the cockpit then shows those in place of the last view.
type Refresher interface {
	Source
	Refresh(ctx context.Context) (Projection, error)
}

// refreshState is the refresh's part of the model.
type refreshState struct {
	// source is asked again every period. Nil means there is no refresh.
	source Source
	period time.Duration
	// ctx ends when the console is closed; stop ends it.
	ctx  context.Context
	stop context.CancelFunc
	// seq numbers the requests. An answer with another number than the
	// latest request's is dropped.
	seq int
	// pending is whether a request is out, sentAt the tick it was started on.
	pending bool
	sentAt  time.Time
	// now is the time of the latest tick.
	now time.Time
	// asOf is when the request that gave the view on the screen was started.
	// It is zero while the screen still shows the first snapshot.
	asOf time.Time
	// failed is whether the latest request failed or is late. The header
	// then shows the view's age.
	failed bool
	// kept is the selection by identity, as it was the last time there were
	// projects to select from: see applyKeeping.
	kept keptSelection
}

// refreshTickMsg is the refresh's tick.
type refreshTickMsg struct{ at time.Time }

// refreshMsg is the answer to request seq.
type refreshMsg struct {
	seq int
	p   Projection
	err error
}

// initRefresh decides whether source is asked again, and how often.
func (m *Model) initRefresh(source Source) {
	period := m.opts.Refresh
	if _, ok := source.(Refresher); ok && period == 0 {
		period = DefaultRefresh
	}
	if period <= 0 {
		return
	}
	m.refresh.source, m.refresh.period = source, period
	m.refresh.ctx, m.refresh.stop = context.WithCancel(context.Background())
}

// startRefresh schedules the first tick, at once. It returns nil when there
// is no refresh.
func (m *Model) startRefresh() tea.Cmd {
	if m.refresh.source == nil {
		return nil
	}
	return m.refreshTickCmd(0)
}

func (m *Model) refreshTickCmd(d time.Duration) tea.Cmd {
	return m.after(d, func(t time.Time) tea.Msg { return refreshTickMsg{at: t} })
}

// onRefreshTick moves the refresh's clock, counts a request that has been out
// for two periods as failed, starts a request when none is out, and schedules
// the next tick. A late request stays the one that is out: a source that does
// not come back is not called a second time.
func (m *Model) onRefreshTick(v refreshTickMsg) tea.Cmd {
	r := &m.refresh
	if r.source == nil {
		return nil
	}
	if v.at.After(r.now) {
		r.now = v.at
	}
	next := m.refreshTickCmd(r.period)
	if r.pending {
		if r.now.Sub(r.sentAt) >= 2*r.period {
			if !r.failed {
				m.markOffline()
			}
			r.failed = true
		}
		return next
	}
	r.seq++
	r.pending, r.sentAt = true, r.now
	return tea.Batch(m.requestCmd(), next)
}

// requestCmd returns the command that asks the source. It reads nothing of
// the model when it runs.
func (m *Model) requestCmd() tea.Cmd {
	src, parent, period, seq := m.refresh.source, m.refresh.ctx, m.refresh.period, m.refresh.seq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, period)
		defer cancel()
		if r, ok := src.(Refresher); ok {
			p, err := r.Refresh(ctx)
			return refreshMsg{seq: seq, p: p, err: err}
		}
		return refreshMsg{seq: seq, p: src.Snapshot()}
	}
}

// onRefresh takes the answer to the request that is out. A failure keeps the
// view; a success replaces it and keeps the selection.
func (m *Model) onRefresh(v refreshMsg) {
	r := &m.refresh
	if !r.pending || v.seq != r.seq {
		return
	}
	r.pending = false
	if v.err != nil {
		m.markOffline()
		r.failed = true
		return
	}
	r.failed = v.p.Offline && slices.ContainsFunc(m.projects, func(p Project) bool { return p.ID != "" })
	if !r.failed {
		r.asOf = r.sentAt
	}
	m.applyKeeping(v.p)
}

// ageText says how old the view is, when the latest refresh failed or is
// late: "as of 12 s ago". It is empty otherwise. The text is written here;
// nothing of the source or of its error is in it.
func (m *Model) ageText() string {
	r := &m.refresh
	if !r.failed {
		return ""
	}
	if r.asOf.IsZero() {
		return "not refreshed yet"
	}
	d := max(0, r.now.Sub(r.asOf))
	switch {
	case d < time.Minute:
		return "as of " + strconv.Itoa(int(d/time.Second)) + " s ago"
	case d < time.Hour:
		return "as of " + strconv.Itoa(int(d/time.Minute)) + " min ago"
	}
	return "as of " + strconv.Itoa(int(d/time.Hour)) + " h ago"
}

// Legacy fixture sources supply only display names. Keep their historical
// navigation behavior separate from directory keys, even for equal strings.
type rowKey struct {
	id          leadctl.Identity
	fixtureName string
}

func (p Project) key() rowKey {
	if p.ID == "" {
		return rowKey{fixtureName: p.Name}
	}
	return rowKey{id: leadctl.Identity{Home: p.HomeID, Project: p.ID}}
}

func (p Project) leadKey(i int) rowKey {
	l := p.Leads[i]
	if p.ID == "" && l.ID == "" {
		return rowKey{fixtureName: l.Name}
	}
	return rowKey{id: leadctl.Identity{Home: p.HomeID, Project: p.ID, Lead: l.ID, Conversation: l.ConversationID, Epoch: l.Epoch}}
}

func (p Project) label() string {
	name := p.Name
	if p.HomeID != "" {
		name = clean(p.HomeID) + " / " + name
	}
	if p.Offline {
		name = "[stale] " + name
	}
	return name
}

func (m *Model) moveProject(delta int) {
	start := m.projectCursor + delta
	if m.projectCursor < 0 && delta < 0 {
		start = len(m.projects) - 1
	}
	for i := start; i >= 0 && i < len(m.projects); i += delta {
		if !m.projects[i].Offline {
			m.projectCursor = i
			return
		}
	}
}

type resumePosition struct {
	conversation, evidence, timeline int
}

func (m *Model) selectedID() leadctl.Identity {
	if m.project < 0 || m.project >= len(m.projects) {
		return leadctl.Identity{}
	}
	p := m.projects[m.project]
	if p.Offline || m.lead < 0 || m.lead >= len(p.Leads) {
		return leadctl.Identity{}
	}
	return p.leadKey(m.lead).id
}

func (m *Model) rememberScroll() {
	id := m.selectedID()
	if id == (leadctl.Identity{}) {
		return
	}
	if m.resume == nil {
		m.resume = make(map[leadctl.Identity]resumePosition)
	}
	pos := m.resume[id]
	pos.evidence, pos.timeline = m.scrolls[paneEvidence], m.scrolls[paneTimeline]
	if m.page == "conversation" {
		pos.conversation = m.scroll
	}
	m.resume[id] = pos
}

func (m *Model) restoreScroll() {
	pos := m.resume[m.selectedID()]
	m.scrolls[paneEvidence], m.scrolls[paneTimeline] = pos.evidence, pos.timeline
	if m.page == "conversation" {
		m.scroll = pos.conversation
	}
}

type keptSelection struct {
	set                                      bool
	project, lead, projectCursor, leadCursor keptRow
	tree, treeLead                           keptRow
	treeIsLead                               bool
	treeIndex                                int
	leads                                    map[rowKey]keptRow
}

type keptRow struct {
	key   rowKey
	index int
}

func (m *Model) keepSelection() keptSelection {
	k := keptSelection{set: true, leads: map[rowKey]keptRow{}, treeIndex: m.treeCursor}
	projectAt := func(i int) keptRow {
		if i >= 0 && i < len(m.projects) {
			return keptRow{m.projects[i].key(), i}
		}
		return keptRow{index: -1}
	}
	leadAt := func(pi, li int) keptRow {
		if pi >= 0 && pi < len(m.projects) && li >= 0 && li < len(m.projects[pi].Leads) {
			return keptRow{m.projects[pi].leadKey(li), li}
		}
		return keptRow{index: -1}
	}
	k.project, k.lead = projectAt(m.project), leadAt(m.project, m.lead)
	k.projectCursor, k.leadCursor = projectAt(m.projectCursor), leadAt(m.project, m.leadCursor)
	k.tree = keptRow{index: -1}
	if rows := m.treeRows(); m.treeCursor >= 0 && m.treeCursor < len(rows) {
		r := rows[m.treeCursor]
		k.tree = projectAt(r.project)
		if r.indent > 0 {
			k.treeIsLead, k.treeLead = true, leadAt(r.project, r.lead)
		}
	}
	for i, p := range m.projects {
		k.leads[p.key()] = leadAt(i, m.leadSelections[i])
	}
	return k
}

// find never substitutes another directory row if a target disappeared.
// Only the legacy fixture navigation falls back to the nearest row.
func (k keptRow) find(n int, key func(int) rowKey) (int, bool) {
	if k.index < 0 {
		return -1, false
	}
	if k.index < n && key(k.index) == k.key {
		return k.index, true
	}
	for i := 0; i < n; i++ {
		if key(i) == k.key {
			return i, true
		}
	}
	if k.key.id != (leadctl.Identity{}) {
		return -1, false
	}
	return max(0, min(k.index, n-1)), false
}

// markOffline handles a failed or overdue refresh. Fixture rows retain
// their historical age-only behavior.
func (m *Model) markOffline() {
	for i := range m.projects {
		p := &m.projects[i]
		if p.ID != "" {
			p.Offline = true
		}
	}
	m.clearOffline()
}

func (m *Model) clearOffline() {
	offline := 0
	for i, p := range m.projects {
		if !p.Offline {
			continue
		}
		offline++
		m.leadSelections[i] = -1
		if i == m.project {
			m.project, m.lead, m.leadCursor = -1, -1, -1
			m.leaveMissingRow()
			m.restoreScroll()
		}
		if i == m.projectCursor {
			m.projectCursor = -1
		}
	}
	if offline > 0 && offline == len(m.projects) {
		m.directoryOffline = true
		m.controls, m.controlUnavailable = nil, "The directory is offline."
		if m.lc.view != nil {
			m.lc.view.MarkOffline()
			m.closeLeads()
		}
	}
	if rows := m.treeRows(); m.treeCursor >= 0 && m.treeCursor < len(rows) && !rows[m.treeCursor].selectable {
		m.treeCursor = -1
	}
}

func (m *Model) leaveMissingRow() {
	switch m.page {
	case "conversation", "leads", "tasks":
		m.page, m.scroll = "projects", 0
	}
}

// applyKeeping takes a full directory read. Only explicitly offline homes
// retain old rows. Missing rows in a successful home read are gone.
func (m *Model) applyKeeping(in Projection) {
	old := m.projects
	if len(old) > 0 {
		m.refresh.kept = m.keepSelection()
	}
	k := m.refresh.kept
	stale := old[:0]
	for _, p := range old {
		if p.ID != "" && (in.Offline || slices.Contains(in.OfflineHomes, p.HomeID)) {
			p.Offline = true
			stale = append(stale, p)
		}
	}
	m.apply(in, maxProjects-len(stale))
	m.projects = append(m.projects, stale...)
	for range stale {
		m.leadSelections = append(m.leadSelections, -1)
	}
	if in.Offline && len(m.projects) > 0 {
		if m.problem != "" {
			m.facts = append(m.facts, Check{Name: "Directory", State: m.problem})
		}
		m.problem = ""
	}
	if len(m.resume) != 0 {
		resume := make(map[leadctl.Identity]resumePosition)
		for _, p := range m.projects {
			for i := range p.Leads {
				id := p.leadKey(i).id
				if pos, ok := m.resume[id]; ok {
					resume[id] = pos
				}
			}
		}
		m.resume = resume
	}
	m.project, m.lead, m.projectCursor, m.leadCursor, m.treeCursor = 0, 0, 0, 0, 0
	m.scrolls[paneTimeline] = min(m.scrolls[paneTimeline], max(0, len(m.timeline)-1))
	if !k.set {
		m.clearOffline()
		return
	}
	if len(m.projects) == 0 {
		if k.project.key.id != (leadctl.Identity{}) {
			m.refresh.kept = keptSelection{set: true, project: keptRow{index: -1}, projectCursor: keptRow{index: -1}, tree: keptRow{index: -1}, treeIndex: -1}
			m.leaveMissingRow()
		}
		return
	}
	projectKey := func(i int) rowKey { return m.projects[i].key() }
	leadIn := func(pi int, r keptRow) (int, bool) {
		p := m.projects[pi]
		return r.find(len(p.Leads), p.leadKey)
	}
	for i, p := range m.projects {
		if r, ok := k.leads[p.key()]; ok {
			m.leadSelections[i], _ = leadIn(i, r)
		}
	}
	var found bool
	m.project, found = k.project.find(len(m.projects), projectKey)
	switch {
	case found:
		m.lead, _ = leadIn(m.project, k.lead)
		m.leadCursor, _ = leadIn(m.project, k.leadCursor)
		if m.lead < 0 && m.page == "conversation" {
			m.page, m.scroll = "leads", 0
		}
	case m.project >= 0:
		m.lead = m.leadSelections[m.project]
		m.leadCursor = m.lead
	default:
		m.lead, m.leadCursor = -1, -1
		m.leaveMissingRow()
	}
	if k.lead.key.id != (leadctl.Identity{}) && k.lead.key.id != m.selectedID() {
		m.restoreScroll()
	}
	m.projectCursor, _ = k.projectCursor.find(len(m.projects), projectKey)
	m.treeCursor = -1
	rows := m.treeRows()
	if pi, ok := k.tree.find(len(m.projects), projectKey); ok {
		wantLead := k.treeIsLead && len(m.projects[pi].Leads) > 0
		li := 0
		if wantLead {
			li, _ = leadIn(pi, k.treeLead)
		}
		for i, r := range rows {
			if r.selectable && r.project == pi && ((wantLead && r.indent == 1 && r.lead == li) || (!wantLead && r.indent == 0)) {
				m.treeCursor = i
				break
			}
		}
	} else if k.tree.key.id == (leadctl.Identity{}) {
		for i := min(k.treeIndex, len(rows)-1); i >= 0; i-- {
			if rows[i].selectable {
				m.treeCursor = i
				break
			}
		}
	}
	m.clearOffline()
}
