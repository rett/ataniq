package plan

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"ataniq/internal/genesis/internal/canon"
)

// The caps of a diff.
const (
	// MaxDiffEntries is the most changes a diff lists, fields, milestones
	// and tasks together. The rest are counted in More.
	MaxDiffEntries = 48
	// MaxShort is the cap of a short text in a diff, in bytes.
	MaxShort = 80
)

// ChangeOp says what happened to a milestone or a task.
type ChangeOp string

// The changes of a milestone or a task.
const (
	Added   ChangeOp = "added"
	Removed ChangeOp = "removed"
	Changed ChangeOp = "changed"
)

// ItemChange is a milestone or a task that was added, removed or changed,
// by id.
type ItemChange struct {
	// ID is the milestone's or the task's id.
	ID string
	// Op says what happened.
	Op ChangeOp
	// Old and New are its title before and after, short; Old is empty
	// when it was added and New when it was removed.
	Old, New string
	// Members name what changed in it, sorted: for a milestone title, goal
	// and tasks; for a task title, kind, tier, depends_on, scope, checks
	// and milestone. Empty unless Op is [Changed].
	Members []string
}

// FieldChange is a top-level field of the plan that changed.
type FieldChange struct {
	// Name is the field's name in the record.
	Name string
	// Old and New are its value before and after, as short text. The
	// owner's notes are given by their count, never their text.
	Old, New string
}

// Changes is what changed from one revision of a plan to another: the
// milestones and tasks added, removed and changed, by id, and the top-level
// fields changed, by name, each with short old and new texts. It holds no
// text of the owner's.
type Changes struct {
	// From and To are the revisions compared.
	From, To int
	// FromDigest and ToDigest are their digests; empty for a plan that
	// does not hold to the record's rules.
	FromDigest, ToDigest string
	// Fields are the top-level fields changed, by name.
	Fields []FieldChange
	// Milestones are the milestones changed, by id.
	Milestones []ItemChange
	// Tasks are the tasks changed, by id.
	Tasks []ItemChange
	// More counts the changes past [MaxDiffEntries], not listed.
	More int
}

// Empty reports whether nothing changed but the revision.
func (d Changes) Empty() bool {
	return len(d.Fields) == 0 && len(d.Milestones) == 0 && len(d.Tasks) == 0 && d.More == 0
}

// Diff compares two plans, prev the one the owner last saw and next the
// one to show against it. The revision number is not a changed field: it is
// From and To. Within the cap, fields come first, then milestones, then
// tasks, each in order of name or id.
func Diff(prev, next Plan) Changes {
	d := Changes{From: prev.Revision, To: next.Revision}
	d.FromDigest, _ = Digest(prev)
	d.ToDigest, _ = Digest(next)
	fields := fieldChanges(prev, next)
	ms := milestoneChanges(prev.Milestones, next.Milestones)
	ts := taskChanges(prev, next)
	room := MaxDiffEntries
	take := func(n int) int {
		k := min(n, room)
		room -= k
		d.More += n - k
		return k
	}
	d.Fields = fields[:take(len(fields))]
	d.Milestones = ms[:take(len(ms))]
	d.Tasks = ts[:take(len(ts))]
	return d
}

// summary is the short text of each top-level field, by name.
func summary(p Plan) map[string]string {
	notes := count(len(p.Notes), "note")
	if p.NotesDropped > 0 {
		notes += " and " + strconv.Itoa(p.NotesDropped) + " not quoted"
	}
	return map[string]string{
		"brief_digest":      shortDigest(p.BriefDigest),
		"catalog_digest":    shortDigest(p.CatalogDigest),
		"estimate":          p.Estimate.Label,
		"external_services": strings.Join(p.ExternalServices, ", "),
		"first_demo":        p.FirstDemo,
		"gates":             strings.Join(p.Gates, ", "),
		"hosts":             p.Hosts,
		"never":             strings.Join(p.Never, ", "),
		"notes":             notes,
		"review_points":     strings.Join(p.ReviewPoints, ", "),
		"spend":             p.Spend,
		"template":          p.Template.ID + " " + p.Template.Version + " " + shortDigest(p.Template.Digest),
		"title":             p.Title,
	}
}

