package plan

import (
	"ataniq/internal/genesis/brief"
)

// Schema is the schema name of the record.
const Schema = "ataniq.project-plan/1"

// The caps of a plan.
const (
	// MaxMilestones is the most milestones a plan holds.
	MaxMilestones = 4
	// MaxTasksPerMilestone is the most tasks one milestone holds.
	MaxTasksPerMilestone = 6
	// MaxTasks is the most tasks a plan holds in all.
	MaxTasks = 12
	// MaxTemplateTasks is the most tasks a template holds, those whose
	// condition does not hold for a brief included. It bounds the task ids
	// (t1 to t24).
	MaxTemplateTasks = 24
	// MaxRevision is the highest revision number of a plan.
	MaxRevision = 9999
	// MaxTitle is the cap of the plan's title, in bytes: a catalog label.
	MaxTitle = 60
	// MaxMilestoneTitle is the cap of a milestone's title, in bytes.
	MaxMilestoneTitle = 60
	// MaxGoal is the cap of a milestone's goal, in bytes.
	MaxGoal = 120
	// MaxTaskTitle is the cap of a task's title, in bytes.
	MaxTaskTitle = 100
	// MaxDemo is the cap of the first demo's definition of done, in bytes.
	MaxDemo = 120
	// MaxNotes is the most notes a plan holds.
	MaxNotes = 8
	// MaxNoteText is the cap of one note's text, in bytes: the brief's cap
	// of free text.
	MaxNoteText = brief.MaxFreeText
	// MaxPlanBytes is the cap of a plan in the canonical form, below the
	// 16 KiB of a result on the wire.
	MaxPlanBytes = 12288
)

// Kind is the kind of work of a task.
type Kind string

// The kinds of a task.
const (
	KindBuild  Kind = "build"
	KindWrite  Kind = "write"
	KindReview Kind = "review"
	KindTest   Kind = "test"
)

// Kinds are the kinds of a task, a closed list.
var Kinds = []Kind{KindBuild, KindWrite, KindReview, KindTest}

// Size is the size class of a task: how much work it is. It chooses no
// model; which model serves a task is decided elsewhere.
type Size string

// The sizes of a task.
const (
	SizeSmall  Size = "small"
	SizeMedium Size = "medium"
	SizeLarge  Size = "large"
)

// Sizes are the sizes of a task, a closed list, with the points each
// counts towards the estimate.
var Sizes = map[Size]int{SizeSmall: 1, SizeMedium: 2, SizeLarge: 4}

// Areas are the area names a template's tasks may name in their scope: a
// part of a repository (repo:) or of an artifact store (store:). A plan is
// held to the grammar of an area name only, so that a plan of an older
// template still reads.
var Areas = []string{
	"repo:build", "repo:deploy", "repo:docs", "repo:src", "repo:tests",
	"store:assets", "store:layout", "store:manuscript", "store:site", "store:sources", "store:style",
}

// Checks are the check ids a template's tasks and gates may name. A plan
// is held to the grammar of a check id only.
var Checks = []string{
	"citation", "duration", "generated-labelled", "layout", "link-check",
	"lint", "loudness", "rights-manifest", "spelling", "unit-tests",
}

// ReviewPoints are the points where the work stops and waits for the
// owner, a closed list: the options of the catalog's review_points.
var ReviewPoints = []string{"demo", "milestone", "plan", "publish"}

// Hosts are the places the project's files may live, a closed list: the
// options of the catalog's hosts.
var Hosts = []string{"file_host", "own_server", "this_computer"}

// Services are the names of the external services a plan may name, a
// closed list. A plan names every external service its work would use.
var Services = []string{"email delivery", "file or code host", "generator service", "hosting service"}

// BudgetBands are the monthly budget bands, a closed list: the options of
// the catalog's budget_band. A plan names a band by its id, never by a
// number.
var BudgetBands = []string{"low", "medium", "none"}

// Enforcement says how the budget band is held: it is advisory. A plan
// enforces nothing; spending is held elsewhere.
const Enforcement = "advisory"

