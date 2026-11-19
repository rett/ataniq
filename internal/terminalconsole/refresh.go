package terminalconsole

import (
	"context"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
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
//     replaces the data and then puts the selection back by name (see
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
	// kept is the selection by name, as it was the last time there were
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
		r.failed = true
		return
	}
	r.failed, r.asOf = false, r.sentAt
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

// keptSelection is the selection as names, with the indexes the names had.
// The names are compared only to find the same row again after a refresh;
// nothing else is decided by them.
type keptSelection struct {
	// set is whether there is a selection to put back.
	set bool
	// project and lead are the selected project and lead.
	project, lead keptRow
	// projectCursor and leadCursor are the cursors of the single-column
	// pages; leadCursor is a lead of the selected project.
	projectCursor, leadCursor keptRow
	// tree is the project of the row under the tree cursor, and treeIndex
	// the row's place in the tree. When treeIsLead is true the row is the
	// lead treeLead of that project, else the project's own row.
	tree, treeLead keptRow
	treeIsLead     bool
	treeIndex      int
	// leads is the lead each project remembers, by the project's name.
	leads map[string]keptRow
}

// keptRow is one name with the index it had.
type keptRow struct {
	name  string
	index int
}

// keepSelection returns the current selection as names.
func (m *Model) keepSelection() keptSelection {
	k := keptSelection{set: true, leads: map[string]keptRow{}}
	projectAt := func(i int) keptRow {
		if i < len(m.projects) {
			return keptRow{m.projects[i].Name, i}
		}
		return keptRow{index: i}
	}
	leadAt := func(pi, li int) keptRow {
		if pi < len(m.projects) && li < len(m.projects[pi].Leads) {
			return keptRow{m.projects[pi].Leads[li].Name, li}
		}
		return keptRow{index: li}
	}
	k.project, k.lead = projectAt(m.project), leadAt(m.project, m.lead)
	k.projectCursor, k.leadCursor = projectAt(m.projectCursor), leadAt(m.project, m.leadCursor)
	if rows := m.treeRows(); m.treeCursor < len(rows) {
		r := rows[m.treeCursor]
		k.tree, k.treeIndex = projectAt(r.project), m.treeCursor
		if r.indent > 0 {
			k.treeIsLead, k.treeLead = true, leadAt(r.project, r.lead)
		}
	}
	for i, p := range m.projects {
		if _, seen := k.leads[p.Name]; !seen {
			k.leads[p.Name] = leadAt(i, m.leadSelections[i])
		}
	}
	return k
}

// find returns the index in names of the row k names: the index it had when
// the name is still there, else the first with the name. When the name is
// gone it returns the nearest index that exists and false.
func (k keptRow) find(n int, name func(int) string) (int, bool) {
	if k.index < n && name(k.index) == k.name {
		return k.index, true
	}
	for i := 0; i < n; i++ {
		if name(i) == k.name {
			return i, true
		}
	}
	return max(0, min(k.index, n-1)), false
}

// applyKeeping replaces the data with in and puts the selection back: the
// selected project and lead, the cursors and the lead each project
// remembers. A thing that still exists is found by its name, wherever it
// moved; for one that is gone the selection moves to the nearest that
// exists, which is the one at the same place or the last. The selection is
// remembered across a time with no projects, so that it comes back with them.
func (m *Model) applyKeeping(in Projection) {
	if len(m.projects) > 0 {
		m.refresh.kept = m.keepSelection()
	}
	k := m.refresh.kept
	m.apply(in)
	m.project, m.lead, m.projectCursor, m.leadCursor, m.treeCursor = 0, 0, 0, 0, 0
	m.scrolls[paneTimeline] = min(m.scrolls[paneTimeline], max(0, len(m.timeline)-1))
	if len(m.projects) == 0 || !k.set {
		return
	}
	projectName := func(i int) string { return m.projects[i].Name }
	leadIn := func(pi int, r keptRow) (int, bool) {
		leads := m.projects[pi].Leads
		return r.find(len(leads), func(i int) string { return leads[i].Name })
	}
	for i, p := range m.projects {
		if r, ok := k.leads[p.Name]; ok {
			m.leadSelections[i], _ = leadIn(i, r)
		}
	}
	var found bool
	if m.project, found = k.project.find(len(m.projects), projectName); found {
		m.lead, _ = leadIn(m.project, k.lead)
		m.leadCursor, _ = leadIn(m.project, k.leadCursor)
	} else {
		// Another project stands where the selected one was: its own
		// remembered lead is the selection.
		m.lead = m.leadSelections[m.project]
		m.leadCursor = m.lead
	}
	m.projectCursor, _ = k.projectCursor.find(len(m.projects), projectName)

	// The tree cursor: the same row when its project is still there (the
	// lead, the nearest lead, or the project's own row when it has no lead
	// left), else the nearest row that can be selected.
	rows := m.treeRows()
	if pi, ok := k.tree.find(len(m.projects), projectName); ok {
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
		return
	}
	for i := min(k.treeIndex, len(rows)-1); i >= 0; i-- {
		if rows[i].selectable {
			m.treeCursor = i
			break
		}
	}
}
