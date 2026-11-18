package terminalconsole

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// newSourceRig is newRig with a source and options of the test's own.
func newSourceRig(t *testing.T, src Source, w, h int, o Options) *rig {
	t.Helper()
	if o.Theme == "" {
		o.Theme = "dark"
	}
	r := &rig{t: t, sink: &recSink{}, t0: time.Unix(1_000_000, 0)}
	r.clock = r.t0
	r.m = NewWithOptions(src, o)
	t.Cleanup(r.m.Close)
	r.m.after = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		r.sched = append(r.sched, scheduled{d: d, fn: fn, at: r.clock.Add(d)})
		return nil
	}
	if w > 0 {
		r.size(w, h)
	}
	return r
}

const testLabel = "a sample view, read-only"

// viewSample is a small projection of a labelled view.
func viewSample() Projection {
	return Projection{
		ControlUnavailable: "The sample view is read-only.",
		Facts:              []Check{{Name: "Service version", State: "v1"}, {Name: "Service state", State: "unavailable"}},
		Controls: []Control{
			{Name: "Approve pending gate", Key: "Ctrl+K", Reason: "The sample view approves nothing."},
			{Name: "Full attach", Key: "a", Reason: "The sample view attaches to no session."},
		},
		Projects: []Project{
			{Name: "alpha", Leads: []Lead{{Name: "alpha/lead", State: "ready", Subagents: []string{"coordinator one"}}}},
			{Name: "beta", Leads: []Lead{{Name: "beta/lead", State: "starting"}}},
		},
	}
}

func TestLabelledViewIsNotCalledAFixture(t *testing.T) {
	r := newSourceRig(t, sample{viewSample()}, 140, 45, Options{Label: testLabel})
	v := r.view()
	for _, want := range []string{
		"ATANIQ · cockpit · " + testLabel, "NO SESSION TERMINAL", noSessionText,
		"alpha/lead · ready", "beta/lead · starting", "coordinator one",
		"SOURCE", "Service version: v1", "Service state: unavailable", "ATTACHMENT CONTRACT",
		"CONSOLE FOCUS · read-only view", "Enter select",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("the labelled view does not say %q:\n%s", want, v)
		}
	}
	for _, bad := range []string{"fixture", "FIXTURE", "recording", "replay", "Title:"} {
		if strings.Contains(v, bad) {
			t.Fatalf("the labelled view says %q:\n%s", bad, v)
		}
	}
	if r.m.rep.pane != nil {
		t.Fatal("a labelled view replays a recording")
	}
	// The single-column pages say the same, on every page.
	r.size(60, 20)
	for _, page := range []string{"projects", "leads", "tasks", "conversation", "help", "actions"} {
		r.m.page = page
		v := r.view()
		if !strings.Contains(v, "ATANIQ / "+testLabel) || strings.Contains(v, "fixture") || strings.Contains(v, "FIXTURE") {
			t.Fatalf("page %s:\n%s", page, v)
		}
	}
	r.m.page = "leads"
	if v := r.view(); !strings.Contains(v, "alpha/lead · ready") {
		t.Fatal(v)
	}
}

func TestLabelledViewKeepsEveryControlUnavailableWithItsReason(t *testing.T) {
	r := newSourceRig(t, sample{viewSample()}, 140, 45, Options{Label: testLabel})
	r.typ("a")
	if v := r.view(); !strings.Contains(v, "Unavailable: Full attach. The sample view attaches to no session.") {
		t.Fatal(v)
	}
	r.key(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	v := wordsOf(r.view())
	for _, want := range []string{"ACTIONS (all unavailable)", "Approve pending gate [Ctrl+K]", "Unavailable: The sample view approves nothing.", "Full attach [a]"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the action list does not say %q:\n%s", want, v)
		}
	}
	r.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	r.typ("?")
	if v := wordsOf(r.view()); !strings.Contains(v, "This is "+testLabel) || strings.Contains(v, "fixture") || strings.Contains(v, "replay") {
		t.Fatal(v)
	}
}

