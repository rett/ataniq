package terminalconsole

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fakeSource is a Refresher the test drives: it answers with what the test
// set last, and counts its calls.
type fakeSource struct {
	mu        sync.Mutex
	p         Projection
	err       error
	snapshots int
	refreshes int
	// next, when set, makes each Refresh answer with next(call number).
	next func(n int) Projection
	// block makes Refresh wait for its context to end.
	block bool
}

func (f *fakeSource) Snapshot() Projection {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots++
	return f.p
}

func (f *fakeSource) Refresh(ctx context.Context) (Projection, error) {
	f.mu.Lock()
	f.refreshes++
	n, p, err, block, next := f.refreshes, f.p, f.err, f.block, f.next
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return Projection{}, ctx.Err()
	}
	if next != nil {
		p = next(n)
	}
	return p, err
}

func (f *fakeSource) set(p Projection, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.p, f.err = p, err
}

func (f *fakeSource) calls() (snapshots, refreshes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshots, f.refreshes
}

// leadsOf makes a projection of projects that each have the named leads:
// "alpha:one,two" is the project alpha with the leads one and two.
func leadsOf(projects ...string) Projection {
	var p Projection
	for _, s := range projects {
		name, leads, _ := strings.Cut(s, ":")
		pr := Project{Name: name}
		for _, l := range strings.Split(leads, ",") {
			if l != "" {
				pr.Leads = append(pr.Leads, Lead{Name: l})
			}
		}
		p.Projects = append(p.Projects, pr)
	}
	return p
}

// refreshTick sends a refresh tick d after the last one and returns the
// request the model started, nil when it started none.
func (r *rig) refreshTick(d time.Duration) tea.Cmd {
	r.t.Helper()
	r.clock = r.clock.Add(d)
	_, cmd := r.m.Update(refreshTickMsg{at: r.clock})
	return cmd
}

// answer runs a request and gives the model its answer.
func (r *rig) answer(request tea.Cmd) {
	r.t.Helper()
	if request == nil {
		r.t.Fatal("no request was started")
	}
	msg := request()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if m, ok := c().(refreshMsg); ok {
				msg = m
			}
		}
	}
	if _, ok := msg.(refreshMsg); !ok {
		r.t.Fatalf("the request answered %T", msg)
	}
	r.m.Update(msg)
}

// refresh sends a tick and answers the request it started.
func (r *rig) refresh(d time.Duration) {
	r.t.Helper()
	r.answer(r.refreshTick(d))
}

// selectedNames returns the names of the selected project and lead, and the
// text of the row under the tree cursor.
func (r *rig) selectedNames() (project, lead, cursor string) {
	project, lead, _ = r.m.selected()
	if rows := r.m.treeRows(); r.m.treeCursor < len(rows) {
		cursor = rows[r.m.treeCursor].text
	}
	return
}

func TestTheFixtureIsAskedOnce(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	if r.m.refresh.source != nil {
		t.Fatal("the fixture has a refresh")
	}
	for _, s := range r.sched {
		if _, ok := s.fn(r.clock).(refreshTickMsg); ok {
			t.Fatal("the fixture scheduled a refresh tick")
		}
	}
	// A tick that arrives anyway starts nothing.
	if cmd := r.refreshTick(time.Second); cmd != nil {
		t.Fatal("a tick started a request")
	}
	// A source that is only a Source is asked once too, and a negative
	// period turns the refresh of a Refresher off.
	plain := sample{leadsOf("alpha:one")}
	if m := NewWithOptions(plain, Options{}); m.refresh.source != nil {
		t.Fatal("a plain source has a refresh")
	}
	if m := NewWithOptions(&fakeSource{}, Options{Refresh: -1}); m.refresh.source != nil {
		t.Fatal("a negative period left the refresh on")
	}
}

