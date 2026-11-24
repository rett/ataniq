package seatlink

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/plan"
	"ataniq/internal/seatapp"
)

// webAppRounds are the answers of the web app path, round by round, in the
// app's words: a complete brief, which a plan template serves.
func webAppRounds() [][]seatapp.GenesisAnswer {
	choice := func(node string, ids ...string) seatapp.GenesisAnswer {
		kind := seatapp.GenesisKindChoice
		if len(ids) > 1 || node == "deliverables" || node == "review_points" {
			kind = seatapp.GenesisKindChoices
		}
		return seatapp.GenesisAnswer{Node: node, Pick: seatapp.GenesisPick{Kind: kind, IDs: ids}}
	}
	return [][]seatapp.GenesisAnswer{
		{choice("kind", "software")},
		{choice("software.sub_kind", "web_app"), choice("goal", "own_use"), choice("audience", "me")},
		{{Node: "software.stack", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindOther, Text: "Go and a small database"}},
			{Node: "software.data", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindYouDecide}}, choice("software.auth", "passkey")},
		{choice("software.deploy_kind", "local"), choice("hosts", "this_computer"), choice("budget_band", "none")},
		{choice("deliverables", "first_version"), choice("quality_bar", "solid"), {Node: "schedule", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindSkip}}},
		{choice("review_points", "plan", "demo")},
	}
}

// walkToSummary starts a thread and answers the web app path to its summary.
func walkToSummary(t *testing.T, d *Daemon, clock *testClock) seatapp.GenesisView {
	t.Helper()
	id := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart}).Thread
	var v seatapp.GenesisView
	for i, answers := range webAppRounds() {
		clock.advance(time.Second)
		v = call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: id, Round: i + 1, Answers: answers})
	}
	if v.State != seatapp.GenesisStateSummary || v.Filled != v.Total {
		t.Fatalf("the walk ended at %+v", v)
	}
	return v
}

// The app's words and closed lists of the plan are the wire's.
func TestSeatLinkGenesisPlanWordsAreTheWires(t *testing.T) {
	if seatapp.GenesisVerbPlan != ownerwire.GenesisVerbPlan || seatapp.GenesisStatePlan != ownerwire.GenesisStatePlan {
		t.Errorf("the plan's verb or state differ: %q %q", seatapp.GenesisVerbPlan, seatapp.GenesisStatePlan)
	}
	sorted := func(l []string) []string {
		out := append([]string{}, l...)
		sort.Strings(out)
		return out
	}
	for _, c := range []struct {
		name      string
		app, wire []string
	}{
		{"kinds", seatapp.GenesisPlanKinds, ownerwire.GenesisPlanKinds},
		{"sizes", seatapp.GenesisPlanSizes, ownerwire.GenesisPlanSizes},
		{"review points", seatapp.GenesisPlanReviewPoints, ownerwire.GenesisPlanReviewPoints},
		{"hosts", seatapp.GenesisPlanHosts, ownerwire.GenesisPlanHosts},
		{"services", seatapp.GenesisPlanServices, ownerwire.GenesisPlanServices},
		{"budget bands", seatapp.GenesisPlanBudgetBands, ownerwire.GenesisPlanBudgetBands},
		{"warnings", seatapp.GenesisPlanWarnings, ownerwire.GenesisPlanWarnings},
	} {
		if !reflect.DeepEqual(sorted(c.app), sorted(c.wire)) {
			t.Errorf("%s: the app holds %q, the wire %q", c.name, c.app, c.wire)
		}
	}
	if !reflect.DeepEqual(seatapp.GenesisPlanEstimates, ownerwire.GenesisPlanBands) {
		t.Errorf("the estimates: the app holds %q, the wire %q", seatapp.GenesisPlanEstimates, ownerwire.GenesisPlanBands)
	}
}

