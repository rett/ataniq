package seatapp

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// The plan page of the guided new project: GET /projects/{thread}/plan, a
// page made on the server with no script, no form and no control but
// links. It shows the thread's project plan as the [Genesis] port answers
// the verb plan: a draft drawn from the answers of an open thread, or the
// plan the desk kept when the interview was finished. A plan is a proposal:
// it binds nothing, grants nothing and starts nothing, and the page says so
// in fixed words, and says what always waits for the owner whatever the
// plan holds. Its last section, what a charter would cover, is a draft of
// words only: nothing is signed or allowed from it, and it is never in the
// card list, a card or a notice.

// The fixed sentences of the plan page. A test pins each.
const (
	planBindsNothing = "A plan binds nothing: nothing has started, and nothing starts from this page."
	planDraftLine    = "This is a draft; finishing the interview keeps it as revision 1."
	planAlwaysWaits  = "Publishing, releases, pushing code, credentials and raising any limit always wait for you, whatever the plan says."
	planCharterLine  = "Draft. Not signed. It allows nothing. Nothing starts from this page. Signing is a later step and is not built."
	planNotReady     = "No plan can be drawn yet: some answers are missing."
	planNoTemplate   = "There is no plan template for this kind of project yet."
	planFinishedNone = "This project was finished before its answers were complete, so no plan was drawn."
	planUnknownNote  = "A note from the plan."
	// planInferredLine is the words of the warning inferred-answers on a
	// draft plan: the plan was drawn from answers decided for the owner, who
	// can still change them from the summary.
	planInferredLine = "Some answers were decided for you; you can change them from the summary."
	// planInferredStoredLine is the same warning on a stored plan: the brief
	// is finished and cannot be changed, so it says finishing accepted them.
	planInferredStoredLine = "Some answers were decided for you, and finishing accepted them."
	planNoteHeading        = "A note"
	planNotesMarked        = "your words, shown as you wrote them; never given to a tool"
	backToBrief            = "Back to the project's brief"
	backToProject          = "Back to the project"
)

// The words of the plan's closed lists and of the words it names. A word
// the app has none for is shown as it is, through Text, but for a warning,
// which is shown as planUnknownNote and never as its id.
var (
	planReviewWords = map[string]string{
		"plan":      "Before work starts: you see the plan first",
		"demo":      "At the first demo",
		"milestone": "At the end of each milestone",
		"publish":   "Before anything goes public",
	}
	planHostWords = map[string]string{
		"own_server":    "On your own server",
		"this_computer": "On this computer",
		"file_host":     "On a file or code host",
	}
	planBudgetWords = map[string]string{
		"none":   "Nothing extra each month",
		"low":    "A little: up to about 10 dollars a month",
		"medium": "Some: up to about 50 dollars a month",
	}
	planKindWords = map[string]string{
		"build":  "building",
		"write":  "writing",
		"review": "a review",
		"test":   "testing",
	}
	planSizeWords = map[string]string{
		"small":  "a small task",
		"medium": "a medium task",
		"large":  "a large task",
	}
	planWarningWords = map[string]string{
		"estimate-may-exceed-band": "The work is near the top of its estimate, and may take longer.",
		"inferred-answers":         planInferredLine,
		"rights-not-cleared":       "Some rights are not cleared yet: they are cleared with you before anything uses them.",
		"settle-at-desk":           "Some answers are in your own words: they are settled with you at the desk.",
	}
	planCheckWords = map[string]string{
		"citation":           "citations",
		"duration":           "duration",
		"generated-labelled": "generated items labelled",
		"layout":             "layout",
		"link-check":         "links",
		"lint":               "lint",
		"loudness":           "loudness",
		"rights-manifest":    "rights manifest",
		"spelling":           "spelling",
		"unit-tests":         "unit tests",
	}
	planAreaWords = map[string]string{
		"repo:build":       "the build files",
		"repo:deploy":      "the deployment set-up",
		"repo:docs":        "the documentation",
		"repo:src":         "the source code",
		"repo:tests":       "the tests",
		"store:assets":     "the assets",
		"store:layout":     "the layouts",
		"store:manuscript": "the manuscript",
		"store:site":       "the site",
		"store:sources":    "the sources and their licences",
		"store:style":      "the style notes",
	}
)

// planNotesField is the field of a note of the brief's own, which answers no
// question.
const planNotesField = "notes"

// Heading is the words a plan page puts before a note's quoted text, to say
// which answer it belongs to: the catalog question's words that the link
// gave for the note's field, or "A note" for a note of the brief's own, a
// field the link has no question for and a plan of another catalog.
func (n GenesisNote) Heading() string {
	if n.Field == planNotesField || n.Label == "" {
		return planNoteHeading
	}
	return n.Label
}

// wordOf is the words of an item, or the item itself when there are none.
func wordOf(words map[string]string, item string) string {
	if w, ok := words[item]; ok {
		return w
	}
	return item
}

// wordsOf are the words of a list's items, in order.
func wordsOf(words map[string]string, items []string) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, wordOf(words, it))
	}
	return out
}

// The data of the plan page.
type (
	planData struct {
		Title string
		// Heading is the page's heading: "Draft plan" or "Plan, revision
		// N", or the heading of a page with no plan.
		Heading string
		// Thread is the thread's id, and Tag its first six digits.
		Thread, Tag string
		// Missing is the fixed sentence of a page with no plan.
		Missing string
		// Back is the words of the link back to the thread's page: its
		// summary, its finished brief, or the page where it stands.
		Back string
		Plan *GenesisPlanView
		// The plan in words: where it waits for the owner, the outside
		// services, where the files live, the budget band, the estimate,
		// the warnings, the checks of every milestone, the milestones and
		// the areas of work.
		Waits, Services        []string
		Hosts, Budget          string
		Estimate               string
		Warnings, Gates, Areas []string
		Milestones             []milestoneView
		Nav                    *navView
	}
	milestoneView struct {
		Open        bool
		Title, Goal string
		Count       string
		Tasks       []taskView
	}
	taskView struct {
		Title, Kind, Size string
		// After are the titles of the tasks it waits for.
		After []string
	}
)

