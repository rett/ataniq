package ownerwire

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// The samples of the plan of seat.genesis. The template digest and the plan's
// own digest are fixed words of the vectors below; the plan's is the SHA-256
// of planJSON.
const (
	genTemplate = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	genPlan     = "b625956f82295f52e190199f9dc774e66a398d9648741c17d778514d66ad4d23"
)

// planJSON is the canonical form of samplePlan: the bytes the plan package
// writes for the same record.
const planJSON = `{"brief_digest":"` + genBrief + `","budget_band":{"enforcement":"advisory","id":"none"},"catalog_digest":"` + genCatalog + `",` +
	`"estimate":{"band":"hours","caveat":"an estimate, not a promise","estimate":true,"label":"a few hours of agent work"},` +
	`"external_services":[],"first_demo":"The page opens and keeps one entry","gates":["lint","unit-tests"],"hosts":"this_computer",` +
	`"milestones":[{"goal":"It runs on this computer","id":"m1","tasks":[` +
	`{"checks":["lint"],"depends_on":[],"id":"t1","kind":"build","scope":["repo:build"],"size":"small","title":"Set up the repository"},` +
	`{"checks":["lint","unit-tests"],"depends_on":["t1"],"id":"t3","kind":"build","scope":["repo:src","repo:tests"],"size":"medium","title":"Write the first page"}],` +
	`"title":"A first version"},` +
	`{"goal":"The owner has seen the demo","id":"m3","tasks":[` +
	`{"checks":[],"depends_on":["t3"],"id":"t7","kind":"review","scope":["repo:src"],"size":"small","title":"Review the first version"}],` +
	`"title":"Review"}],` +
	`"notes":[{"field":"software.stack","text":"Go and a small database","untrusted":true}],"notes_dropped":0,` +
	`"plan":1,"prev_digest":"","review_points":["demo","plan"],"schema":"ataniq.project-plan/1",` +
	`"template":{"id":"software","version":"v1"},"template_digest":"` + genTemplate + `","title":"A web app","warnings":["settle-at-desk"]}`

func samplePlan() GenesisPlan {
	return GenesisPlan{
		Schema: GenesisPlanSchema, Revision: 1, PrevDigest: "", BriefDigest: genBrief, CatalogDigest: genCatalog,
		Template: GenesisPlanTemplate{ID: "software", Version: "v1"}, TemplateDigest: genTemplate, Title: "A web app",
		Milestones: []GenesisPlanMilestone{
			{ID: "m1", Title: "A first version", Goal: "It runs on this computer", Tasks: []GenesisPlanTask{
				{ID: "t1", Title: "Set up the repository", Kind: "build", Size: "small", DependsOn: []string{}, Scope: []string{"repo:build"}, Checks: []string{"lint"}},
				{ID: "t3", Title: "Write the first page", Kind: "build", Size: "medium", DependsOn: []string{"t1"}, Scope: []string{"repo:src", "repo:tests"}, Checks: []string{"lint", "unit-tests"}},
			}},
			{ID: "m3", Title: "Review", Goal: "The owner has seen the demo", Tasks: []GenesisPlanTask{
				{ID: "t7", Title: "Review the first version", Kind: "review", Size: "small", DependsOn: []string{"t3"}, Scope: []string{"repo:src"}, Checks: []string{}},
			}},
		},
		Gates: []string{"lint", "unit-tests"}, FirstDemo: "The page opens and keeps one entry", ReviewPoints: []string{"demo", "plan"},
		Hosts: "this_computer", ExternalServices: []string{},
		BudgetBand: GenesisPlanBudget{ID: "none", Enforcement: GenesisPlanEnforcement},
		Estimate:   GenesisPlanEstimate{Band: "hours", Label: "a few hours of agent work", Estimate: true, Caveat: GenesisPlanCaveat},
		Warnings:   []string{"settle-at-desk"},
		Notes:      []GenesisPlanNote{{Field: "software.stack", Text: "Go and a small database", Untrusted: true}},
	}
}

