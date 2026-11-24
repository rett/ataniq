package plan

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sort"
	"strconv"
	"sync"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/internal/canon"
)

// TemplateSchema is the schema name of a plan template.
const TemplateSchema = "ataniq.genesis-plan-template/1"

// The caps of a template.
const (
	// MaxTemplateBytes is the cap of a template's data.
	MaxTemplateBytes = 32768
	// MaxConds is the most gates, services or demos a template lists.
	MaxConds = 16
	// MaxInValues is the most values of an in.
	MaxInValues = 8
)

// Op is the operation of a template's [Predicate]. The set is closed: there
// is no expression language.
type Op string

// The operations of a predicate.
const (
	// OpEq holds when the field holds the one choice. The value "other"
	// holds when the field holds the owner's own text instead of a
	// choice; the text itself is never compared.
	OpEq Op = "eq"
	// OpIn holds when OpEq holds for one of the values.
	OpIn Op = "in"
	// OpContains holds when a field of several choices holds the choice.
	OpContains Op = "contains"
	// OpSet holds when the field is set.
	OpSet Op = "set"
)

// Predicate is a condition over one field of the brief. Value is used by
// eq and contains, Values by in.
type Predicate struct {
	Op     Op
	Field  string
	Value  string
	Values []string
}

// Cond is an item of a template that holds when its condition does: a
// gate's check, a service's name or a demo's sentence. A nil When always
// holds.
type Cond struct {
	Value string
	When  *Predicate
}

// TemplateTask is one task of a template. Ref names it within the
// template, for the dependencies; the plan names it t1, t2 and so on, by
// its place among all the template's tasks.
type TemplateTask struct {
	Ref       string
	Title     string
	Kind      Kind
	Tier      Tier
	DependsOn []string
	Scope     []string
	Checks    []string
	When      *Predicate
}

// TemplateMilestone is one milestone of a template, named m1, m2 and so on
// by its place. A milestone none of whose tasks holds is left out.
type TemplateMilestone struct {
	Title string
	Goal  string
	Tasks []TemplateTask
}

// Template is a loaded, valid plan template: the data the compiler turns a
// brief of one kind into a plan with. Build one with [LoadTemplate] or
// [Templates].
type Template struct {
	// ID is the template's id, such as software.
	ID string
	// Version is its version id, such as v1.
	Version string
	// Kind is the brief kind it plans for.
	Kind string
	// TitleField is the brief field whose option's catalog label is the
	// plan's title.
	TitleField string
	// Areas are the areas its tasks may name, a part of [Areas].
	Areas []string
	// Checks are the checks its tasks and gates may name, a part of
	// [Checks].
	Checks []string
	// Gates are the checks required at the end of every milestone.
	Gates []Cond
	// Services are the external services the work would use.
	Services []Cond
	// Demos are the first demo's definitions of done: the first that
	// holds is the plan's. The last always holds.
	Demos []Cond
	// Milestones are the milestones and their tasks.
	Milestones []TemplateMilestone

	digest string
}

// Digest returns the SHA-256 of the template's canonical form in lower-case
// hexadecimal. A plan compiled from the template records it.
func (t Template) Digest() string { return t.digest }

// The security block every template holds, word for word: no push, no
// outward step, secrets by reference.
var securityMembers = []canon.Member{
	canon.M("push", canon.Boolean(false)),
	canon.M("outward_steps", canon.Boolean(false)),
	canon.M("secrets", canon.Str("by-reference")),
}

// templateKinds are the kinds a template may plan for: the brief's.
var templateKinds = []string{brief.DomainMedia, brief.DomainOther, brief.DomainPublishing, brief.DomainSoftware}

func tplInvalid(detail string) error { return fail(brief.CodeTemplateInvalid, detail) }

//go:embed templates/*.json
var templateFS embed.FS

var embedded = sync.OnceValues(func() ([]Template, error) {
	names, err := fs.Glob(templateFS, "templates/*.json")
	if err != nil {
		return nil, tplInvalid("the embedded templates cannot be listed")
	}
	sort.Strings(names)
	var out []Template
	for _, name := range names {
		data, err := templateFS.ReadFile(name)
		if err != nil {
			return nil, tplInvalid("an embedded template cannot be read")
		}
		t, err := LoadTemplate(data)
		if err != nil {
			return nil, err
		}
		if "templates/"+t.ID+".json" != name {
			return nil, tplInvalid("an embedded template's file is not named for its id")
		}
		out = append(out, t)
	}
	if err := checkSetOf(out); err != nil {
		return nil, err
	}
	return out, nil
})

