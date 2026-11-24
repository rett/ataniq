package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/internal/canon"
)

// maxDepth is how deep a plan nests: the plan, the milestones, a
// milestone, its tasks, a task and its lists need six levels.
const maxDepth = 8

func invalid(detail string) error { return fail(brief.CodePlanInvalid, detail) }

// Check checks the record on its own: the schema, the revision and the
// digest before it, the digests, the template's id and version, the texts
// and their caps, the milestones and the tasks (ids unique and in order,
// every dependency a task before it, so no cycle), the grammar of area
// names and check ids, the closed lists of the record (kinds, sizes,
// review points, hosts, services, budget bands, estimate bands, warnings)
// and the notes. It never reads a template: a plan compiled from a template
// that is no longer embedded still holds. [Decode] holds a plan to it, and
// [Compile] holds its own result to it.
func Check(p Plan) error {
	switch {
	case p.Schema != Schema:
		return invalid("plan.schema is not " + Schema)
	case p.Revision < 1 || p.Revision > MaxRevision:
		return invalid("plan.plan is not a revision from 1 to 9999")
	case (p.Revision == 1) != (p.PrevDigest == ""):
		return invalid("plan.prev_digest is empty exactly for revision 1")
	case p.PrevDigest != "" && !isLowerHex64(p.PrevDigest):
		return invalid("plan.prev_digest is not a SHA-256 digest in lower-case hexadecimal")
	case !isLowerHex64(p.BriefDigest):
		return invalid("plan.brief_digest is not a SHA-256 digest in lower-case hexadecimal")
	case !isLowerHex64(p.CatalogDigest):
		return invalid("plan.catalog_digest is not a SHA-256 digest in lower-case hexadecimal")
	case !validTemplateID(p.Template.ID):
		return invalid("plan.template.id is not a template id")
	case !validVersion(p.Template.Version):
		return invalid("plan.template.version is not a version id")
	case !isLowerHex64(p.TemplateDigest):
		return invalid("plan.template_digest is not a SHA-256 digest in lower-case hexadecimal")
	}
	if err := checkText(p.Title, MaxTitle, "plan.title", invalid); err != nil {
		return err
	}
	if err := checkText(p.FirstDemo, MaxDemo, "plan.first_demo", invalid); err != nil {
		return err
	}
	if err := checkMilestones(p.Milestones); err != nil {
		return err
	}
	if len(p.Gates) == 0 {
		return invalid("plan.gates is empty")
	}
	if err := checkSet(p.Gates, validCheckID, "plan.gates"); err != nil {
		return err
	}
	if len(p.ReviewPoints) == 0 {
		return invalid("plan.review_points is empty")
	}
	if err := checkSet(p.ReviewPoints, in(ReviewPoints), "plan.review_points"); err != nil {
		return err
	}
	if !contains(Hosts, p.Hosts) {
		return invalid("plan.hosts is not one of the hosts")
	}
	if err := checkSet(p.ExternalServices, in(Services), "plan.external_services"); err != nil {
		return err
	}
	if !contains(BudgetBands, p.BudgetBand) {
		return invalid("plan.budget_band is not one of the bands")
	}
	if b, ok := bandOf(p.Estimate.Band); !ok || b.Label != p.Estimate.Label {
		return invalid("plan.estimate is not one of the bands with its label")
	}
	if err := checkSet(p.Warnings, in(Warnings), "plan.warnings"); err != nil {
		return err
	}
	if len(p.Notes) > MaxNotes {
		return fail(brief.CodeTooLarge, "the plan holds more than 8 notes")
	}
	for i, n := range p.Notes {
		where := "plan.notes[" + strconv.Itoa(i) + "]"
		if n.Field != FieldNotes && !brief.ValidFieldName(n.Field) {
			return invalid(where + ".field is not a field of the brief, or notes")
		}
		if err := checkNote(string(n.Text), where+".text"); err != nil {
			return err
		}
	}
	if p.NotesDropped < 0 || p.NotesDropped > 1<<16 {
		return invalid("plan.notes_dropped is out of range")
	}
	if p.NotesDropped > 0 && len(p.Notes) < MaxNotes {
		return invalid("plan.notes_dropped counts notes the plan had room for")
	}
	return nil
}