// planOf is the page's data for a plan.
func planOf(thread string, p *GenesisPlanView) planData {
	d := planData{Title: pageTitles["plan"], Thread: thread, Tag: thread[:6], Plan: p, Back: "Back to the summary",
		Waits: wordsOf(planReviewWords, p.ReviewPoints), Services: append([]string{}, p.ExternalServices...),
		Hosts: wordOf(planHostWords, p.Hosts), Budget: wordOf(planBudgetWords, p.BudgetBand),
		Gates: wordsOf(planCheckWords, p.Gates), Warnings: []string{}, Areas: []string{}, Nav: projectsNav()}
	d.Heading = "Draft plan"
	if p.Stored {
		d.Heading = "Plan, revision " + strconv.Itoa(p.Revision)
		d.Back = backToBrief
	}
	d.Estimate = p.Estimate
	if w, ok := GenesisPlanEstimates[p.Estimate]; ok {
		d.Estimate = w
	}
	for _, w := range p.Warnings {
		if s, ok := planWarningWords[w]; ok {
			if w == "inferred-answers" && p.Stored {
				s = planInferredStoredLine
			}
			d.Warnings = append(d.Warnings, s)
		} else {
			d.Warnings = append(d.Warnings, planUnknownNote)
		}
	}
	titles := map[string]string{}
	seenArea := map[string]bool{}
	for i, m := range p.Milestones {
		mv := milestoneView{Open: i == 0, Title: m.Title, Goal: m.Goal, Count: "1 task"}
		if len(m.Tasks) != 1 {
			mv.Count = strconv.Itoa(len(m.Tasks)) + " tasks"
		}
		for _, t := range m.Tasks {
			tv := taskView{Title: t.Title, Kind: wordOf(planKindWords, t.Kind), Size: wordOf(planSizeWords, t.Size), After: []string{}}
			for _, dep := range t.DependsOn {
				if title, ok := titles[dep]; ok {
					tv.After = append(tv.After, title)
				} else {
					tv.After = append(tv.After, "an earlier task")
				}
			}
			titles[t.ID] = t.Title
			for _, a := range t.Scope {
				if !seenArea[a] {
					seenArea[a] = true
					d.Areas = append(d.Areas, wordOf(planAreaWords, a))
				}
			}
			mv.Tasks = append(mv.Tasks, tv)
		}
		d.Milestones = append(d.Milestones, mv)
	}
	return d
}

// planPage answers GET /projects/{thread}/plan: the thread's plan, or a
// page that says in fixed words why there is none. One call of the port
// reads it; only when the port says the answers are not complete is the
// thread's round read too, to tell an open thread from one finished early.
func (a *app) planPage(w http.ResponseWriter, r *http.Request, s Session) {
	thread := r.PathValue("thread")
	if !ValidThreadID(thread) {
		a.problem(w, problemNotFound)
		return
	}
	g, ctx, cancel, err := a.genesisCall(r, s)
	if err != nil {
		if errors.Is(err, errTooMany) {
			a.problem(w, problemTooManyProjects)
		} else {
			a.problem(w, problemGenesisNotOffered)
		}
		return
	}
	defer cancel()
	v, err := g.Genesis(ctx, GenesisCall{Verb: GenesisVerbPlan, Thread: thread})
	switch {
	case errors.Is(err, ErrGenesisNotReady):
		d := planData{Title: pageTitles["plan"], Heading: "No plan yet", Thread: thread, Tag: thread[:6], Missing: planNotReady, Back: backToProject, Nav: projectsNav()}
		if rv, rerr := g.Genesis(ctx, GenesisCall{Verb: GenesisVerbRound, Thread: thread}); rerr == nil && rv.Thread == thread && rv.State == GenesisStateFinished {
			d.Heading, d.Missing, d.Back = "No plan", planFinishedNone, backToBrief
		}
		a.render(w, "plan", http.StatusConflict, d)
		return
	case errors.Is(err, ErrGenesisNoTemplate):
		a.render(w, "plan", http.StatusConflict, planData{Title: pageTitles["plan"], Heading: "No plan yet", Thread: thread, Tag: thread[:6], Missing: planNoTemplate, Back: backToProject, Nav: projectsNav()})
		return
	case err != nil:
		a.problem(w, genesisProblem(err))
		return
	}
	if v.Thread != thread || v.State != GenesisStatePlan || v.Plan == nil || !validPlan(v.Plan) {
		a.problem(w, problemFailed)
		return
	}
	a.render(w, "plan", http.StatusOK, planOf(thread, v.Plan))
}

// validPlan reports whether a plan is within the app's own bounds: the
// counts the page is made for. The texts are shown through Text, which cuts
// any that is longer than a page shows.
func validPlan(p *GenesisPlanView) bool {
	if len(p.Milestones) < 1 || len(p.Milestones) > MaxGenesisPlanMilestones || len(p.Notes) > MaxGenesisPlanNotes ||
		p.Revision < 1 || p.Revision > MaxGenesisPlanRevision || len(p.Digest) != 64 || strings.Trim(p.Digest, "0123456789abcdef") != "" {
		return false
	}
	tasks := 0
	for _, m := range p.Milestones {
		if len(m.Tasks) < 1 || len(m.Tasks) > MaxGenesisPlanMilestoneTasks {
			return false
		}
		tasks += len(m.Tasks)
	}
	return tasks <= MaxGenesisPlanTasks
}