// goldenPlans are the plans the compiler gives for the golden briefs of
// package plan: each template, read and written through the plan package,
// then read by the wire's types, which take the record's own names.
func goldenPlans(t *testing.T) map[string]ownerwire.GenesisPlan {
	t.Helper()
	names, err := filepath.Glob(filepath.Join("..", "genesis", "plan", "testdata", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ownerwire.GenesisPlan{}
	templates := map[string]bool{}
	for _, name := range names {
		if strings.HasPrefix(filepath.Base(name), "diff-") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		p, err := plan.Decode(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		canonical, err := plan.Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		var w ownerwire.GenesisPlan
		if err := json.Unmarshal(canonical, &w); err != nil {
			t.Fatal(err)
		}
		templates[w.Template.ID] = true
		out[filepath.Base(name)] = w
	}
	if len(templates) != 3 {
		t.Fatalf("the golden plans are of %d templates: %v", len(templates), templates)
	}
	return out
}

// planResult is the result of a plan call for the plan p of the thread.
func planResult(t *testing.T, thread string, p ownerwire.GenesisPlan, stored bool) ownerwire.SeatGenesisResult {
	t.Helper()
	d, err := ownerwire.GenesisPlanDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	return ownerwire.SeatGenesisResult{State: ownerwire.GenesisStatePlan, Thread: thread, Round: 6, Of: 6, CatalogDigest: p.CatalogDigest,
		Progress: ownerwire.GenesisProgress{Filled: 14, Total: 14, PlanReady: true}, Nodes: []ownerwire.GenesisNode{},
		Plan: &p, PlanDigest: d, Stored: stored}
}

// The plan of each template comes through the link as the app's view, every
// member in place: the plan's own, as the plan package wrote it.
func TestSeatLinkGenesisPlanOfEachTemplate(t *testing.T) {
	f, d, g, clock := genesisGate(t, []string{"genesis"})
	const id = "0123456789abcdef"
	for name, p := range goldenPlans(t) {
		for _, stored := range []bool{false, true} {
			res := planResult(t, id, p, stored)
			b, err := ownerwire.EncodeSeatGenesisResult(res)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			f.set(b, "")
			clock.advance(time.Hour)
			n := g.calls.Load()
			v := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: id})
			if g.calls.Load() != n+1 {
				t.Fatalf("%s: %d calls of the gate for one plan", name, g.calls.Load()-n)
			}
			pv := v.Plan
			if v.State != seatapp.GenesisStatePlan || v.Thread != id || !v.PlanReady || v.Filled != 14 || pv == nil {
				t.Fatalf("%s: the view %+v", name, v)
			}
			if pv.Revision != p.Revision || pv.Digest != res.PlanDigest || pv.Stored != stored || pv.Title != p.Title || pv.FirstDemo != p.FirstDemo ||
				pv.Hosts != p.Hosts || pv.BudgetBand != p.BudgetBand.ID || pv.Estimate != p.Estimate.Band || pv.NotesDropped != p.NotesDropped ||
				!reflect.DeepEqual(pv.Gates, p.Gates) || !reflect.DeepEqual(pv.ReviewPoints, p.ReviewPoints) ||
				!reflect.DeepEqual(pv.ExternalServices, p.ExternalServices) || !reflect.DeepEqual(pv.Warnings, p.Warnings) ||
				len(pv.Milestones) != len(p.Milestones) || len(pv.Notes) != len(p.Notes) {
				t.Fatalf("%s: the plan view %+v of %+v", name, pv, p)
			}
			for i, m := range p.Milestones {
				am := pv.Milestones[i]
				if am.ID != m.ID || am.Title != m.Title || am.Goal != m.Goal || len(am.Tasks) != len(m.Tasks) {
					t.Fatalf("%s: milestone %d: %+v", name, i, am)
				}
				for j, task := range m.Tasks {
					at := am.Tasks[j]
					if at.ID != task.ID || at.Title != task.Title || at.Kind != task.Kind || at.Size != task.Size ||
						!reflect.DeepEqual(at.DependsOn, task.DependsOn) || !reflect.DeepEqual(at.Scope, task.Scope) {
						t.Fatalf("%s: task %s: %+v", name, task.ID, at)
					}
				}
			}
			for i, n := range p.Notes {
				if pv.Notes[i] != (seatapp.GenesisNote{Field: n.Field, Text: n.Text}) {
					t.Fatalf("%s: note %d: %+v", name, i, pv.Notes[i])
				}
			}
		}
	}
}

// A plan past the app's own bounds, or of a word the app does not know, is
// not taken: appPlan refuses each change of a valid plan below.
func TestSeatLinkGenesisPlanPastTheAppsBounds(t *testing.T) {
	var p ownerwire.GenesisPlan
	for _, gp := range goldenPlans(t) {
		if gp.Template.ID == "software" && len(gp.Notes) > 0 {
			p = gp
		}
	}
	if p.Title == "" {
		t.Fatal("no software plan with a note among the golden plans")
	}
	clone := func() ownerwire.GenesisPlan {
		var c ownerwire.GenesisPlan
		b, _ := json.Marshal(p)
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if _, ok := appPlan(planResult(t, "0123456789abcdef", clone(), false)); !ok {
		t.Fatal("a valid plan was refused")
	}
	many := func(n int, prefix string) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, fmt.Sprintf("%s%02d", prefix, i))
		}
		return out
	}
	for name, change := range map[string]func(*ownerwire.GenesisPlan){
		"no milestone":            func(c *ownerwire.GenesisPlan) { c.Milestones = nil },
		"a title past its cap":    func(c *ownerwire.GenesisPlan) { c.Title = strings.Repeat("x", seatapp.MaxGenesisPlanTitle+1) },
		"an empty demo":           func(c *ownerwire.GenesisPlan) { c.FirstDemo = "" },
		"too many gates":          func(c *ownerwire.GenesisPlan) { c.Gates = many(seatapp.MaxGenesisPlanList+1, "g") },
		"a gate of no grammar":    func(c *ownerwire.GenesisPlan) { c.Gates = []string{"Lint"} },
		"another host":            func(c *ownerwire.GenesisPlan) { c.Hosts = "the_cloud" },
		"another band":            func(c *ownerwire.GenesisPlan) { c.BudgetBand.ID = "high" },
		"another estimate":        func(c *ownerwire.GenesisPlan) { c.Estimate.Band = "year" },
		"an estimate's label":     func(c *ownerwire.GenesisPlan) { c.Estimate.Label = "no time at all" },
		"another warning":         func(c *ownerwire.GenesisPlan) { c.Warnings = []string{"trust-me"} },
		"a review point twice":    func(c *ownerwire.GenesisPlan) { c.ReviewPoints = []string{"demo", "demo"} },
		"another service":         func(c *ownerwire.GenesisPlan) { c.ExternalServices = []string{"a crypto exchange"} },
		"another kind":            func(c *ownerwire.GenesisPlan) { c.Milestones[0].Tasks[0].Kind = "deploy" },
		"another size":            func(c *ownerwire.GenesisPlan) { c.Milestones[0].Tasks[0].Size = "huge" },
		"a task title too long":   func(c *ownerwire.GenesisPlan) { c.Milestones[0].Tasks[0].Title = strings.Repeat("x", 101) },
		"a dependency not before": func(c *ownerwire.GenesisPlan) { c.Milestones[0].Tasks[0].DependsOn = []string{"t99"} },
		"an area of no grammar":   func(c *ownerwire.GenesisPlan) { c.Milestones[0].Tasks[0].Scope = []string{"disk:/etc"} },
		"a scope too wide": func(c *ownerwire.GenesisPlan) {
			c.Milestones[0].Tasks[0].Scope = many(seatapp.MaxGenesisPlanList+1, "repo:a")
		},
		"no scope":            func(c *ownerwire.GenesisPlan) { c.Milestones[0].Tasks[0].Scope = nil },
		"a note not marked":   func(c *ownerwire.GenesisPlan) { c.Notes[0].Untrusted = false },
		"a note past its cap": func(c *ownerwire.GenesisPlan) { c.Notes[0].Text = strings.Repeat("x", seatapp.MaxGenesisOther+1) },
		"too many notes": func(c *ownerwire.GenesisPlan) {
			c.Notes = make([]ownerwire.GenesisPlanNote, seatapp.MaxGenesisPlanNotes+1)
		},
		"notes dropped below 0": func(c *ownerwire.GenesisPlan) { c.NotesDropped = -1 },
		"a revision of 0":       func(c *ownerwire.GenesisPlan) { c.Revision = 0 },
	} {
		c := clone()
		change(&c)
		res := planResult(t, "0123456789abcdef", p, false)
		res.Plan = &c
		if _, ok := appPlan(res); ok {
			t.Errorf("%s: taken", name)
		}
	}
	res := planResult(t, "0123456789abcdef", clone(), false)
	res.PlanDigest = strings.ToUpper(res.PlanDigest)
	if _, ok := appPlan(res); ok {
		t.Error("a digest in capitals: taken")
	}
	res.Plan = nil
	if _, ok := appPlan(res); ok {
		t.Error("no plan: taken")
	}
}

