package ownerwire

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"ataniq/internal/approval/record"
)

// This file holds the plan as the result of seat.genesis carries it: the
// verb plan answers the state plan, with the project plan of the thread,
// ataniq.project-plan/1, as typed members that mirror the record of package
// internal/genesis/plan member by member, under the same JSON names. The
// wire cannot import that package, so it holds the record's grammar, caps and
// closed lists as forms of its own, and a test of package genesis holds the
// two in step: a plan the host compiles is written here to the very bytes of
// its canonical form, and [GenesisPlanDigest] is its digest.
//
// A plan is a proposal: it binds nothing and grants nothing, and no member of
// it is, or leads to, a decision, a contract or an approval.

// The bounds of a plan, the plan record's own (package plan).
const (
	// GenesisPlanSchema is the schema name of the plan record.
	GenesisPlanSchema = "ataniq.project-plan/1"
	// MaxGenesisPlanBytes caps the plan in its canonical form, the plan
	// member of a result: 12 KiB, under [MaxSeatGenesisResultBytes].
	MaxGenesisPlanBytes = 12288
	// MaxGenesisPlanRevision is the highest revision of a plan.
	MaxGenesisPlanRevision = 9999
	// MaxGenesisPlanMilestones is the most milestones of a plan.
	MaxGenesisPlanMilestones = 4
	// MaxGenesisPlanMilestoneTasks is the most tasks of one milestone.
	MaxGenesisPlanMilestoneTasks = 6
	// MaxGenesisPlanTasks is the most tasks of a plan in all.
	MaxGenesisPlanTasks = 12
	// MaxGenesisPlanTitle is the cap of the plan's title, a catalog label.
	MaxGenesisPlanTitle = 60
	// MaxGenesisPlanMilestoneTitle is the cap of a milestone's title.
	MaxGenesisPlanMilestoneTitle = 60
	// MaxGenesisPlanGoal is the cap of a milestone's goal.
	MaxGenesisPlanGoal = 120
	// MaxGenesisPlanTaskTitle is the cap of a task's title.
	MaxGenesisPlanTaskTitle = 100
	// MaxGenesisPlanDemo is the cap of the first demo's definition of done.
	MaxGenesisPlanDemo = 120
	// MaxGenesisPlanNotes is the most notes of a plan.
	MaxGenesisPlanNotes = 8
	// MaxGenesisPlanNoteText is the cap of one note's text: the owner's own
	// words, as an "other" answer's.
	MaxGenesisPlanNoteText = MaxGenesisOther
	// MaxGenesisPlanNotesDropped is the highest count of notes dropped.
	MaxGenesisPlanNotesDropped = 1 << 16
	// MaxGenesisPlanWordBytes is the longest template id, check id and area
	// name after its prefix.
	MaxGenesisPlanWordBytes = 32
	// MaxGenesisPlanVersionBytes is the longest template version.
	MaxGenesisPlanVersionBytes = 16
	// GenesisPlanEnforcement is how the budget band is held: advisory.
	GenesisPlanEnforcement = "advisory"
	// GenesisPlanCaveat is the words every estimate carries.
	GenesisPlanCaveat = "an estimate, not a promise"
	// GenesisPlanNotesField is the field of a note taken from the brief's
	// notes, which is not a field of the brief.
	GenesisPlanNotesField = "notes"
)

// The closed lists of a plan, the record's own, sorted.
var (
	// GenesisPlanKinds are the kinds of a task.
	GenesisPlanKinds = []string{"build", "review", "test", "write"}
	// GenesisPlanSizes are the sizes of a task.
	GenesisPlanSizes = []string{"large", "medium", "small"}
	// GenesisPlanReviewPoints are the points where the work waits for the
	// owner.
	GenesisPlanReviewPoints = []string{"demo", "milestone", "plan", "publish"}
	// GenesisPlanHosts are the places the project's files may live.
	GenesisPlanHosts = []string{"file_host", "own_server", "this_computer"}
	// GenesisPlanServices are the external services a plan may name.
	GenesisPlanServices = []string{"email delivery", "file or code host", "generator service", "hosting service"}
	// GenesisPlanBudgetBands are the monthly budget bands, by id.
	GenesisPlanBudgetBands = []string{"low", "medium", "none"}
	// GenesisPlanWarnings are the warnings a plan may carry.
	GenesisPlanWarnings = []string{"estimate-may-exceed-band", "rights-not-cleared", "settle-at-desk"}
	// GenesisPlanBands are the estimate bands, each id with its label.
	GenesisPlanBands = map[string]string{
		"hours": "a few hours of agent work",
		"day":   "about a day of agent work",
		"days":  "a few days of agent work",
		"week":  "about a week of agent work",
	}
)