// Templates returns the embedded templates, sorted by id. Each call returns
// its own copy.
func Templates() ([]Template, error) {
	ts, err := embedded()
	if err != nil {
		return nil, err
	}
	out := make([]Template, len(ts))
	for i, t := range ts {
		out[i] = t.clone()
	}
	return out, nil
}

// checkSetOf refuses a set of templates in which two plan for one kind.
func checkSetOf(ts []Template) error {
	seen := map[string]bool{}
	for _, t := range ts {
		if seen[t.Kind] {
			return tplInvalid("two templates plan for one kind")
		}
		seen[t.Kind] = true
	}
	return nil
}

func (t Template) clone() Template {
	c := t
	c.Areas = append([]string(nil), t.Areas...)
	c.Checks = append([]string(nil), t.Checks...)
	c.Gates = cloneConds(t.Gates)
	c.Services = cloneConds(t.Services)
	c.Demos = cloneConds(t.Demos)
	c.Milestones = make([]TemplateMilestone, len(t.Milestones))
	for i, m := range t.Milestones {
		tasks := make([]TemplateTask, len(m.Tasks))
		for j, k := range m.Tasks {
			k.DependsOn = append([]string(nil), k.DependsOn...)
			k.Scope = append([]string(nil), k.Scope...)
			k.Checks = append([]string(nil), k.Checks...)
			k.When = k.When.clone()
			tasks[j] = k
		}
		m.Tasks = tasks
		c.Milestones[i] = m
	}
	return c
}

func cloneConds(cs []Cond) []Cond {
	out := make([]Cond, len(cs))
	for i, c := range cs {
		c.When = c.When.clone()
		out[i] = c
	}
	return out
}

func (p *Predicate) clone() *Predicate {
	if p == nil {
		return nil
	}
	c := *p
	c.Values = append([]string(nil), p.Values...)
	return &c
}

// LoadTemplate reads a template strictly and checks it: its form, its
// texts, its closed lists, its conditions and its dependencies. A refusal
// has the code template_invalid, or too_large for data over the cap. A
// template is checked against a catalog, for the fields and options its
// conditions name, when a plan is compiled with it.
func LoadTemplate(data []byte) (Template, error) {
	if len(data) > MaxTemplateBytes {
		return Template{}, fail(brief.CodeTooLarge, "the template is larger than 32768 bytes")
	}
	v, err := canon.Parse(data, 8)
	if err != nil {
		return Template{}, tplInvalid(err.Error())
	}
	t, err := decodeTemplate(v)
	if err != nil {
		return Template{}, err
	}
	if err := t.check(); err != nil {
		return Template{}, err
	}
	// The strict reader took every member, so the canonical form of what
	// it read is the canonical form of the template.
	out, err := canon.Encode(v)
	if err != nil {
		return Template{}, tplInvalid("the template does not encode")
	}
	sum := sha256.Sum256(out)
	t.digest = hex.EncodeToString(sum[:])
	return t, nil
}

