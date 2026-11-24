package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/internal/canon"
)

// timeLayout is the one form of a time: RFC 3339 in UTC to the second.
const timeLayout = "2006-01-02T15:04:05Z"

// maxDepth is how deep a plan nests: the envelope, the plan, the
// milestones, a milestone, its tasks, a task and its lists need seven
// levels.
const maxDepth = 8

func invalid(detail string) error { return fail(brief.CodePlanInvalid, detail) }

// Check checks every rule of the record: the schema, the revision, the
// digests, the template, the texts and their caps, the milestones and the
// tasks (ids unique and in order, every dependency a task of the plan,
// no cycle), the closed lists (kinds, tiers, areas, checks, review points,
// hosts, services, spends, estimate bands and the never-list), and the
// notes. [Decode] holds a plan to it, and [Compile] holds its own result to
// it.
func Check(p Plan) error {
	switch {
	case p.Schema != Schema:
		return invalid("plan.schema is not " + Schema)
	case p.Revision < 1 || p.Revision > MaxRevision:
		return invalid("plan.plan is not a revision from 1 to 9999")
	case !isLowerHex64(p.BriefDigest):
		return invalid("plan.brief_digest is not a SHA-256 digest in lower-case hexadecimal")
	case !isLowerHex64(p.CatalogDigest):
		return invalid("plan.catalog_digest is not a SHA-256 digest in lower-case hexadecimal")
	case !validTemplateID(p.Template.ID):
		return invalid("plan.template.id is not a template id")
	case !validVersion(p.Template.Version):
		return invalid("plan.template.version is not a version id")
	case !isLowerHex64(p.Template.Digest):
		return invalid("plan.template.digest is not a SHA-256 digest in lower-case hexadecimal")
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
	if err := checkSet(p.Gates, Checks, "plan.gates"); err != nil {
		return err
	}
	if len(p.ReviewPoints) == 0 {
		return invalid("plan.review_points is empty")
	}
	if err := checkSet(p.ReviewPoints, ReviewPoints, "plan.review_points"); err != nil {
		return err
	}
	if !contains(Hosts, p.Hosts) {
		return invalid("plan.hosts is not one of the hosts")
	}
	if err := checkSet(p.ExternalServices, Services, "plan.external_services"); err != nil {
		return err
	}
	if !contains(Spends, p.Spend) {
		return invalid("plan.spend is not one of the ceilings")
	}
	if b, ok := bandOf(p.Estimate.Band); !ok || b.Label != p.Estimate.Label {
		return invalid("plan.estimate is not one of the bands with its label")
	}
	if len(p.Never) != len(Never) {
		return invalid("plan.never is not the never-list")
	}
	for i, n := range Never {
		if p.Never[i] != n {
			return invalid("plan.never is not the never-list")
		}
	}
	if len(p.Notes) > MaxNotes {
		return fail(brief.CodeTooLarge, "the plan quotes more than 8 notes")
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
		return invalid("plan.notes_dropped counts notes the plan had room to quote")
	}
	return nil
}

func checkMilestones(ms []Milestone) error {
	if len(ms) == 0 || len(ms) > MaxMilestones {
		return invalid("plan.milestones does not hold 1 to 8 milestones")
	}
	all := map[string]int{}
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
		for _, t := range m.Tasks {
			all[t.ID] = len(tasks)
			tasks = append(tasks, t)
		}
	}
	if len(tasks) > MaxTasks {
		return invalid("the plan holds more than 40 tasks")
	}
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
		if !contains(Tiers, t.Tier) {
			return invalid(where + ".tier is not one of the tiers")
		}
		if len(t.Scope) == 0 {
			return invalid(where + ".scope is empty")
		}
		if err := checkSet(t.Scope, Areas, where+".scope"); err != nil {
			return err
		}
		if err := checkSet(t.Checks, Checks, where+".checks"); err != nil {
			return err
		}
		prev := 0
		for _, d := range t.DependsOn {
			dn, ok := idNumber(d, 't')
			if !ok {
				return invalid(where + ".depends_on names an id that is not a task id")
			}
			if _, ok := all[d]; !ok {
				return invalid(where + ".depends_on names a task the plan does not hold")
			}
			if d == t.ID {
				return invalid(where + ".depends_on names the task itself")
			}
			if dn <= prev {
				return invalid(where + ".depends_on is not sorted, or repeats an id")
			}
			prev = dn
		}
	}
	// The dependencies form no cycle.
	const visiting, done = 1, 2
	state := make([]int, len(tasks))
	var visit func(i int) bool
	visit = func(i int) bool {
		switch state[i] {
		case visiting:
			return false
		case done:
			return true
		}
		state[i] = visiting
		for _, d := range tasks[i].DependsOn {
			if !visit(all[d]) {
				return false
			}
		}
		state[i] = done
		return true
	}
	for i := range tasks {
		if !visit(i) {
			return invalid("the dependencies of the tasks form a cycle")
		}
	}
	return nil
}