func fieldChanges(prev, next Plan) []FieldChange {
	a, b := summary(prev), summary(next)
	names := make([]string, 0, len(a))
	for name := range a {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []FieldChange{}
	for _, name := range names {
		changed := a[name] != b[name]
		if name == "notes" && !changed && !sameNotes(prev.Notes, next.Notes) {
			changed = true
			b[name] += ", changed"
		}
		if changed {
			out = append(out, FieldChange{Name: name, Old: short(a[name]), New: short(b[name])})
		}
	}
	return out
}

func sameNotes(a, b []Note) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func milestoneChanges(prev, next []Milestone) []ItemChange {
	a, b := map[string]Milestone{}, map[string]Milestone{}
	for _, m := range prev {
		a[m.ID] = m
	}
	for _, m := range next {
		b[m.ID] = m
	}
	out := []ItemChange{}
	for _, id := range unionIDs(a, b) {
		x, inA := a[id]
		y, inB := b[id]
		switch {
		case !inB:
			out = append(out, ItemChange{ID: id, Op: Removed, Old: short(x.Title), Members: []string{}})
		case !inA:
			out = append(out, ItemChange{ID: id, Op: Added, New: short(y.Title), Members: []string{}})
		default:
			var members []string
			if x.Title != y.Title {
				members = append(members, "title")
			}
			if x.Goal != y.Goal {
				members = append(members, "goal")
			}
			if !sameStrings(taskIDs(x.Tasks), taskIDs(y.Tasks)) {
				members = append(members, "tasks")
			}
			if len(members) > 0 {
				sort.Strings(members)
				out = append(out, ItemChange{ID: id, Op: Changed, Old: short(x.Title), New: short(y.Title), Members: members})
			}
		}
	}
	return out
}

type placed struct {
	Task
	milestone string
}

func taskChanges(prev, next Plan) []ItemChange {
	index := func(p Plan) map[string]placed {
		out := map[string]placed{}
		for _, m := range p.Milestones {
			for _, t := range m.Tasks {
				out[t.ID] = placed{t, m.ID}
			}
		}
		return out
	}
	a, b := index(prev), index(next)
	out := []ItemChange{}
	for _, id := range unionIDs(a, b) {
		x, inA := a[id]
		y, inB := b[id]
		switch {
		case !inB:
			out = append(out, ItemChange{ID: id, Op: Removed, Old: short(x.Title), Members: []string{}})
		case !inA:
			out = append(out, ItemChange{ID: id, Op: Added, New: short(y.Title), Members: []string{}})
		default:
			var members []string
			for name, same := range map[string]bool{
				"title":      x.Title == y.Title,
				"kind":       x.Kind == y.Kind,
				"tier":       x.Tier == y.Tier,
				"depends_on": sameStrings(x.DependsOn, y.DependsOn),
				"scope":      sameStrings(x.Scope, y.Scope),
				"checks":     sameStrings(x.Checks, y.Checks),
				"milestone":  x.milestone == y.milestone,
			} {
				if !same {
					members = append(members, name)
				}
			}
			if len(members) > 0 {
				sort.Strings(members)
				out = append(out, ItemChange{ID: id, Op: Changed, Old: short(x.Title), New: short(y.Title), Members: members})
			}
		}
	}
	return out
}

// unionIDs returns the ids of both maps in the order of their numbers; an
// id that is not of the form is placed after the others, by its bytes.
func unionIDs[V any](a, b map[string]V) []string {
	var ids []string
	for id := range a {
		ids = append(ids, id)
	}
	for id := range b {
		if _, ok := a[id]; !ok {
			ids = append(ids, id)
		}
	}
	num := func(id string) int {
		if len(id) > 0 {
			if n, ok := idNumber(id, id[0]); ok {
				return n
			}
		}
		return 1 << 30
	}
	sort.Slice(ids, func(i, j int) bool {
		ni, nj := num(ids[i]), num(ids[j])
		if ni != nj {
			return ni < nj
		}
		return ids[i] < ids[j]
	})
	return ids
}

func taskIDs(ts []Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// count says n of a thing, in words: "1 note", "2 notes".
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}

func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

// short cuts s to [MaxShort] bytes on a character boundary, ending a cut
// text with "...", and drops what cannot stand in the data.
func short(s string) string {
	if !canon.Plain(s) {
		return "(not shown)"
	}
	if len(s) <= MaxShort {
		return s
	}
	cut := MaxShort - 3
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// EncodeDiff returns the canonical form of a diff: keys sorted, no
// whitespace. The same two plans give the same bytes.
func EncodeDiff(d Changes) ([]byte, error) {
	items := func(cs []ItemChange) canon.Value {
		vs := make([]canon.Value, len(cs))
		for i, c := range cs {
			vs[i] = canon.Obj(
				canon.M("id", canon.Str(c.ID)),
				canon.M("op", canon.Str(string(c.Op))),
				canon.M("old", canon.Str(c.Old)),
				canon.M("new", canon.Str(c.New)),
				canon.M("members", canon.Strs(c.Members)),
			)
		}
		return canon.List(vs...)
	}
	fields := make([]canon.Value, len(d.Fields))
	for i, f := range d.Fields {
		fields[i] = canon.Obj(
			canon.M("name", canon.Str(f.Name)),
			canon.M("old", canon.Str(f.Old)),
			canon.M("new", canon.Str(f.New)),
		)
	}
	out, err := canon.Encode(canon.Obj(
		canon.M("from", canon.Num(int64(d.From))),
		canon.M("to", canon.Num(int64(d.To))),
		canon.M("from_digest", canon.Str(d.FromDigest)),
		canon.M("to_digest", canon.Str(d.ToDigest)),
		canon.M("fields", canon.List(fields...)),
		canon.M("milestones", items(d.Milestones)),
		canon.M("tasks", items(d.Tasks)),
		canon.M("more", canon.Num(int64(d.More))),
	))
	if err != nil {
		return nil, invalid("the diff does not encode: " + err.Error())
	}
	return out, nil
}