func decodeTemplate(v canon.Value) (Template, error) {
	m, err := canon.Fields(v, "template", []string{
		"schema", "id", "version", "kind", "title_field", "security", "areas", "checks", "gates", "services",
		"demos", "milestones",
	}, nil)
	if err != nil {
		return Template{}, tplInvalid(err.Error())
	}
	var t Template
	var schema string
	for key, dst := range map[string]*string{"schema": &schema, "id": &t.ID, "version": &t.Version, "kind": &t.Kind, "title_field": &t.TitleField} {
		if *dst, err = canon.AsString(m[key], "template."+key); err != nil {
			return Template{}, tplInvalid(err.Error())
		}
	}
	if schema != TemplateSchema {
		return Template{}, tplInvalid("template.schema is not " + TemplateSchema)
	}
	sec, err := canon.Fields(m["security"], "template.security", []string{"push", "outward_steps", "secrets"}, nil)
	if err != nil {
		return Template{}, tplInvalid(err.Error())
	}
	for _, want := range securityMembers {
		got := sec[want.Key]
		if got.Kind != want.Val.Kind || got.B != want.Val.B || got.Str != want.Val.Str {
			return Template{}, tplInvalid("template.security." + want.Key + " is not the security default")
		}
	}
	if t.Areas, err = canon.AsStrings(m["areas"], "template.areas", len(Areas)); err != nil {
		return Template{}, tplInvalid(err.Error())
	}
	if t.Checks, err = canon.AsStrings(m["checks"], "template.checks", len(Checks)); err != nil {
		return Template{}, tplInvalid(err.Error())
	}
	for key, dst := range map[string]*[]Cond{"gates": &t.Gates, "services": &t.Services, "demos": &t.Demos} {
		if *dst, err = decodeConds(m[key], "template."+key); err != nil {
			return Template{}, err
		}
	}
	mv := m["milestones"]
	if mv.Kind != canon.Array {
		return Template{}, tplInvalid("template.milestones is not a list")
	}
	if len(mv.Arr) == 0 || len(mv.Arr) > MaxMilestones {
		return Template{}, tplInvalid("template.milestones does not hold 1 to 8 milestones")
	}
	t.Milestones = make([]TemplateMilestone, len(mv.Arr))
	for i, e := range mv.Arr {
		where := "template.milestones[" + strconv.Itoa(i) + "]"
		mm, err := canon.Fields(e, where, []string{"title", "goal", "tasks"}, nil)
		if err != nil {
			return Template{}, tplInvalid(err.Error())
		}
		ms := &t.Milestones[i]
		if ms.Title, err = canon.AsString(mm["title"], where+".title"); err != nil {
			return Template{}, tplInvalid(err.Error())
		}
		if ms.Goal, err = canon.AsString(mm["goal"], where+".goal"); err != nil {
			return Template{}, tplInvalid(err.Error())
		}
		tv := mm["tasks"]
		if tv.Kind != canon.Array {
			return Template{}, tplInvalid(where + ".tasks is not a list")
		}
		if len(tv.Arr) == 0 || len(tv.Arr) > MaxTasksPerMilestone {
			return Template{}, tplInvalid(where + ".tasks does not hold 1 to 6 tasks")
		}
		ms.Tasks = make([]TemplateTask, len(tv.Arr))
		for j, te := range tv.Arr {
			if ms.Tasks[j], err = decodeTemplateTask(te, where+".tasks["+strconv.Itoa(j)+"]"); err != nil {
				return Template{}, err
			}
		}
	}
	return t, nil
}

func decodeTemplateTask(v canon.Value, where string) (TemplateTask, error) {
	m, err := canon.Fields(v, where, []string{"ref", "title", "kind", "tier", "depends_on", "scope", "checks"}, []string{"when"})
	if err != nil {
		return TemplateTask{}, tplInvalid(err.Error())
	}
	var t TemplateTask
	var kind, tier string
	for key, dst := range map[string]*string{"ref": &t.Ref, "title": &t.Title, "kind": &kind, "tier": &tier} {
		if *dst, err = canon.AsString(m[key], where+"."+key); err != nil {
			return TemplateTask{}, tplInvalid(err.Error())
		}
	}
	t.Kind, t.Tier = Kind(kind), Tier(tier)
	for key, dst := range map[string]*[]string{"depends_on": &t.DependsOn, "scope": &t.Scope, "checks": &t.Checks} {
		if *dst, err = canon.AsStrings(m[key], where+"."+key, MaxTasks); err != nil {
			return TemplateTask{}, tplInvalid(err.Error())
		}
	}
	if wv, ok := m["when"]; ok {
		p, err := decodePredicate(wv, where+".when")
		if err != nil {
			return TemplateTask{}, err
		}
		t.When = &p
	}
	return t, nil
}

// condKey is the member that holds a Cond's value, by list.
var condKey = map[string]string{"template.gates": "check", "template.services": "name", "template.demos": "text"}

