package brief

import (
	"strings"
	"time"
)

// Schema is the schema name of the record.
const Schema = "ataniq.project-brief/1"

// The caps of a brief.
const (
	// MaxFields is the most fields a brief holds.
	MaxFields = 64
	// MaxFieldBytes is the cap of one field in the canonical form.
	MaxFieldBytes = 4096
	// MaxBriefBytes is the cap of the whole brief in the canonical form.
	MaxBriefBytes = 16384
	// MaxNotes is the most notes a brief holds.
	MaxNotes = 8
	// MaxHistory is the most history entries a brief keeps. Past it, and
	// past the size cap, the oldest entries are dropped and counted.
	MaxHistory = 32
	// MaxRound is the highest round number a field may carry. The walker
	// holds a much lower limit of its own.
	MaxRound = 64
)

// Provenance says who set a field. It is never upgraded by reading a
// brief: a field decoded as inferred stays inferred.
type Provenance string

// The provenances of a field.
const (
	// OwnerChosen is a value the owner picked.
	OwnerChosen Provenance = "owner-chosen"
	// SupervisorInferred is a value picked for the owner, which counts
	// towards readiness only once the owner confirms it.
	SupervisorInferred Provenance = "supervisor-inferred"
	// Default is the default value of a question the owner skipped, or
	// left to the default.
	Default Provenance = "default"
)

// Valid reports whether p is one of the provenances.
func (p Provenance) Valid() bool {
	return p == OwnerChosen || p == SupervisorInferred || p == Default
}

// Reason says why a value left the live fields for the history.
type Reason string

// The reasons a value is kept in the history.
const (
	// ReasonReplaced is an answer changed to another value.
	ReasonReplaced Reason = "replaced"
	// ReasonCleared is a value cleared on its own.
	ReasonCleared Reason = "cleared"
	// ReasonInvalidated is a value cleared because an earlier answer it
	// depended on changed. The change names that earlier field as its
	// cause.
	ReasonInvalidated Reason = "invalidated"
)

func (r Reason) valid() bool {
	return r == ReasonReplaced || r == ReasonCleared || r == ReasonInvalidated
}

// The domains of a brief: its kind, which decides the one block of domain
// extension fields it may hold.
const (
	DomainSoftware   = "software"
	DomainPublishing = "publishing"
	DomainMedia      = "media"
	DomainOther      = "other"
)

// TopFields are the domain-neutral top-level field names.
var TopFields = []string{
	"audience", "budget_band", "deliverables", "formats", "goal", "hosts",
	"kind", "quality_bar", "review_points", "schedule",
}

// FieldKind is the name of the field that holds the project's kind.
const FieldKind = "kind"

// Brief is the project brief, ataniq.project-brief/1: the typed record an
// interview fills. It grants nothing: an answer writes a field and nothing
// else.
type Brief struct {
	// Schema is always [Schema].
	Schema string
	// Catalog is the digest of the question catalog the brief was started
	// under: 64 lower-case hexadecimal digits.
	Catalog string
	// Kind is the brief's domain, derived from the kind field: software,
	// publishing, media, other, or "" while the kind is not chosen.
	Kind string
	// Fields are the live values, by field name.
	Fields map[string]Field
	// Notes are free texts, at most [MaxNotes].
	Notes []Untrusted
	// History keeps every value that left the live fields, oldest first.
	History []Change
	// HistoryDropped counts the oldest history entries dropped to hold
	// the caps.
	HistoryDropped int
	// Rounds is the highest round number any answer was given in.
	Rounds int
}

// Field is one live value with where it came from.
type Field struct {
	// Value is the value.
	Value Value
	// Provenance says who set it.
	Provenance Provenance
	// Confirmed is set only on a supervisor-inferred field the owner
	// confirmed.
	Confirmed bool
	// Node is the id of the catalog question that set it.
	Node string
	// Round is the round it was set in.
	Round int
	// At is when it was set, in UTC to the second.
	At time.Time
}

// Change is one value kept in the history.
type Change struct {
	// Field is the name of the field the value was in.
	Field string
	// Old is the field as it was.
	Old Field
	// Reason says why it left.
	Reason Reason
	// Cause is the field whose change invalidated it; set only with
	// [ReasonInvalidated].
	Cause string
	// At is when it left.
	At time.Time
}