func checkMilestones(ms []Milestone) error {
	if len(ms) == 0 || len(ms) > MaxMilestones {
		return invalid("plan.milestones does not hold 1 to 4 milestones")
	}
	var tasks []Task
	last := 0
	for i, m := range ms {
		where := "plan.milestones[" + strconv.Itoa(i) + "]"
		n, ok := idNumber(m.ID, 'm')
		if !ok {
			return invalid(where + ".id is not a milestone id")
		}
		if n <= last {
			return invalid(where + ".id is not after the id before it")
		}
		last = n
		if err := checkText(m.Title, MaxMilestoneTitle, where+".title", invalid); err != nil {
			return err
		}
		if err := checkText(m.Goal, MaxGoal, where+".goal", invalid); err != nil {
			return err
		}
		if len(m.Tasks) == 0 || len(m.Tasks) > MaxTasksPerMilestone {
			return invalid(where + ".tasks does not hold 1 to 6 tasks")
		}
		tasks = append(tasks, m.Tasks...)
	}
	if len(tasks) > MaxTasks {
		return invalid("the plan holds more than 12 tasks")
	}
	// Tasks are in the order of their ids, and a task waits only for a
	// task before it: the dependencies form no cycle.
	before := map[string]bool{}
	last = 0
	for i, t := range tasks {
		where := "plan.tasks[" + strconv.Itoa(i) + "]"
		n, ok := idNumber(t.ID, 't')
		if !ok {
			return invalid(where + ".id is not a task id")
		}
		if n <= last {
			return invalid(where + ".id is not after the id before it")
		}
		last = n
		if err := checkText(t.Title, MaxTaskTitle, where+".title", invalid); err != nil {
			return err
		}
		if !contains(Kinds, t.Kind) {
			return invalid(where + ".kind is not one of the kinds")
		}
		if _, ok := Sizes[t.Size]; !ok {
			return invalid(where + ".size is not one of the sizes")
		}
		if len(t.Scope) == 0 {
			return invalid(where + ".scope is empty")
		}
		if err := checkSet(t.Scope, validArea, where+".scope"); err != nil {
			return err
		}
		if err := checkSet(t.Checks, validCheckID, where+".checks"); err != nil {
			return err
		}
		prev := 0
		for _, d := range t.DependsOn {
			dn, ok := idNumber(d, 't')
			if !ok {
				return invalid(where + ".depends_on names an id that is not a task id")
			}
			if d == t.ID {
				return invalid(where + ".depends_on names the task itself")
			}
			if !before[d] {
				return invalid(where + ".depends_on names a task that is not before it in the plan")
			}
			if dn <= prev {
				return invalid(where + ".depends_on is not in order, or repeats an id")
			}
			prev = dn
		}
		before[t.ID] = true
	}
	return nil
}

func in(closed []string) func(string) bool {
	return func(s string) bool { return contains(closed, s) }
}

// checkSet checks that list is sorted, holds no item twice and holds only
// items ok accepts.
func checkSet(list []string, ok func(string) bool, where string) error {
	for i, s := range list {
		if !ok(s) {
			return invalid(where + " names an item that is not one of its kind")
		}
		if i > 0 && list[i-1] >= s {
			return invalid(where + " is not sorted, or repeats an item")
		}
	}
	return nil
}

// validArea reports whether s is an area name: repo: or store:, then a
// lower-case letter and up to 31 lower-case letters, digits and "-".
func validArea(s string) bool {
	prefix, name, ok := strings.Cut(s, ":")
	return ok && (prefix == "repo" || prefix == "store") && validWord(name, 32)
}

// validCheckID reports whether s is a check id: a lower-case letter and up
// to 31 lower-case letters, digits and "-".
func validCheckID(s string) bool { return validWord(s, 32) }

func validWord(s string, max int) bool {
	if len(s) == 0 || len(s) > max || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}

// checkText holds the rules of a host text, the catalog's: 1 to max bytes
// of plain text, no leading or trailing space, no markup character and no
// web address.
func checkText(s string, max int, where string, refuse func(string) error) error {
	lower := strings.ToLower(s)
	switch {
	case s == "" || len(s) > max:
		return refuse(where + " is empty or longer than " + strconv.Itoa(max) + " bytes")
	case !canon.Plain(s):
		return refuse(where + " holds a control character or bytes that are not UTF-8")
	case strings.TrimSpace(s) != s:
		return refuse(where + " starts or ends with a space")
	case strings.ContainsAny(s, "<>"):
		return refuse(where + " holds a markup character")
	case strings.Contains(lower, "://") || strings.Contains(lower, "www.") || strings.Contains(lower, "mailto:"):
		return refuse(where + " holds a web address")
	}
	return nil
}