func decodeConds(v canon.Value, where string) ([]Cond, error) {
	if v.Kind != canon.Array {
		return nil, tplInvalid(where + " is not a list")
	}
	if len(v.Arr) > MaxConds {
		return nil, tplInvalid(where + " holds more than 16 items")
	}
	key := condKey[where]
	out := make([]Cond, len(v.Arr))
	for i, e := range v.Arr {
		w := where + "[" + strconv.Itoa(i) + "]"
		m, err := canon.Fields(e, w, []string{key}, []string{"when"})
		if err != nil {
			return nil, tplInvalid(err.Error())
		}
		if out[i].Value, err = canon.AsString(m[key], w+"."+key); err != nil {
			return nil, tplInvalid(err.Error())
		}
		if wv, ok := m["when"]; ok {
			p, err := decodePredicate(wv, w+".when")
			if err != nil {
				return nil, err
			}
			out[i].When = &p
		}
	}
	return out, nil
}

func decodePredicate(v canon.Value, where string) (Predicate, error) {
	if v.Kind != canon.Object {
		return Predicate{}, tplInvalid(where + " is not an object")
	}
	var op string
	for _, m := range v.Obj {
		if m.Key == "op" && m.Val.Kind == canon.String {
			op = m.Val.Str
		}
	}
	var required []string
	switch Op(op) {
	case OpEq, OpContains:
		required = []string{"op", "field", "value"}
	case OpIn:
		required = []string{"op", "field", "values"}
	case OpSet:
		required = []string{"op", "field"}
	default:
		return Predicate{}, tplInvalid(where + ".op is not one of the operations")
	}
	m, err := canon.Fields(v, where, required, nil)
	if err != nil {
		return Predicate{}, tplInvalid(err.Error())
	}
	p := Predicate{Op: Op(op)}
	if p.Field, err = canon.AsString(m["field"], where+".field"); err != nil {
		return Predicate{}, tplInvalid(err.Error())
	}
	if vv, ok := m["value"]; ok {
		if p.Value, err = canon.AsString(vv, where+".value"); err != nil {
			return Predicate{}, tplInvalid(err.Error())
		}
	}
	if vv, ok := m["values"]; ok {
		if p.Values, err = canon.AsStrings(vv, where+".values", MaxInValues); err != nil {
			return Predicate{}, tplInvalid(err.Error())
		}
	}
	return p, nil
}

