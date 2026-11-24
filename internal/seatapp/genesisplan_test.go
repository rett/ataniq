package seatapp

import (
	"errors"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// samplePlan is a preview of a web app's plan with text in every field that
// a page must escape: markup in the titles, a bidirectional control and a
// zero-width space in a task's title and in the owner's notes, and an
// unknown area and warning.
func samplePlan() GenesisPlanView {
	return GenesisPlanView{
		Revision: 1, Digest: strings.Repeat("3c", 32), Title: "A web <b>app</b>",
		Milestones: []GenesisMilestone{
			{ID: "m1", Title: "Set up <i>the</i> project", Goal: "A repository that builds.", Tasks: []GenesisTask{
				{ID: "t1", Title: "Settle the language with you", Kind: "review", Size: "small", DependsOn: []string{}, Scope: []string{"repo:docs"}},
				{ID: "t2", Title: "Lay out the repository\u202eevil", Kind: "build", Size: "small", DependsOn: []string{"t1"}, Scope: []string{"repo:docs", "repo:src"}},
			}},
			{ID: "m2", Title: "Build the first flow", Goal: "One flow works.", Tasks: []GenesisTask{
				{ID: "t5", Title: "Build the first page <script>alert(1)</script>", Kind: "build", Size: "medium", DependsOn: []string{"t1", "t2"}, Scope: []string{"repo:src", "repo:tests", "repo:weird"}},
				{ID: "t6", Title: "Write tests", Kind: "test", Size: "large", DependsOn: []string{"t5"}, Scope: []string{"repo:tests"}},
			}},
		},
		Gates: []string{"lint", "unit-tests"}, FirstDemo: "It runs on this computer & one flow works.",
		ReviewPoints: []string{"demo", "plan"}, Hosts: "this_computer", ExternalServices: []string{"hosting service"},
		BudgetBand: "low", Estimate: "days", Warnings: []string{"settle-at-desk", "something-new"},
		Notes: []GenesisNote{{Field: "software.stack", Label: "Do you prefer a <b>programming</b> language?", Text: "Go <img src=x> and \u200ba small \u202edatabase"}}, NotesDropped: 0,
	}
}

// planRig is a rig whose port answers the plan verb with p.
func planRig(t *testing.T, p GenesisPlanView) (*rig, *fakeGenesis, *http.Cookie) {
	t.Helper()
	r, g, s := genesisRig(t)
	g.set(GenesisVerbPlan, &GenesisView{State: GenesisStatePlan, Thread: testThread, Round: 6, Of: 6, Filled: 14, Total: 14, PlanReady: true, Plan: &p}, nil)
	return r, g, s
}

// planPath is the plan page of the fake's thread.
const planPath = "/projects/" + testThread + "/plan"

// sectionOf is the text of the page from the heading words to the next
// section's end.
func sectionOf(t *testing.T, page, heading string) string {
	t.Helper()
	i := strings.Index(page, heading)
	if i < 0 {
		t.Fatalf("no section %q", heading)
	}
	j := strings.Index(page[i:], "</section>")
	if j < 0 {
		t.Fatalf("the section %q does not end", heading)
	}
	return page[i : i+j]
}

// The draft plan page, section by section, in the order of reading: the
// heading with the title and the thread's tag, the fixed lines of a draft,
// the first demo, what waits for the owner with the fixed sentence, the
// services, the hosts, the budget band, the estimate, the warnings by id,
// the milestones, the owner's notes and what a charter would cover.
func TestGenesisPlanPageSections(t *testing.T) {
	r, g, s := planRig(t, samplePlan())
	w := r.getWith(planPath, s)
	if w.Code != http.StatusOK {
		t.Fatalf("the plan page: %d\n%s", w.Code, w.Body)
	}
	page := w.Body.String()
	order := []string{
		"<h1>Draft plan</h1>",
		`<strong>A web &lt;b&gt;app&lt;/b&gt;</strong>, <span class="mono">012345</span>`,
		planBindsNothing,
		planDraftLine,
		"3c3c3c3c3c3c",
		"First demo", "It runs on this computer &amp; one flow works.",
		"What waits for you", "At the first demo", "Before work starts: you see the plan first", html.EscapeString(planAlwaysWaits),
		"Outside services the work would use", "hosting service",
		"Where the files live", "On this computer",
		"Budget band", "A little: up to about 10 dollars a month", "(advisory: nothing enforces it yet)",
		"Estimate", "a few days of agent work", "(an estimate, not a promise)",
		"Warnings", "Some answers are in your own words: they are settled with you at the desk.", planUnknownNote,
		"Milestones and tasks", "Each milestone ends when these checks pass: lint, unit tests.",
		`<details class="milestone" open>`, "<summary>Set up &lt;i&gt;the&lt;/i&gt; project", "(2 tasks)",
		"Settle the language with you", "a review, a small task",
		`Lay out the repository\u202eevil`, "building, a small task; after: Settle the language with you",
		`<details class="milestone">`, "Build the first flow",
		"Build the first page &lt;script&gt;alert(1)&lt;/script&gt;",
		"building, a medium task; after: Settle the language with you; Lay out the repository\\u202eevil",
		"testing, a large task; after: Build the first page",
		"Your own notes", "Do you prefer a &lt;b&gt;programming&lt;/b&gt; language?", `<q class="owners">Go &lt;img src=x&gt; and \u200ba small \u202edatabase</q>`, "(" + planNotesMarked + ")",
		"What a charter would cover", planCharterLine, "A signed charter would allow at most:",
		"Work on: the documentation; the source code; the tests; repo:weird.",
		"Outside services: hosting service.",
		"Stopping for you at: At the first demo; Before work starts: you see the plan first.",
		planCharterAlways,
		"Monthly budget band (advisory; nothing enforces it yet): A little: up to about 10 dollars a month.",
		`<a id="project-page" href="/projects/` + testThread + `">Back to the summary</a>`,
		`<a id="project-list" href="/projects/">Back to the project list</a>`,
	}
	at := 0
	for _, want := range order {
		i := strings.Index(page[at:], want)
		if i < 0 {
			t.Fatalf("the plan page lacks %q after offset %d:\n%s", want, at, page)
		}
		at += i + len(want)
	}
	// Only the first milestone is open.
	if n := strings.Count(page, " open>"); n != 1 {
		t.Errorf("%d milestones open, want the first", n)
	}
	// Nothing of the owner's or the host's text is markup, and no hidden
	// character is shown as itself.
	for _, bad := range []string{"<b>", "<i>", "<script>", "<img", "\u202e", "\u200b", "something-new"} {
		if strings.Contains(page, bad) {
			t.Errorf("the page holds %q", bad)
		}
	}
	// No script, no form, no control but the two links.
	for _, bad := range []string{"<script", "<form", "<button", "<input", "<textarea", "<select"} {
		if strings.Contains(page, bad) {
			t.Errorf("the plan page holds %s", bad)
		}
	}
	if c := g.seen(); len(c) != 1 || c[0].Verb != GenesisVerbPlan || c[0].Thread != testThread {
		t.Errorf("the page made %+v", c)
	}
}

// A plan drawn from answers decided for the owner carries the warning
// inferred-answers, and the page says so in its fixed sentence, escaped,
// among the warnings; a plan without the warning has no such sentence.
func TestGenesisPlanPageInferredAnswers(t *testing.T) {
	p := samplePlan()
	p.Warnings = []string{"inferred-answers"}
	r, _, s := planRig(t, p)
	w := r.getWith(planPath, s)
	if w.Code != http.StatusOK {
		t.Fatalf("the plan page: %d\n%s", w.Code, w.Body)
	}
	page := w.Body.String()
	warnings := sectionOf(t, page, `<h2 id="warnings-h">Warnings</h2>`)
	if strings.Count(page, html.EscapeString(planInferredLine)) != 1 || !strings.Contains(warnings, "<li>"+html.EscapeString(planInferredLine)+"</li>") {
		t.Fatalf("the warnings do not say some answers were decided for the owner:\n%s", page)
	}
	if strings.Contains(page, "inferred-answers") {
		t.Error("the page shows the warning's id")
	}

	// The stored plan, of a finished brief that cannot be changed, says
	// finishing accepted the answers, and never offers the summary.
	p.Stored, p.Revision = true, 1
	r, _, s = planRig(t, p)
	page = r.getWith(planPath, s).Body.String()
	warnings = sectionOf(t, page, `<h2 id="warnings-h">Warnings</h2>`)
	if strings.Count(page, html.EscapeString(planInferredStoredLine)) != 1 || !strings.Contains(warnings, "<li>"+html.EscapeString(planInferredStoredLine)+"</li>") {
		t.Fatalf("the stored plan does not say finishing accepted the answers:\n%s", page)
	}
	if strings.Contains(page, "change them from the summary") {
		t.Error("the stored plan offers a change from the summary")
	}

	// A plan with no inferred answer, draft or stored, says neither.
	for _, stored := range []bool{false, true} {
		p.Stored, p.Warnings = stored, []string{"settle-at-desk"}
		r, _, s = planRig(t, p)
		if page := r.getWith(planPath, s).Body.String(); strings.Contains(page, html.EscapeString(planInferredLine)) || strings.Contains(page, html.EscapeString(planInferredStoredLine)) || strings.Contains(page, "decided for you") {
			t.Fatalf("a plan (stored %v) with no inferred answer says some were decided:\n%s", stored, page)
		}
	}
}

// The stored plan: its heading names its revision, there is no draft line,
// and the way back is the finished brief.
func TestGenesisPlanPageStored(t *testing.T) {
	p := samplePlan()
	p.Stored, p.Revision = true, 3
	r, _, s := planRig(t, p)
	page := r.getWith(planPath, s).Body.String()
	for _, want := range []string{"<h1>Plan, revision 3</h1>", planBindsNothing, `>Back to the project&#39;s brief</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the stored plan's page lacks %q", want)
		}
	}
	if strings.Contains(page, "Draft plan") || strings.Contains(page, planDraftLine) {
		t.Error("the stored plan is called a draft")
	}
}

// The sentence of what always waits for the owner is on the page whatever
// the review points hold: with "publish" among them, and without.
func TestGenesisPlanPageAlwaysSaysWhatWaits(t *testing.T) {
	for _, points := range [][]string{{"plan"}, {"publish"}, {"demo", "milestone", "plan", "publish"}, {}} {
		p := samplePlan()
		p.ReviewPoints = points
		r, _, s := planRig(t, p)
		page := r.getWith(planPath, s).Body.String()
		if strings.Count(page, html.EscapeString(planAlwaysWaits)) != 1 {
			t.Errorf("review points %v: the fixed sentence is not on the page once", points)
		}
	}
}

// What a charter would cover is a draft of words: no "card", no "allows"
// of the plan but the fixed "It allows nothing.", no control and no link
// out of it, and it is nowhere else: not on the card list and not in the
// pages of the interview.
func TestGenesisPlanPageCharterSection(t *testing.T) {
	r, _, s := planRig(t, samplePlan())
	page := r.getWith(planPath, s).Body.String()
	charter := sectionOf(t, page, `<section id="charter"`)
	if regexp.MustCompile(`(?i)card`).MatchString(charter) {
		t.Errorf("the charter section says card:\n%s", charter)
	}
	if strings.Count(charter, "allows") != 1 || !strings.Contains(charter, "It allows nothing.") {
		t.Errorf("the charter section says allows of the plan:\n%s", charter)
	}
	for _, bad := range []string{"<a ", "<button", "<input", "<form", "Approve", "approve", "Sign it", "passkey", "/cards"} {
		if strings.Contains(charter, bad) {
			t.Errorf("the charter section holds %q", bad)
		}
	}
	// The page links into no card at all.
	if strings.Contains(page, `href="/cards`) {
		t.Error("the plan page links to the cards")
	}
	for _, path := range []string{"/cards/", "/projects/", "/projects/" + testThread} {
		if body := r.getWith(path, s).Body.String(); strings.Contains(body, "What a charter would cover") || strings.Contains(body, "charter") {
			t.Errorf("%s shows the charter", path)
		}
	}
	// The budget is not an item of the "at most" list: it is a sentence of its
	// own after it, and the list holds the always-stopping line.
	list := charter[strings.Index(charter, "<ul>"):strings.Index(charter, "</ul>")]
	if strings.Contains(list, "budget") || strings.Contains(list, "Spending") || !strings.Contains(list, planCharterAlways) {
		t.Errorf("the charter's list:\n%s", list)
	}
	if rest := charter[strings.Index(charter, "</ul>"):]; !strings.Contains(rest, "Monthly budget band (advisory; nothing enforces it yet):") {
		t.Errorf("the budget is not a sentence after the list:\n%s", rest)
	}
	// No services, and the services lines say none.
	p := samplePlan()
	p.ExternalServices = []string{}
	r, _, s = planRig(t, p)
	page = r.getWith(planPath, s).Body.String()
	if !strings.Contains(page, "Outside services: none.") || !strings.Contains(sectionOf(t, page, "Outside services the work would use"), "None.") {
		t.Error("a plan with no outside service does not say none")
	}
}

// planCharterAlways is the line of the charter's list that says what always
// waits for the owner.
const planCharterAlways = "Always stopping for you before publishing, releases, pushing code, credentials and raising any limit."

// The owner's notes: shown only when there are some, each through Text, and
// a count of the ones left out.
func TestGenesisPlanPageNotes(t *testing.T) {
	p := samplePlan()
	p.Notes = nil
	r, _, s := planRig(t, p)
	if page := r.getWith(planPath, s).Body.String(); strings.Contains(page, "Your own notes") {
		t.Error("a plan with no note shows the notes' section")
	}
	p = samplePlan()
	for i := 0; i < MaxGenesisPlanNotes-1; i++ {
		p.Notes = append(p.Notes, GenesisNote{Field: "notes", Text: "note \u2066x"})
	}
	p.NotesDropped = 3
	r, _, s = planRig(t, p)
	page := r.getWith(planPath, s).Body.String()
	if strings.Count(page, `<q class="owners">`) != MaxGenesisPlanNotes || !strings.Contains(page, "3 more notes were left out.") ||
		!strings.Contains(page, `note \u2066x`) {
		t.Errorf("the notes:\n%s", sectionOf(t, page, "Your own notes"))
	}
}

// Each note says which answer it belongs to: the question's words that the
// link gave for its field, shown before the quoted text, or "A note" for a
// note of the brief's own, a field with no words and a label on the field
// "notes". The words are escaped like every text.
func TestGenesisPlanPageNotesSayWhichAnswer(t *testing.T) {
	p := samplePlan()
	p.Notes = []GenesisNote{
		{Field: "software.stack", Label: "Do you prefer a programming language?", Text: "one"},
		{Field: "software.auth", Label: "How should people <i>sign</i> in\u202e?", Text: "two"},
		{Field: "notes", Text: "three"},
		{Field: "notes", Label: "A label that must not be used", Text: "four"},
		{Field: "software.unheard", Text: "five"},
	}
	r, _, s := planRig(t, p)
	notes := sectionOf(t, r.getWith(planPath, s).Body.String(), "Your own notes")
	for _, want := range []string{
		`<span class="meta">Do you prefer a programming language?</span><br><q class="owners">one</q>`,
		`<span class="meta">How should people &lt;i&gt;sign&lt;/i&gt; in\u202e?</span><br><q class="owners">two</q>`,
		`<span class="meta">A note</span><br><q class="owners">three</q>`,
		`<span class="meta">A note</span><br><q class="owners">four</q>`,
		`<span class="meta">A note</span><br><q class="owners">five</q>`,
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("the notes lack %q:\n%s", want, notes)
		}
	}
	for _, bad := range []string{"must not be used", "<i>", "\u202e"} {
		if strings.Contains(notes, bad) {
			t.Errorf("the notes hold %q", bad)
		}
	}
}