// GenesisPlan is the project plan, ataniq.project-plan/1, member by member as
// the record of package internal/genesis/plan writes it. Every text in it is
// a constant of a template, of the catalog or of that package, but the notes,
// which are the owner's own words and marked untrusted.
type GenesisPlan struct {
	// Schema is always [GenesisPlanSchema].
	Schema string `json:"schema"`
	// Revision is the plan's revision, 1 to [MaxGenesisPlanRevision]: the
	// record's member plan.
	Revision int `json:"plan"`
	// PrevDigest is the digest of the revision before, and empty for 1.
	PrevDigest string `json:"prev_digest"`
	// BriefDigest is the digest of the brief the plan was compiled from.
	BriefDigest string `json:"brief_digest"`
	// CatalogDigest is the digest of the catalog of that brief: the
	// thread's.
	CatalogDigest string `json:"catalog_digest"`
	// Template names the template the plan was compiled from.
	Template GenesisPlanTemplate `json:"template"`
	// TemplateDigest is the digest of the template's data.
	TemplateDigest string `json:"template_digest"`
	// Title is the plan's title, a label of the catalog.
	Title string `json:"title"`
	// Milestones are 1 to [MaxGenesisPlanMilestones] milestones.
	Milestones []GenesisPlanMilestone `json:"milestones"`
	// Gates are the checks required at the end of every milestone: one or
	// more check ids, sorted.
	Gates []string `json:"gates"`
	// FirstDemo is the first demo's definition of done.
	FirstDemo string `json:"first_demo"`
	// ReviewPoints are where the work waits for the owner, sorted.
	ReviewPoints []string `json:"review_points"`
	// Hosts is where the project's files live.
	Hosts string `json:"hosts"`
	// ExternalServices names every external service the work would use,
	// sorted.
	ExternalServices []string `json:"external_services"`
	// BudgetBand is the monthly budget band, advisory.
	BudgetBand GenesisPlanBudget `json:"budget_band"`
	// Estimate is the size of the work, as a band.
	Estimate GenesisPlanEstimate `json:"estimate"`
	// Warnings are the plan's warnings, sorted.
	Warnings []string `json:"warnings"`
	// Notes are the owner's own words, at most [MaxGenesisPlanNotes], for
	// the owner's eyes only.
	Notes []GenesisPlanNote `json:"notes"`
	// NotesDropped counts the owner's words past [MaxGenesisPlanNotes] the
	// plan does not hold; the brief keeps them.
	NotesDropped int `json:"notes_dropped"`
}

// GenesisPlanTemplate names a template: its id and version.
type GenesisPlanTemplate struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// GenesisPlanMilestone is one milestone of a plan.
type GenesisPlanMilestone struct {
	// ID is m and the milestone's place in the template.
	ID string `json:"id"`
	// Title is the milestone's title.
	Title string `json:"title"`
	// Goal says what is true at its end.
	Goal string `json:"goal"`
	// Tasks are 1 to [MaxGenesisPlanMilestoneTasks] tasks.
	Tasks []GenesisPlanTask `json:"tasks"`
}

// GenesisPlanTask is one task of a milestone.
type GenesisPlanTask struct {
	// ID is t and the task's place in the template.
	ID string `json:"id"`
	// Title is the task's title.
	Title string `json:"title"`
	// Kind is build, write, review or test.
	Kind string `json:"kind"`
	// Size is small, medium or large; it chooses no model.
	Size string `json:"size"`
	// DependsOn are the ids of the tasks before it that it waits for, in
	// order.
	DependsOn []string `json:"depends_on"`
	// Scope are the areas it may change (repo:x or store:x), sorted.
	Scope []string `json:"scope"`
	// Checks are the checks its result passes, sorted.
	Checks []string `json:"checks"`
}