// check holds every rule of a template that needs no catalog.
func (t Template) check() error {
	switch {
	case !validTemplateID(t.ID):
		return tplInvalid("template.id is not a template id")
	case !validVersion(t.Version):
		return tplInvalid("template.version is not a version id")
	case !contains(templateKinds, t.Kind):
		return tplInvalid("template.kind is not one of the kinds of a brief")
	}
	if err := t.checkField(t.TitleField, "template.title_field"); err != nil {
		return err
	}
	if len(t.Areas) == 0 {
		return tplInvalid("template.areas is empty")
	}
	if err := checkClosed(t.Areas, Areas, "template.areas"); err != nil {
		return err
	}
	if len(t.Checks) == 0 {
		return tplInvalid("template.checks is empty")
	}
	if err := checkClosed(t.Checks, Checks, "template.checks"); err != nil {
		return err
	}
	always := false
	for i, g := range t.Gates {
		where := "template.gates[" + strconv.Itoa(i) + "]"
		if !contains(t.Checks, g.Value) {
			return tplInvalid(where + ".check is not one of the template's checks")
		}
		for _, h := range t.Gates[:i] {
			if h.Value == g.Value {
				return tplInvalid(where + ".check repeats a gate")
			}
		}
		if err := t.checkWhen(g.When, where+".when"); err != nil {
			return err
		}
		always = always || g.When == nil
	}
	if !always {
		return tplInvalid("template.gates holds no gate that always holds")
	}
	for i, s := range t.Services {
		where := "template.services[" + strconv.Itoa(i) + "]"
		if !contains(Services, s.Value) {
			return tplInvalid(where + ".name is not one of the services")
		}
		if err := t.checkWhen(s.When, where+".when"); err != nil {
			return err
		}
	}
	if len(t.Demos) == 0 {
		return tplInvalid("template.demos is empty")
	}
	for i, d := range t.Demos {
		where := "template.demos[" + strconv.Itoa(i) + "]"
		if err := checkText(d.Value, MaxDemo, where+".text", tplInvalid); err != nil {
			return err
		}
		if (i == len(t.Demos)-1) != (d.When == nil) {
			return tplInvalid(where + " has a condition exactly when it is not the last demo")
		}
		if err := t.checkWhen(d.When, where+".when"); err != nil {
			return err
		}
	}
	if len(t.Milestones) == 0 || len(t.Milestones) > MaxMilestones {
		return tplInvalid("template.milestones does not hold 1 to 8 milestones")
	}
	var refs []string
	always = false
	for i, m := range t.Milestones {
		where := "template.milestones[" + strconv.Itoa(i) + "]"
		if err := checkText(m.Title, MaxMilestoneTitle, where+".title", tplInvalid); err != nil {
			return err
		}
		if err := checkText(m.Goal, MaxGoal, where+".goal", tplInvalid); err != nil {
			return err
		}
		if len(m.Tasks) == 0 || len(m.Tasks) > MaxTasksPerMilestone {
			return tplInvalid(where + ".tasks does not hold 1 to 6 tasks")
		}
		for j, k := range m.Tasks {
			tw := where + ".tasks[" + strconv.Itoa(j) + "]"
			if !validRef(k.Ref) {
				return tplInvalid(tw + ".ref is not a task ref")
			}
			if contains(refs, k.Ref) {
				return tplInvalid(tw + ".ref repeats the ref of another task")
			}
			if err := checkText(k.Title, MaxTaskTitle, tw+".title", tplInvalid); err != nil {
				return err
			}
			if !contains(Kinds, k.Kind) {
				return tplInvalid(tw + ".kind is not one of the kinds")
			}
			if !contains(Tiers, k.Tier) {
				return tplInvalid(tw + ".tier is not one of the tiers")
			}
			if len(k.Scope) == 0 {
				return tplInvalid(tw + ".scope is empty")
			}
			if err := checkClosed(k.Scope, t.Areas, tw+".scope"); err != nil {
				return err
			}
			if err := checkClosed(k.Checks, t.Checks, tw+".checks"); err != nil {
				return err
			}
			for d, dep := range k.DependsOn {
				// A task waits only for a task before it, so the
				// dependencies form no cycle.
				if !contains(refs, dep) {
					return tplInvalid(tw + ".depends_on names a task that is not before it")
				}
				if contains(k.DependsOn[:d], dep) {
					return tplInvalid(tw + ".depends_on names a task twice")
				}
			}
			if err := t.checkWhen(k.When, tw+".when"); err != nil {
				return err
			}
			always = always || k.When == nil
			refs = append(refs, k.Ref)
		}
	}
	if len(refs) > MaxTasks {
		return tplInvalid("the template holds more than 40 tasks")
	}
	if !always {
		return tplInvalid("the template holds no task that always holds")
	}
	return nil
}

// checkClosed checks that list is sorted, holds no item twice and holds
// only items of closed.
func checkClosed(list, closed []string, where string) error {
	for i, s := range list {
		if !contains(closed, s) {
			return tplInvalid(where + " names an item outside its closed list")
		}
		if i > 0 && list[i-1] >= s {
			return tplInvalid(where + " is not sorted, or repeats an item")
		}
	}
	return nil
}

// checkField refuses a field that is not of the brief, or an extension
// field of another kind than the template's.
func (t Template) checkField(f, where string) error {
	if !brief.ValidFieldName(f) {
		return tplInvalid(where + " is not a field of the brief")
	}
	if d := brief.FieldDomain(f); d != "" && d != t.Kind {
		return tplInvalid(where + " is an extension field of another kind")
	}
	return nil
}

func (t Template) checkWhen(p *Predicate, where string) error {
	if p == nil {
		return nil
	}
	if err := t.checkField(p.Field, where+".field"); err != nil {
		return err
	}
	switch p.Op {
	case OpEq, OpContains:
		if !brief.ValidChoice(p.Value) || p.Values != nil {
			return tplInvalid(where + " is an " + string(p.Op) + " without exactly one choice id")
		}
	case OpIn:
		if p.Value != "" || len(p.Values) == 0 || len(p.Values) > MaxInValues {
			return tplInvalid(where + " is an in without 1 to 8 values")
		}
		for i, v := range p.Values {
			if !brief.ValidChoice(v) || contains(p.Values[:i], v) {
				return tplInvalid(where + ".values holds a value that is not a choice id, or repeats one")
			}
		}
	case OpSet:
		if p.Value != "" || p.Values != nil {
			return tplInvalid(where + " is a set with a value")
		}
	default:
		return tplInvalid(where + ".op is not one of the operations")
	}
	return nil
}