// The warnings a plan may carry, a closed list.
const (
	// WarnEstimate says the work is near the top of its estimate band and
	// may run past it.
	WarnEstimate = "estimate-may-exceed-band"
	// WarnInferred says some answers were decided for the owner (a "you
	// decide" answer the host inferred): the plan was drawn from them as
	// they are, whether or not he has confirmed them yet.
	WarnInferred = "inferred-answers"
	// WarnRights says some rights are not cleared yet.
	WarnRights = "rights-not-cleared"
	// WarnDesk says some answers are the owner's own words, to be settled
	// at the desk.
	WarnDesk = "settle-at-desk"
)

// Warnings are the warnings a plan may carry, sorted.
var Warnings = []string{WarnEstimate, WarnInferred, WarnRights, WarnDesk}

// EstimateCaveat is the words every estimate carries.
const EstimateCaveat = "an estimate, not a promise"

// Band is one estimate band: its id, its label, and the most task points it
// covers (see [Sizes]).
type Band struct {
	ID        string
	Label     string
	MaxPoints int
}

// Bands are the estimate bands, a closed list, smallest first. The last
// covers every plan.
var Bands = []Band{
	{ID: "hours", Label: "a few hours of agent work", MaxPoints: 6},
	{ID: "day", Label: "about a day of agent work", MaxPoints: 14},
	{ID: "days", Label: "a few days of agent work", MaxPoints: 28},
	{ID: "week", Label: "about a week of agent work", MaxPoints: MaxTasks * 4},
}

// FieldNotes is the field of a note taken from the brief's notes.
const FieldNotes = "notes"

// Plan is the project plan, ataniq.project-plan/1: a proposal of work
// compiled from a finished brief and a template. It binds nothing and
// grants nothing. It holds no time: when it was made is the store's.
type Plan struct {
	// Schema is always [Schema].
	Schema string
	// Revision is the plan's revision number, 1 for a first compile.
	Revision int
	// PrevDigest is the digest of the revision before, and empty for
	// revision 1.
	PrevDigest string
	// BriefDigest is the digest of the brief the plan was compiled from.
	BriefDigest string
	// CatalogDigest is the digest of the catalog the brief was walked
	// under.
	CatalogDigest string
	// Template names the template the plan was compiled from.
	Template TemplateRef
	// TemplateDigest is the digest of the template's data.
	TemplateDigest string
	// Title is the plan's title: a label of the catalog, never the
	// owner's text.
	Title string
	// Milestones are the milestones, by id.
	Milestones []Milestone
	// Gates are the checks required at the end of every milestone.
	Gates []string
	// FirstDemo is the first demo's definition of done, one sentence of
	// the template's.
	FirstDemo string
	// ReviewPoints are where the work stops and waits for the owner,
	// exactly as the owner chose them.
	ReviewPoints []string
	// Hosts is where the project's files live: a catalog option id.
	Hosts string
	// ExternalServices names every external service the work would use.
	ExternalServices []string
	// BudgetBand is the monthly budget band's id; it is advisory.
	BudgetBand string
	// Estimate is the size of the work, as a band.
	Estimate Estimate
	// Warnings are the plan's warnings, sorted.
	Warnings []string
	// Notes are the owner's own texts, for the owner's eyes only.
	Notes []Note
	// NotesDropped counts the owner's texts past [MaxNotes] the plan does
	// not hold; the brief keeps them.
	NotesDropped int
}

// TemplateRef names a template: its id and version.
type TemplateRef struct {
	ID      string
	Version string
}

// Milestone is one milestone: a step with a goal and its tasks.
type Milestone struct {
	// ID is m1, m2 and so on: the milestone's place in the template, so
	// that a milestone keeps its id across revisions.
	ID string
	// Title is the milestone's title, the template's.
	Title string
	// Goal says what is true at its end, the template's.
	Goal string
	// Tasks are its tasks, by id.
	Tasks []Task
}