// checkSet checks that list is sorted, holds no item twice and holds only
// items of closed.
func checkSet(list, closed []string, where string) error {
	for i, s := range list {
		if !contains(closed, s) {
			return invalid(where + " names an item outside its closed list")
		}
		if i > 0 && list[i-1] >= s {
			return invalid(where + " is not sorted, or repeats an item")
		}
	}
	return nil
}

// checkText holds the rules of a host text: 1 to max bytes of plain text,
// no leading or trailing space, no markup character and no web address.
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
// whitespace. The time of the compile is not part of it.
func Encode(p Plan) ([]byte, error) {
	if err := Check(p); err != nil {
		return nil, err
	}
	out, err := canon.Encode(planValue(p))
	if err != nil {
		return nil, invalid("the plan does not encode: " + err.Error())
	}
	if len(out) > MaxPlanBytes {
		return nil, fail(brief.CodeTooLarge, "the plan is larger than 32768 bytes")
	}
	return out, nil
}

// Digest returns the SHA-256 of the canonical form of a valid plan, in
// lower-case hexadecimal. The revision number is part of it, and the time
// of the compile is not.
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
				canon.M("tier", canon.Str(string(t.Tier))),
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
		canon.M("brief_digest", canon.Str(p.BriefDigest)),
		canon.M("catalog_digest", canon.Str(p.CatalogDigest)),
		canon.M("template", canon.Obj(
			canon.M("id", canon.Str(p.Template.ID)),
			canon.M("version", canon.Str(p.Template.Version)),
			canon.M("digest", canon.Str(p.Template.Digest)),
		)),
		canon.M("title", canon.Str(p.Title)),
		canon.M("milestones", canon.List(ms...)),
		canon.M("gates", canon.Strs(p.Gates)),
		canon.M("first_demo", canon.Str(p.FirstDemo)),
		canon.M("review_points", canon.Strs(p.ReviewPoints)),
		canon.M("hosts", canon.Str(p.Hosts)),
		canon.M("external_services", canon.Strs(p.ExternalServices)),
		canon.M("spend", canon.Str(p.Spend)),
		canon.M("estimate", canon.Obj(
			canon.M("band", canon.Str(p.Estimate.Band)),
			canon.M("label", canon.Str(p.Estimate.Label)),
			canon.M("estimate", canon.Boolean(true)),
			canon.M("caveat", canon.Str(EstimateCaveat)),
		)),
		canon.M("never", canon.Strs(p.Never)),
		canon.M("notes", canon.List(notes...)),
		canon.M("notes_dropped", canon.Num(int64(p.NotesDropped))),
	)
}

// Decode reads a plan strictly: it refuses data over the cap, data that is
// not one JSON object under the strict rules (no float, no null, no
// control character, no repeated key), an unknown or missing field, and a
// plan that breaks any rule of [Check]. It accepts any JSON spelling of a
// valid plan; decoding a canonical plan and encoding it gives the same
// bytes. The plan it returns has no time of compile: that is the
// envelope's.
func Decode(data []byte) (Plan, error) {
	if len(data) > MaxPlanBytes {
		return Plan{}, fail(brief.CodeTooLarge, "the plan is larger than 32768 bytes")
	}
	v, err := canon.Parse(data, maxDepth)
	if err != nil {
		return Plan{}, invalid(err.Error())
	}
	return decodeChecked(v)
}