// The plan's two codes map to the app's errors; a plan answered for another
// verb, a plan of another thread, or another state for a plan, is an error
// that holds nothing of it.
func TestSeatLinkGenesisPlanCodesAndAnswers(t *testing.T) {
	f, d, _, clock := genesisGate(t, []string{"genesis"})
	const id = "0123456789abcdef"
	for code, want := range map[string]error{
		"genesis-not-ready": seatapp.ErrGenesisNotReady, "genesis-no-template": seatapp.ErrGenesisNoTemplate,
		"genesis-unknown-thread": seatapp.ErrGenesisUnknownThread, "rate-limited": seatapp.ErrBusy, "unknown-op": seatapp.ErrNotOffered,
	} {
		f.set(nil, code)
		if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: id}); !errors.Is(err, want) {
			t.Errorf("%s: %v", code, err)
		}
	}
	var p ownerwire.GenesisPlan
	for _, gp := range goldenPlans(t) {
		p = gp
		break
	}
	p.Title = "MARKER plan"
	enc := func(r ownerwire.SeatGenesisResult) []byte {
		b, err := ownerwire.EncodeSeatGenesisResult(r)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	sum := ownerwire.SeatGenesisResult{State: ownerwire.GenesisStateSummary, Thread: id, Round: 6, Of: 6, CatalogDigest: p.CatalogDigest,
		Progress: ownerwire.GenesisProgress{Filled: 1, Total: 1}, Nodes: []ownerwire.GenesisNode{},
		Groups: &ownerwire.GenesisGroups{YouChose: []ownerwire.GenesisRow{}, DecidedForYou: []ownerwire.GenesisRow{}, Defaults: []ownerwire.GenesisRow{}}}
	for name, c := range map[string]struct {
		call   seatapp.GenesisCall
		answer []byte
	}{
		"a plan for a round":         {seatapp.GenesisCall{Verb: seatapp.GenesisVerbRound, Thread: id}, enc(planResult(t, id, p, false))},
		"a plan for a summary":       {seatapp.GenesisCall{Verb: seatapp.GenesisVerbSummary, Thread: id}, enc(planResult(t, id, p, true))},
		"a plan of another thread":   {seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: id}, enc(planResult(t, "fedcba9876543210", p, false))},
		"a summary for a plan":       {seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: id}, enc(sum)},
		"a plan whose digest is off": {seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: id}, []byte(strings.Replace(string(enc(planResult(t, id, p, false))), "MARKER plan", "MARKER plaN", 1))},
	} {
		f.set(c.answer, "")
		clock.advance(time.Hour)
		_, err := d.Genesis(bg, c.call)
		if err == nil || !errors.Is(err, errGenesisAnswer) || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "MARKER") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A thread id not of its form is not sent.
	f.set(nil, "")
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: "../x"}); !errors.Is(err, seatapp.ErrGenesisUnknownThread) {
		t.Errorf("a forged thread: %v", err)
	}
}

