package agentsession

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"
)

// Read names as tokens so repeated members cannot overwrite earlier evidence.
// Decode the stored values in declaration order, with exact (case-sensitive) names.
type field struct {
	name string
	read func(json.RawMessage, string) error
}

func object(b []byte, path string, validate func() error, fields ...field) error {
	if !utf8.Valid(b) || !json.Valid(b) {
		return invalid(path, "inconsistent")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	tok, _ := d.Token()
	if tok != json.Delim('{') {
		return invalid(path, "inconsistent")
	}
	values := make(map[string]json.RawMessage, len(fields))
	var unknown string
	hasUnknown := false
	for d.More() {
		key, _ := d.Token()
		name := key.(string)
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return invalid(path+"."+name, "inconsistent")
		}
		if !slices.ContainsFunc(fields, func(f field) bool { return f.name == name }) {
			if !hasUnknown || name < unknown {
				unknown, hasUnknown = name, true
			}
		} else if _, duplicate := values[name]; duplicate {
			values[name] = nil // neither occurrence supplies a value
		} else {
			values[name] = raw
		}
	}
	// json.Valid accepts whitespace after the document. Both wire decoders
	// require the closing brace to consume the last byte of their input.
	if _, err := d.Token(); err != nil || d.InputOffset() != int64(len(b)) {
		return invalid(path, "inconsistent")
	}
	// Unknown names are object errors. Choose one by byte order since none
	// has a position in the declaration.
	if hasUnknown {
		return invalid(path+"."+unknown, "unknown-value")
	}
	var first error
	firstIndex := len(fields)
	for i, f := range fields {
		raw, present := values[f.name]
		if !present {
			continue
		}
		member := path + "." + f.name
		var err error
		if raw == nil {
			err = invalid(member, "duplicate")
		} else {
			err = f.read(raw, member)
		}
		if err != nil && first == nil {
			first, firstIndex = err, i
		}
	}
	// A later value can constrain an earlier member (Form constrains Bound,
	// for example). Validate once all readable members have been populated.
	if validate != nil {
		if err := validate(); err != nil {
			if first == nil {
				return err
			}
			member := err.(*InputError).Member
			for _, f := range fields[:firstIndex] {
				p := path + "." + f.name
				if member == p || strings.HasPrefix(member, p+".") || strings.HasPrefix(member, p+"[") {
					return err
				}
			}
		}
	}
	return first
}
func value[T any](name string, target *T) field {
	return field{name, func(b json.RawMessage, path string) error {
		if bytes.Equal(b, []byte("null")) || json.Unmarshal(b, target) != nil {
			return invalid(path, "inconsistent")
		}
		return nil
	}}
}
func list[T any](name string, target *[]T, read func([]byte, string, *T) error) field {
	return field{name, func(b json.RawMessage, path string) error {
		var items []json.RawMessage
		if err := json.Unmarshal(b, &items); err != nil {
			return invalid(path, "inconsistent")
		}
		if items == nil {
			return nil
		}
		out := make([]T, len(items))
		var first error
		for i, raw := range items {
			if err := read(raw, index(path, i), &out[i]); err != nil && first == nil {
				first = err
			}
		}
		*target = out
		return first
	}}
}