func TestLabelledViewHasNoSessionToFocusOrReplay(t *testing.T) {
	r := newSourceRig(t, sample{viewSample()}, 140, 45, Options{Label: testLabel})
	// Enter on the session pane, in the cockpit and in zoom, takes no focus.
	r.key(tea.KeyPressMsg{Code: tea.KeyTab})
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r.m.focus != consoleFocus || !strings.Contains(r.view(), noSessionText) {
		t.Fatal(r.m.focus.name(), r.view())
	}
	r.key(tea.KeyPressMsg{Code: zoomKey})
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if v := r.view(); r.m.focus != consoleFocus || !strings.Contains(v, "NO SESSION TERMINAL") || !strings.Contains(v, testLabel) {
		t.Fatal(r.m.focus.name(), v)
	}
	// The zoom's header offers no session focus either.
	r.key(tea.KeyPressMsg{Code: tea.KeyEscape})
	if first := r.rows()[0]; !strings.Contains(first, "CONSOLE FOCUS · F12 leaves zoom · "+testLabel) || strings.Contains(first, "Enter") {
		t.Fatal(first)
	}
	r.key(tea.KeyPressMsg{Code: zoomKey})
	if cmd := r.key(tea.KeyPressMsg{Code: 'r', Text: "r"}); cmd != nil {
		t.Fatal("r scheduled something")
	}
	if v := r.view(); r.m.rep.pane != nil || !strings.Contains(v, "There is no recording to replay in this view.") {
		t.Fatal(v)
	}
	// Pasted text and the mouse have nowhere to go.
	r.m.Update(tea.PasteMsg{Content: "text"})
	r.m.Update(tea.MouseClickMsg{X: 60, Y: 20, Button: tea.MouseLeft})
	if len(r.sink.keys)+len(r.sink.pastes)+len(r.sink.mouse) != 0 {
		t.Fatal("input left the console")
	}
}

func TestProblemStandsWhereTheProjectsWouldBe(t *testing.T) {
	p := viewSample()
	p.Projects = nil
	p.Problem, p.NextStep = "no service answers at the socket", "start the service with: service run"
	r := newSourceRig(t, sample{p}, 140, 45, Options{Label: testLabel})
	v := wordsOf(r.view())
	// The tree is narrow, so the texts are wrapped: look for their words.
	for _, want := range []string{"no service", "answers", "Next step:", "service run"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the cockpit does not say %q:\n%s", want, r.view())
		}
	}
	if strings.Contains(v, "No projects") {
		t.Fatal(r.view())
	}
	// Every key still works with nothing to select.
	r.typ("jjkk")
	r.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	r.typ("a")
	if v := r.view(); !strings.Contains(v, "Unavailable: Full attach.") {
		t.Fatal(v)
	}
	r.size(70, 20)
	v = r.view()
	for _, want := range []string{"no service answers at the socket", "Next step: start the service with: service run"} {
		if !strings.Contains(v, want) {
			t.Fatalf("the pages do not say %q:\n%s", want, v)
		}
	}
	// The action list keeps its reasons while the source has a problem.
	r.key(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if v := r.view(); !strings.Contains(v, "Full attach: The sample view attaches to no session.") {
		t.Fatal(v)
	}
}

func TestLabelledViewCleansEveryStringOfItsSource(t *testing.T) {
	bad := "x\x1b]52;c;bad\x07\u009b\n\u202ey"
	long := strings.Repeat("z", 1<<20)
	for _, p := range []Projection{
		{Problem: bad + long, NextStep: bad + long},
		{
			Facts:    []Check{{Name: bad, State: bad + long}},
			Projects: []Project{{Name: bad, Leads: []Lead{{Name: bad, State: bad + long, Subagents: []string{bad}}}}},
		},
	} {
		r := newSourceRig(t, sample{p}, 140, 45, Options{Label: "view" + bad})
		for _, size := range [][2]int{{140, 45}, {70, 20}} {
			r.size(size[0], size[1])
			s := r.view()
			// No sequence, no direction override, and the line break in the
			// middle of the text is gone.
			if strings.ContainsAny(s, "\x1b\x07\u009b\u202e") || strings.Contains(s, "bad\n") {
				t.Fatalf("%dx%d: %q", size[0], size[1], s)
			}
			if !strings.Contains(s, "viewx]52;c;bady") {
				t.Fatalf("%dx%d: the label is not shown:\n%s", size[0], size[1], s)
			}
		}
		if len(r.m.problem) > 512 || len(r.m.nextStep) > 512 || len(r.m.label) > 512 {
			t.Fatal("unbounded")
		}
	}
	many := make([]Check, 100)
	if m := NewWithOptions(sample{Projection{Facts: many}}, Options{Label: testLabel}); len(m.facts) != maxContract {
		t.Fatal(len(m.facts))
	}
}

func TestTheFixtureShowsNoneOfALabelledView(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	v := r.view()
	if !strings.Contains(v, "ATANIQ / DEMO FIXTURE · cockpit · "+fixtureLabel) || strings.Contains(v, "SOURCE") || strings.Contains(v, "Next step") {
		t.Fatal(v)
	}
	if r.m.rep.pane == nil || r.m.labelled() {
		t.Fatal("the fixture replays no recording")
	}
	// A label does not change a live session's cockpit.
	if m := NewWithOptions(nil, Options{Label: testLabel, Live: &fakeLive{}}); m.labelled() {
		t.Fatal("a live session is a labelled view")
	}
}