// checkNote holds the rules of the owner's text, the brief's: 1 to 200
// bytes of plain text, not only spaces, and nothing shaped like a secret.
func checkNote(s, where string) error {
	switch {
	case len(s) > MaxNoteText:
		return fail(brief.CodeTooLarge, where+" is longer than 200 bytes")
	case strings.TrimSpace(s) == "":
		return invalid(where + " is empty")
	case !canon.Plain(s):
		return invalid(where + " holds a control character or bytes that are not UTF-8")
	case brief.SecretShaped(s):
		return fail(brief.CodeSecretShaped, where+" looks like a key, a token or a password")
	}
	return nil
}

// idNumber returns the number of an id: the letter, then 1 to 3 decimal
// digits without a leading zero.
func idNumber(id string, letter byte) (int, bool) {
	if len(id) < 2 || len(id) > 4 || id[0] != letter || id[1] == '0' {
		return 0, false
	}
	n := 0
	for i := 1; i < len(id); i++ {
		c := id[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func bandOf(id string) (Band, bool) {
	for _, b := range Bands {
		if b.ID == id {
			return b, true
		}
	}
	return Band{}, false
}

// Encode returns the canonical form of a valid plan: keys sorted, no
// whitespace.
func Encode(p Plan) ([]byte, error) {
	if err := Check(p); err != nil {
		return nil, err
	}
	out, err := canon.Encode(planValue(p))
	if err != nil {
		return nil, invalid("the plan does not encode: " + err.Error())
	}
	if len(out) > MaxPlanBytes {
		return nil, fail(brief.CodeTooLarge, "the plan is larger than 12288 bytes")
	}
	return out, nil
}

// Digest returns the SHA-256 of the canonical form of a valid plan, in
// lower-case hexadecimal. The revision number and the digest of the
// revision before are part of it.
func Digest(p Plan) (string, error) {
	out, err := Encode(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), nil
}

func planValue(p Plan) canon.Value {
	ms := make([]canon.Value, len(p.Milestones))
	for i, m := range p.Milestones {
		ts := make([]canon.Value, len(m.Tasks))
		for j, t := range m.Tasks {
			ts[j] = canon.Obj(
				canon.M("id", canon.Str(t.ID)),
				canon.M("title", canon.Str(t.Title)),
				canon.M("kind", canon.Str(string(t.Kind))),
				canon.M("size", canon.Str(string(t.Size))),
				canon.M("depends_on", canon.Strs(t.DependsOn)),
				canon.M("scope", canon.Strs(t.Scope)),
				canon.M("checks", canon.Strs(t.Checks)),
			)
		}
		ms[i] = canon.Obj(
			canon.M("id", canon.Str(m.ID)),
			canon.M("title", canon.Str(m.Title)),
			canon.M("goal", canon.Str(m.Goal)),
			canon.M("tasks", canon.List(ts...)),
		)
	}
	notes := make([]canon.Value, len(p.Notes))
	for i, n := range p.Notes {
		notes[i] = canon.Obj(
			canon.M("field", canon.Str(n.Field)),
			canon.M("text", canon.Str(string(n.Text))),
			canon.M("untrusted", canon.Boolean(true)),
		)
	}
	return canon.Obj(
		canon.M("schema", canon.Str(p.Schema)),
		canon.M("plan", canon.Num(int64(p.Revision))),
		canon.M("prev_digest", canon.Str(p.PrevDigest)),
		canon.M("brief_digest", canon.Str(p.BriefDigest)),
		canon.M("catalog_digest", canon.Str(p.CatalogDigest)),
		canon.M("template", canon.Obj(
			canon.M("id", canon.Str(p.Template.ID)),
			canon.M("version", canon.Str(p.Template.Version)),
		)),
		canon.M("template_digest", canon.Str(p.TemplateDigest)),
		canon.M("title", canon.Str(p.Title)),
		canon.M("milestones", canon.List(ms...)),
		canon.M("gates", canon.Strs(p.Gates)),
		canon.M("first_demo", canon.Str(p.FirstDemo)),
		canon.M("review_points", canon.Strs(p.ReviewPoints)),
		canon.M("hosts", canon.Str(p.Hosts)),
		canon.M("external_services", canon.Strs(p.ExternalServices)),
		canon.M("budget_band", canon.Obj(
			canon.M("id", canon.Str(p.BudgetBand)),
			canon.M("enforcement", canon.Str(Enforcement)),
		)),
		canon.M("estimate", canon.Obj(
			canon.M("band", canon.Str(p.Estimate.Band)),
			canon.M("label", canon.Str(p.Estimate.Label)),
			canon.M("estimate", canon.Boolean(true)),
			canon.M("caveat", canon.Str(EstimateCaveat)),
		)),
		canon.M("warnings", canon.Strs(p.Warnings)),
		canon.M("notes", canon.List(notes...)),
		canon.M("notes_dropped", canon.Num(int64(p.NotesDropped))),
	)
}

// Decode reads a plan strictly: it refuses data over the cap, data that is
// not one JSON object under the strict rules (no float, no null, no
// control character, no repeated key), an unknown or missing field, and a
// plan that breaks any rule of [Check]. It accepts any JSON spelling of a
// valid plan; decoding a canonical plan and encoding it gives the same
// bytes. It reads no template.
func Decode(data []byte) (Plan, error) {
	if len(data) > MaxPlanBytes {
		return Plan{}, fail(brief.CodeTooLarge, "the plan is larger than 12288 bytes")
	}
	v, err := canon.Parse(data, maxDepth)
	if err != nil {
		return Plan{}, invalid(err.Error())
	}
	p, err := decodePlan(v)
	if err != nil {
		return Plan{}, err
	}
	if err := Check(p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// wrap turns an error of the strict reader into a refusal.
func wrap(err error) error { return invalid(err.Error()) }

func decodePlan(v canon.Value) (Plan, error) {
	m, err := canon.Fields(v, "plan", []string{
		"schema", "plan", "prev_digest", "brief_digest", "catalog_digest", "template", "template_digest", "title",
		"milestones", "gates", "first_demo", "review_points", "hosts", "external_services", "budget_band",
		"estimate", "warnings", "notes", "notes_dropped",
	}, nil)
	if err != nil {
		return Plan{}, wrap(err)
	}
	var p Plan
	str := func(key string, dst *string) {
		if err == nil {
			*dst, err = canon.AsString(m[key], "plan."+key)
		}
	}
	strs := func(key string, dst *[]string, max int) {
		if err == nil {
			*dst, err = canon.AsStrings(m[key], "plan."+key, max)
		}
	}
	str("schema", &p.Schema)
	str("prev_digest", &p.PrevDigest)
	str("brief_digest", &p.BriefDigest)
	str("catalog_digest", &p.CatalogDigest)
	str("template_digest", &p.TemplateDigest)
	str("title", &p.Title)
	str("first_demo", &p.FirstDemo)
	str("hosts", &p.Hosts)
	strs("gates", &p.Gates, MaxTasks*4)
	strs("review_points", &p.ReviewPoints, len(ReviewPoints))
	strs("external_services", &p.ExternalServices, len(Services))
	strs("warnings", &p.Warnings, len(Warnings))
	if err == nil {
		p.Revision, err = canon.AsInt(m["plan"], "plan.plan", 1, MaxRevision)
	}
	if err == nil {
		p.NotesDropped, err = canon.AsInt(m["notes_dropped"], "plan.notes_dropped", 0, 1<<16)
	}
	if err != nil {
		return Plan{}, wrap(err)
	}
	tm, err := canon.Fields(m["template"], "plan.template", []string{"id", "version"}, nil)
	if err != nil {
		return Plan{}, wrap(err)
	}
	if p.Template.ID, err = canon.AsString(tm["id"], "plan.template.id"); err != nil {
		return Plan{}, wrap(err)
	}
	if p.Template.Version, err = canon.AsString(tm["version"], "plan.template.version"); err != nil {
		return Plan{}, wrap(err)
	}
	bm, err := canon.Fields(m["budget_band"], "plan.budget_band", []string{"id", "enforcement"}, nil)
	if err != nil {
		return Plan{}, wrap(err)
	}
	if p.BudgetBand, err = canon.AsString(bm["id"], "plan.budget_band.id"); err != nil {
		return Plan{}, wrap(err)
	}
	if e, err := canon.AsString(bm["enforcement"], "plan.budget_band.enforcement"); err != nil || e != Enforcement {
		return Plan{}, invalid("plan.budget_band.enforcement is not advisory")
	}
	em, err := canon.Fields(m["estimate"], "plan.estimate", []string{"band", "label", "estimate", "caveat"}, nil)
	if err != nil {
		return Plan{}, wrap(err)
	}
	if p.Estimate.Band, err = canon.AsString(em["band"], "plan.estimate.band"); err != nil {
		return Plan{}, wrap(err)
	}
	if p.Estimate.Label, err = canon.AsString(em["label"], "plan.estimate.label"); err != nil {
		return Plan{}, wrap(err)
	}
	if is, err := canon.AsBool(em["estimate"], "plan.estimate.estimate"); err != nil || !is {
		return Plan{}, invalid("plan.estimate.estimate is not true")
	}
	if c, err := canon.AsString(em["caveat"], "plan.estimate.caveat"); err != nil || c != EstimateCaveat {
		return Plan{}, invalid("plan.estimate.caveat is not the words of an estimate")
	}
	mv := m["milestones"]
	if mv.Kind != canon.Array {
		return Plan{}, invalid("plan.milestones is not a list")
	}
	if len(mv.Arr) > MaxMilestones {
		return Plan{}, invalid("plan.milestones does not hold 1 to 4 milestones")
	}
	p.Milestones = make([]Milestone, len(mv.Arr))
	for i, e := range mv.Arr {
		if p.Milestones[i], err = decodeMilestone(e, "plan.milestones["+strconv.Itoa(i)+"]"); err != nil {
			return Plan{}, err
		}
	}
	nv := m["notes"]
	if nv.Kind != canon.Array {
		return Plan{}, invalid("plan.notes is not a list")
	}
	if len(nv.Arr) > MaxNotes {
		return Plan{}, fail(brief.CodeTooLarge, "the plan holds more than 8 notes")
	}
	p.Notes = make([]Note, len(nv.Arr))
	for i, e := range nv.Arr {
		where := "plan.notes[" + strconv.Itoa(i) + "]"
		nm, err := canon.Fields(e, where, []string{"field", "text", "untrusted"}, nil)
		if err != nil {
			return Plan{}, wrap(err)
		}
		if p.Notes[i].Field, err = canon.AsString(nm["field"], where+".field"); err != nil {
			return Plan{}, wrap(err)
		}
		text, err := canon.AsString(nm["text"], where+".text")
		if err != nil {
			return Plan{}, wrap(err)
		}
		p.Notes[i].Text = brief.Untrusted(text)
		if u, err := canon.AsBool(nm["untrusted"], where+".untrusted"); err != nil || !u {
			return Plan{}, invalid(where + ".untrusted is not true")
		}
	}
	return p, nil
}

func decodeMilestone(v canon.Value, where string) (Milestone, error) {
	m, err := canon.Fields(v, where, []string{"id", "title", "goal", "tasks"}, nil)
	if err != nil {
		return Milestone{}, wrap(err)
	}
	var ms Milestone
	for _, key := range []string{"id", "title", "goal"} {
		dst := map[string]*string{"id": &ms.ID, "title": &ms.Title, "goal": &ms.Goal}[key]
		if *dst, err = canon.AsString(m[key], where+"."+key); err != nil {
			return Milestone{}, wrap(err)
		}
	}
	tv := m["tasks"]
	if tv.Kind != canon.Array {
		return Milestone{}, invalid(where + ".tasks is not a list")
	}
	if len(tv.Arr) > MaxTasksPerMilestone {
		return Milestone{}, invalid(where + ".tasks does not hold 1 to 6 tasks")
	}
	ms.Tasks = make([]Task, len(tv.Arr))
	for i, e := range tv.Arr {
		tw := where + ".tasks[" + strconv.Itoa(i) + "]"
		tm, err := canon.Fields(e, tw, []string{"id", "title", "kind", "size", "depends_on", "scope", "checks"}, nil)
		if err != nil {
			return Milestone{}, wrap(err)
		}
		t := &ms.Tasks[i]
		var kind, size string
		for _, key := range []string{"id", "title", "kind", "size"} {
			dst := map[string]*string{"id": &t.ID, "title": &t.Title, "kind": &kind, "size": &size}[key]
			if *dst, err = canon.AsString(tm[key], tw+"."+key); err != nil {
				return Milestone{}, wrap(err)
			}
		}
		t.Kind, t.Size = Kind(kind), Size(size)
		for _, key := range []string{"depends_on", "scope", "checks"} {
			dst := map[string]*[]string{"depends_on": &t.DependsOn, "scope": &t.Scope, "checks": &t.Checks}[key]
			if *dst, err = canon.AsStrings(tm[key], tw+"."+key, MaxTasks*2); err != nil {
				return Milestone{}, wrap(err)
			}
		}
	}
	return ms, nil
}

func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validTemplateID(s string) bool { return validWord(s, 32) }

func validVersion(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && !(i > 0 && (c == '.' || c == '-')) {
			return false
		}
	}
	return true
}

// sortedUnique returns list sorted with no item twice, never nil.
func sortedUnique(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		if !contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