// No plan: the answers are not complete, the kind has no template, or the
// thread was finished early. Each is a page of fixed words with the ways
// back, and no plan section.
func TestGenesisPlanPageWithNoPlan(t *testing.T) {
	r, g, s := genesisRig(t)
	g.set(GenesisVerbPlan, nil, ErrGenesisNotReady)
	w := r.getWith(planPath, s)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), planNotReady) || strings.Contains(w.Body.String(), "charter") {
		t.Errorf("not ready: %d\n%s", w.Code, w.Body)
	}
	// The open thread's round was read to tell it from a finished one.
	if c := g.seen(); len(c) != 2 || c[1].Verb != GenesisVerbRound {
		t.Errorf("not ready made %+v", c)
	}
	fin := sampleSummary()
	fin.State, fin.Digest, fin.PlanReady = GenesisStateFinished, strings.Repeat("7d", 32), false
	g.set(GenesisVerbRound, &fin, nil)
	w = r.getWith(planPath, s)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), planFinishedNone) || !strings.Contains(w.Body.String(), "Back to the project&#39;s brief") {
		t.Errorf("finished early: %d\n%s", w.Code, w.Body)
	}
	// A round that fails says the plain words.
	g.set(GenesisVerbRound, nil, ErrBusy)
	if w = r.getWith(planPath, s); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), planNotReady) {
		t.Errorf("not ready, round busy: %d", w.Code)
	}
	g.set(GenesisVerbPlan, nil, ErrGenesisNoTemplate)
	w = r.getWith(planPath, s)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), planNoTemplate) {
		t.Errorf("no template: %d\n%s", w.Code, w.Body)
	}
	for _, body := range []string{w.Body.String()} {
		for _, f := range inspectPage(body, []string{"a#project-page", "a#project-list"}, false) {
			t.Errorf("the page with no plan: %s", f)
		}
	}
}