// The plan through the link, the gate and a real store: the summary of the
// web app path says a plan is ready; the plan is a preview, kept as a round
// is (viewFresh) and forgotten by a change of its thread; finish stores it;
// the stored plan, with the preview's digest, is kept storedPlanFresh and is
// not forgotten by a call that changes the thread. Each plan read the gate is
// asked is one call.
func TestSeatLinkGenesisPlanOverARealStore(t *testing.T) {
	_, d, g, clock := genesisGate(t, []string{"genesis"})
	calls := func() int64 { return g.calls.Load() }
	sum := walkToSummary(t, d, clock)
	id := sum.Thread
	if !sum.PlanReady {
		t.Fatalf("the summary is not ready for a plan: %+v", sum)
	}
	planCall := seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: id}
	n := calls()
	pre := call(t, d, planCall)
	if calls() != n+1 || pre.Plan == nil || pre.Plan.Stored || pre.Plan.Revision != 1 || pre.Plan.Title == "" || !pre.PlanReady {
		t.Fatalf("the preview: %+v (%d calls)", pre, calls()-n)
	}
	// The owner's words are a note of the plan, never a title or a task.
	if len(pre.Plan.Notes) != 1 || pre.Plan.Notes[0].Text != "Go and a small database" {
		t.Fatalf("the preview's notes: %+v", pre.Plan.Notes)
	}
	// Read again within viewFresh: from the memory.
	clock.advance(viewFresh - time.Second)
	if again := call(t, d, planCall); !reflect.DeepEqual(again, pre) || calls() != n+1 {
		t.Fatalf("the preview read again: %d calls", calls()-n)
	}
	// At viewFresh: asked again.
	clock.advance(time.Second)
	call(t, d, planCall)
	if calls() != n+2 {
		t.Fatalf("a stale preview was not asked again: %d calls", calls()-n)
	}
	// A change of the thread forgets the preview: the next read asks.
	ch := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbChange, Thread: id, Node: "goal",
		Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindChoice, IDs: []string{"share"}}})
	n = calls()
	changed := call(t, d, planCall)
	if calls() != n+1 || ch.State != seatapp.GenesisStateSummary {
		t.Fatalf("the preview after a change: %d calls, change %+v", calls()-n, ch)
	}
	// An answer on another thread leaves this thread's preview kept: the
	// memory of a change is its thread's.
	other := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	n = calls()
	call(t, d, planCall)
	if calls() != n {
		t.Fatalf("the preview after a start of another thread: %d calls", calls()-n)
	}
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbDiscard, Thread: other.Thread})
	// Finish: nothing of the plan is kept; the next read is the stored
	// plan, with the digest of the last preview.
	fin := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbFinish, Thread: id})
	if fin.State != seatapp.GenesisStateFinished || !fin.PlanReady {
		t.Fatalf("the finish: %+v", fin)
	}
	n = calls()
	stored := call(t, d, planCall)
	if calls() != n+1 || stored.Plan == nil || !stored.Plan.Stored || stored.Plan.Revision != 1 || stored.Plan.Digest != changed.Plan.Digest {
		t.Fatalf("the stored plan: %+v (%d calls), want the digest %s", stored.Plan, calls()-n, changed.Plan.Digest)
	}
	// It is kept storedPlanFresh, through a finish again (a call that
	// changes threads, and changes nothing of a finished one).
	clock.advance(storedPlanFresh - time.Second)
	call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbFinish, Thread: id})
	n = calls()
	if again := call(t, d, planCall); !reflect.DeepEqual(again, stored) || calls() != n {
		t.Fatalf("the stored plan read again: %d calls", calls()-n)
	}
	clock.advance(time.Second)
	call(t, d, planCall)
	if calls() != n+1 {
		t.Fatalf("a stored plan at storedPlanFresh was not asked again: %d calls", calls()-n)
	}
}

