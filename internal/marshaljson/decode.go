// Package marshaljson reads Qargi J1 evidence. It does not run Qargi.
// Free text in a document is display data, including warnings and handoff notes.
package marshaljson

import (
	"bytes"
	"errors"
)

// Kind selects the expected contract before reading a document.
type Kind string

const (
	Status  Kind = "qargi-marshal-status"
	Check   Kind = "qargi-marshal-check"
	Handoff Kind = "qargi-handoff"
	Refusal Kind = "qargi-refusal"
)

// Revision is an opaque record revision. Revisions support equality only.
// Its zero value denotes an unobserved revision and matches no record.
type Revision struct {
	value    int64
	observed bool
}

// Document holds the known fields of a snapshot. Its zero value is unusable.
// Objects are map[string]any, arrays are []any and integers are int64,
// except for rev, which is a Revision.
type Document struct {
	kind                       Kind
	fields                     map[string]any
	unknown, stale, incomplete bool
}

func (d Document) Kind() Kind       { return d.kind }
func (d Document) Unknown() bool    { return d.unknown }
func (d Document) Stale() bool      { return d.stale }
func (d Document) Incomplete() bool { return d.incomplete }

// Fields returns a deep copy so display code can never alter checked evidence.
func (d Document) Fields() map[string]any {
	if d.fields == nil {
		return nil
	}
	return copyField(d.fields).(map[string]any)
}

func copyField(v any) any {
	switch v := v.(type) {
	case map[string]any:
		fields := make(map[string]any, len(v))
		for k, child := range v {
			fields[k] = copyField(child)
		}
		return fields
	case []any:
		items := make([]any, len(v))
		for i, child := range v {
			items[i] = copyField(child)
		}
		return items
	default:
		return v
	}
}

// ActionsAllowed reports whether the whole document can support a dependent
// action. The caller must also check its project, target and authorization.
func (d Document) ActionsAllowed() bool {
	return (d.kind == Status || d.kind == Check || d.kind == Handoff) &&
		d.fields != nil && !d.unknown && !d.stale && !d.incomplete
}

// AllowsRevision checks evidence for an action that also depends on a
// previously observed record. J1 supplies no ordering or lease fence for rev.
func (d Document) AllowsRevision(project, feature string, expected Revision) bool {
	if !expected.observed || !d.ActionsAllowed() || d.fields["project"] != project {
		return false
	}
	rows, _ := d.fields["features"].([]any)
	found := false
	for _, item := range rows {
		row := item.(map[string]any)
		name := row["feature"]
		if d.kind == Handoff {
			name = row["name"]
			row = row["record"].(map[string]any)
		}
		if name == feature {
			if found || row["status"] == "unled" || row["has_record"] == false || row["rev"] != expected {
				return false
			}
			found = true
		}
	}
	return found
}

// RefusalReason classifies a decoded refusal by its catalogue code. The
// original code, including an unfamiliar one, is available in Fields()["code"].
func (d Document) RefusalReason() string {
	if d.kind != Refusal {
		return ""
	}
	switch d.fields["code"] {
	case "marshal-feature-held":
		return "conflict"
	case "marshal-feature-unknown":
		return "not-found"
	case "marshal-remote-unreachable":
		return "remote-unreachable"
	default:
		return "refused"
	}
}

// Decode reads one complete document of the expected kind, including the
// version prefix for a refusal line. It checks Unicode, duplicate keys and
// exact integers throughout the input, including in unrecognized fields.
// An error always returns the zero document. Use DecodeResult for verb output.
func Decode(expected Kind, data []byte) (Document, error) {
	s := schemas[expected]
	if s == nil {
		return Document{}, errors.New("unsupported J1 kind")
	}
	if expected == Refusal {
		line := bytes.TrimSuffix(data, []byte{'\n'})
		body, ok := bytes.CutPrefix(line, []byte("qargi-refusal/1 "))
		if !ok || len(line) >= 4096 || bytes.IndexByte(body, '\n') >= 0 {
			return Document{}, errors.New("invalid refusal line")
		}
		data = body
	}
	fields, err := object(data)
	if err != nil {
		return Document{}, err
	}
	// These two markers are the only const fields in the four registries.
	if fields["kind"] != string(expected) || fields["schema"] != int64(1) {
		return Document{}, errors.New("unexpected kind or unsupported schema")
	}
	d := Document{kind: expected, fields: fields}
	if err := s.check(fields, s.Defs, &d.unknown); err != nil {
		return Document{}, err
	}
	if expected == Refusal {
		switch fields["exit"] {
		case int64(1), int64(2), int64(3), int64(77):
		default:
			return Document{}, errors.New("invalid J1 refusal exit")
		}
	} else {
		d.assess()
	}
	return d, nil
}

// DecodeResult reads a completed status, check or handoff call. A nonzero
// status requires one refusal line on stderr and an equal exit field. Warnings
// may precede the line. Success bytes left by a failed write are never used.
func DecodeResult(expected Kind, exit int, stdout, stderr []byte) (Document, error) {
	if expected != Status && expected != Check && expected != Handoff {
		return Document{}, errors.New("unexpected verb result kind")
	}
	var refusal []byte
	var warning bool
	for line := range bytes.SplitSeq(stderr, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("qargi-refusal/")) {
			if refusal != nil || exit == 0 {
				return Document{}, errors.New("ambiguous verb output")
			}
			refusal = line
		} else if len(line) > 0 {
			warning = true
		}
	}
	if exit == 0 {
		d, err := Decode(expected, stdout)
		if err == nil && warning {
			d.incomplete = true
		}
		return d, err
	}
	if refusal == nil || len(stdout) != 0 {
		return Document{}, errors.New("failed verb without an unambiguous refusal")
	}
	d, err := Decode(Refusal, refusal)
	if err != nil {
		return Document{}, err
	}
	if d.fields["exit"] != int64(exit) {
		return Document{}, errors.New("refusal exit differs from process status")
	}
	return d, nil
}

func nonemptyList(v any) bool {
	items, _ := v.([]any)
	return len(items) != 0
}

func (d *Document) assess() {
	f := d.fields
	remote := f["remote"]
	d.stale = remote == "unchecked" || remote == "not-asked" || f["answer"] == "free-stale"
	d.incomplete = f["backend_fallback"] == true || f["truncated"] == true ||
		(f["backend"] == "local") != (remote == "none") || nonemptyList(f["warnings"])
	rows, hasRows := f["features"].([]any)
	if d.kind == Handoff && (f["features_omitted"] != nil || f["features_left_out"] != nil ||
		f["features_left_out_more"] != nil || f["lists_left_out"] != nil) {
		// Each field describes an omission, even if the summary flag disagrees.
		d.incomplete = true
	}
	if d.kind == Check {
		named := f["feature"] != nil
		if named == hasRows || named != (f["answer"] != nil) ||
			(f["answer"] == "free-stale") != (f["lead"] != nil) {
			d.incomplete = true
		}
	}
	for _, item := range rows {
		row := item.(map[string]any)
		if d.kind == Handoff {
			row = row["record"].(map[string]any)
			data := row["data"].(map[string]any)
			d.incomplete = d.incomplete || nonemptyList(data["truncated"])
		} else if row["project"] != f["project"] || nonemptyList(row["warnings"]) {
			d.incomplete = true
		}
		row["rev"] = Revision{value: row["rev"].(int64), observed: true}
		d.stale = d.stale || row["lease"] == "stale"
		d.incomplete = d.incomplete || row["provisional"] == true ||
			row["integrity"] == "INVALID" || row["integrity"] == "cannot verify"
	}
}
