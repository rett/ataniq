package genesis

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/plan"
)

// The wire (package ownerwire) cannot import the plan package, so it holds the
// plan record as types of its own, with the record's grammar, caps and closed
// lists. These tests hold the two in step: the caps and the lists are equal,
// the wire accepts a plan exactly when the record does, and every plan the
// compiler gives, of every template and every combination of the answers the
// templates read, is written by the wire to the very bytes of the record's
// canonical form, under the same digest, in a result under its cap.

// toWire is the plan as the result of seat.genesis carries it.
func toWire(p plan.Plan) ownerwire.GenesisPlan {
	out := ownerwire.GenesisPlan{
		Schema: p.Schema, Revision: p.Revision, PrevDigest: p.PrevDigest, BriefDigest: p.BriefDigest, CatalogDigest: p.CatalogDigest,
		Template:       ownerwire.GenesisPlanTemplate{ID: p.Template.ID, Version: p.Template.Version},
		TemplateDigest: p.TemplateDigest, Title: p.Title, Milestones: []ownerwire.GenesisPlanMilestone{},
		Gates: append([]string{}, p.Gates...), FirstDemo: p.FirstDemo, ReviewPoints: append([]string{}, p.ReviewPoints...),
		Hosts: p.Hosts, ExternalServices: append([]string{}, p.ExternalServices...),
		BudgetBand: ownerwire.GenesisPlanBudget{ID: p.BudgetBand, Enforcement: plan.Enforcement},
		Estimate:   ownerwire.GenesisPlanEstimate{Band: p.Estimate.Band, Label: p.Estimate.Label, Estimate: true, Caveat: plan.EstimateCaveat},
		Warnings:   append([]string{}, p.Warnings...), Notes: []ownerwire.GenesisPlanNote{}, NotesDropped: p.NotesDropped,
	}
	for _, m := range p.Milestones {
		wm := ownerwire.GenesisPlanMilestone{ID: m.ID, Title: m.Title, Goal: m.Goal, Tasks: []ownerwire.GenesisPlanTask{}}
		for _, t := range m.Tasks {
			wm.Tasks = append(wm.Tasks, ownerwire.GenesisPlanTask{ID: t.ID, Title: t.Title, Kind: string(t.Kind), Size: string(t.Size),
				DependsOn: append([]string{}, t.DependsOn...), Scope: append([]string{}, t.Scope...), Checks: append([]string{}, t.Checks...)})
		}
		out.Milestones = append(out.Milestones, wm)
	}
	for _, n := range p.Notes {
		out.Notes = append(out.Notes, ownerwire.GenesisPlanNote{Field: n.Field, Text: string(n.Text), Untrusted: true})
	}
	return out
}

// fromWire is the plan record read back from the wire's types.
func fromWire(w ownerwire.GenesisPlan) plan.Plan {
	out := plan.Plan{
		Schema: w.Schema, Revision: w.Revision, PrevDigest: w.PrevDigest, BriefDigest: w.BriefDigest, CatalogDigest: w.CatalogDigest,
		Template:       plan.TemplateRef{ID: w.Template.ID, Version: w.Template.Version},
		TemplateDigest: w.TemplateDigest, Title: w.Title, Gates: w.Gates, FirstDemo: w.FirstDemo, ReviewPoints: w.ReviewPoints,
		Hosts: w.Hosts, ExternalServices: w.ExternalServices, BudgetBand: w.BudgetBand.ID,
		Estimate: plan.Estimate{Band: w.Estimate.Band, Label: w.Estimate.Label}, Warnings: w.Warnings, NotesDropped: w.NotesDropped,
	}
	for _, m := range w.Milestones {
		pm := plan.Milestone{ID: m.ID, Title: m.Title, Goal: m.Goal}
		for _, t := range m.Tasks {
			pm.Tasks = append(pm.Tasks, plan.Task{ID: t.ID, Title: t.Title, Kind: plan.Kind(t.Kind), Size: plan.Size(t.Size),
				DependsOn: t.DependsOn, Scope: t.Scope, Checks: t.Checks})
		}
		out.Milestones = append(out.Milestones, pm)
	}
	for _, n := range w.Notes {
		out.Notes = append(out.Notes, plan.Note{Field: n.Field, Text: brief.Untrusted(n.Text)})
	}
	return out
}

