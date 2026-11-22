package seatgate

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// checkObject holds data to the strict form of every frame and every payload in
// it, before anything is decoded into a type:
//
//   - it is valid UTF-8 and valid JSON, one value, and that value is an object,
//     with no byte before its opening brace or after its closing one (white
//     space included);
//   - objects and arrays nest at most maxDepth levels;
//   - no object has the same key twice, at any depth, where a key is compared
//     after its escapes are read (so "id" and "id" are the same key).
//
// The standard decoder takes the last of two equal keys and says nothing, which
// is how two readers of one message come to see two messages; this check is the
// reason a reader here cannot. A refusal is a violation with a fixed text.
func checkObject(data []byte, maxDepth int) error {
	if len(data) < 2 || data[0] != '{' || data[len(data)-1] != '}' {
		return violation("not a JSON object")
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		return violation("not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walk(dec, 1, maxDepth); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return violation("bytes after the object")
	}
	return nil
}

// walk reads one value's tokens, the value having depth levels around it, and
// reports a repeated key or a nesting beyond maxDepth.
func walk(dec *json.Decoder, depth, maxDepth int) error {
	tok, err := dec.Token()
	if err != nil {
		return violation("not valid JSON")
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth > maxDepth {
		return violation("nested too deeply")
	}
	switch d {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return violation("not valid JSON")
			}
			key, _ := k.(string)
			if _, dup := seen[key]; dup {
				return violation("repeated key")
			}
			seen[key] = struct{}{}
			if err := walk(dec, depth+1, maxDepth); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walk(dec, depth+1, maxDepth); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing delimiter
		return violation("not valid JSON")
	}
	return nil
}

// fieldSpec names the keys an object may have, and which of them it must have.
type fieldSpec map[string]bool // key -> required

// splitObject checks data with [checkObject] and returns the raw value of each
// top-level key. A key not in spec is refused, whatever its case: the match is
// exact, unlike the standard decoder's. A required key that is absent is
// refused.
func splitObject(data []byte, spec fieldSpec) (map[string]json.RawMessage, error) {
	if err := checkObject(data, MaxDepth); err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, violation("not valid JSON")
	}
	for k := range m {
		if _, ok := spec[k]; !ok {
			return nil, violation("unknown field")
		}
	}
	for k, required := range spec {
		if _, ok := m[k]; required && !ok {
			return nil, violation("missing field")
		}
	}
	return m, nil
}

// DecodeStrict decodes raw, the params of a request or the result of a
// response, into dst, a pointer to a struct, under the rules of the wire: one
// object (an absent one reads as {}), no repeated key at any depth, no field
// the struct does not name, no byte after it. A handler decodes its params
// with it. Unlike the standard decoder, it matches a field name in its case,
// at any depth: a key must be the exact JSON name of a field (its tag's name,
// or else the field's own), so {"ID":1} does not fill a field named "id", and
// {"id":1,"ID":2} cannot give one field two values. The error is a violation
// and holds no byte of the data.
func DecodeStrict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := checkObject(raw, MaxDepth); err != nil {
		return err
	}
	if t := reflect.TypeOf(dst); t != nil {
		if !exactKeys(raw, t) {
			return violation("fields do not fit")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return violation("fields do not fit")
	}
	return nil
}

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// exactKeys reports whether every key of every object in raw that is decoded
// into a struct field names that field exactly, case included, following the
// value down through the types of t. raw has passed [checkObject], so it is
// valid JSON with no repeated key and a bounded depth. What is not a struct's
// object here (a value of the wrong kind, a type that decodes itself, an
// interface, a map's keys) is left to the decoder, which refuses what does not
// fit.
func exactKeys(raw json.RawMessage, t reflect.Type) bool {
	for {
		if t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType) {
			return true
		}
		if t.Kind() != reflect.Pointer {
			break
		}
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return true
		}
		fields := jsonFields(t)
		for k, v := range m {
			ft, ok := fields[k]
			if !ok || !exactKeys(v, ft) {
				return false
			}
		}
	case reflect.Map:
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return true
		}
		for _, v := range m {
			if !exactKeys(v, t.Elem()) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		var a []json.RawMessage
		if json.Unmarshal(raw, &a) != nil {
			return true
		}
		for _, v := range a {
			if !exactKeys(v, t.Elem()) {
				return false
			}
		}
	}
	return true
}

// jsonFields is the JSON name of each field the standard decoder can fill in
// struct type t, with the field's type: the name in its tag, or else the
// field's own; fields of embedded structs without a tag name are promoted,
// and a shallower field hides a deeper one of the same name. Of two fields at
// one depth with one name, a tagged one is taken over one that is not, as the
// decoder does; a name two fields share otherwise is kept, and the decoder
// refuses it as unknown.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	type field struct {
		t      reflect.Type
		tagged bool
	}
	out := map[string]reflect.Type{}
	seen := map[reflect.Type]bool{}
	level := []reflect.Type{t}
	for len(level) > 0 {
		var next []reflect.Type
		found := map[string]field{}
		for _, st := range level {
			if seen[st] {
				continue
			}
			seen[st] = true
			for i := 0; i < st.NumField(); i++ {
				f := st.Field(i)
				tag := f.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, _, _ := strings.Cut(tag, ",")
				ft := f.Type
				if f.Anonymous && name == "" {
					et := ft
					if et.Kind() == reflect.Pointer {
						et = et.Elem()
					}
					if et.Kind() == reflect.Struct {
						next = append(next, et)
						continue
					}
				}
				if !f.IsExported() {
					continue
				}
				tagged := name != ""
				if !tagged {
					name = f.Name
				}
				if _, hidden := out[name]; hidden {
					continue
				}
				if prev, ok := found[name]; !ok || tagged && !prev.tagged {
					found[name] = field{ft, tagged}
				}
			}
		}
		for name, f := range found {
			out[name] = f.t
		}
		level = next
	}
	return out
}