// GenesisPlanBudget is the monthly budget band: an id, never a number, and
// always advisory.
type GenesisPlanBudget struct {
	ID          string `json:"id"`
	Enforcement string `json:"enforcement"`
}

// GenesisPlanEstimate is the size of the work: a band and its label, always
// marked an estimate with its caveat.
type GenesisPlanEstimate struct {
	Band     string `json:"band"`
	Label    string `json:"label"`
	Estimate bool   `json:"estimate"`
	Caveat   string `json:"caveat"`
}

// GenesisPlanNote is one text of the owner's: an "other" answer's, or a note
// of the brief. Untrusted is always true: a surface shows it as quoted data,
// and it is never served to an implementation agent.
type GenesisPlanNote struct {
	// Field is the brief field the text was given for, or "notes".
	Field string `json:"field"`
	// Text is the owner's text, 1 to [MaxGenesisPlanNoteText] bytes.
	Text string `json:"text"`
	// Untrusted is always true.
	Untrusted bool `json:"untrusted"`
}

// EncodeGenesisPlan writes a plan in its canonical form, which is the plan
// record's: the bytes [GenesisPlanDigest] is the digest of. It makes the
// checks a result makes of its plan.
func EncodeGenesisPlan(p GenesisPlan) ([]byte, error) {
	c := &checker{invalid: record.CodeRecordInvalid}
	c.genesisPlan(&p)
	if err := c.result(); err != nil {
		return nil, err
	}
	return encodePlan(&p)
}

// GenesisPlanDigest returns the digest of a plan: the SHA-256 of its
// canonical form, in lower-case hexadecimal. It is the digest the plan
// package gives the same record.
func GenesisPlanDigest(p GenesisPlan) (string, error) {
	out, err := EncodeGenesisPlan(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), nil
}

func encodePlan(p *GenesisPlan) ([]byte, error) {
	out, err := record.Marshal(p, "genesis.plan")
	if err != nil {
		return nil, &record.Error{Code: record.CodeRecordInvalid, Detail: err.Error()}
	}
	if len(out) > MaxGenesisPlanBytes {
		return nil, tooLarge("genesis.plan", MaxGenesisPlanBytes)
	}
	return out, nil
}

func inList(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// planWord checks a word of a plan: a lower-case letter, then lower-case
// letters, digits and "-", at most [MaxGenesisPlanWordBytes] bytes.
func planWord(s string) bool {
	if len(s) == 0 || len(s) > MaxGenesisPlanWordBytes || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if ch := s[i]; !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') && ch != '-' {
			return false
		}
	}
	return true
}

// planArea checks an area name: repo: or store: and a word.
func planArea(s string) bool {
	prefix, name, ok := strings.Cut(s, ":")
	return ok && (prefix == "repo" || prefix == "store") && planWord(name)
}

// planVersion checks a template's version: 1 to 16 lower-case letters,
// digits, "." and "-", not starting with "." or "-".
func planVersion(s string) bool {
	if len(s) == 0 || len(s) > MaxGenesisPlanVersionBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		if ch := s[i]; !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') && !(i > 0 && (ch == '.' || ch == '-')) {
			return false
		}
	}
	return true
}

