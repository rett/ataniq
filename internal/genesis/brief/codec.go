package brief

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"time"

	"ataniq/internal/genesis/internal/canon"
)

// timeLayout is the one form of a time in a brief.
const timeLayout = "2006-01-02T15:04:05Z"

// maxDepth is how deep a brief nests: the brief, the history, a change,
// its old field and an other value need five levels.
const maxDepth = 8

// Validate checks every rule of the record: the schema, the catalog
// digest, the kind derived from the kind field, the field names and their
// domain, each field, the notes, the history and the caps.
func (b Brief) Validate() error {
	if b.Schema != Schema {
		return fail(CodeBriefInvalid, "brief.schema is not "+Schema)
	}
	if !isLowerHex64(b.Catalog) {
		return fail(CodeBriefInvalid, "brief.catalog is not a SHA-256 digest in lower-case hexadecimal")
	}
	if b.Rounds < 0 || b.Rounds > MaxRound {
		return fail(CodeBriefInvalid, "brief.rounds is out of range")
	}
	if b.HistoryDropped < 0 {
		return fail(CodeBriefInvalid, "brief.history_dropped is negative")
	}
	if len(b.Fields) > MaxFields {
		return fail(CodeTooLarge, "the brief holds more than 64 fields")
	}
	want := ""
	if k, ok := b.Fields[FieldKind]; ok {
		if k.Value.Kind() == KindMany {
			return fail(CodeValueInvalid, "brief.fields.kind holds several choices")
		}
		want = DomainOf(k.Value)
	}
	if b.Kind != want {
		return fail(CodeBriefInvalid, "brief.kind is not the domain of the kind field")
	}
	for _, name := range sortedNames(b.Fields) {
		f := b.Fields[name]
		if !ValidFieldName(name) {
			return fail(CodeFieldName, "a field name is not one of the brief")
		}
		if d := FieldDomain(name); d != "" && d != b.Kind {
			return fail(CodeDomainMismatch, "an extension field does not belong to the brief's kind")
		}
		if err := f.check("a field"); err != nil {
			return err
		}
		if f.Round > b.Rounds {
			return fail(CodeBriefInvalid, "a field's round is past the brief's rounds")
		}
		out, err := canon.Encode(fieldValue(f))
		if err != nil {
			return fail(CodeBriefInvalid, "a field does not encode")
		}
		if len(out) > MaxFieldBytes {
			return fail(CodeTooLarge, "a field is larger than 4096 bytes")
		}
	}
	if len(b.Notes) > MaxNotes {
		return fail(CodeTooLarge, "the brief holds more than 8 notes")
	}
	for i, n := range b.Notes {
		if err := checkFree(string(n), "brief.notes["+strconv.Itoa(i)+"]"); err != nil {
			return err
		}
	}
	if len(b.History) > MaxHistory {
		return fail(CodeTooLarge, "the brief holds more than 32 history entries")
	}
	for i, h := range b.History {
		where := "brief.history[" + strconv.Itoa(i) + "]"
		switch {
		case !ValidFieldName(h.Field):
			return fail(CodeFieldName, where+".field is not one of the brief")
		case !h.Reason.valid():
			return fail(CodeBriefInvalid, where+".reason is not one of the reasons")
		case (h.Reason == ReasonInvalidated) != (h.Cause != ""):
			return fail(CodeBriefInvalid, where+".cause is set exactly when the reason is invalidated")
		case h.Cause != "" && !ValidFieldName(h.Cause):
			return fail(CodeFieldName, where+".cause is not one of the brief")
		case !validTime(h.At):
			return fail(CodeValueInvalid, where+".at is not a time in UTC to the second")
		case h.Old.Round > b.Rounds:
			return fail(CodeBriefInvalid, where+".old.round is past the brief's rounds")
		}
		if err := h.Old.check(where + ".old"); err != nil {
			return err
		}
	}
	out, err := encode(b)
	if err != nil {
		return err
	}
	if len(out) > MaxBriefBytes {
		return fail(CodeTooLarge, "the brief is larger than 16384 bytes")
	}
	return nil
}

// Encode returns the canonical form of a valid brief: keys sorted, no
// whitespace, no float.
func Encode(b Brief) ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return encode(b)
}

// Digest returns the SHA-256 of the canonical form of a valid brief, in
// lower-case hexadecimal.
func Digest(b Brief) (string, error) {
	out, err := Encode(b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:]), nil
}