// The errors of a read are the project pages' problems; a plan of another
// thread, or an answer that is no plan, is "something went wrong".
func TestGenesisPlanPageProblems(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		words  string
	}{
		{ErrGenesisUnknownThread, 404, "No such project"},
		{ErrBusy, 503, "The desk is busy"},
		{ErrDaemonUnavailable, 503, "The desk cannot be reached"},
		{ErrNotOffered, 503, "New projects are not offered"},
		{errors.New("MARKER"), 502, "Something went wrong"},
	} {
		r, g, s := genesisRig(t)
		g.set(GenesisVerbPlan, nil, c.err)
		w := r.getWith(planPath, s)
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.words) || strings.Contains(w.Body.String(), "MARKER") {
			t.Errorf("%v: %d", c.err, w.Code)
		}
	}
	other := samplePlan()
	for name, v := range map[string]GenesisView{
		"another thread": {State: GenesisStatePlan, Thread: "fedcba9876543210", Plan: &other},
		"no plan":        {State: GenesisStatePlan, Thread: testThread},
		"a summary":      sampleSummary(),
		"no milestone":   {State: GenesisStatePlan, Thread: testThread, Plan: &GenesisPlanView{Revision: 1, Digest: strings.Repeat("ab", 32)}},
	} {
		r, g, s := genesisRig(t)
		g.set(GenesisVerbPlan, &v, nil)
		if w := r.getWith(planPath, s); w.Code != http.StatusBadGateway {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

// A thread id not of its form is never asked of the port; the page needs a
// session; it is bounded with the other project pages; and it is not
// offered without the interview.
func TestGenesisPlanPageIsGuarded(t *testing.T) {
	r, g, s := genesisRig(t)
	for _, id := range []string{"0123456789ABCDEF", "0123456789abcde", "%2e%2e", "new", "ap-20261122-0001"} {
		if w := r.getWith("/projects/"+id+"/plan", s); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", id, w.Code)
		}
	}
	seeOtherTo(t, "no session", r.getWith(planPath), "/login/")
	if c := g.seen(); len(c) != 0 {
		t.Fatalf("the port was called: %+v", c)
	}
	// No post: the route is a read only.
	if w := r.postForm(planPath, nil, s); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("a post to the plan page: %d", w.Code)
	}
	// The limit of the project pages, shared with the other pages.
	for i := 0; i < genesisRequests-1; i++ {
		if w := r.getWith("/projects/"+testThread, s); w.Code != 200 {
			t.Fatalf("page %d: %d", i, w.Code)
		}
	}
	if w := r.getWith(planPath, s); w.Code != http.StatusOK {
		t.Fatalf("the last page within the limit: %d", w.Code)
	}
	before := len(g.seen())
	if w := r.getWith(planPath, s); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a plan page past the limit: %d", w.Code)
	}
	if len(g.seen()) != before {
		t.Error("a page past the limit called the port")
	}
	// Not offered, or no port: the "not offered" page.
	g2 := &fakeGenesis{}
	g2.set(GenesisVerbPlan, nil, ErrNotOffered)
	for name, rr := range map[string]*rig{"no port": newRig(t), "not offered": newRig(t, withGenesis(g2))} {
		ss := rr.login(t)
		if w := rr.getWith(planPath, ss); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "New projects are not offered") {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

// finishNoPlanLine is the warning of the summary page above the finish
// button when the answers are not complete.
const finishNoPlanLine = "If you finish now, no plan is drawn for this project, and a finished brief cannot be changed."

// finishAcceptsLine is the summary's sentence, in the finish paragraph, when
// some answers were decided for the owner.
const finishAcceptsLine = "Finishing also accepts the answers decided for you."

// The summary's finish paragraph says that finishing accepts the answers
// decided for the owner when the summary has such rows, and only then.
func TestGenesisSummarySaysFinishingAccepts(t *testing.T) {
	r, g, s := genesisRig(t)
	sum := sampleSummary()
	g.set(GenesisVerbRound, &sum, nil)
	page := r.getWith("/projects/"+testThread, s).Body.String()
	form := page[strings.Index(page, `<form id="finish"`):]
	form = form[:strings.Index(form, "</form>")]
	if !strings.Contains(form, "<p>Finishing keeps the answers as the project's brief. "+finishAcceptsLine+" Nothing starts, and no agent is given the brief.</p>") ||
		strings.Count(page, finishAcceptsLine) != 1 || strings.Index(page, "What was decided for you") > strings.Index(page, finishAcceptsLine) {
		t.Fatalf("the summary with answers decided for the owner:\n%s", page)
	}
	sum.DecidedForYou = []GenesisRow{}
	g.set(GenesisVerbRound, &sum, nil)
	page = r.getWith("/projects/"+testThread, s).Body.String()
	if strings.Contains(page, finishAcceptsLine) || !strings.Contains(page, "<p>Finishing keeps the answers as the project's brief. Nothing starts, and no agent is given the brief.</p>") {
		t.Fatalf("the summary with no answer decided for the owner:\n%s", page)
	}
}

// The summary links the draft plan when the port says it is ready, and
// otherwise says why there is none; the finished page links the plan kept
// with the brief, and says what is true now.
func TestGenesisPlanLinks(t *testing.T) {
	r, g, s := genesisRig(t)
	sum := sampleSummary()
	g.set(GenesisVerbRound, &sum, nil)
	page := r.getWith("/projects/"+testThread, s).Body.String()
	if !strings.Contains(page, `<a id="see-plan" href="/projects/`+testThread+`/plan">See the draft plan</a>`) {
		t.Errorf("the summary lacks the plan's link:\n%s", page)
	}
	sum.PlanReady, sum.Filled = false, 9
	g.set(GenesisVerbRound, &sum, nil)
	page = r.getWith("/projects/"+testThread, s).Body.String()
	if strings.Contains(page, "see-plan") || !strings.Contains(page, "9 of 14 answers are filled in: no plan can be drawn yet.") {
		t.Errorf("the summary of a brief not complete:\n%s", page)
	}
	// Finishing with answers missing is warned of, above the button, in
	// the form; with a plan ready, or with all answers in and no template,
	// there is no such line.
	if !strings.Contains(page, finishNoPlanLine) || strings.Index(page, finishNoPlanLine) > strings.Index(page, `id="finish-go"`) {
		t.Errorf("the summary of a brief not complete does not warn above the finish button:\n%s", page)
	}
	sum.Filled = 14
	g.set(GenesisVerbRound, &sum, nil)
	page = r.getWith("/projects/"+testThread, s).Body.String()
	if strings.Contains(page, "see-plan") || !strings.Contains(page, planNoTemplate) {
		t.Errorf("the summary of a kind with no template:\n%s", page)
	}
	if strings.Contains(page, finishNoPlanLine) {
		t.Error("the summary of a kind with no template warns of no plan beside its own sentence")
	}
	sum.PlanReady = true
	g.set(GenesisVerbRound, &sum, nil)
	if page = r.getWith("/projects/"+testThread, s).Body.String(); strings.Contains(page, finishNoPlanLine) {
		t.Error("the summary with a plan ready warns of no plan")
	}
	sum.PlanReady = false
	fin := sampleSummary()
	fin.State, fin.Digest = GenesisStateFinished, strings.Repeat("7d", 32)
	g.set(GenesisVerbRound, &fin, nil)
	page = r.getWith("/projects/"+testThread, s).Body.String()
	for _, want := range []string{`<a id="see-plan" href="/projects/` + testThread + `/plan">See the plan</a>`,
		"Handing a plan to a project is a later step.",
		"Nothing has started, and no agent was given the brief yet."} {
		if !strings.Contains(page, want) {
			t.Errorf("the finished page lacks %q", want)
		}
	}
	if strings.Contains(page, "Planning the project is a later step") {
		t.Error("the finished page still says planning is a later step")
	}
	// The page does not say the plan is kept: the link shows what the desk
	// has, and the plan page says what it is.
	if strings.Contains(page, "kept with") || strings.Contains(page, "is drawn from the brief") {
		t.Errorf("the finished page claims the plan is kept:\n%s", page)
	}
	fin.PlanReady = false
	g.set(GenesisVerbRound, &fin, nil)
	page = r.getWith("/projects/"+testThread, s).Body.String()
	if strings.Contains(page, "see-plan") || !strings.Contains(page, "No plan was drawn") {
		t.Errorf("the finished page of a brief with no plan:\n%s", page)
	}
}

// Every word list of the page has words for every item of the app's closed
// lists.
func TestGenesisPlanWordsCoverTheClosedLists(t *testing.T) {
	for _, c := range []struct {
		name  string
		words map[string]string
		items []string
	}{
		{"review points", planReviewWords, GenesisPlanReviewPoints},
		{"hosts", planHostWords, GenesisPlanHosts},
		{"budget bands", planBudgetWords, GenesisPlanBudgetBands},
		{"kinds", planKindWords, GenesisPlanKinds},
		{"sizes", planSizeWords, GenesisPlanSizes},
		{"warnings", planWarningWords, GenesisPlanWarnings},
	} {
		if len(c.words) != len(c.items) {
			t.Errorf("%s: %d words for %d items", c.name, len(c.words), len(c.items))
		}
		for _, it := range c.items {
			if c.words[it] == "" {
				t.Errorf("%s: no words for %q", c.name, it)
			}
		}
	}
}