// New returns an empty brief started under the catalog with the digest.
func New(catalog string) Brief {
	return Brief{Schema: Schema, Catalog: catalog, Fields: map[string]Field{}, Notes: []Untrusted{}, History: []Change{}}
}

// ValidFieldName reports whether name is a field of the brief: one of
// [TopFields], or a domain extension field software.x, media.x or
// publishing.x, where x is a lower-case letter followed by lower-case
// letters, digits and "_", and the whole name is at most 41 bytes.
func ValidFieldName(name string) bool {
	for _, f := range TopFields {
		if name == f {
			return true
		}
	}
	return FieldDomain(name) != ""
}

// FieldDomain returns the domain of a well-formed extension field name,
// and "" for any other name.
func FieldDomain(name string) string {
	if len(name) > 41 {
		return ""
	}
	domain, rest, ok := strings.Cut(name, ".")
	if !ok || (domain != DomainSoftware && domain != DomainPublishing && domain != DomainMedia) || rest == "" {
		return ""
	}
	if rest[0] < 'a' || rest[0] > 'z' {
		return ""
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' {
			return ""
		}
	}
	return domain
}

// DomainOf returns the domain a value of the kind field gives: the choice
// itself for software, publishing and media, and other for any other
// choice or free text.
func DomainOf(kind Value) string {
	switch {
	case kind.Kind() == KindOne && (kind.one == DomainSoftware || kind.one == DomainPublishing || kind.one == DomainMedia):
		return kind.one
	default:
		return DomainOther
	}
}