// encode writes b without checking it.
func encode(b Brief) ([]byte, error) {
	fields := make([]canon.Member, 0, len(b.Fields))
	for _, name := range sortedNames(b.Fields) {
		fields = append(fields, canon.M(name, fieldValue(b.Fields[name])))
	}
	notes := make([]canon.Value, len(b.Notes))
	for i, n := range b.Notes {
		notes[i] = canon.Str(string(n))
	}
	history := make([]canon.Value, len(b.History))
	for i, h := range b.History {
		ms := []canon.Member{
			canon.M("at", canon.Str(h.At.Format(timeLayout))),
			canon.M("field", canon.Str(h.Field)),
			canon.M("old", fieldValue(h.Old)),
			canon.M("reason", canon.Str(string(h.Reason))),
		}
		if h.Cause != "" {
			ms = append(ms, canon.M("cause", canon.Str(h.Cause)))
		}
		history[i] = canon.Obj(ms...)
	}
	out, err := canon.Encode(canon.Obj(
		canon.M("catalog", canon.Str(b.Catalog)),
		canon.M("fields", canon.Obj(fields...)),
		canon.M("history", canon.List(history...)),
		canon.M("history_dropped", canon.Num(int64(b.HistoryDropped))),
		canon.M("kind", canon.Str(b.Kind)),
		canon.M("notes", canon.List(notes...)),
		canon.M("rounds", canon.Num(int64(b.Rounds))),
		canon.M("schema", canon.Str(b.Schema)),
	))
	if err != nil {
		return nil, fail(CodeBriefInvalid, "the brief does not encode: "+err.Error())
	}
	return out, nil
}

func fieldValue(f Field) canon.Value {
	ms := []canon.Member{
		canon.M("at", canon.Str(f.At.Format(timeLayout))),
		canon.M("node", canon.Str(f.Node)),
		canon.M("provenance", canon.Str(string(f.Provenance))),
		canon.M("round", canon.Num(int64(f.Round))),
		canon.M("value", valueValue(f.Value)),
	}
	if f.Provenance == SupervisorInferred {
		ms = append(ms, canon.M("confirmed", canon.Boolean(f.Confirmed)))
	}
	return canon.Obj(ms...)
}

func valueValue(v Value) canon.Value {
	switch v.kind {
	case KindMany:
		return canon.Strs(v.many)
	case KindOther:
		return canon.Obj(canon.M("other", canon.Str(string(v.other))))
	default:
		return canon.Str(v.one)
	}
}

func sortedNames(m map[string]Field) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Decode reads a brief strictly: it refuses data over the cap, data that
// is not one JSON object under the strict rules (no float, no null, no
// control character, no repeated key), an unknown or missing field, and a
// brief that breaks any rule of [Brief.Validate]. It accepts any JSON
// spelling of a valid brief; decoding a canonical brief and encoding it
// gives the same bytes. It sets no provenance and no confirmation the data
// does not carry.
func Decode(data []byte) (Brief, error) {
	if len(data) > MaxBriefBytes {
		return Brief{}, fail(CodeTooLarge, "the brief is larger than 16384 bytes")
	}
	v, err := canon.Parse(data, maxDepth)
	if err != nil {
		return Brief{}, fail(CodeBriefInvalid, err.Error())
	}
	b, err := decodeBrief(v)
	if err != nil {
		return Brief{}, err
	}
	if err := b.Validate(); err != nil {
		return Brief{}, err
	}
	return b, nil
}

func invalid(err error) error { return fail(CodeBriefInvalid, err.Error()) }

func decodeBrief(v canon.Value) (Brief, error) {
	m, err := canon.Fields(v, "brief",
		[]string{"catalog", "fields", "history", "history_dropped", "kind", "notes", "rounds", "schema"}, nil)
	if err != nil {
		return Brief{}, invalid(err)
	}
	var b Brief
	if b.Schema, err = canon.AsString(m["schema"], "brief.schema"); err != nil {
		return Brief{}, invalid(err)
	}
	if b.Catalog, err = canon.AsString(m["catalog"], "brief.catalog"); err != nil {
		return Brief{}, invalid(err)
	}
	if b.Kind, err = canon.AsString(m["kind"], "brief.kind"); err != nil {
		return Brief{}, invalid(err)
	}
	if b.Rounds, err = canon.AsInt(m["rounds"], "brief.rounds", 0, MaxRound); err != nil {
		return Brief{}, invalid(err)
	}
	if b.HistoryDropped, err = canon.AsInt(m["history_dropped"], "brief.history_dropped", 0, 1<<31-1); err != nil {
		return Brief{}, invalid(err)
	}
	fv := m["fields"]
	if fv.Kind != canon.Object {
		return Brief{}, fail(CodeBriefInvalid, "brief.fields is not an object")
	}
	if len(fv.Obj) > MaxFields {
		return Brief{}, fail(CodeTooLarge, "the brief holds more than 64 fields")
	}
	b.Fields = make(map[string]Field, len(fv.Obj))
	for _, mem := range fv.Obj {
		if !ValidFieldName(mem.Key) {
			return Brief{}, fail(CodeFieldName, "a field name is not one of the brief")
		}
		f, err := decodeField(mem.Val, "brief.fields."+mem.Key)
		if err != nil {
			return Brief{}, err
		}
		b.Fields[mem.Key] = f
	}
	if nv := m["notes"]; nv.Kind == canon.Array && len(nv.Arr) > MaxNotes {
		return Brief{}, fail(CodeTooLarge, "the brief holds more than 8 notes")
	}
	notes, err := canon.AsStrings(m["notes"], "brief.notes", MaxNotes)
	if err != nil {
		return Brief{}, invalid(err)
	}
	b.Notes = make([]Untrusted, len(notes))
	for i, n := range notes {
		b.Notes[i] = Untrusted(n)
	}
	hv := m["history"]
	if hv.Kind != canon.Array {
		return Brief{}, fail(CodeBriefInvalid, "brief.history is not a list")
	}
	if len(hv.Arr) > MaxHistory {
		return Brief{}, fail(CodeTooLarge, "the brief holds more than 32 history entries")
	}
	b.History = make([]Change, len(hv.Arr))
	for i, e := range hv.Arr {
		if b.History[i], err = decodeChange(e, "brief.history["+strconv.Itoa(i)+"]"); err != nil {
			return Brief{}, err
		}
	}
	return b, nil
}