// samplePlanResult is the answer to a plan of a finished thread: the plan
// the host stored.
func samplePlanResult() SeatGenesisResult {
	p := samplePlan()
	return SeatGenesisResult{State: GenesisStatePlan, Thread: genThread, Round: 6, Of: 6, Progress: GenesisProgress{Filled: 14, Total: 14, PlanReady: true},
		Nodes: []GenesisNode{}, CatalogDigest: genCatalog, Plan: &p, PlanDigest: genPlan, Stored: true}
}

// The plan is written in the plan record's canonical form, and its digest is
// the SHA-256 of those bytes; the answer to a plan carries it whole, stored or
// as a preview.
func TestSeatGenesisPlanVectors(t *testing.T) {
	out, err := EncodeGenesisPlan(samplePlan())
	if err != nil || string(out) != planJSON {
		t.Fatalf("the plan encodes to\n%s\nwant\n%s\n(%v)", out, planJSON, err)
	}
	if d, err := GenesisPlanDigest(samplePlan()); err != nil || d != genPlan {
		t.Fatalf("the digest is %s (%v), want %s", d, err, genPlan)
	}
	dec, enc := DecodeSeatGenesisResult, EncodeSeatGenesisResult
	vector(t, "a stored plan", `{"catalog_digest":"`+genCatalog+`","nodes":[],"of":6,"plan":`+planJSON+`,"plan_digest":"`+genPlan+`",`+
		`"progress":{"filled":14,"plan_ready":true,"total":14},"reset":0,"round":6,"state":"plan","stored":true,"thread":"`+genThread+`"}`,
		samplePlanResult(), dec, enc)
	preview := samplePlanResult()
	preview.Stored = false
	vector(t, "a preview", `{"catalog_digest":"`+genCatalog+`","nodes":[],"of":6,"plan":`+planJSON+`,"plan_digest":"`+genPlan+`",`+
		`"progress":{"filled":14,"plan_ready":true,"total":14},"reset":0,"round":6,"state":"plan","thread":"`+genThread+`"}`,
		preview, dec, enc)
	// The mark plan_ready on a summary, and the verb's params.
	summary := SeatGenesisResult{State: GenesisStateSummary, Thread: genThread, Round: 3, Of: 6, Progress: GenesisProgress{Filled: 3, Total: 3, PlanReady: true},
		Nodes: []GenesisNode{}, Groups: genesisRows(), CatalogDigest: genCatalog}
	groups := `{"decided_for_you":[],"defaults":[{"field":"schedule","label":"How soon do you need it?","node":"schedule","untrusted":false,"value":"No hurry"}],` +
		`"you_chose":[{"field":"goal","label":"What is the project for?","node":"goal","untrusted":false,"value":"To share with others"},` +
		`{"field":"software.stack","label":"What will it be built with?","node":"software.stack","untrusted":true,"value":"Go and a small database"}]}`
	vector(t, "a summary that is ready for a plan", `{"catalog_digest":"`+genCatalog+`","groups":`+groups+`,"nodes":[],"of":6,"progress":{"filled":3,"plan_ready":true,"total":3},"reset":0,"round":3,"state":"summary","thread":"`+genThread+`"}`,
		summary, dec, enc)
	vector(t, "plan", `{"thread":"`+genThread+`","verb":"plan"}`, SeatGenesisParams{Verb: GenesisVerbPlan, Thread: genThread}, DecodeSeatGenesisParams, EncodeSeatGenesisParams)
	for name, in := range map[string]string{
		"plan without a thread": `{"verb":"plan"}`,
		"plan with a round":     `{"round":1,"thread":"` + genThread + `","verb":"plan"}`,
		"plan with a node":      `{"node":"goal","thread":"` + genThread + `","verb":"plan"}`,
		"a verb in capitals":    `{"thread":"` + genThread + `","verb":"Plan"}`,
	} {
		_, err := DecodeSeatGenesisParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

// What a plan may hold: the rules of the plan record, each refused when it is
// broken, the plan alone and inside a result.
func TestSeatGenesisPlanIsHeldToTheRecord(t *testing.T) {
	str := strings.Repeat
	cases := map[string]func(p *GenesisPlan){
		"another schema":                  func(p *GenesisPlan) { p.Schema = "ataniq.project-plan/2" },
		"revision zero":                   func(p *GenesisPlan) { p.Revision = 0 },
		"revision 10000":                  func(p *GenesisPlan) { p.Revision = 10000 },
		"revision 2 without the one":      func(p *GenesisPlan) { p.Revision = 2 },
		"revision 1 with a digest before": func(p *GenesisPlan) { p.PrevDigest = genBrief },
		"a digest before in capitals": func(p *GenesisPlan) {
			p.Revision, p.PrevDigest = 2, strings.ToUpper(genBrief)
		},
		"a short brief digest":             func(p *GenesisPlan) { p.BriefDigest = genBrief[:63] },
		"no catalog digest":                func(p *GenesisPlan) { p.CatalogDigest = "" },
		"a template digest in capitals":    func(p *GenesisPlan) { p.TemplateDigest = strings.ToUpper(genTemplate) },
		"no template id":                   func(p *GenesisPlan) { p.Template.ID = "" },
		"a template id in capitals":        func(p *GenesisPlan) { p.Template.ID = "Software" },
		"a template id of 33 bytes":        func(p *GenesisPlan) { p.Template.ID = str("a", 33) },
		"a version that starts with a dot": func(p *GenesisPlan) { p.Template.Version = ".1" },
		"a version of 17 bytes":            func(p *GenesisPlan) { p.Template.Version = str("1", 17) },
		"no title":                         func(p *GenesisPlan) { p.Title = "" },
		"a title of 61 bytes":              func(p *GenesisPlan) { p.Title = str("t", 61) },
		"a title with a space at its end":  func(p *GenesisPlan) { p.Title = "A web app " },
		"a title with markup":              func(p *GenesisPlan) { p.Title = "A <b>web</b> app" },
		"a title with a web address":       func(p *GenesisPlan) { p.Title = "See https://example.com" },
		"a demo of 121 bytes":              func(p *GenesisPlan) { p.FirstDemo = str("d", 121) },
		"a demo with www.":                 func(p *GenesisPlan) { p.FirstDemo = "Open www.example.com" },
		"no milestone":                     func(p *GenesisPlan) { p.Milestones = []GenesisPlanMilestone{} },
		"five milestones": func(p *GenesisPlan) {
			for i := 4; i <= 6; i++ {
				m := p.Milestones[1]
				m.ID, m.Tasks = "m"+strconv.Itoa(i), []GenesisPlanTask{{ID: "t" + strconv.Itoa(i+4), Title: "x", Kind: "test", Size: "small",
					DependsOn: []string{}, Scope: []string{"repo:src"}, Checks: []string{}}}
				p.Milestones = append(p.Milestones, m)
			}
		},
		"a milestone id twice":           func(p *GenesisPlan) { p.Milestones[1].ID = "m1" },
		"milestones out of order":        func(p *GenesisPlan) { p.Milestones[0].ID = "m4" },
		"a milestone id with a zero":     func(p *GenesisPlan) { p.Milestones[0].ID = "m01" },
		"a milestone id of a task":       func(p *GenesisPlan) { p.Milestones[0].ID = "t1" },
		"a goal of 121 bytes":            func(p *GenesisPlan) { p.Milestones[0].Goal = str("g", 121) },
		"a milestone title of 61 bytes":  func(p *GenesisPlan) { p.Milestones[0].Title = str("m", 61) },
		"a milestone without a task":     func(p *GenesisPlan) { p.Milestones[1].Tasks = []GenesisPlanTask{} },
		"a task id twice":                func(p *GenesisPlan) { p.Milestones[0].Tasks[1].ID = "t1" },
		"tasks out of order":             func(p *GenesisPlan) { p.Milestones[1].Tasks[0].ID = "t2" },
		"a task title of 101 bytes":      func(p *GenesisPlan) { p.Milestones[0].Tasks[0].Title = str("x", 101) },
		"a kind that is not one":         func(p *GenesisPlan) { p.Milestones[0].Tasks[0].Kind = "deploy" },
		"a size that is not one":         func(p *GenesisPlan) { p.Milestones[0].Tasks[0].Size = "huge" },
		"an empty scope":                 func(p *GenesisPlan) { p.Milestones[0].Tasks[0].Scope = []string{} },
		"an area of another prefix":      func(p *GenesisPlan) { p.Milestones[0].Tasks[0].Scope = []string{"disk:src"} },
		"an area in capitals":            func(p *GenesisPlan) { p.Milestones[0].Tasks[0].Scope = []string{"repo:Src"} },
		"a scope out of order":           func(p *GenesisPlan) { p.Milestones[0].Tasks[1].Scope = []string{"repo:tests", "repo:src"} },
		"a check twice":                  func(p *GenesisPlan) { p.Milestones[0].Tasks[1].Checks = []string{"lint", "lint"} },
		"a check id with a space":        func(p *GenesisPlan) { p.Milestones[0].Tasks[1].Checks = []string{"unit tests"} },
		"a dependency on itself":         func(p *GenesisPlan) { p.Milestones[0].Tasks[1].DependsOn = []string{"t3"} },
		"a dependency on a later task":   func(p *GenesisPlan) { p.Milestones[0].Tasks[1].DependsOn = []string{"t7"} },
		"a dependency on no task":        func(p *GenesisPlan) { p.Milestones[0].Tasks[1].DependsOn = []string{"t2"} },
		"a dependency that is no id":     func(p *GenesisPlan) { p.Milestones[0].Tasks[1].DependsOn = []string{"m1"} },
		"dependencies out of order":      func(p *GenesisPlan) { p.Milestones[1].Tasks[0].DependsOn = []string{"t3", "t1"} },
		"no gate":                        func(p *GenesisPlan) { p.Gates = []string{} },
		"gates out of order":             func(p *GenesisPlan) { p.Gates = []string{"unit-tests", "lint"} },
		"no review point":                func(p *GenesisPlan) { p.ReviewPoints = []string{} },
		"a review point that is not one": func(p *GenesisPlan) { p.ReviewPoints = []string{"deploy"} },
		"a review point twice":           func(p *GenesisPlan) { p.ReviewPoints = []string{"demo", "demo"} },
		"a host that is not one":         func(p *GenesisPlan) { p.Hosts = "the cloud" },
		"a service that is not one":      func(p *GenesisPlan) { p.ExternalServices = []string{"payments"} },
		"services out of order":          func(p *GenesisPlan) { p.ExternalServices = []string{"hosting service", "email delivery"} },
		"a band that is a number":        func(p *GenesisPlan) { p.BudgetBand.ID = "50" },
		"an enforced band":               func(p *GenesisPlan) { p.BudgetBand.Enforcement = "enforced" },
		"an estimate band not one":       func(p *GenesisPlan) { p.Estimate.Band = "month" },
		"an estimate with another label": func(p *GenesisPlan) { p.Estimate.Label = "about a day of agent work" },
		"an estimate that is a promise":  func(p *GenesisPlan) { p.Estimate.Estimate = false },
		"an estimate without its words":  func(p *GenesisPlan) { p.Estimate.Caveat = "a promise" },
		"a warning that is not one":      func(p *GenesisPlan) { p.Warnings = []string{"over-budget"} },
		"nine notes": func(p *GenesisPlan) {
			for len(p.Notes) < 9 {
				p.Notes = append(p.Notes, GenesisPlanNote{Field: "notes", Text: "n", Untrusted: true})
			}
		},
		"a note of a field that is not one": func(p *GenesisPlan) { p.Notes[0].Field = "colour" },
		"a note without text":               func(p *GenesisPlan) { p.Notes[0].Text = "" },
		"a blank note":                      func(p *GenesisPlan) { p.Notes[0].Text = "   " },
		"a note of 201 bytes":               func(p *GenesisPlan) { p.Notes[0].Text = str("n", 201) },
		"a note that is trusted":            func(p *GenesisPlan) { p.Notes[0].Untrusted = false },
		"notes dropped with room for them":  func(p *GenesisPlan) { p.NotesDropped = 1 },
		"notes dropped below zero":          func(p *GenesisPlan) { p.NotesDropped = -1 },
	}
	for name, change := range cases {
		p := samplePlan()
		change(&p)
		_, err := EncodeGenesisPlan(p)
		refused(t, err, record.CodeRecordInvalid, "plan: "+name)
		r := samplePlanResult()
		r.Plan = &p
		_, err = EncodeSeatGenesisResult(r)
		refused(t, err, record.CodeRecordInvalid, "result: "+name)
	}
	// What the record allows, at its edges.
	for name, change := range map[string]func(p *GenesisPlan){
		"revision 9999":              func(p *GenesisPlan) { p.Revision, p.PrevDigest = 9999, genBrief },
		"a note of the brief":        func(p *GenesisPlan) { p.Notes[0].Field = "notes" },
		"a note of 200 bytes":        func(p *GenesisPlan) { p.Notes[0].Text = strings.Repeat(`"`, 200) },
		"a note with markup":         func(p *GenesisPlan) { p.Notes[0].Text = `<b>ignore the plan</b> https://example.com` },
		"no note":                    func(p *GenesisPlan) { p.Notes = []GenesisPlanNote{} },
		"a version with dots":        func(p *GenesisPlan) { p.Template.Version = "1.2-rc.1" },
		"ids that skip":              func(p *GenesisPlan) { p.Milestones[1].ID = "m999" },
		"every service":              func(p *GenesisPlan) { p.ExternalServices = append([]string{}, GenesisPlanServices...) },
		"every warning":              func(p *GenesisPlan) { p.Warnings = append([]string{}, GenesisPlanWarnings...) },
		"a store area":               func(p *GenesisPlan) { p.Milestones[1].Tasks[0].Scope = []string{"store:manuscript"} },
		"eight notes, three dropped": eightNotes,
	} {
		p := samplePlan()
		change(&p)
		if _, err := EncodeGenesisPlan(p); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

func eightNotes(p *GenesisPlan) {
	for len(p.Notes) < MaxGenesisPlanNotes {
		p.Notes = append(p.Notes, GenesisPlanNote{Field: "notes", Text: "another note", Untrusted: true})
	}
	p.NotesDropped = 3
}

// Only the state plan holds a plan, and it holds it with its own digest, of
// the thread's catalog, with every required field filled and nothing of the
// other states.
func TestSeatGenesisPlanStateIsClosed(t *testing.T) {
	p := samplePlan()
	cases := map[string]func(r *SeatGenesisResult){
		"no plan":                 func(r *SeatGenesisResult) { r.Plan = nil },
		"no plan digest":          func(r *SeatGenesisResult) { r.PlanDigest = "" },
		"another plan digest":     func(r *SeatGenesisResult) { r.PlanDigest = genBrief },
		"a plan digest of 63":     func(r *SeatGenesisResult) { r.PlanDigest = genPlan[:63] },
		"a plan of another kind":  func(r *SeatGenesisResult) { r.CatalogDigest = genBrief },
		"a brief not filled":      func(r *SeatGenesisResult) { r.Progress.Filled = 13 },
		"a reset":                 func(r *SeatGenesisResult) { r.Reset = 1 },
		"nodes":                   func(r *SeatGenesisResult) { r.Nodes = genesisNodes()[:1] },
		"groups":                  func(r *SeatGenesisResult) { r.Groups = genesisRows() },
		"a brief digest":          func(r *SeatGenesisResult) { r.Digest = genBrief },
		"threads":                 func(r *SeatGenesisResult) { r.Threads = []GenesisThread{} },
		"no thread":               func(r *SeatGenesisResult) { r.Thread = "" },
		"a round over of":         func(r *SeatGenesisResult) { r.Round = 7 },
		"a plan changed under it": func(r *SeatGenesisResult) { q := samplePlan(); q.Title = "A web tool"; r.Plan = &q },
	}
	for name, change := range cases {
		r := samplePlanResult()
		change(&r)
		_, err := EncodeSeatGenesisResult(r)
		refused(t, err, record.CodeRecordInvalid, "plan: "+name)
	}
	others := map[string]SeatGenesisResult{
		"question": sampleSeatGenesisResult(),
		"summary": {State: GenesisStateSummary, Thread: genThread, Round: 3, Of: 6, Progress: GenesisProgress{Filled: 3, Total: 7},
			Nodes: []GenesisNode{}, Groups: genesisRows(), CatalogDigest: genCatalog},
		"finished": {State: GenesisStateFinished, Thread: genThread, Round: 3, Of: 6, Progress: GenesisProgress{Filled: 3, Total: 7},
			Nodes: []GenesisNode{}, Groups: genesisRows(), CatalogDigest: genCatalog, Digest: genBrief},
		"discarded": {State: GenesisStateDiscarded, Thread: genThread, Of: 6, Nodes: []GenesisNode{}, CatalogDigest: genCatalog},
		"threads":   sampleThreads(),
	}
	for name, base := range others {
		if _, err := EncodeSeatGenesisResult(base); err != nil {
			t.Fatalf("the %s sample is refused: %v", name, err)
		}
		for what, change := range map[string]func(r *SeatGenesisResult){
			"a plan":        func(r *SeatGenesisResult) { r.Plan = &p },
			"a plan digest": func(r *SeatGenesisResult) { r.PlanDigest = genPlan },
			"stored":        func(r *SeatGenesisResult) { r.Stored = true },
		} {
			r := base
			change(&r)
			_, err := EncodeSeatGenesisResult(r)
			refused(t, err, record.CodeRecordInvalid, name+" with "+what)
		}
	}
	// A discarded thread and a list have nothing ready.
	for _, name := range []string{"discarded", "threads"} {
		r := others[name]
		r.Progress.PlanReady = true
		_, err := EncodeSeatGenesisResult(r)
		refused(t, err, record.CodeRecordInvalid, name+" ready for a plan")
	}
	// The decoder refuses the same, from the JSON: a plan member the record
	// does not have, one it has left out, and a plan under a wrong digest.
	sample, err := EncodeSeatGenesisResult(samplePlanResult())
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string][]byte{
		"a member the plan lacks":   bytes.Replace(sample, []byte(`"notes_dropped":0`), []byte(`"notes_dropped":0,"charter":"yes"`), 1),
		"a plan without its notes":  bytes.Replace(sample, []byte(`"notes":[{"field":"software.stack","text":"Go and a small database","untrusted":true}],`), nil, 1),
		"a task without its checks": bytes.Replace(sample, []byte(`{"checks":["lint"],`), []byte(`{`), 1),
		"a plan of a revision null": bytes.Replace(sample, []byte(`"plan":1,`), []byte(`"plan":null,`), 1),
		"a plan changed in transit": bytes.Replace(sample, []byte(`"title":"A web app"`), []byte(`"title":"A web toy"`), 1),
		"stored that is a string":   bytes.Replace(sample, []byte(`"stored":true`), []byte(`"stored":"true"`), 1),
	} {
		if bytes.Equal(in, sample) {
			t.Fatalf("%s: the sample was not changed", name)
		}
		_, err := DecodeSeatGenesisResult(in)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	// The members of a plan are all required: each left out is refused.
	planOut, _ := EncodeGenesisPlan(samplePlan())
	for _, key := range []string{"schema", "plan", "prev_digest", "brief_digest", "catalog_digest", "template", "template_digest", "title",
		"milestones", "gates", "first_demo", "review_points", "hosts", "external_services", "budget_band", "estimate", "warnings", "notes",
		"notes_dropped", "enforcement", "caveat", "label", "goal", "depends_on", "scope", "untrusted", "version"} {
		in := bytes.Replace(sample, []byte(`"`+key+`":`), []byte(`"x`+key+`":`), 1)
		if !bytes.Contains(planOut, []byte(`"`+key+`":`)) {
			t.Fatalf("the plan has no member %s", key)
		}
		_, err := DecodeSeatGenesisResult(in)
		refused(t, err, record.CodeRecordInvalid, "a plan without "+key)
	}
}

// The plan's cap is under the result's: the largest plan the record allows
// (every count and every text at its cap) is under the plan's cap, and a plan
// at that cap, in a result whose other members are at their largest, fits
// 16 KiB.
func TestSeatGenesisPlanFitsTheResult(t *testing.T) {
	// A plan with every text at its cap: four milestones of three tasks,
	// eight notes of the longest escaped text.
	str := strings.Repeat
	p := samplePlan()
	p.Title, p.FirstDemo = str("t", MaxGenesisPlanTitle), str("d", MaxGenesisPlanDemo)
	p.Milestones = nil
	n := 0
	for i := 1; i <= MaxGenesisPlanMilestones; i++ {
		m := GenesisPlanMilestone{ID: "m" + strconv.Itoa(100+i), Title: str("m", MaxGenesisPlanMilestoneTitle), Goal: str("g", MaxGenesisPlanGoal)}
		for j := 0; j < MaxGenesisPlanTasks/MaxGenesisPlanMilestones; j++ {
			n++
			deps := []string{}
			for k := 1; k < n; k++ {
				deps = append(deps, "t"+strconv.Itoa(100+k))
			}
			m.Tasks = append(m.Tasks, GenesisPlanTask{ID: "t" + strconv.Itoa(100+n), Title: str("x", MaxGenesisPlanTaskTitle), Kind: "review", Size: "medium",
				DependsOn: deps, Scope: []string{"repo:" + str("a", MaxGenesisPlanWordBytes), "store:" + str("b", MaxGenesisPlanWordBytes)},
				Checks: []string{str("c", MaxGenesisPlanWordBytes), str("d", MaxGenesisPlanWordBytes)}})
		}
		p.Milestones = append(p.Milestones, m)
	}
	p.Gates = []string{str("c", MaxGenesisPlanWordBytes)}
	p.ReviewPoints = append([]string{}, GenesisPlanReviewPoints...)
	p.ExternalServices = append([]string{}, GenesisPlanServices...)
	p.Warnings = append([]string{}, GenesisPlanWarnings...)
	p.Notes = nil
	for len(p.Notes) < MaxGenesisPlanNotes {
		p.Notes = append(p.Notes, GenesisPlanNote{Field: "software." + str("f", MaxGenesisFieldBytes-len("software.")), Text: str(`"`, MaxGenesisPlanNoteText), Untrusted: true})
	}
	p.NotesDropped = MaxGenesisPlanNotesDropped
	planOut, err := EncodeGenesisPlan(p)
	if err != nil {
		t.Fatalf("the largest plan the record allows is refused: %v", err)
	}
	d, _ := GenesisPlanDigest(p)
	r := SeatGenesisResult{State: GenesisStatePlan, Thread: genThread, Round: MaxGenesisRounds, Of: MaxGenesisRounds,
		Progress: GenesisProgress{Filled: MaxGenesisRows, Total: MaxGenesisRows, PlanReady: true}, Nodes: []GenesisNode{},
		CatalogDigest: genCatalog, Plan: &p, PlanDigest: d, Stored: true}
	out, err := EncodeSeatGenesisResult(r)
	if err != nil {
		t.Fatalf("the largest plan does not fit a result: %v", err)
	}
	room := len(out) - len(planOut) + MaxGenesisPlanBytes
	t.Logf("a plan of %d bytes in a result of %d; a plan at its cap would take %d of %d", len(planOut), len(out), room, MaxSeatGenesisResultBytes)
	if room > MaxSeatGenesisResultBytes {
		t.Errorf("a plan at its cap takes a result to %d bytes, over %d", room, MaxSeatGenesisResultBytes)
	}
	back, err := DecodeSeatGenesisResult(out)
	if err != nil || !reflect.DeepEqual(back.Plan.Milestones, p.Milestones) {
		t.Fatalf("the largest plan does not decode to itself: %v", err)
	}
}