func sorted(list []string) []string {
	out := append([]string{}, list...)
	sort.Strings(out)
	return out
}

// The caps, the names and the closed lists of the wire's plan are the plan
// record's.
func TestTheWirePlanIsThePlanRecord(t *testing.T) {
	for _, c := range []struct {
		name       string
		wire, host int
	}{
		{"bytes", ownerwire.MaxGenesisPlanBytes, plan.MaxPlanBytes},
		{"revision", ownerwire.MaxGenesisPlanRevision, plan.MaxRevision},
		{"milestones", ownerwire.MaxGenesisPlanMilestones, plan.MaxMilestones},
		{"tasks of a milestone", ownerwire.MaxGenesisPlanMilestoneTasks, plan.MaxTasksPerMilestone},
		{"tasks", ownerwire.MaxGenesisPlanTasks, plan.MaxTasks},
		{"title", ownerwire.MaxGenesisPlanTitle, plan.MaxTitle},
		{"milestone title", ownerwire.MaxGenesisPlanMilestoneTitle, plan.MaxMilestoneTitle},
		{"goal", ownerwire.MaxGenesisPlanGoal, plan.MaxGoal},
		{"task title", ownerwire.MaxGenesisPlanTaskTitle, plan.MaxTaskTitle},
		{"first demo", ownerwire.MaxGenesisPlanDemo, plan.MaxDemo},
		{"notes", ownerwire.MaxGenesisPlanNotes, plan.MaxNotes},
		{"note text", ownerwire.MaxGenesisPlanNoteText, plan.MaxNoteText},
	} {
		if c.wire != c.host {
			t.Errorf("%s: the wire holds %d, the plan %d", c.name, c.wire, c.host)
		}
	}
	if ownerwire.MaxGenesisPlanBytes >= ownerwire.MaxSeatGenesisResultBytes {
		t.Errorf("the plan's cap %d is not under the result's %d", ownerwire.MaxGenesisPlanBytes, ownerwire.MaxSeatGenesisResultBytes)
	}
	for _, c := range []struct{ name, wire, host string }{
		{"schema", ownerwire.GenesisPlanSchema, plan.Schema},
		{"enforcement", ownerwire.GenesisPlanEnforcement, plan.Enforcement},
		{"caveat", ownerwire.GenesisPlanCaveat, plan.EstimateCaveat},
		{"the field of a note", ownerwire.GenesisPlanNotesField, plan.FieldNotes},
	} {
		if c.wire != c.host {
			t.Errorf("%s: the wire holds %q, the plan %q", c.name, c.wire, c.host)
		}
	}
	var kinds, sizes []string
	for _, k := range plan.Kinds {
		kinds = append(kinds, string(k))
	}
	for s := range plan.Sizes {
		sizes = append(sizes, string(s))
	}
	for _, c := range []struct {
		name       string
		wire, host []string
	}{
		{"kinds", ownerwire.GenesisPlanKinds, kinds},
		{"sizes", ownerwire.GenesisPlanSizes, sizes},
		{"review points", ownerwire.GenesisPlanReviewPoints, plan.ReviewPoints},
		{"hosts", ownerwire.GenesisPlanHosts, plan.Hosts},
		{"services", ownerwire.GenesisPlanServices, plan.Services},
		{"budget bands", ownerwire.GenesisPlanBudgetBands, plan.BudgetBands},
		{"warnings", ownerwire.GenesisPlanWarnings, plan.Warnings},
	} {
		if !reflect.DeepEqual(sorted(c.wire), sorted(c.host)) || !sort.StringsAreSorted(c.wire) {
			t.Errorf("%s: the wire holds %q, the plan %q", c.name, c.wire, c.host)
		}
	}
	bands := map[string]string{}
	for _, b := range plan.Bands {
		bands[b.ID] = b.Label
	}
	if !reflect.DeepEqual(ownerwire.GenesisPlanBands, bands) {
		t.Errorf("the estimate bands: the wire holds %v, the plan %v", ownerwire.GenesisPlanBands, bands)
	}
}

