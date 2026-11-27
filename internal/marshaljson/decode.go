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
type Revision struct{ value int64 }

// Document holds the known fields of a snapshot. Treat them as read-only.
// Objects are map[string]any, arrays are []any and integers are int64,
// except for rev, which is a Revision.
type Document struct {
	Kind                       Kind
	Fields                     map[string]any
	Unknown, Stale, Incomplete bool
}

// ActionsAllowed reports whether the whole document can support a dependent
// action. The caller must also check its project, target and authorization.
func (d Document) ActionsAllowed() bool {
	return (d.Kind == Status || d.Kind == Check || d.Kind == Handoff) &&
		d.Fields != nil && !d.Unknown && !d.Stale && !d.Incomplete
}

// AllowsRevision checks evidence for an action that also depends on a
// previously observed record. J1 supplies no ordering or lease fence for rev.
func (d Document) AllowsRevision(project, feature string, expected Revision) bool {
	if !d.ActionsAllowed() || d.Fields["project"] != project {
		return false
	}
	rows, _ := d.Fields["features"].([]any)
	found := false
	for _, item := range rows {
		row := item.(map[string]any)
		name := row["feature"]
		if d.Kind == Handoff {
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
// original code, including an unfamiliar one, is available in Fields["code"].
func (d Document) RefusalReason() string {
	if d.Kind != Refusal {
		return ""
	}
	switch d.Fields["code"] {
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
	d := Document{Kind: expected, Fields: fields}
	if err := s.check(fields, s.Defs, &d.Unknown); err != nil {
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
	for line := range bytes.SplitSeq(stderr, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("qargi-refusal/")) {
			if refusal != nil || exit == 0 {
				return Document{}, errors.New("ambiguous verb output")
			}
			refusal = line
		}
	}
	if exit == 0 {
		return Decode(expected, stdout)
	}
	if refusal == nil || len(stdout) != 0 {
		return Document{}, errors.New("failed verb without an unambiguous refusal")
	}
	d, err := Decode(Refusal, refusal)
	if err != nil {
		return Document{}, err
	}
	if d.Fields["exit"] != int64(exit) {
		return Document{}, errors.New("refusal exit differs from process status")
	}
	return d, nil
}

func nonemptyList(v any) bool {
	items, _ := v.([]any)
	return len(items) != 0
}

func (d *Document) assess() {
	f := d.Fields
	remote := f["remote"]
	d.Stale = remote == "unchecked" || remote == "not-asked" || f["answer"] == "free-stale"
	d.Incomplete = f["backend_fallback"] == true || f["truncated"] == true ||
		(f["backend"] == "local") != (remote == "none") || nonemptyList(f["warnings"])
	rows, hasRows := f["features"].([]any)
	if d.Kind == Handoff && (f["features_omitted"] != nil || f["features_left_out"] != nil ||
		f["features_left_out_more"] != nil || f["lists_left_out"] != nil) {
		// Each field describes an omission, even if the summary flag disagrees.
		d.Incomplete = true
	}
	if d.Kind == Check {
		named := f["feature"] != nil
		if named == hasRows || named != (f["answer"] != nil) ||
			(f["answer"] == "free-stale") != (f["lead"] != nil) {
			d.Incomplete = true
		}
	}
	for _, item := range rows {
		row := item.(map[string]any)
		if d.Kind == Handoff {
			row = row["record"].(map[string]any)
			data := row["data"].(map[string]any)
			d.Incomplete = d.Incomplete || nonemptyList(data["truncated"])
		} else if row["project"] != f["project"] || nonemptyList(row["warnings"]) {
			d.Incomplete = true
		}
		row["rev"] = Revision{row["rev"].(int64)}
		d.Stale = d.Stale || row["lease"] == "stale"
		d.Incomplete = d.Incomplete || row["provisional"] == true ||
			row["integrity"] == "INVALID" || row["integrity"] == "cannot verify"
	}
}