// A brief that is not complete is not ready for a plan, and a kind in the
// owner's own words has no template: both from a real store, and neither is
// kept.
func TestSeatLinkGenesisPlanNotReadyAndNoTemplate(t *testing.T) {
	_, d, g, _ := genesisGate(t, []string{"genesis"})
	st := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbStart})
	if st.PlanReady {
		t.Fatal("a new thread is ready for a plan")
	}
	for i := 0; i < 2; i++ {
		n := g.calls.Load()
		if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: st.Thread}); !errors.Is(err, seatapp.ErrGenesisNotReady) || g.calls.Load() != n+1 {
			t.Fatalf("the plan of a new thread: %v (%d calls)", err, g.calls.Load()-n)
		}
	}
	other := call(t, d, seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: st.Thread, Round: 1,
		Answers: []seatapp.GenesisAnswer{{Node: "kind", Pick: seatapp.GenesisPick{Kind: seatapp.GenesisKindOther, Text: "A garden plan"}}}})
	if other.PlanReady {
		t.Fatal("a kind in the owner's words is ready for a plan")
	}
	if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: st.Thread}); !errors.Is(err, seatapp.ErrGenesisNoTemplate) {
		t.Fatalf("the plan of a kind in the owner's words: %v", err)
	}
}

// The memory of plans on its own: a preview is forgotten by every call that
// changes its thread, whatever its answer, a stored plan by none of them; a
// plan read that crossed a change is not kept; at most maxViews plans are
// kept; and a thread the gate no longer knows is forgotten, stored plan and
// all.
func TestSeatLinkGenesisMemoryOfPlans(t *testing.T) {
	const th = "0123456789abcdef"
	now := decideNow
	read := seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: th}
	planOf := func(stored bool) *ownerwire.SeatGenesisResult {
		return &ownerwire.SeatGenesisResult{State: ownerwire.GenesisStatePlan, Thread: th, Stored: stored, PlanDigest: strings.Repeat("ab", 32)}
	}
	for _, verb := range []string{seatapp.GenesisVerbAnswer, seatapp.GenesisVerbChange, seatapp.GenesisVerbFinish, seatapp.GenesisVerbDiscard} {
		for _, stored := range []bool{false, true} {
			var m genesisMemory
			g, _, _ := m.before(read, now)
			m.after(read, planOf(stored), g, now)
			change := seatapp.GenesisCall{Verb: verb, Thread: th}
			cg, _, _ := m.before(change, now)
			m.after(change, nil, cg, now)
			_, _, kept := m.before(read, now)
			if kept != stored {
				t.Errorf("%s, stored %v: kept %v", verb, stored, kept)
			}
		}
	}
	// A read that crossed a change is not kept, stored or not.
	for _, stored := range []bool{false, true} {
		var m genesisMemory
		g, _, _ := m.before(read, now)
		change := seatapp.GenesisCall{Verb: seatapp.GenesisVerbAnswer, Thread: "fedcba9876543210"}
		cg, _, _ := m.before(change, now)
		m.after(read, planOf(stored), g, now)
		m.after(change, nil, cg, now)
		if _, _, ok := m.before(read, now); ok {
			t.Errorf("stored %v: a plan read that crossed a change was kept", stored)
		}
	}
	// A preview lives viewFresh, a stored plan storedPlanFresh.
	for stored, fresh := range map[bool]time.Duration{false: viewFresh, true: storedPlanFresh} {
		var m genesisMemory
		g, _, _ := m.before(read, now)
		m.after(read, planOf(stored), g, now)
		if _, _, ok := m.before(read, now.Add(fresh-time.Second)); !ok {
			t.Errorf("stored %v: not kept just inside %v", stored, fresh)
		}
		if _, _, ok := m.before(read, now.Add(fresh)); ok {
			t.Errorf("stored %v: kept at %v", stored, fresh)
		}
	}
	// A thread the gate does not know is forgotten, its stored plan too.
	var m genesisMemory
	g, _, _ := m.before(read, now)
	m.after(read, planOf(true), g, now)
	m.forget(th)
	if _, _, ok := m.before(read, now); ok {
		t.Error("a forgotten thread's stored plan is kept")
	}
	// At most maxViews plans.
	for i := 0; i < 3*maxViews; i++ {
		c := seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: fmt.Sprintf("%016x", i)}
		g, _, _ := m.before(c, now)
		r := planOf(true)
		r.Thread = c.Thread
		m.after(c, r, g, now)
		if len(m.plans) > maxViews {
			t.Fatalf("%d plans kept", len(m.plans))
		}
	}
}

// The link's log holds no thread id: plan calls of a thread past the room
// of the interview are told in fixed words.
func TestSeatLinkGenesisPlanLogHoldsNoThread(t *testing.T) {
	_, d, _, clock := genesisGate(t, []string{"genesis"})
	var mu sync.Mutex
	var lines []string
	if err := WithLogf(func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	})(d); err != nil {
		t.Fatal(err)
	}
	bclock := &testClock{t: decideNow}
	d.budget.now = bclock.now
	const id = "0123456789abcdef"
	busy := 0
	for i := 0; i < 2*genesisRoom; i++ {
		clock.advance(time.Hour)
		if _, err := d.Genesis(bg, seatapp.GenesisCall{Verb: seatapp.GenesisVerbPlan, Thread: id}); errors.Is(err, seatapp.ErrBusy) {
			busy++
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if busy == 0 || len(lines) == 0 {
		t.Fatalf("%d busy, %d lines", busy, len(lines))
	}
	for _, l := range lines {
		if strings.Contains(l, id) || strings.Contains(l, id[:6]) {
			t.Errorf("a line holds the thread: %q", l)
		}
	}
}