func validRef(s string) bool {
	if len(s) == 0 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}

// checkAgainst holds the template to the catalog: the title field and
// every field a condition names are required fields of the kind's brief,
// every value a condition compares with is one an answer can set, eq and
// in name fields of one choice and contains fields of several, and the
// title field's questions take no other text, so that the title is always a
// catalog label.
func (t Template) checkAgainst(c catalog.Catalog) error {
	required, ok := c.Required[t.Kind]
	if !ok {
		return tplInvalid("the catalog has no brief schema for the template's kind")
	}
	writers := func(field string) []catalog.Node {
		var out []catalog.Node
		for _, n := range c.Nodes {
			if n.Field == field {
				out = append(out, n)
			}
		}
		return out
	}
	if !contains(required, t.TitleField) {
		return tplInvalid("template.title_field is not a required field of the kind")
	}
	for _, n := range writers(t.TitleField) {
		if n.Type != catalog.Single || n.AllowOther {
			return tplInvalid("template.title_field is set by a question that is not a single choice of options")
		}
	}
	check := func(p *Predicate, where string) error {
		if p == nil {
			return nil
		}
		if !contains(required, p.Field) {
			return tplInvalid(where + ".field is not a required field of the kind")
		}
		ws := writers(p.Field)
		for _, n := range ws {
			if (n.Type == catalog.Multi) != (p.Op == OpContains) && p.Op != OpSet {
				return tplInvalid(where + " compares a field of several choices with eq or in, or of one with contains")
			}
		}
		values := p.Values
		if p.Op == OpEq || p.Op == OpContains {
			values = []string{p.Value}
		}
		for _, v := range values {
			found := false
			for _, n := range ws {
				if canSet(n, v, p.Op != OpContains) {
					found = true
				}
			}
			if !found {
				return tplInvalid(where + " compares with a value no answer can set")
			}
		}
		return nil
	}
	for name, cs := range map[string][]Cond{"template.gates": t.Gates, "template.services": t.Services, "template.demos": t.Demos} {
		for i, cd := range cs {
			if err := check(cd.When, name+"["+strconv.Itoa(i)+"].when"); err != nil {
				return err
			}
		}
	}
	for i, m := range t.Milestones {
		for j, k := range m.Tasks {
			if err := check(k.When, "template.milestones["+strconv.Itoa(i)+"].tasks["+strconv.Itoa(j)+"].when"); err != nil {
				return err
			}
		}
	}
	return nil
}

// canSet reports whether an answer to the node can set the value: one of
// its options, a step of a scale, or, where other is allowed, "other".
func canSet(n catalog.Node, v string, otherCounts bool) bool {
	if otherCounts && v == catalog.OtherChoice && n.AllowOther {
		return true
	}
	if n.Type == catalog.Scale {
		return len(v) == 1 && v[0] >= '1' && v[0] <= '5'
	}
	for _, o := range n.Options {
		if o.ID == v {
			return true
		}
	}
	return false
}

// holds reports whether the predicate holds for the brief. A nil predicate
// always holds. The owner's own text is never read: eq "other" holds for
// any text.
func (p *Predicate) holds(b brief.Brief) bool {
	if p == nil {
		return true
	}
	f, ok := b.Get(p.Field)
	if !ok {
		return false
	}
	eq := func(v string) bool {
		if _, other := f.Value.Other(); other {
			return v == catalog.OtherChoice
		}
		return f.Value.Kind() == brief.KindOne && f.Value.One() == v
	}
	switch p.Op {
	case OpEq:
		return eq(p.Value)
	case OpIn:
		for _, v := range p.Values {
			if eq(v) {
				return true
			}
		}
		return false
	case OpContains:
		return f.Value.Kind() == brief.KindMany && f.Value.Has(p.Value)
	case OpSet:
		return true
	}
	return false
}
