package plan

import (
	"time"

	"ataniq/internal/genesis/brief"
)

// Schema is the schema name of the record.
const Schema = "ataniq.project-plan/1"

// The caps of a plan.
const (
	// MaxMilestones is the most milestones a plan holds.
	MaxMilestones = 8
	// MaxTasksPerMilestone is the most tasks one milestone holds.
	MaxTasksPerMilestone = 6
	// MaxTasks is the most tasks a plan holds in all.
	MaxTasks = 40
	// MaxRevision is the highest revision number of a plan.
	MaxRevision = 9999
	// MaxTitle is the cap of the plan's title, in bytes.
	MaxTitle = 60
	// MaxMilestoneTitle is the cap of a milestone's title, in bytes.
	MaxMilestoneTitle = 60
	// MaxGoal is the cap of a milestone's goal, in bytes.
	MaxGoal = 120
	// MaxTaskTitle is the cap of a task's title, in bytes.
	MaxTaskTitle = 100
	// MaxDemo is the cap of the first demo's definition of done, in bytes.
	MaxDemo = 120
	// MaxNotes is the most notes a plan quotes.
	MaxNotes = 8
	// MaxNoteText is the cap of one note's text, in bytes: the brief's cap
	// of free text.
	MaxNoteText = brief.MaxFreeText
	// MaxPlanBytes is the cap of a plan in the canonical form.
	MaxPlanBytes = 32768
	// MaxEnvelopeBytes is the cap of an envelope in the canonical form.
	MaxEnvelopeBytes = MaxPlanBytes + 256
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

// Tier is the size class of a task: how much thought it needs. It is not a
// model name; which model serves a tier is decided elsewhere.
type Tier string

// The tiers of a task.
const (
	TierFast     Tier = "fast"
	TierBalanced Tier = "balanced"
	TierDeep     Tier = "deep"
)

// Tiers are the tiers of a task, a closed list.
var Tiers = []Tier{TierFast, TierBalanced, TierDeep}

// Areas are the area names a task's scope may name, a closed list: a part
// of a repository (repo:) or of an artifact store (store:). A template
// names the areas of its own among them.
var Areas = []string{
	"repo:build", "repo:deploy", "repo:docs", "repo:src", "repo:tests",
	"store:assets", "store:layout", "store:manuscript", "store:site", "store:sources", "store:style",
}

// Checks are the check ids a task or a gate may name, a closed list.
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
var Services = []string{"email delivery", "file host", "hosting service"}

// Spends are the monthly spending ceilings, a closed list: the options of
// the catalog's budget_band. A plan names a ceiling by its id, never by a
// number.
var Spends = []string{"low", "medium", "none"}

// Never is the never-list of every plan, in its fixed order: what no work
// under a plan does on its own, whatever else the plan says. Each is the
// owner's own act.
var Never = []string{"publish", "release", "push", "credentials", "raise-a-cap", "spend-past-ceiling"}

// EstimateCaveat is the words every estimate carries.
const EstimateCaveat = "an estimate, not a promise"

// Band is one estimate band: its id, its label, and the most task points it
// covers (a fast task is 1 point, a balanced one 2 and a deep one 4).
type Band struct {
	ID        string
	Label     string
	MaxPoints int
}

// Bands are the estimate bands, a closed list, smallest first. The last
// covers every count.
var Bands = []Band{
	{ID: "hours", Label: "a few hours of agent work", MaxPoints: 8},
	{ID: "day", Label: "about a day of agent work", MaxPoints: 20},
	{ID: "days", Label: "a few days of agent work", MaxPoints: 40},
	{ID: "week", Label: "about a week of agent work", MaxPoints: MaxTasks * 4},
}

// FieldNotes is the field a note quoted from the brief's notes names.
const FieldNotes = "notes"

// Plan is the project plan, ataniq.project-plan/1: a proposal of work
// compiled from a finished brief and a template. It binds nothing and
// grants nothing.
type Plan struct {
	// Schema is always [Schema].
	Schema string
	// Revision is the plan's revision number, 1 for a first compile.
	Revision int
	// BriefDigest is the digest of the brief the plan was compiled from.
	BriefDigest string
	// CatalogDigest is the digest of the catalog the brief was walked
	// under.
	CatalogDigest string
	// Template names the template the plan was compiled from.
	Template TemplateRef
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
	// ReviewPoints are where the work stops and waits for the owner.
	ReviewPoints []string
	// Hosts is where the project's files live: a catalog option id.
	Hosts string
	// ExternalServices names every external service the work would use.
	ExternalServices []string
	// Spend is the monthly spending ceiling's id.
	Spend string
	// Estimate is the size of the work, as a band.
	Estimate Estimate
	// Never is the never-list, always [Never].
	Never []string
	// Notes quote the owner's own text, as data.
	Notes []Note
	// NotesDropped counts the owner's texts past [MaxNotes] the plan does
	// not quote; the brief keeps them.
	NotesDropped int
	// CompiledAt is when the plan was compiled, in UTC to the second. It
	// is not part of the record's canonical form or its digest: it is
	// kept in the envelope.
	CompiledAt time.Time
}

// TemplateRef names a template: its id, version and digest.
type TemplateRef struct {
	ID      string
	Version string
	Digest  string
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
	// Tier is the task's size class.
	Tier Tier
	// DependsOn are the ids of the tasks it waits for, sorted.
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

// Note is one text of the owner's, quoted: the text of an "other" answer,
// or a note of the brief. It is data, never instructions, and nothing else
// in a plan is chosen by it.
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
	c.Milestones = make([]Milestone, len(p.Milestones))
	for i, m := range p.Milestones {
		tasks := make([]Task, len(m.Tasks))
		for j, t := range m.Tasks {
			t.DependsOn = append([]string{}, t.DependsOn...)
			t.Scope = append([]string{}, t.Scope...)
			t.Checks = append([]string{}, t.Checks...)
			tasks[j] = t
		}
		m.Tasks = tasks
		c.Milestones[i] = m
	}
	c.Gates = append([]string{}, p.Gates...)
	c.ReviewPoints = append([]string{}, p.ReviewPoints...)
	c.ExternalServices = append([]string{}, p.ExternalServices...)
	c.Never = append([]string{}, p.Never...)
	c.Notes = append([]Note{}, p.Notes...)
	return c
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