// validNode reports whether id is a node id: a lower-case letter followed
// by up to 47 lower-case letters, digits, "_" and ".".
func validNode(id string) bool {
	if len(id) == 0 || len(id) > 48 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// ValidNodeID reports whether id is a well-formed catalog node id.
func ValidNodeID(id string) bool { return validNode(id) }

// stamp turns at into the brief's form of a time: UTC, to the second.
func stamp(at time.Time) time.Time { return at.UTC().Truncate(time.Second) }

// validTime reports whether t is in the brief's form and can be written.
func validTime(t time.Time) bool {
	return t.Equal(stamp(t)) && t.Location() == time.UTC && t.Year() >= 1 && t.Year() <= 9999
}

// Clone returns a deep copy of b.
func (b Brief) Clone() Brief {
	c := b
	c.Fields = make(map[string]Field, len(b.Fields))
	for k, f := range b.Fields {
		f.Value = f.Value.clone()
		c.Fields[k] = f
	}
	c.Notes = append([]Untrusted{}, b.Notes...)
	c.History = make([]Change, len(b.History))
	for i, h := range b.History {
		h.Old.Value = h.Old.Value.clone()
		c.History[i] = h
	}
	return c
}

// Get returns the live field of the name.
func (b Brief) Get(name string) (Field, bool) {
	f, ok := b.Fields[name]
	return f, ok
}

// Set sets a field. A changed value moves the old one to the history as
// replaced. Setting the kind field derives the brief's [Brief.Kind]; it is
// refused while fields of another domain are set, and so is an extension
// field of another domain than the brief's. Set is refused, and the brief
// left as it was, when the value, provenance, node, round or time is not
// well formed, when a text looks like a secret, or when the brief would go
// over a cap.
func (b *Brief) Set(field string, v Value, p Provenance, node string, round int, at time.Time) error {
	if !ValidFieldName(field) {
		return fail(CodeFieldName, "the field name is not one of the brief")
	}
	f := Field{Value: v.clone(), Provenance: p, Node: node, Round: round, At: stamp(at)}
	if err := f.check("the field"); err != nil {
		return err
	}
	c := b.Clone()
	if old, ok := c.Fields[field]; ok && !old.Value.Equal(v) {
		c.History = append(c.History, Change{Field: field, Old: old, Reason: ReasonReplaced, At: f.At})
	}
	c.Fields[field] = f
	if round > c.Rounds {
		c.Rounds = round
	}
	return b.commit(c)
}

// Confirm marks a supervisor-inferred field as confirmed by the owner.
func (b *Brief) Confirm(field string) error {
	f, ok := b.Fields[field]
	if !ok || f.Provenance != SupervisorInferred {
		return fail(CodeNotInferred, "the field is not set, or was not inferred")
	}
	c := b.Clone()
	f.Confirmed = true
	c.Fields[field] = f
	return b.commit(c)
}

// Clear clears a field and keeps its value in the history as cleared.
// Clearing a field that is not set does nothing.
func (b *Brief) Clear(field string, at time.Time) error {
	if !ValidFieldName(field) {
		return fail(CodeFieldName, "the field name is not one of the brief")
	}
	if _, ok := b.Fields[field]; !ok {
		return nil
	}
	c := b.Clone()
	c.clear(field, ReasonCleared, "", stamp(at))
	return b.commit(c)
}

// ClearClosure clears every field of fields that is set, because the
// earlier field cause changed, and keeps each cleared value in the history
// as invalidated. It returns how many fields it cleared. The cleared
// values are history and never live values again.
func (b *Brief) ClearClosure(fields []string, cause string, at time.Time) (int, error) {
	if !ValidFieldName(cause) {
		return 0, fail(CodeFieldName, "the cause is not a field of the brief")
	}
	for _, f := range fields {
		if !ValidFieldName(f) {
			return 0, fail(CodeFieldName, "a field name is not one of the brief")
		}
	}
	c := b.Clone()
	n := 0
	for _, f := range fields {
		if _, ok := c.Fields[f]; ok {
			c.clear(f, ReasonInvalidated, cause, stamp(at))
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	if err := b.commit(c); err != nil {
		return 0, err
	}
	return n, nil
}

// AddNote adds a free-text note.
func (b *Brief) AddNote(note Untrusted) error {
	if len(b.Notes) >= MaxNotes {
		return fail(CodeTooLarge, "the brief holds 8 notes already")
	}
	if err := checkFree(string(note), "the note"); err != nil {
		return err
	}
	c := b.Clone()
	c.Notes = append(c.Notes, note)
	return b.commit(c)
}

func (b *Brief) clear(field string, reason Reason, cause string, at time.Time) {
	old := b.Fields[field]
	delete(b.Fields, field)
	b.History = append(b.History, Change{Field: field, Old: old, Reason: reason, Cause: cause, At: at})
}

// commit derives the kind of c, trims its history to the caps, checks it
// and, when it holds, makes it b.
func (b *Brief) commit(c Brief) error {
	c.Kind = ""
	if k, ok := c.Fields[FieldKind]; ok {
		c.Kind = DomainOf(k.Value)
	}
	for len(c.History) > MaxHistory {
		c.History = c.History[1:]
		c.HistoryDropped++
	}
	for {
		out, err := encode(c)
		if err != nil {
			return err
		}
		if len(out) <= MaxBriefBytes || len(c.History) == 0 {
			break
		}
		c.History = c.History[1:]
		c.HistoryDropped++
	}
	if err := c.Validate(); err != nil {
		return err
	}
	c.History = append([]Change{}, c.History...)
	*b = c
	return nil
}

// Readiness counts the required fields that are filled: set by the owner,
// set to a default, or inferred and confirmed. An inferred field that is
// not confirmed is not filled. A name listed twice counts once.
func (b Brief) Readiness(required []string) (filled, total int) {
	seen := make(map[string]bool, len(required))
	for _, name := range required {
		if seen[name] {
			continue
		}
		seen[name] = true
		total++
		if f, ok := b.Fields[name]; ok && f.Filled() {
			filled++
		}
	}
	return filled, total
}

// Filled reports whether the field counts towards readiness.
func (f Field) Filled() bool {
	switch f.Provenance {
	case OwnerChosen, Default:
		return true
	case SupervisorInferred:
		return f.Confirmed
	}
	return false
}

// check checks one field on its own.
func (f Field) check(where string) error {
	if err := f.Value.check(where + ".value"); err != nil {
		return err
	}
	switch {
	case !f.Provenance.Valid():
		return fail(CodeValueInvalid, where+".provenance is not one of the provenances")
	case f.Confirmed && f.Provenance != SupervisorInferred:
		return fail(CodeValueInvalid, where+".confirmed is set on a field that was not inferred")
	case !validNode(f.Node):
		return fail(CodeValueInvalid, where+".node is not a node id")
	case f.Round < 1 || f.Round > MaxRound:
		return fail(CodeValueInvalid, where+".round is out of range")
	case !validTime(f.At):
		return fail(CodeValueInvalid, where+".at is not a time in UTC to the second")
	}
	return nil
}
