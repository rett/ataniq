package agentsession

import (
	"bytes"
	"encoding/json"
	"slices"
	"unicode/utf8"
)

// Read names as tokens so repeated members cannot overwrite earlier evidence.
// Decode the stored values in declaration order, with exact (case-sensitive) names.
type field struct {
	name string
	read func(json.RawMessage, string) error
}

func object(b []byte, path string, fields ...field) error {
	if !utf8.Valid(b) || !json.Valid(b) {
		return invalid(path, "inconsistent")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	tok, _ := d.Token()
	if tok != json.Delim('{') {
		return invalid(path, "inconsistent")
	}
	values := make(map[string]json.RawMessage, len(fields))
	for d.More() {
		key, _ := d.Token()
		name := key.(string)
		if _, ok := values[name]; ok {
			return invalid(path+"."+name, "duplicate")
		}
		if !slices.ContainsFunc(fields, func(f field) bool { return f.name == name }) {
			return invalid(path+"."+name, "unknown-value")
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return invalid(path+"."+name, "inconsistent")
		}
		values[name] = raw
	}
	// json.Valid accepts whitespace after the document. Both wire decoders
	// require the closing brace to consume the last byte of their input.
	if _, err := d.Token(); err != nil || d.InputOffset() != int64(len(b)) {
		return invalid(path, "inconsistent")
	}
	for _, f := range fields {
		if raw, ok := values[f.name]; ok {
			if err := f.read(raw, path+"."+f.name); err != nil {
				return err
			}
		}
	}
	return nil
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
		for i, raw := range items {
			if err := read(raw, index(path, i), &out[i]); err != nil {
				return err
			}
		}
		*target = out
		return nil
	}}
}