// planID returns the number of a milestone's or a task's id: the letter, then
// 1 to 3 decimal digits without a leading zero.
func planID(id string, letter byte) (int, bool) {
	if len(id) < 2 || len(id) > 4 || id[0] != letter || id[1] == '0' {
		return 0, false
	}
	n := 0
	for i := 1; i < len(id); i++ {
		ch := id[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, true
}

// planText checks a text of a template: 1 to max bytes, no space at either
// end, no markup character and no web address. The codec holds that it is
// valid UTF-8 without a control character.
func (c *checker) planText(s string, max int, where string) {
	lower := strings.ToLower(s)
	c.need(len(s) >= 1 && len(s) <= max && strings.TrimSpace(s) == s, where+" is not 1 to "+strconv.Itoa(max)+" bytes of text without a space at either end")
	c.need(!strings.ContainsAny(s, "<>"), where+" holds a markup character")
	c.need(!strings.Contains(lower, "://") && !strings.Contains(lower, "www.") && !strings.Contains(lower, "mailto:"), where+" holds a web address")
}

// planSet checks a list that is sorted, holds no item twice and holds only
// items ok accepts.
func (c *checker) planSet(list []string, ok func(string) bool, where string) {
	for i, s := range list {
		c.need(ok(s), where+"["+strconv.Itoa(i)+"] is not one of its kind")
		c.need(i == 0 || list[i-1] < s, where+" is not sorted, or repeats an item")
	}
}

func closed(list []string) func(string) bool { return func(s string) bool { return inList(list, s) } }

// genesisPlan checks a plan as the plan record's own check does: the schema,
// the revision and the digest before it, the digests, the template, the
// texts and their caps, the milestones and the tasks (ids unique and in
// order, every dependency a task before it), the grammars of area names and
// check ids, the closed lists and the notes. Like the record's, it never
// holds a plan to a template: a plan of a template no longer embedded reads.
func (c *checker) genesisPlan(p *GenesisPlan) {
	c.need(p.Schema == GenesisPlanSchema, "genesis.plan.schema is not "+GenesisPlanSchema)
	c.need(p.Revision >= 1 && p.Revision <= MaxGenesisPlanRevision, "genesis.plan.plan is not a revision from 1 to "+strconv.Itoa(MaxGenesisPlanRevision))
	c.need((p.Revision == 1) == (p.PrevDigest == ""), "genesis.plan.prev_digest is not empty exactly for revision 1")
	if p.PrevDigest != "" {
		c.sha256(p.PrevDigest, "genesis.plan.prev_digest")
	}
	c.sha256(p.BriefDigest, "genesis.plan.brief_digest")
	c.sha256(p.CatalogDigest, "genesis.plan.catalog_digest")
	c.sha256(p.TemplateDigest, "genesis.plan.template_digest")
	c.need(planWord(p.Template.ID), "genesis.plan.template.id is not a template id")
	c.need(planVersion(p.Template.Version), "genesis.plan.template.version is not a version id")
	c.planText(p.Title, MaxGenesisPlanTitle, "genesis.plan.title")
	c.planText(p.FirstDemo, MaxGenesisPlanDemo, "genesis.plan.first_demo")
	c.planMilestones(p.Milestones)
	c.need(len(p.Gates) > 0, "genesis.plan.gates is empty")
	c.planSet(p.Gates, planWord, "genesis.plan.gates")
	c.need(len(p.ReviewPoints) > 0, "genesis.plan.review_points is empty")
	c.planSet(p.ReviewPoints, closed(GenesisPlanReviewPoints), "genesis.plan.review_points")
	c.need(inList(GenesisPlanHosts, p.Hosts), "genesis.plan.hosts is not one of the hosts")
	c.planSet(p.ExternalServices, closed(GenesisPlanServices), "genesis.plan.external_services")
	c.need(inList(GenesisPlanBudgetBands, p.BudgetBand.ID), "genesis.plan.budget_band.id is not one of the bands")
	c.need(p.BudgetBand.Enforcement == GenesisPlanEnforcement, "genesis.plan.budget_band.enforcement is not advisory")
	label, ok := GenesisPlanBands[p.Estimate.Band]
	c.need(ok && p.Estimate.Label == label, "genesis.plan.estimate is not one of the bands with its label")
	c.need(p.Estimate.Estimate, "genesis.plan.estimate.estimate is not true")
	c.need(p.Estimate.Caveat == GenesisPlanCaveat, "genesis.plan.estimate.caveat is not the words of an estimate")
	c.planSet(p.Warnings, closed(GenesisPlanWarnings), "genesis.plan.warnings")
	c.need(len(p.Notes) <= MaxGenesisPlanNotes, "genesis.plan.notes is more than "+strconv.Itoa(MaxGenesisPlanNotes)+" notes")
	for i := range p.Notes {
		n, at := &p.Notes[i], "genesis.plan.notes["+strconv.Itoa(i)+"]"
		if n.Field != GenesisPlanNotesField {
			c.genesisField(n.Field, at+".field")
		}
		c.hostText(n.Text, MaxGenesisPlanNoteText, at+".text")
		c.need(n.Untrusted, at+".untrusted is not true")
	}
	c.need(p.NotesDropped >= 0 && p.NotesDropped <= MaxGenesisPlanNotesDropped, "genesis.plan.notes_dropped is out of range")
	c.need(p.NotesDropped == 0 || len(p.Notes) == MaxGenesisPlanNotes, "genesis.plan.notes_dropped counts notes the plan had room for")
}

func (c *checker) planMilestones(ms []GenesisPlanMilestone) {
	c.need(len(ms) >= 1 && len(ms) <= MaxGenesisPlanMilestones, "genesis.plan.milestones is not 1 to "+strconv.Itoa(MaxGenesisPlanMilestones)+" milestones")
	last, lastTask, tasks := 0, 0, 0
	before := map[string]bool{}
	for i := range ms {
		m, at := &ms[i], "genesis.plan.milestones["+strconv.Itoa(i)+"]"
		n, ok := planID(m.ID, 'm')
		c.need(ok && n > last, at+".id is not a milestone id after the one before it")
		last = n
		c.planText(m.Title, MaxGenesisPlanMilestoneTitle, at+".title")
		c.planText(m.Goal, MaxGenesisPlanGoal, at+".goal")
		c.need(len(m.Tasks) >= 1 && len(m.Tasks) <= MaxGenesisPlanMilestoneTasks, at+".tasks is not 1 to "+strconv.Itoa(MaxGenesisPlanMilestoneTasks)+" tasks")
		for j := range m.Tasks {
			t, tat := &m.Tasks[j], at+".tasks["+strconv.Itoa(j)+"]"
			tasks++
			n, ok := planID(t.ID, 't')
			c.need(ok && n > lastTask, tat+".id is not a task id after the one before it")
			lastTask = n
			c.planText(t.Title, MaxGenesisPlanTaskTitle, tat+".title")
			c.need(inList(GenesisPlanKinds, t.Kind), tat+".kind is not one of the kinds")
			c.need(inList(GenesisPlanSizes, t.Size), tat+".size is not one of the sizes")
			c.need(len(t.Scope) > 0, tat+".scope is empty")
			c.planSet(t.Scope, planArea, tat+".scope")
			c.planSet(t.Checks, planWord, tat+".checks")
			prev := 0
			for k, d := range t.DependsOn {
				dn, ok := planID(d, 't')
				c.need(ok && before[d], tat+".depends_on["+strconv.Itoa(k)+"] is not a task before it")
				c.need(dn > prev, tat+".depends_on is not in order, or repeats an id")
				prev = dn
			}
			before[t.ID] = true
		}
	}
	c.need(tasks <= MaxGenesisPlanTasks, "genesis.plan holds more than "+strconv.Itoa(MaxGenesisPlanTasks)+" tasks")
}

// genesisPlanResult checks the members of the state plan: the plan, its
// digest, which must be the SHA-256 of the plan's canonical form, and a
// catalog digest that is the thread's.
func (c *checker) genesisPlanResult(r *SeatGenesisResult) {
	c.need(r.Plan != nil, "genesis.plan is missing for a plan")
	c.sha256(r.PlanDigest, "genesis.plan_digest")
	c.need(r.Progress.Filled == r.Progress.Total, "genesis.progress of a plan is not every required field filled")
	if r.Plan == nil || c.err != nil {
		return
	}
	c.genesisPlan(r.Plan)
	c.need(r.Plan.CatalogDigest == r.CatalogDigest, "genesis.plan.catalog_digest is not the thread's")
	if c.err != nil {
		return
	}
	out, err := encodePlan(r.Plan)
	if e, ok := err.(*record.Error); ok {
		c.fail(e.Code, e.Detail)
		return
	}
	sum := sha256.Sum256(out)
	c.need(hex.EncodeToString(sum[:]) == r.PlanDigest, "genesis.plan_digest is not the digest of the plan")
}