// The wire accepts a plan exactly when the record does, over a corpus of the
// strings that differ in each place a plan holds one of a grammar or a text.
// The one difference is the host's own: text of the owner's that looks like a
// secret is refused by the plan and carried by the wire, as an "other" answer
// is (the host refuses it on the way in).
func TestTheWirePlanGrammarsAreTheRecords(t *testing.T) {
	c, err := catalog.V1()
	if err != nil {
		t.Fatal(err)
	}
	base := planOf(t, c, map[string]catalog.Answer{"kind": {Choice: "software"}, "software.sub_kind": {Choice: "web_app"},
		"software.stack": {Other: "a stack of my own"}})
	if len(base.Notes) == 0 {
		t.Fatal("the base plan holds no note")
	}
	words := []string{"", "a", "a1", "a-b", "a_b", "A", "1a", "-a", "a.b", "a b", "aé", strings.Repeat("a", 32), strings.Repeat("a", 33), "repo", "v1"}
	versions := []string{"", "v1", "1", "1.2", "1-2", ".1", "-1", "V1", "v_1", strings.Repeat("1", 16), strings.Repeat("1", 17), "1.", "a b"}
	texts := []string{"", "a", " a", "a ", "a\tb", "a<b", "a>b", "see http://example.com", "WWW.example.com", "mailto:a@example.com", "a café", strings.Repeat("t", 60), strings.Repeat("t", 61),
		strings.Repeat("t", 100), strings.Repeat("t", 101), strings.Repeat("t", 120), strings.Repeat("t", 121), " a"}
	fields := []string{"", "notes", "goal", "kind", "software.stack", "software.", "music.key", "Goal", "software.1a", "software." + strings.Repeat("a", 32),
		"software." + strings.Repeat("a", 33), "note"}
	notes := []string{"", " ", "a", strings.Repeat("n", 200), strings.Repeat("n", 201), "a\tb", `"quoted" <b>`}
	type place struct {
		name   string
		corpus []string
		set    func(p *plan.Plan, s string)
	}
	places := []place{
		{"template id", words, func(p *plan.Plan, s string) { p.Template.ID = s }},
		{"template version", versions, func(p *plan.Plan, s string) { p.Template.Version = s }},
		{"check id", words, func(p *plan.Plan, s string) { p.Gates = []string{s} }},
		{"area name", words, func(p *plan.Plan, s string) { p.Milestones[0].Tasks[0].Scope = []string{"repo:" + s} }},
		{"title", texts, func(p *plan.Plan, s string) { p.Title = s }},
		{"first demo", texts, func(p *plan.Plan, s string) { p.FirstDemo = s }},
		{"milestone title", texts, func(p *plan.Plan, s string) { p.Milestones[0].Title = s }},
		{"goal", texts, func(p *plan.Plan, s string) { p.Milestones[0].Goal = s }},
		{"task title", texts, func(p *plan.Plan, s string) { p.Milestones[0].Tasks[0].Title = s }},
		{"note field", fields, func(p *plan.Plan, s string) { p.Notes[0].Field = s }},
		{"note text", notes, func(p *plan.Plan, s string) { p.Notes[0].Text = brief.Untrusted(s) }},
		{"hosts", append(append([]string{}, plan.Hosts...), "cloud", ""), func(p *plan.Plan, s string) { p.Hosts = s }},
		{"budget band", append(append([]string{}, plan.BudgetBands...), "high", "50"), func(p *plan.Plan, s string) { p.BudgetBand = s }},
		{"task id", []string{"t1", "t0", "t01", "t999", "t1000", "m1", "t", "T1"}, func(p *plan.Plan, s string) { p.Milestones[0].Tasks[0].ID = s }},
		{"milestone id", []string{"m1", "m0", "m01", "m999", "m1000", "t1", "m"}, func(p *plan.Plan, s string) { p.Milestones[0].ID = s }},
	}
	checked := 0
	for _, pl := range places {
		for _, s := range pl.corpus {
			p := base.Clone()
			pl.set(&p, s)
			host := plan.Check(p) == nil
			_, err := ownerwire.EncodeGenesisPlan(toWire(p))
			if wire := err == nil; wire != host {
				t.Errorf("%s %q: the wire accepts %v, the plan %v (%v)", pl.name, s, wire, host, err)
			}
			checked++
		}
	}
	// The structural cases: ids and sets, the counts of notes, the estimate,
	// the revision and the closed lists. Wire and record agree on each.
	for _, sc := range structuralCases(base) {
		p := base.Clone()
		sc.set(&p)
		host := plan.Check(p) == nil
		_, err := ownerwire.EncodeGenesisPlan(toWire(p))
		if wire := err == nil; wire != host {
			t.Errorf("%s: the wire accepts %v, the plan %v (%v)", sc.name, wire, host, err)
		}
		if host != sc.ok {
			t.Errorf("%s: the plan accepts %v, expected %v", sc.name, host, sc.ok)
		}
		checked++
	}
	// Text that looks like a secret: the plan refuses it, the wire carries it.
	p := base.Clone()
	p.Notes[0].Text = brief.Untrusted("my key " + "AKI" + "A" + "IOSFODNN7" + "EXAMPLE")
	if err := plan.Check(p); err == nil {
		t.Error("the plan holds a note that looks like a key")
	}
	if _, err := ownerwire.EncodeGenesisPlan(toWire(p)); err != nil {
		t.Errorf("the wire refuses text that looks like a key, which is the host's to refuse: %v", err)
	}
	t.Logf("%d plans held to both", checked)
}