// Task is one task of a milestone.
type Task struct {
	// ID is t1, t2 and so on: the task's place in the template, so that a
	// task keeps its id across revisions.
	ID string
	// Title is the task's title, the template's.
	Title string
	// Kind is the kind of work.
	Kind Kind
	// Size is the task's size class.
	Size Size
	// DependsOn are the ids of the tasks it waits for, in order.
	DependsOn []string
	// Scope are the areas it may change, sorted.
	Scope []string
	// Checks are the checks its result passes, sorted.
	Checks []string
}

// Estimate is the size of the work: a band, always marked an estimate.
type Estimate struct {
	// Band is the band's id.
	Band string
	// Label is the band's label.
	Label string
}

// Note is one text of the owner's: the text of an "other" answer, or a
// note of the brief. It is for the owner's eyes only: it is never served
// to an implementation agent ([Plan.ForAgent] leaves it out), and nothing
// else in a plan is chosen by it.
type Note struct {
	// Field is the brief field the text was given for, or "notes".
	Field string
	// Text is the owner's text, at most [MaxNoteText] bytes.
	Text brief.Untrusted
}

// Tasks returns every task of the plan, in order.
func (p Plan) Tasks() []Task {
	var out []Task
	for _, m := range p.Milestones {
		out = append(out, m.Tasks...)
	}
	return out
}

// Clone returns a deep copy of p.
func (p Plan) Clone() Plan {
	c := p
	c.Milestones = cloneMilestones(p.Milestones)
	c.Gates = append([]string{}, p.Gates...)
	c.ReviewPoints = append([]string{}, p.ReviewPoints...)
	c.ExternalServices = append([]string{}, p.ExternalServices...)
	c.Warnings = append([]string{}, p.Warnings...)
	c.Notes = append([]Note{}, p.Notes...)
	return c
}

func cloneMilestones(ms []Milestone) []Milestone {
	out := make([]Milestone, len(ms))
	for i, m := range ms {
		tasks := make([]Task, len(m.Tasks))
		for j, t := range m.Tasks {
			t.DependsOn = append([]string{}, t.DependsOn...)
			t.Scope = append([]string{}, t.Scope...)
			t.Checks = append([]string{}, t.Checks...)
			tasks[j] = t
		}
		m.Tasks = tasks
		out[i] = m
	}
	return out
}

// AgentPlan is what of a plan may be served to an implementation agent:
// the plan without the owner's notes, and without the digests. For a plan
// that [Compile] made, every string in it is a constant of a template, of
// the catalog or of this package; none is the owner's text. A plan that
// [Decode] read is held to its grammar only, so a plan from a store must be
// re-checked against its template before it is handed to an agent.
type AgentPlan struct {
	Revision         int
	Title            string
	Template         TemplateRef
	Milestones       []Milestone
	Gates            []string
	FirstDemo        string
	ReviewPoints     []string
	Hosts            string
	ExternalServices []string
	BudgetBand       string
	Enforcement      string
	Estimate         Estimate
	EstimateCaveat   string
	Warnings         []string
}

// ForAgent returns the projection of the plan an implementation agent may
// be given. It holds no note: the owner's own words are never served to an
// agent. That every other string is a constant is true of a plan Compile
// made; a decoded plan is held to grammar only (see [AgentPlan]).
func (p Plan) ForAgent() AgentPlan {
	return AgentPlan{
		Revision:         p.Revision,
		Title:            p.Title,
		Template:         p.Template,
		Milestones:       cloneMilestones(p.Milestones),
		Gates:            append([]string{}, p.Gates...),
		FirstDemo:        p.FirstDemo,
		ReviewPoints:     append([]string{}, p.ReviewPoints...),
		Hosts:            p.Hosts,
		ExternalServices: append([]string{}, p.ExternalServices...),
		BudgetBand:       p.BudgetBand,
		Enforcement:      Enforcement,
		Estimate:         p.Estimate,
		EstimateCaveat:   EstimateCaveat,
		Warnings:         append([]string{}, p.Warnings...),
	}
}

func fail(c brief.Code, detail string) *brief.Error { return &brief.Error{Code: c, Detail: detail} }

func contains[T comparable](list []T, x T) bool {
	for _, y := range list {
		if y == x {
			return true
		}
	}
	return false
}