func decodeChange(v canon.Value, where string) (Change, error) {
	m, err := canon.Fields(v, where, []string{"at", "field", "old", "reason"}, []string{"cause"})
	if err != nil {
		return Change{}, invalid(err)
	}
	var c Change
	if c.Field, err = canon.AsString(m["field"], where+".field"); err != nil {
		return Change{}, invalid(err)
	}
	reason, err := canon.AsString(m["reason"], where+".reason")
	if err != nil {
		return Change{}, invalid(err)
	}
	c.Reason = Reason(reason)
	if cv, ok := m["cause"]; ok {
		if c.Cause, err = canon.AsString(cv, where+".cause"); err != nil {
			return Change{}, invalid(err)
		}
		if c.Cause == "" {
			return Change{}, fail(CodeBriefInvalid, where+".cause is empty; an empty cause is left out")
		}
	}
	if c.At, err = decodeTime(m["at"], where+".at"); err != nil {
		return Change{}, err
	}
	if c.Old, err = decodeField(m["old"], where+".old"); err != nil {
		return Change{}, err
	}
	return c, nil
}

// decodeField reads one field. The detail of an error names the place by
// the form's own names; a field name in where has passed ValidFieldName.
func decodeField(v canon.Value, where string) (Field, error) {
	m, err := canon.Fields(v, where, []string{"at", "node", "provenance", "round", "value"}, []string{"confirmed"})
	if err != nil {
		return Field{}, invalid(err)
	}
	var f Field
	p, err := canon.AsString(m["provenance"], where+".provenance")
	if err != nil {
		return Field{}, invalid(err)
	}
	f.Provenance = Provenance(p)
	cv, hasConfirmed := m["confirmed"]
	if hasConfirmed != (f.Provenance == SupervisorInferred) {
		return Field{}, fail(CodeBriefInvalid, where+".confirmed is present exactly on an inferred field")
	}
	if hasConfirmed {
		if f.Confirmed, err = canon.AsBool(cv, where+".confirmed"); err != nil {
			return Field{}, invalid(err)
		}
	}
	if f.Node, err = canon.AsString(m["node"], where+".node"); err != nil {
		return Field{}, invalid(err)
	}
	if f.Round, err = canon.AsInt(m["round"], where+".round", 1, MaxRound); err != nil {
		return Field{}, invalid(err)
	}
	if f.At, err = decodeTime(m["at"], where+".at"); err != nil {
		return Field{}, err
	}
	if f.Value, err = decodeValue(m["value"], where+".value"); err != nil {
		return Field{}, err
	}
	return f, f.check(where)
}

func decodeValue(v canon.Value, where string) (Value, error) {
	switch v.Kind {
	case canon.String:
		return One(v.Str), nil
	case canon.Array:
		ids, err := canon.AsStrings(v, where, MaxChoices)
		if err != nil {
			return Value{}, fail(CodeValueInvalid, err.Error())
		}
		// Kept in the order read: check refuses a list that is not
		// sorted, so a value has one form.
		return Value{kind: KindMany, many: ids}, nil
	case canon.Object:
		m, err := canon.Fields(v, where, []string{"other"}, nil)
		if err != nil {
			return Value{}, invalid(err)
		}
		s, err := canon.AsString(m["other"], where+".other")
		if err != nil {
			return Value{}, invalid(err)
		}
		return OtherText(Untrusted(s)), nil
	}
	return Value{}, fail(CodeValueInvalid, where+" is not a choice, a list of choices or an other text")
}

func decodeTime(v canon.Value, where string) (time.Time, error) {
	s, err := canon.AsString(v, where)
	if err != nil {
		return time.Time{}, invalid(err)
	}
	t, err := time.Parse(timeLayout, s)
	if err != nil || t.Format(timeLayout) != s {
		return time.Time{}, fail(CodeValueInvalid, where+" is not a time in UTC to the second")
	}
	return t, nil
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