// structuralCase is one change of a plan that is not a string of a grammar:
// ok says whether the record accepts it.
type structuralCase struct {
	name string
	ok   bool
	set  func(p *plan.Plan)
}

// structuralCases are the changes of the plan's structure: the order,
// uniqueness and references of its ids, its sets, its notes' count, its
// estimate, its revision and its closed lists. Each is made of what base
// holds, so that a change of a template does not break them.
func structuralCases(base plan.Plan) []structuralCase {
	var ids []string
	for _, m := range base.Milestones {
		for _, t := range m.Tasks {
			ids = append(ids, t.ID)
		}
	}
	last := len(base.Milestones) - 1
	lastTask := len(base.Milestones[last].Tasks) - 1
	// task is the base's last task, the one with the most before it.
	task := func(p *plan.Plan) *plan.Task { return &p.Milestones[last].Tasks[lastTask] }
	first := func(p *plan.Plan) *plan.Task { return &p.Milestones[0].Tasks[0] }
	notes := func(p *plan.Plan, n int) {
		p.Notes = nil
		for i := 0; i < n; i++ {
			p.Notes = append(p.Notes, plan.Note{Field: plan.FieldNotes, Text: brief.Untrusted("note " + strings.Repeat("n", i))})
		}
	}
	hex := strings.Repeat("ab", 32)
	other := func(list []string, not string) string {
		for _, s := range list {
			if s != not {
				return s
			}
		}
		return ""
	}
	cs := []structuralCase{
		{"base", true, func(p *plan.Plan) {}},
		// depends_on: in order, no duplicate, backward, known.
		{"depends_on in order", true, func(p *plan.Plan) { task(p).DependsOn = []string{ids[0], ids[1]} }},
		{"depends_on out of order", false, func(p *plan.Plan) { task(p).DependsOn = []string{ids[1], ids[0]} }},
		{"depends_on twice", false, func(p *plan.Plan) { task(p).DependsOn = []string{ids[0], ids[0]} }},
		{"depends_on on itself", false, func(p *plan.Plan) { task(p).DependsOn = []string{task(p).ID} }},
		{"depends_on a later task", false, func(p *plan.Plan) { first(p).DependsOn = []string{ids[len(ids)-1]} }},
		{"depends_on an unknown task", false, func(p *plan.Plan) { task(p).DependsOn = []string{"t998"} }},
		{"depends_on a milestone", false, func(p *plan.Plan) { task(p).DependsOn = []string{"m1"} }},
		{"depends_on the empty id", false, func(p *plan.Plan) { task(p).DependsOn = []string{""} }},
		{"task ids out of order", false, func(p *plan.Plan) {
			m := &p.Milestones[0]
			m.Tasks[0].ID, m.Tasks[1].ID = m.Tasks[1].ID, m.Tasks[0].ID
		}},
		{"task id twice", false, func(p *plan.Plan) { p.Milestones[0].Tasks[1].ID = p.Milestones[0].Tasks[0].ID }},
		{"milestone id twice", false, func(p *plan.Plan) {
			if len(p.Milestones) > 1 {
				p.Milestones[1].ID = p.Milestones[0].ID
			} else {
				p.Milestones = append(p.Milestones, p.Milestones[0])
			}
		}},
		// Sets: sorted, no duplicate.
		{"gates twice", false, func(p *plan.Plan) { p.Gates = []string{plan.Checks[0], plan.Checks[0]} }},
		{"gates out of order", false, func(p *plan.Plan) { p.Gates = []string{plan.Checks[1], plan.Checks[0]} }},
		{"gates in order", true, func(p *plan.Plan) { p.Gates = []string{plan.Checks[0], plan.Checks[1]} }},
		{"review points twice", false, func(p *plan.Plan) { p.ReviewPoints = []string{plan.ReviewPoints[0], plan.ReviewPoints[0]} }},
		{"review points out of order", false, func(p *plan.Plan) { p.ReviewPoints = []string{plan.ReviewPoints[1], plan.ReviewPoints[0]} }},
		{"review points, none", false, func(p *plan.Plan) { p.ReviewPoints = nil }},
		{"services twice", false, func(p *plan.Plan) { p.ExternalServices = []string{plan.Services[0], plan.Services[0]} }},
		{"services out of order", false, func(p *plan.Plan) { p.ExternalServices = []string{plan.Services[1], plan.Services[0]} }},
		{"services in order", true, func(p *plan.Plan) { p.ExternalServices = []string{plan.Services[0], plan.Services[1]} }},
		{"warnings twice", false, func(p *plan.Plan) { p.Warnings = []string{plan.Warnings[0], plan.Warnings[0]} }},
		{"warnings out of order", false, func(p *plan.Plan) { p.Warnings = []string{plan.Warnings[1], plan.Warnings[0]} }},
		{"warnings in order", true, func(p *plan.Plan) { p.Warnings = []string{plan.Warnings[0], plan.Warnings[1]} }},
		{"scope twice", false, func(p *plan.Plan) { first(p).Scope = []string{plan.Areas[0], plan.Areas[0]} }},
		{"scope out of order", false, func(p *plan.Plan) { first(p).Scope = []string{plan.Areas[1], plan.Areas[0]} }},
		{"scope in order", true, func(p *plan.Plan) { first(p).Scope = []string{plan.Areas[0], plan.Areas[1]} }},
		{"checks twice", false, func(p *plan.Plan) { first(p).Checks = []string{plan.Checks[0], plan.Checks[0]} }},
		{"checks out of order", false, func(p *plan.Plan) { first(p).Checks = []string{plan.Checks[1], plan.Checks[0]} }},
		{"checks in order", true, func(p *plan.Plan) { first(p).Checks = []string{plan.Checks[0], plan.Checks[1]} }},
		// The owner's notes and the count left out.
		{"no notes, none dropped", true, func(p *plan.Plan) { notes(p, 0); p.NotesDropped = 0 }},
		{"no notes, one dropped", false, func(p *plan.Plan) { notes(p, 0); p.NotesDropped = 1 }},
		{"7 notes, one dropped", false, func(p *plan.Plan) { notes(p, plan.MaxNotes-1); p.NotesDropped = 1 }},
		{"1 note, 7 dropped", false, func(p *plan.Plan) { notes(p, 1); p.NotesDropped = plan.MaxNotes - 1 }},
		{"8 notes, none dropped", true, func(p *plan.Plan) { notes(p, plan.MaxNotes); p.NotesDropped = 0 }},
		{"8 notes, one dropped", true, func(p *plan.Plan) { notes(p, plan.MaxNotes); p.NotesDropped = 1 }},
		{"8 notes, dropped at the cap", true, func(p *plan.Plan) { notes(p, plan.MaxNotes); p.NotesDropped = ownerwire.MaxGenesisPlanNotesDropped }},
		{"8 notes, dropped past the cap", false, func(p *plan.Plan) { notes(p, plan.MaxNotes); p.NotesDropped = ownerwire.MaxGenesisPlanNotesDropped + 1 }},
		{"8 notes, dropped below none", false, func(p *plan.Plan) { notes(p, plan.MaxNotes); p.NotesDropped = -1 }},
		{"9 notes", false, func(p *plan.Plan) { notes(p, plan.MaxNotes+1) }},
		// The estimate: a band of the list with its own label.
		{"estimate unknown band", false, func(p *plan.Plan) { p.Estimate = plan.Estimate{Band: "years", Label: p.Estimate.Label} }},
		{"estimate no band", false, func(p *plan.Plan) { p.Estimate = plan.Estimate{Label: p.Estimate.Label} }},
		{"estimate no label", false, func(p *plan.Plan) { p.Estimate.Label = "" }},
		{"estimate unknown label", false, func(p *plan.Plan) { p.Estimate.Label = "about a fortnight" }},
		// The revision and the one before it.
		{"revision 1 with a previous digest", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = 1, hex }},
		{"revision 2 with no previous digest", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = 2, "" }},
		{"revision 2 with a previous digest", true, func(p *plan.Plan) { p.Revision, p.PrevDigest = 2, hex }},
		{"revision 2 with a short digest", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = 2, hex[:63] }},
		{"revision 2 with a long digest", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = 2, hex+"a" }},
		{"revision 2 with an upper-case digest", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = 2, strings.ToUpper(hex) }},
		{"revision 2 with a digest not of hexadecimal", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = 2, strings.Repeat("xy", 32) }},
		{"revision 0", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = 0, "" }},
		{"revision at the cap", true, func(p *plan.Plan) { p.Revision, p.PrevDigest = plan.MaxRevision, hex }},
		{"revision past the cap", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = plan.MaxRevision+1, hex }},
		{"revision negative", false, func(p *plan.Plan) { p.Revision, p.PrevDigest = -1, hex }},
		{"brief digest short", false, func(p *plan.Plan) { p.BriefDigest = hex[:63] }},
		{"catalog digest upper-case", false, func(p *plan.Plan) { p.CatalogDigest = strings.ToUpper(hex) }},
		{"template digest empty", false, func(p *plan.Plan) { p.TemplateDigest = "" }},
		// The closed lists: a word that is none of them.
		{"task kind unknown", false, func(p *plan.Plan) { task(p).Kind = "deploy" }},
		{"task kind empty", false, func(p *plan.Plan) { task(p).Kind = "" }},
		{"task size unknown", false, func(p *plan.Plan) { task(p).Size = "huge" }},
		{"task size empty", false, func(p *plan.Plan) { task(p).Size = "" }},
		{"service unknown", false, func(p *plan.Plan) { p.ExternalServices = []string{"payment service"} }},
		{"warning unknown", false, func(p *plan.Plan) { p.Warnings = []string{"something-new"} }},
		{"review point unknown", false, func(p *plan.Plan) { p.ReviewPoints = []string{"always"} }},
		{"area of the grammar and not of the list", true, func(p *plan.Plan) { first(p).Scope = []string{"repo:weird"} }},
		{"check of another grammar", false, func(p *plan.Plan) { first(p).Checks = []string{"Unit Tests"} }},
		{"another budget band", true, func(p *plan.Plan) { p.BudgetBand = other(plan.BudgetBands, p.BudgetBand) }},
		{"schema of another version", false, func(p *plan.Plan) { p.Schema = "ataniq.project-plan/2" }},
		{"no milestone", false, func(p *plan.Plan) { p.Milestones = nil }},
		{"a milestone with no task", false, func(p *plan.Plan) { p.Milestones[0].Tasks = nil }},
	}
	return cs
}