func TestRefreshPeriod(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:one")}
	r := newSourceRig(t, src, 140, 45, Options{})
	if r.m.refresh.period != DefaultRefresh {
		t.Fatal(r.m.refresh.period)
	}
	// The first tick is scheduled at once, when the window size is known,
	// and each tick schedules the next one a period later.
	var first []time.Duration
	for _, s := range r.sched {
		if _, ok := s.fn(r.clock).(refreshTickMsg); ok {
			first = append(first, s.d)
		}
	}
	if len(first) != 1 || first[0] != 0 {
		t.Fatal(first)
	}
	r.sched = nil
	r.refresh(0)
	if len(r.sched) != 1 || r.sched[0].d != DefaultRefresh {
		t.Fatalf("%+v", r.sched)
	}
	if _, ok := r.sched[0].fn(r.clock).(refreshTickMsg); !ok {
		t.Fatal("the next tick is not a refresh tick")
	}
	if r := newSourceRig(t, src, 140, 45, Options{Refresh: 7 * time.Second}); r.m.refresh.period != 7*time.Second {
		t.Fatal(r.m.refresh.period)
	}
}

// countingSource is a Source that is not a Refresher.
type countingSource struct {
	mu sync.Mutex
	n  int
}

func (c *countingSource) Snapshot() Projection {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return leadsOf("gen-" + string(rune('0'+c.n)) + ":lead")
}

func TestAPlainSourceIsAskedAgainWithSnapshot(t *testing.T) {
	src := &countingSource{}
	r := newSourceRig(t, src, 140, 45, Options{Refresh: time.Second})
	if v := r.view(); !strings.Contains(v, "gen-1") {
		t.Fatal(v)
	}
	r.refresh(0)
	r.refresh(time.Second)
	if v := r.view(); !strings.Contains(v, "gen-3") || strings.Contains(v, "gen-1") {
		t.Fatal(v)
	}
}

func TestRefreshIsACommandAndNeverRunsInUpdateOrTheDraw(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:one")}
	r := newSourceRig(t, src, 140, 45, Options{})
	request := r.refreshTick(0)
	r.view()
	r.typ("jk")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	r.refreshTick(time.Second)
	r.view()
	if s, n := src.calls(); s != 1 || n != 0 {
		t.Fatalf("the source was called outside a command: %d snapshots, %d refreshes", s, n)
	}
	r.answer(request)
	if s, n := src.calls(); s != 1 || n != 1 {
		t.Fatal(s, n)
	}
}

func TestRefreshShowsWhatTheSourceSaysNow(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:one")}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	src.set(leadsOf("alpha:one", "beta:two"), nil)
	if strings.Contains(r.view(), "beta") {
		t.Fatal("the view changed without a refresh")
	}
	r.refresh(0)
	v := r.view()
	if !strings.Contains(v, "beta") || !strings.Contains(v, "two") || strings.Contains(v, "as of") || strings.Contains(v, "not refreshed") {
		t.Fatal(v)
	}
	// A source with a problem is a success: the problem replaces the view,
	// and the source is asked again all the same.
	src.set(Projection{Problem: "no service answers", NextStep: "start the service"}, nil)
	r.refresh(3 * time.Second)
	v = wordsOf(r.view())
	if strings.Contains(v, "beta") || !strings.Contains(v, "no service") || !strings.Contains(v, "Asking again every 3s.") || strings.Contains(v, "as of") {
		t.Fatal(r.view())
	}
	src.set(leadsOf("alpha:one"), nil)
	r.refresh(3 * time.Second)
	if v := r.view(); !strings.Contains(v, "alpha") || strings.Contains(v, "no service") {
		t.Fatal(v)
	}
}

