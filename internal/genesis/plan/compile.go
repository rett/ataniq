package plan

import (
	"reflect"
	"sort"
	"strconv"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
)

// Compile compiles a finished brief into a first plan, revision 1, with the
// embedded template of the brief's kind. It is pure and deterministic: the
// same brief and catalog give the same plan, byte for byte. It reads no
// clock: when a plan was made is the store's to keep.
//
// It refuses, with a [*brief.Error]: a catalog that was not loaded
// (catalog_invalid); a brief started under another catalog
// (catalog_mismatch); a brief that breaks its own rules, or holds a value
// the catalog does not offer (brief_invalid and the brief's codes); a brief
// whose required fields are not all filled, an inferred value the owner did
// not confirm counting as not filled (brief_incomplete); a kind with no
// template, such as other (no_template); and a template that does not hold
// to the catalog, or gives a plan over its caps (template_invalid).
//
// The owner's own text, the text of an "other" answer or a note, is kept in
// the plan's notes for the owner's eyes and chooses nothing: a condition
// may hold for an "other" answer, never for what it says.
func Compile(b brief.Brief, cat catalog.Catalog) (Plan, error) {
	ts, err := Templates()
	if err != nil {
		return Plan{}, err
	}
	return compileWith(b, cat, ts)
}

// Next compiles the brief into the revision after prev, with the embedded
// templates, as [Compile] does, and records prev's digest in it. When the
// content would be prev's ([ContentEqual]: brief digests differ for the
// same answers given again), it refuses with no_change and there is no new
// revision.
func Next(prev Plan, b brief.Brief, cat catalog.Catalog) (Plan, error) {
	prevDigest, err := Digest(prev)
	if err != nil {
		return Plan{}, err
	}
	if prev.Revision >= MaxRevision {
		return Plan{}, invalid("the plan is at its last revision")
	}
	p, err := Compile(b, cat)
	if err != nil {
		return Plan{}, err
	}
	if ContentEqual(prev, p) {
		return Plan{}, fail(brief.CodeNoChange, "the plan would be the revision before it")
	}
	p.Revision, p.PrevDigest = prev.Revision+1, prevDigest
	if _, err := Encode(p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// ContentEqual reports whether two plans propose the same: equal but for
// their revision, the digest before and the brief's digest, which differs
// for the same answers given again.
func ContentEqual(a, b Plan) bool {
	strip := func(p Plan) Plan {
		p = p.Clone()
		p.Revision, p.PrevDigest, p.BriefDigest = 0, "", ""
		return p
	}
	return reflect.DeepEqual(strip(a), strip(b))
}

func compileWith(b brief.Brief, cat catalog.Catalog, ts []Template) (Plan, error) {
	if cat.Digest() == "" {
		return Plan{}, fail(brief.CodeCatalogInvalid, "the catalog was not loaded")
	}
	if b.Catalog != cat.Digest() {
		return Plan{}, fail(brief.CodeCatalogMismatch, "the brief was started under another catalog version")
	}
	briefDigest, err := brief.Digest(b)
	if err != nil {
		return Plan{}, err
	}
	if filled, total := catalog.Readiness(cat, b); b.Kind == "" || filled < total {
		return Plan{}, fail(brief.CodeBriefIncomplete, "the brief's required fields are not all filled")
	}
	if err := checkSetOf(ts); err != nil {
		return Plan{}, err
	}
	var t Template
	found := false
	for _, x := range ts {
		if x.Kind == b.Kind {
			t, found = x, true
		}
	}
	if !found {
		return Plan{}, fail(brief.CodeNoTemplate, "no template plans for the brief's kind")
	}
	if t.digest == "" {
		return Plan{}, tplInvalid("the template was not loaded")
	}
	if err := t.checkAgainst(cat); err != nil {
		return Plan{}, err
	}
	if err := checkValues(b, cat); err != nil {
		return Plan{}, err
	}
	title, err := titleOf(b, cat, t.TitleField)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{
		Schema:         Schema,
		Revision:       1,
		BriefDigest:    briefDigest,
		CatalogDigest:  cat.Digest(),
		Template:       TemplateRef{ID: t.ID, Version: t.Version},
		TemplateDigest: t.digest,
		Title:          title,
	}
	p.Milestones = milestones(t, b)
	if n := len(p.Tasks()); len(p.Milestones) > MaxMilestones || n > MaxTasks {
		return Plan{}, tplInvalid("the template gives the brief a plan of more than 12 tasks")
	}
	p.Estimate, p.Warnings = estimate(p.Tasks()), holding(t.Warnings, b)
	if estimateMayExceed(p.Tasks()) {
		p.Warnings = sortedUnique(append(p.Warnings, WarnEstimate))
	}
	p.Gates = holding(t.Gates, b)
	p.ExternalServices = holding(t.Services, b)
	for _, d := range t.Demos {
		if d.When.holds(b) {
			p.FirstDemo = d.Value
			break
		}
	}
	p.ReviewPoints = sortedUnique(choices(b, "review_points"))
	p.Hosts = one(b, "hosts")
	p.BudgetBand = one(b, "budget_band")
	p.Notes, p.NotesDropped = notesOf(b)
	if _, err := Encode(p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// points counts the tasks' sizes.
func points(tasks []Task) int {
	n := 0
	for _, t := range tasks {
		n += Sizes[t.Size]
	}
	return n
}

// estimate is the smallest band that covers the tasks' sizes. It counts
// work, not money.
func estimate(tasks []Task) Estimate {
	n := points(tasks)
	for _, b := range Bands {
		if n <= b.MaxPoints {
			return Estimate{Band: b.ID, Label: b.Label}
		}
	}
	last := Bands[len(Bands)-1]
	return Estimate{Band: last.ID, Label: last.Label}
}

// estimateMayExceed reports whether the tasks fill more than three quarters
// of their band.
func estimateMayExceed(tasks []Task) bool {
	n := points(tasks)
	for _, b := range Bands {
		if n <= b.MaxPoints {
			return 4*n > 3*b.MaxPoints
		}
	}
	return true
}

// milestones builds the milestones of the template whose tasks hold for the
// brief. A task keeps its place in the template as its id. A dependency on a
// task that does not hold is replaced by that task's own dependencies, so
// the order the template sets is kept.
func milestones(t Template, b brief.Brief) []Milestone {
	type entry struct {
		task TemplateTask
		n    int
		in   bool
	}
	byRef := map[string]*entry{}
	n := 0
	for _, m := range t.Milestones {
		for _, k := range m.Tasks {
			n++
			byRef[k.Ref] = &entry{task: k, n: n, in: k.When.holds(b)}
		}
	}
	memo := map[string][]int{}
	var resolve func(ref string) []int
	resolve = func(ref string) []int {
		e := byRef[ref]
		if e.in {
			return []int{e.n}
		}
		if got, ok := memo[ref]; ok {
			return got
		}
		var out []int
		for _, d := range e.task.DependsOn {
			out = append(out, resolve(d)...)
		}
		memo[ref] = out
		return out
	}
	out := []Milestone{}
	for i, m := range t.Milestones {
		ms := Milestone{ID: "m" + strconv.Itoa(i+1), Title: m.Title, Goal: m.Goal, Tasks: []Task{}}
		for _, k := range m.Tasks {
			e := byRef[k.Ref]
			if !e.in {
				continue
			}
			var deps []int
			for _, d := range k.DependsOn {
				for _, x := range resolve(d) {
					if !containsInt(deps, x) {
						deps = append(deps, x)
					}
				}
			}
			sort.Ints(deps)
			ids := make([]string, len(deps))
			for j, x := range deps {
				ids[j] = "t" + strconv.Itoa(x)
			}
			ms.Tasks = append(ms.Tasks, Task{
				ID:        "t" + strconv.Itoa(e.n),
				Title:     k.Title,
				Kind:      k.Kind,
				Size:      k.Size,
				DependsOn: ids,
				Scope:     append([]string{}, k.Scope...),
				Checks:    append([]string{}, k.Checks...),
			})
		}
		if len(ms.Tasks) > 0 {
			out = append(out, ms)
		}
	}
	return out
}

func containsInt(list []int, x int) bool {
	for _, y := range list {
		if y == x {
			return true
		}
	}
	return false
}

// holding returns the values of the items whose condition holds, sorted.
func holding(cs []Cond, b brief.Brief) []string {
	var out []string
	for _, c := range cs {
		if c.When.holds(b) {
			out = append(out, c.Value)
		}
	}
	return sortedUnique(out)
}

// checkValues refuses a brief that holds, in a required field, a value no
// answer to the catalog can set: the plan is chosen by the values, so a
// value the catalog does not offer is never read as some default.
func checkValues(b brief.Brief, cat catalog.Catalog) error {
	for _, field := range catalog.Required(cat, b) {
		f, ok := b.Get(field)
		if !ok {
			return fail(brief.CodeBriefIncomplete, "the brief's required fields are not all filled")
		}
		node, ok := cat.Node(f.Node)
		if !ok || node.Field != field {
			return fail(brief.CodeBriefInvalid, "a field of the brief names a question of the catalog that does not set it")
		}
		good := false
		switch f.Value.Kind() {
		case brief.KindOne:
			good = node.Type != catalog.Multi && canSet(node, f.Value.One())
		case brief.KindMany:
			good = node.Type == catalog.Multi
			for _, c := range f.Value.Many() {
				good = good && canSet(node, c)
			}
		case brief.KindOther:
			good = node.AllowOther
		}
		if !good {
			return fail(brief.CodeBriefInvalid, "a field of the brief holds a value its question does not offer")
		}
	}
	// The fields whose values the plan carries as they are have a word in
	// the plan's closed lists for every option of the catalog.
	for _, field := range []string{"hosts", "budget_band", "review_points"} {
		closed := map[string][]string{"hosts": Hosts, "budget_band": BudgetBands, "review_points": ReviewPoints}[field]
		f, _ := b.Get(field)
		vals := f.Value.Many()
		if f.Value.Kind() == brief.KindOne {
			vals = []string{f.Value.One()}
		}
		if len(vals) == 0 {
			return fail(brief.CodeCatalogInvalid, "the catalog's brief schema does not hold hosts, budget_band and review_points as choices")
		}
		for _, v := range vals {
			if !contains(closed, v) {
				return fail(brief.CodeCatalogInvalid, "the catalog offers an option the plan has no word for")
			}
		}
	}
	return nil
}

// titleOf returns the catalog label of the option the title field holds.
func titleOf(b brief.Brief, cat catalog.Catalog, field string) (string, error) {
	f, ok := b.Get(field)
	if !ok || f.Value.Kind() != brief.KindOne {
		return "", fail(brief.CodeBriefInvalid, "the brief's title field does not hold one choice")
	}
	node, ok := cat.Node(f.Node)
	if ok {
		for _, o := range node.Options {
			if o.ID == f.Value.One() {
				return o.Label, nil
			}
		}
	}
	return "", fail(brief.CodeBriefInvalid, "the brief's title field holds no option of the catalog")
}

func one(b brief.Brief, field string) string {
	f, _ := b.Get(field)
	return f.Value.One()
}

func choices(b brief.Brief, field string) []string {
	f, _ := b.Get(field)
	if f.Value.Kind() == brief.KindOne {
		return []string{f.Value.One()}
	}
	return f.Value.Many()
}

// notesOf keeps the owner's own texts: the "other" answers, by field name,
// then the brief's notes, in order. Past [MaxNotes] they are counted, not
// kept.
func notesOf(b brief.Brief) ([]Note, int) {
	names := make([]string, 0, len(b.Fields))
	for name := range b.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	var all []Note
	for _, name := range names {
		if text, ok := b.Fields[name].Value.Other(); ok {
			all = append(all, Note{Field: name, Text: text})
		}
	}
	for _, n := range b.Notes {
		all = append(all, Note{Field: FieldNotes, Text: n})
	}
	if len(all) > MaxNotes {
		return all[:MaxNotes:MaxNotes], len(all) - MaxNotes
	}
	return append([]Note{}, all...), 0
}