// planOf walks an interview to its end, as the store does, answering from
// picks or by the question's default or first option, and compiles it.
func planOf(t testing.TB, c catalog.Catalog, picks map[string]catalog.Answer) plan.Plan {
	t.Helper()
	b := walkBrief(t, c, picks)
	p, err := plan.Compile(b, c)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// walkBrief runs a whole interview through the catalog's walker: each round
// the walker picks, each question answered from picks by node id, or skipped
// to its default, or given its first option.
func walkBrief(t testing.TB, c catalog.Catalog, picks map[string]catalog.Answer) brief.Brief {
	t.Helper()
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	b := brief.New(c.Digest())
	for i := 0; ; i++ {
		if i > catalog.MaxRounds {
			t.Fatal("the walk does not end")
		}
		r, done, err := catalog.Next(c, b, catalog.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return b
		}
		for _, n := range r.Nodes {
			a, ok := picks[n.ID]
			switch {
			case ok:
			case n.Default != "":
				a = catalog.Answer{Skip: true}
			case n.Type == catalog.Multi:
				a = catalog.Answer{Choices: []string{n.Options[0].ID}}
			default:
				a = catalog.Answer{Choice: n.Options[0].ID}
			}
			a.Round = r.Number
			if b, _, err = catalog.Apply(c, b, n.ID, a, at); err != nil {
				t.Fatalf("%s: %v", n.ID, err)
			}
		}
	}
}

// Every combination of the answers the templates read, for every kind (912
// briefs, each with the owner's longest text in every "other" answer and
// eight notes of the longest escaped text), compiles to a plan the wire
// writes to the record's own bytes, under the record's digest; read back from
// the wire it is the same record; and in a result whose other members are at
// their largest it is under the result's cap, 16 KiB.
func TestEveryPlanIsAResultTheWireAccepts(t *testing.T) {
	c, err := catalog.V1()
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat(`"`, brief.MaxFreeText)
	one := func(ids ...string) []catalog.Answer {
		var out []catalog.Answer
		for _, id := range ids {
			out = append(out, catalog.Answer{Choice: id})
		}
		return out
	}
	subsets := func(ids ...string) []catalog.Answer {
		var out []catalog.Answer
		for mask := 1; mask < 1<<len(ids); mask++ {
			var pick []string
			for i, id := range ids {
				if mask&(1<<i) != 0 {
					pick = append(pick, id)
				}
			}
			out = append(out, catalog.Answer{Choices: pick})
		}
		return out
	}
	other := catalog.Answer{Other: brief.Untrusted(long)}
	type dim struct {
		node    string
		answers []catalog.Answer
	}
	hosts := dim{"hosts", one("own_server", "this_computer", "file_host")}
	kinds := map[string][]dim{
		"software": {
			{"software.sub_kind", one("web_app", "web_tool")},
			{"software.stack", append(one("go", "python"), other)},
			{"software.data", one("none", "local", "accounts")},
			{"software.auth", append(one("none", "passkey"), other)},
			{"software.deploy_kind", one("own_server", "hosting", "local")},
			hosts,
		},
		"publishing": {
			{"publishing.sub_kind", one("newsletter", "article")},
			{"publishing.medium", one("text", "text_images", "text_media")},
			{"publishing.editorial_steps", one("self", "reviewer")},
			{"publishing.style_guide", one("yes", "no")},
			{"publishing.channel", one("email", "site", "both")},
			hosts,
		},
		"media": {
			{"media.sub_kind", one("podcast", "book")},
			{"media.format", nil},
			{"media.sources", subsets("own", "licensed", "generated")},
			{"media.rights", one("yes", "no")},
			hosts,
		},
	}
	formats := map[string][]catalog.Answer{"podcast": one("audio", "audio_video"), "book": one("ebook", "print", "audiobook")}
	formatNodes := map[string]string{"podcast": "media.format.podcast", "book": "media.format.book"}
	templates := map[string]bool{}
	count, largestPlan, largestResult := 0, 0, 0
	check := func(p plan.Plan) {
		count++
		templates[p.Template.ID] = true
		canonical, err := plan.Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := plan.Digest(p)
		if err != nil {
			t.Fatal(err)
		}
		w := toWire(p)
		out, err := ownerwire.EncodeGenesisPlan(w)
		if err != nil || !bytes.Equal(out, canonical) {
			t.Fatalf("%s: the wire writes the plan as\n%s\nthe plan package as\n%s\n(%v)", p.Template.ID, out, canonical, err)
		}
		if wd, err := ownerwire.GenesisPlanDigest(w); err != nil || wd != digest {
			t.Fatalf("%s: the wire's digest %s (%v), the plan's %s", p.Template.ID, wd, err, digest)
		}
		res := ownerwire.SeatGenesisResult{State: ownerwire.GenesisStatePlan, Thread: "0123456789abcdef",
			Round: ownerwire.MaxGenesisRounds, Of: ownerwire.MaxGenesisRounds, Nodes: []ownerwire.GenesisNode{},
			Progress:      ownerwire.GenesisProgress{Filled: ownerwire.MaxGenesisRows, Total: ownerwire.MaxGenesisRows, PlanReady: true},
			CatalogDigest: p.CatalogDigest, Plan: &w, PlanDigest: digest, Stored: true}
		enc, err := ownerwire.EncodeSeatGenesisResult(res)
		if err != nil {
			t.Fatalf("%s: the result is refused: %v", p.Template.ID, err)
		}
		if !bytes.Contains(enc, append(append([]byte(`"plan":`), canonical...), ',')) {
			t.Fatalf("%s: the result does not carry the plan's own bytes", p.Template.ID)
		}
		back, err := ownerwire.DecodeSeatGenesisResult(enc)
		if err != nil {
			t.Fatalf("%s: the result does not decode: %v", p.Template.ID, err)
		}
		again, err := plan.Encode(fromWire(*back.Plan))
		if err != nil || !bytes.Equal(again, canonical) {
			t.Fatalf("%s: the plan read back from the wire is not the record: %v", p.Template.ID, err)
		}
		if read, err := plan.Decode(out); err != nil || !plan.ContentEqual(read, p) || read.BriefDigest != p.BriefDigest {
			t.Fatalf("%s: the wire's bytes do not decode as the plan: %v", p.Template.ID, err)
		}
		largestPlan, largestResult = max(largestPlan, len(canonical)), max(largestResult, len(enc))
	}
	for _, kind := range []string{"software", "publishing", "media"} {
		dims := kinds[kind]
		var rec func(i int, picks map[string]catalog.Answer)
		rec = func(i int, picks map[string]catalog.Answer) {
			if i == len(dims) {
				b := walkBrief(t, c, picks)
				for j := 0; j < brief.MaxNotes; j++ {
					if err := b.AddNote(brief.Untrusted(long)); err != nil {
						t.Fatal(err)
					}
				}
				p, err := plan.Compile(b, c)
				if err != nil {
					t.Fatalf("%v: %v", picks, err)
				}
				check(p)
				return
			}
			node, answers := dims[i].node, dims[i].answers
			if node == "media.format" {
				sub := picks["media.sub_kind"].Choice
				node, answers = formatNodes[sub], formats[sub]
			}
			for _, a := range answers {
				next := map[string]catalog.Answer{}
				for k, v := range picks {
					next[k] = v
				}
				next[node] = a
				rec(i+1, next)
			}
		}
		rec(0, map[string]catalog.Answer{"kind": {Choice: kind}})
	}
	// 486 software, 216 publishing and 84 + 126 media briefs.
	if count != 912 {
		t.Fatalf("checked %d plans, want 912", count)
	}
	ts, err := plan.Templates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range ts {
		if !templates[tpl.ID] {
			t.Errorf("no plan of the template %s was checked", tpl.ID)
		}
	}
	t.Logf("the largest plan is %d bytes of %d, the largest result %d of %d", largestPlan, ownerwire.MaxGenesisPlanBytes, largestResult, ownerwire.MaxSeatGenesisResultBytes)
	if largestResult > ownerwire.MaxSeatGenesisResultBytes {
		t.Fatalf("the largest result is %d bytes", largestResult)
	}
}