func decodeChecked(v canon.Value) (Plan, error) {
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
		"schema", "plan", "brief_digest", "catalog_digest", "template", "title", "milestones", "gates",
		"first_demo", "review_points", "hosts", "external_services", "spend", "estimate", "never", "notes",
		"notes_dropped",
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
	str("brief_digest", &p.BriefDigest)
	str("catalog_digest", &p.CatalogDigest)
	str("title", &p.Title)
	str("first_demo", &p.FirstDemo)
	str("hosts", &p.Hosts)
	str("spend", &p.Spend)
	strs("gates", &p.Gates, len(Checks))
	strs("review_points", &p.ReviewPoints, len(ReviewPoints))
	strs("external_services", &p.ExternalServices, len(Services))
	strs("never", &p.Never, len(Never))
	if err == nil {
		p.Revision, err = canon.AsInt(m["plan"], "plan.plan", 1, MaxRevision)
	}
	if err == nil {
		p.NotesDropped, err = canon.AsInt(m["notes_dropped"], "plan.notes_dropped", 0, 1<<16)
	}
	if err != nil {
		return Plan{}, wrap(err)
	}
	tm, err := canon.Fields(m["template"], "plan.template", []string{"id", "version", "digest"}, nil)
	if err != nil {
		return Plan{}, wrap(err)
	}
	for key, dst := range map[string]*string{"id": &p.Template.ID, "version": &p.Template.Version, "digest": &p.Template.Digest} {
		if *dst, err = canon.AsString(tm[key], "plan.template."+key); err != nil {
			return Plan{}, wrap(err)
		}
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
		return Plan{}, invalid("plan.milestones does not hold 1 to 8 milestones")
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
		return Plan{}, fail(brief.CodeTooLarge, "the plan quotes more than 8 notes")
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
	for key, dst := range map[string]*string{"id": &ms.ID, "title": &ms.Title, "goal": &ms.Goal} {
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
		tm, err := canon.Fields(e, tw, []string{"id", "title", "kind", "tier", "depends_on", "scope", "checks"}, nil)
		if err != nil {
			return Milestone{}, wrap(err)
		}
		t := &ms.Tasks[i]
		var kind, tier string
		for key, dst := range map[string]*string{"id": &t.ID, "title": &t.Title, "kind": &kind, "tier": &tier} {
			if *dst, err = canon.AsString(tm[key], tw+"."+key); err != nil {
				return Milestone{}, wrap(err)
			}
		}
		t.Kind, t.Tier = Kind(kind), Tier(tier)
		for key, dst := range map[string]*[]string{"depends_on": &t.DependsOn, "scope": &t.Scope, "checks": &t.Checks} {
			if *dst, err = canon.AsStrings(tm[key], tw+"."+key, MaxTasks); err != nil {
				return Milestone{}, wrap(err)
			}
		}
	}
	return ms, nil
}

// EncodeEnvelope returns the canonical form of a plan in its envelope:
// {"compiled_at", "digest", "plan"}, where compiled_at is the time of the
// compile and digest the plan's [Digest]. The envelope is what a store
// keeps; the plan inside it is what the digest covers.
func EncodeEnvelope(p Plan) ([]byte, error) {
	if !validTime(p.CompiledAt) {
		return nil, invalid("the time of the compile is not a time in UTC to the second")
	}
	if err := Check(p); err != nil {
		return nil, err
	}
	d, err := Digest(p)
	if err != nil {
		return nil, err
	}
	out, err := canon.Encode(canon.Obj(
		canon.M("compiled_at", canon.Str(p.CompiledAt.Format(timeLayout))),
		canon.M("digest", canon.Str(d)),
		canon.M("plan", planValue(p)),
	))
	if err != nil {
		return nil, invalid("the envelope does not encode: " + err.Error())
	}
	return out, nil
}

// DecodeEnvelope reads a plan in its envelope strictly, as [Decode] does,
// and refuses one whose digest is not the plan's. The plan it returns
// carries the time of the compile.
func DecodeEnvelope(data []byte) (Plan, error) {
	if len(data) > MaxEnvelopeBytes {
		return Plan{}, fail(brief.CodeTooLarge, "the envelope is larger than 33024 bytes")
	}
	v, err := canon.Parse(data, maxDepth)
	if err != nil {
		return Plan{}, invalid(err.Error())
	}
	m, err := canon.Fields(v, "envelope", []string{"compiled_at", "digest", "plan"}, nil)
	if err != nil {
		return Plan{}, wrap(err)
	}
	at, err := canon.AsString(m["compiled_at"], "envelope.compiled_at")
	if err != nil {
		return Plan{}, wrap(err)
	}
	t, err := time.Parse(timeLayout, at)
	if err != nil || t.Format(timeLayout) != at {
		return Plan{}, invalid("envelope.compiled_at is not a time in UTC to the second")
	}
	want, err := canon.AsString(m["digest"], "envelope.digest")
	if err != nil {
		return Plan{}, wrap(err)
	}
	p, err := decodeChecked(m["plan"])
	if err != nil {
		return Plan{}, err
	}
	d, err := Digest(p)
	if err != nil {
		return Plan{}, err
	}
	if d != want {
		return Plan{}, invalid("envelope.digest is not the digest of its plan")
	}
	p.CompiledAt = t
	return p, nil
}

// stamp turns at into the plan's form of a time: UTC, to the second.
func stamp(at time.Time) time.Time { return at.UTC().Truncate(time.Second) }

func validTime(t time.Time) bool {
	return t.Equal(stamp(t)) && t.Location() == time.UTC && t.Year() >= 1 && t.Year() <= 9999
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

func validTemplateID(s string) bool {
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