func TestAFailedRefreshKeepsTheViewAndShowsItsAge(t *testing.T) {
	for _, label := range []string{testLabel, ""} {
		src := &fakeSource{p: leadsOf("alpha:one")}
		r := newSourceRig(t, src, 140, 45, Options{Label: label})
		r.refresh(0)
		src.set(leadsOf("gone:gone"), errors.New("secret\x1b]52;c;payload\a"))
		for i, want := range []string{"as of 3 s ago", "as of 6 s ago", "as of 9 s ago", "as of 12 s ago"} {
			r.refresh(3 * time.Second)
			v := r.view()
			if !strings.Contains(strings.Split(v, "\n")[0], want) {
				t.Fatalf("label %q, failure %d: the header does not say %q:\n%s", label, i+1, want, v)
			}
			if !strings.Contains(v, "alpha") || strings.Contains(v, "gone") || strings.Contains(v, "secret") || strings.Contains(v, "payload") {
				t.Fatalf("label %q: the view was not kept, or the error is shown:\n%s", label, v)
			}
		}
		// Longer ages are in minutes and hours.
		r.refresh(5 * time.Minute)
		if v := r.view(); !strings.Contains(v, "as of 5 min ago") {
			t.Fatal(v)
		}
		r.refresh(3 * time.Hour)
		if v := r.view(); !strings.Contains(v, "as of 3 h ago") {
			t.Fatal(v)
		}
		// The next success clears the age.
		src.set(leadsOf("alpha:one", "beta:two"), nil)
		r.refresh(3 * time.Second)
		if v := r.view(); strings.Contains(v, "as of") || !strings.Contains(v, "beta") {
			t.Fatal(v)
		}
	}
	// The single-column pages of a labelled view show the age too.
	src := &fakeSource{p: leadsOf("alpha:one")}
	r := newSourceRig(t, src, 70, 20, Options{Label: testLabel})
	r.refresh(0)
	src.set(Projection{}, errors.New("failed"))
	r.refresh(3 * time.Second)
	if v := r.view(); !strings.Contains(v, testLabel+" · as of 3 s ago") || !strings.Contains(v, "alpha") {
		t.Fatal(v)
	}
}

func TestARefreshThatFailsBeforeAnyAnswerSaysSo(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:one"), err: errors.New("failed")}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	r.refresh(0)
	if v := r.view(); !strings.Contains(v, "not refreshed yet") || !strings.Contains(v, "alpha") {
		t.Fatal(v)
	}
}

func TestARefreshThatDoesNotComeBackCountsAsFailedAndIsNotRepeated(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:one")}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	r.refresh(0)
	late := r.refreshTick(3 * time.Second)
	if late == nil {
		t.Fatal("no request")
	}
	// One period later the request is not late yet; two periods later it is.
	if cmd := r.refreshTick(3 * time.Second); cmd != nil {
		t.Fatal("a second request was started while one is out")
	}
	if v := r.view(); strings.Contains(v, "as of") {
		t.Fatal(v)
	}
	if cmd := r.refreshTick(3 * time.Second); cmd != nil {
		t.Fatal("a second request was started while one is out")
	}
	if v := r.view(); !strings.Contains(v, "as of 9 s ago") || !strings.Contains(v, "alpha") {
		t.Fatal(v)
	}
	// Keys are handled meanwhile.
	r.typ("a")
	if v := r.view(); !strings.Contains(v, "Unavailable") {
		t.Fatal(v)
	}
	// The late answer is taken when it comes, and the next tick asks again.
	src.set(leadsOf("alpha:one", "beta:two"), nil)
	r.answer(late)
	if v := r.view(); strings.Contains(v, "as of") || !strings.Contains(v, "beta") {
		t.Fatal(v)
	}
	if cmd := r.refreshTick(3 * time.Second); cmd == nil {
		t.Fatal("no request after the late answer")
	}
	// An answer to a request that is not the one out is dropped.
	r.m.Update(refreshMsg{seq: r.m.refresh.seq + 5, p: leadsOf("stray:stray")})
	if v := r.view(); strings.Contains(v, "stray") {
		t.Fatal(v)
	}
}

func TestTheRequestEndsWithItsPeriodAndWithTheConsole(t *testing.T) {
	// A request's context ends one period after it began.
	src := &fakeSource{block: true}
	r := newSourceRig(t, src, 140, 45, Options{Refresh: 20 * time.Millisecond})
	request := r.refreshTick(0)
	done := make(chan tea.Msg, 1)
	go func() { done <- request() }()
	select {
	case msg := <-done:
		if m, ok := msg.(refreshMsg); !ok || !errors.Is(m.err, context.DeadlineExceeded) {
			t.Fatalf("%#v", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the request outlived its period")
	}
	// Close ends a request that is out.
	r = newSourceRig(t, src, 140, 45, Options{Refresh: time.Hour})
	request = r.refreshTick(0)
	go func() { done <- request() }()
	r.m.Close()
	select {
	case msg := <-done:
		if m, ok := msg.(refreshMsg); !ok || !errors.Is(m.err, context.Canceled) {
			t.Fatalf("%#v", msg)
		}
		// Its answer changes nothing that matters after Close.
		r.m.Update(msg)
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not end the request")
	}
}

// endedSource is a Refresher that records, for each call of Refresh, whether
// the context it was given had already ended.
type endedSource struct {
	mu    sync.Mutex
	ended []bool
}

func (e *endedSource) Snapshot() Projection { return leadsOf("alpha:one") }

func (e *endedSource) Refresh(ctx context.Context) (Projection, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ended = append(e.ended, ctx.Err() != nil)
	return leadsOf("alpha:one"), ctx.Err()
}

func TestTheRefreshOutlivesAReplay(t *testing.T) {
	// A view with no label replays the recording on "r". That stops the
	// replay's pane and nothing else: the next request's context is not one
	// that has already ended.
	src := &endedSource{}
	r := newSourceRig(t, src, 140, 45, Options{})
	if r.m.labelled() || r.m.rep.pane == nil {
		t.Fatal("the view has no recording to replay")
	}
	r.refresh(0)
	before := r.m.rep.pane
	r.typ("r")
	if r.m.rep.pane == nil || r.m.rep.pane == before {
		t.Fatal("the recording was not started again")
	}
	r.refresh(3 * time.Second)
	r.refresh(3 * time.Second)
	if got := src.ended; len(got) != 3 || got[0] || got[1] || got[2] {
		t.Fatalf("the contexts that had ended: %v", got)
	}
	if v := r.view(); strings.Contains(v, "as of") {
		t.Fatalf("the view shows an age after a replay:\n%s", v)
	}
	// Close still ends the refresh.
	r.m.Close()
	r.refresh(3 * time.Second)
	if got := src.ended; len(got) != 4 || !got[3] {
		t.Fatalf("after Close: %v", got)
	}
}

func TestTheTreeCursorMovesToTheNearestRowWhenItsProjectIsGone(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:a1", "beta:b1,b2", "gamma:g1")}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	r.refresh(0)
	// The rows are alpha, a1, beta, b1, b2, gamma, g1. The cursor goes to
	// gamma's lead; nothing is selected with it, so the selection stays on
	// alpha and only the cursor is in the project that goes away.
	r.typ("jjjjjj")
	if p, _, c := r.selectedNames(); p != "alpha" || c != "g1" {
		t.Fatal(p, c)
	}
	src.set(leadsOf("alpha:a1", "beta:b1,b2"), nil)
	r.refresh(3 * time.Second)
	// The nearest row at or above its old place is beta's last lead, not
	// the first row.
	if p, l, c := r.selectedNames(); p != "alpha" || l != "a1" || c != "b2" {
		t.Fatal(p, l, c)
	}
	if rows := r.m.treeRows(); r.m.treeCursor != len(rows)-1 || !rows[r.m.treeCursor].selectable {
		t.Fatal(r.m.treeCursor)
	}
	// The row at the old place is a coordinator's, which cannot be selected:
	// the cursor is on the nearest row above it that can be.
	src.set(leadsOf("alpha:a1", "beta:b1,b2", "gamma:g1"), nil)
	r.refresh(3 * time.Second)
	r.typ("jj")
	if _, _, c := r.selectedNames(); c != "g1" {
		t.Fatal(c)
	}
	src.set(Projection{Projects: []Project{{Name: "alpha", Leads: []Lead{{Name: "a1", Subagents: []string{"s1", "s2"}}}}}}, nil)
	r.refresh(3 * time.Second)
	if _, _, c := r.selectedNames(); c != "a1" {
		t.Fatal(c)
	}
}

func TestSelectionSurvivesARefresh(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:a1,a2", "beta:b1,b2", "gamma:g1")}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	r.refresh(0)
	// Select beta's second lead and leave the cursor on it: rows are alpha,
	// a1, a2, beta, b1, b2.
	r.typ("jjjjj")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p, l, c := r.selectedNames(); p != "beta" || l != "b2" || c != "b2" {
		t.Fatal(p, l, c)
	}
	// The same things in another order, with new ones around them.
	src.set(leadsOf("new:n1", "gamma:g1", "beta:b0,b2,b1", "alpha:a1,a2"), nil)
	r.refresh(3 * time.Second)
	if p, l, c := r.selectedNames(); p != "beta" || l != "b2" || c != "b2" {
		t.Fatal(p, l, c)
	}
	if !strings.Contains(r.view(), "● b2") {
		t.Fatal(r.view())
	}
	// The selected lead is gone: the nearest lead of its project takes its
	// place, here the one where it stood.
	src.set(leadsOf("new:n1", "gamma:g1", "beta:b0,b1", "alpha:a1,a2"), nil)
	r.refresh(3 * time.Second)
	if p, l, c := r.selectedNames(); p != "beta" || l != "b1" || c != "b1" {
		t.Fatal(p, l, c)
	}
	// Its project has no lead left: the project's own row.
	src.set(leadsOf("new:n1", "gamma:g1", "beta:", "alpha:a1,a2"), nil)
	r.refresh(3 * time.Second)
	if p, l, c := r.selectedNames(); p != "beta" || l != "" || c != "beta" {
		t.Fatal(p, l, c)
	}
	// The selected project is gone: the project where it stood, with the
	// lead that project remembers; the cursor is on a row that can be
	// selected.
	src.set(leadsOf("new:n1", "gamma:g1", "alpha:a1,a2"), nil)
	r.refresh(3 * time.Second)
	p, l, c := r.selectedNames()
	if p != "alpha" || l != "a1" || c == "" {
		t.Fatal(p, l, c)
	}
	if rows := r.m.treeRows(); !rows[r.m.treeCursor].selectable {
		t.Fatal("the cursor is on a row that cannot be selected")
	}
	// Fewer projects than the selection's place: the last one.
	r.typ("jjjjjjjj")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p, l, _ := r.selectedNames(); p != "alpha" || l != "a2" {
		t.Fatal(p, l)
	}
	src.set(leadsOf("new:n1"), nil)
	r.refresh(3 * time.Second)
	if p, l, c := r.selectedNames(); p != "new" || l != "n1" || c == "" {
		t.Fatal(p, l, c)
	}
}

func TestSelectionComesBackAfterATimeWithNoProjects(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:a1,a2", "beta:b1,b2")}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	r.refresh(0)
	r.typ("jjjj")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p, l, c := r.selectedNames(); p != "beta" || l != "b1" || c != "b1" {
		t.Fatal(p, l, c)
	}
	src.set(Projection{Problem: "no service answers"}, nil)
	r.refresh(3 * time.Second)
	r.refresh(3 * time.Second)
	// Keys with nothing to select change nothing and break nothing.
	r.typ("jk")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	r.view()
	src.set(leadsOf("alpha:a1,a2", "beta:b1,b2"), nil)
	r.refresh(3 * time.Second)
	if p, l, c := r.selectedNames(); p != "beta" || l != "b1" || c != "b1" {
		t.Fatal(p, l, c)
	}
}

func TestEachProjectRemembersItsLeadAcrossARefresh(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:a1,a2", "beta:b1,b2")}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	r.refresh(0)
	// Select a2, then beta's b1: alpha remembers a2.
	r.typ("jj")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	r.typ("jj")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	src.set(leadsOf("beta:b1,b2", "alpha:a0,a1,a2"), nil)
	r.refresh(3 * time.Second)
	// The row of alpha selects the lead alpha remembers.
	for r.m.treeRows()[r.m.treeCursor].text != "alpha" {
		before := r.m.treeCursor
		r.typ("j")
		if r.m.treeCursor == before {
			t.Fatal("alpha's row was not reached")
		}
	}
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p, l, _ := r.selectedNames(); p != "alpha" || l != "a2" {
		t.Fatal(p, l)
	}
}

func TestSelectionSurvivesARefreshInTheSingleColumnPages(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:a1,a2", "beta:b1,b2")}
	r := newSourceRig(t, src, 70, 20, Options{Label: testLabel})
	r.refresh(0)
	// Open beta, and put the lead cursor on b2.
	r.typ("j")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	r.typ("j")
	if v := r.view(); !strings.Contains(v, "Project: beta") || !strings.Contains(v, "> b2") {
		t.Fatal(v)
	}
	src.set(leadsOf("new:n1", "beta:b0,b1,b2", "alpha:a1,a2"), nil)
	r.refresh(3 * time.Second)
	if v := r.view(); !strings.Contains(v, "Project: beta") || !strings.Contains(v, "> b2") {
		t.Fatal(v)
	}
	// Enter opens the lead under the cursor, which is still b2.
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if v := r.view(); !strings.Contains(v, "Project: beta / Lead: b2") {
		t.Fatal(v)
	}
	// Every page draws with no projects, and with a project without leads.
	for _, p := range []Projection{{}, leadsOf("beta:"), {Problem: "no service answers"}} {
		src.set(p, nil)
		r.refresh(3 * time.Second)
		for _, page := range []string{"projects", "leads", "tasks", "conversation", "help", "actions"} {
			r.m.page = page
			r.view()
			r.typ("jk")
			r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
			r.view()
		}
	}
}

// TestAKeyAndATickInEitherOrder: a key that arrives just before a refresh's
// answer and one that arrives just after it mean the same row, and neither is
// lost. The refresh moves every row (a project is added in front), so an
// index kept across it would point at another row.
func TestAKeyAndATickInEitherOrder(t *testing.T) {
	before := leadsOf("alpha:a1,a2", "beta:b1,b2")
	after := leadsOf("new:n1,n2", "alpha:a1,a2", "beta:b1,b2")
	for _, order := range []string{"key, tick, answer", "tick, key, answer", "tick, answer, key"} {
		t.Run(order, func(t *testing.T) {
			src := &fakeSource{p: before}
			r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
			r.refresh(0)
			// The cursor is on a1; the key moves it to a2.
			r.typ("j")
			if _, _, c := r.selectedNames(); c != "a1" {
				t.Fatal(c)
			}
			src.set(after, nil)
			switch order {
			case "key, tick, answer":
				r.typ("j")
				r.refresh(3 * time.Second)
			case "tick, key, answer":
				request := r.refreshTick(3 * time.Second)
				r.typ("j")
				// The key is handled at once: the request is still out.
				if _, _, c := r.selectedNames(); c != "a2" {
					t.Fatal(c)
				}
				r.answer(request)
			case "tick, answer, key":
				r.refresh(3 * time.Second)
				r.typ("j")
			}
			if _, _, c := r.selectedNames(); c != "a2" {
				t.Fatalf("the cursor is on %q", c)
			}
			if !strings.Contains(r.view(), "n1") {
				t.Fatal("the refresh was lost")
			}
			// Enter then selects the row the key chose.
			r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
			if p, l, _ := r.selectedNames(); p != "alpha" || l != "a2" {
				t.Fatal(p, l)
			}
		})
	}
}

// TestARefreshLeavesWhatAKeyDidAlone: the notice a key raised, an open
// overlay and its scroll, and the focused pane are not the source's, and a
// tick or an answer between two keys leaves them as they are.
func TestARefreshLeavesWhatAKeyDidAlone(t *testing.T) {
	src := &fakeSource{p: viewSample()}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	r.refresh(0)
	r.typ("a")
	request := r.refreshTick(3 * time.Second)
	if v := r.view(); !strings.Contains(v, "Unavailable: Full attach.") {
		t.Fatal(v)
	}
	r.answer(request)
	if v := r.view(); !strings.Contains(v, "Unavailable: Full attach.") {
		t.Fatal(v)
	}
	// The action list is open across a tick, an answer and a failure, and
	// Esc then closes it.
	r.key(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	r.typ("j")
	r.refresh(3 * time.Second)
	src.set(Projection{}, errors.New("failed"))
	r.refresh(3 * time.Second)
	if v := r.view(); r.m.overlay != "actions" || r.m.overlayScroll != 1 || !strings.Contains(v, "ACTIONS (all unavailable)") {
		t.Fatal(r.m.overlay, r.m.overlayScroll, v)
	}
	r.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if r.m.overlay != "" {
		t.Fatal("Esc did not close the action list")
	}
	// The focused pane and its scroll stay.
	src.set(viewSample(), nil)
	r.key(tea.KeyPressMsg{Code: tea.KeyTab})
	r.key(tea.KeyPressMsg{Code: tea.KeyTab})
	r.typ("jj")
	r.refresh(3 * time.Second)
	if r.m.pane != paneEvidence || r.m.scrolls[paneEvidence] != 2 {
		t.Fatal(r.m.pane, r.m.scrolls)
	}
	// q quits whatever is out.
	r.refreshTick(3 * time.Second)
	if cmd := r.key(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Fatal("q did not quit")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("wrong quit message")
	}
}

func TestARefreshKeepsTheBoundsAndCleansAgain(t *testing.T) {
	src := &fakeSource{p: leadsOf("alpha:one")}
	r := newSourceRig(t, src, 140, 45, Options{Label: testLabel})
	big := Projection{Timeline: make([]TimelineEntry, 500), Controls: make([]Control, 500), Contract: make([]Check, 500), Facts: make([]Check, 500)}
	for i := 0; i < 500; i++ {
		big.Projects = append(big.Projects, Project{Name: "p\x1b]52;c;bad\x07\u202e", Leads: make([]Lead, 500)})
	}
	src.set(big, nil)
	r.refresh(0)
	m := r.m
	if len(m.projects) != maxProjects || len(m.projects[0].Leads) != maxLeads || len(m.leadSelections) != maxProjects ||
		len(m.timeline) != maxList || len(m.controls) != maxControls || len(m.contract) != maxContract || len(m.facts) != maxContract {
		t.Fatal("a refresh went over a bound")
	}
	if v := r.view(); strings.ContainsAny(v, "\x1b\x07\u202e") {
		t.Fatalf("%q", v)
	}
	// A timeline that got shorter does not leave the scroll past its end.
	r.key(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	r.typ("kkkkkkkkkk")
	src.set(Projection{Timeline: make([]TimelineEntry, 3)}, nil)
	r.refresh(3 * time.Second)
	if m.scrolls[paneTimeline] > 2 {
		t.Fatal(m.scrolls[paneTimeline])
	}
	r.view()
}

// viewLog is a model that notes, on the program's own goroutine, each view it
// has drawn the tree of.
type viewLog struct {
	*Model
	mu   sync.Mutex
	seen []string
}

func (l *viewLog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := l.Model.Update(msg)
	if len(l.Model.projects) > 0 {
		name := l.Model.projects[0].Name
		l.mu.Lock()
		if len(l.seen) == 0 || l.seen[len(l.seen)-1] != name {
			l.seen = append(l.seen, name)
		}
		l.mu.Unlock()
	}
	return l, cmd
}

func (l *viewLog) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seen...)
}

// TestRefreshInAProgram runs the console in a real Bubble Tea program against
// a source that changes between ticks: the ticks are tea.Tick's, the requests
// run as the program's commands, and keys arrive while they do. It runs under
// the race detector.
func TestRefreshInAProgram(t *testing.T) {
	src := &fakeSource{p: leadsOf("gen-0:lead"), next: func(n int) Projection {
		return leadsOf("gen-"+string(rune('0'+min(n, 9)))+":lead", "steady:kept")
	}}
	l := &viewLog{Model: NewWithOptions(src, Options{Theme: "dark", Label: testLabel, Refresh: 5 * time.Millisecond})}
	defer l.Close()
	in, inw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer inw.Close()
	out := &syncBuffer{}
	prog := tea.NewProgram(l, tea.WithInput(in), tea.WithOutput(out), tea.WithWindowSize(140, 45), tea.WithoutSignalHandler(),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}))
	done := make(chan error, 1)
	go func() {
		_, err := prog.Run()
		done <- err
	}()
	waitFor(t, "the first refresh", func() bool { return len(l.names()) >= 2 })
	// Keys typed while the ticks run: down to the row of the steady
	// project's lead, and Enter.
	if _, err := inw.WriteString("jjj\r"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the later refreshes", func() bool {
		_, n := src.calls()
		return n >= 12 && len(l.names()) >= 10
	})
	if _, err := inw.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not quit on q")
	}
	// Every generation was shown once, in order.
	want := "gen-0 gen-1 gen-2 gen-3 gen-4 gen-5 gen-6 gen-7 gen-8 gen-9"
	if got := strings.Join(l.names(), " "); got != want {
		t.Fatalf("the views were %q", got)
	}
	// The keys chose the steady project's lead, and it stayed selected while
	// the project before it changed its name on every tick.
	if p, ld, _ := l.selected(); p != "steady" || ld != "kept" {
		t.Fatal(p, ld)
	}
}
