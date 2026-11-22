package seatgate

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"strings"
	"unicode"
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
// {"id":1,"ID":2} cannot give one field two values. The fields and their
// names are the decoder's own; a struct whose names the builds of
// encoding/json with and without json/v2 read differently (a tag name of odd
// characters, the embed option) is refused, and a type with its own
// UnmarshalJSON reads its object itself and is not looked into. The error is
// a violation and holds no byte of the data.
func DecodeStrict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := checkObject(raw, MaxDepth); err != nil {
		return err
	}
	if t := reflect.TypeOf(dst); t != nil {
		if !exactKeys(raw, t, reflect.ValueOf(dst)) {
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
// value down through the types of t and, where the decoder reuses what is
// already there, through v, the destination (the zero Value where there is
// none): the decoder decodes into the pointer an interface holds, so the keys
// under it are held to the pointed-to type. raw has passed [checkObject], so
// it is valid JSON with no repeated key and a bounded depth. What is not a
// struct's object here (a value of the wrong kind, an interface with no
// pointer in it, a map's keys) is left to the decoder, which refuses what does
// not fit. A type that decodes itself (UnmarshalJSON) is out of scope: it
// reads its own object and must hold its keys to their names itself.
//
// A chain of pointers that comes back on itself, by its types (type P *P) or
// by its values (an interface that holds a pointer to itself), takes null and
// nothing else: the decoder would follow it without end.
func exactKeys(raw json.RawMessage, t reflect.Type, v reflect.Value) bool {
	type hop struct {
		t reflect.Type
		p uintptr // the pointer's value, or 0 where there is none
	}
	var seen map[hop]bool
	for {
		if t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType) {
			return true
		}
		if t.Kind() == reflect.Pointer {
			h := hop{t: t}
			if v.IsValid() && !v.IsNil() {
				h.p = v.Pointer()
			}
			if seen[h] {
				return string(bytes.TrimSpace(raw)) == "null"
			}
			if seen == nil {
				seen = map[hop]bool{}
			}
			seen[h] = true
		}
		switch t.Kind() {
		case reflect.Interface:
			if !v.IsValid() || v.IsNil() || v.Elem().Kind() != reflect.Pointer || v.Elem().IsNil() {
				return true
			}
			v = v.Elem()
			t = v.Type()
			continue
		case reflect.Pointer:
			if v.IsValid() && !v.IsNil() {
				v = v.Elem()
			} else {
				v = reflect.Value{}
			}
			t = t.Elem()
			continue
		}
		break
	}
	switch t.Kind() {
	case reflect.Struct:
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return true
		}
		fields, ok := jsonFields(t)
		if !ok {
			return false
		}
		for k, sub := range m {
			f, ok := fields[k]
			if !ok {
				return false
			}
			var fv reflect.Value
			if v.IsValid() {
				fv, _ = v.FieldByIndexErr(f.index) // the zero Value past a nil embedded pointer
			}
			if !exactKeys(sub, f.t, fv) {
				return false
			}
		}
	case reflect.Map: // the decoder gives each value a zero one to decode into
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return true
		}
		for _, sub := range m {
			if !exactKeys(sub, t.Elem(), reflect.Value{}) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		var a []json.RawMessage
		if json.Unmarshal(raw, &a) != nil {
			return true
		}
		for i, sub := range a {
			// The decoder decodes into the elements already there: a
			// slice's up to its capacity, an array's.
			var ev reflect.Value
			if v.IsValid() && t.Kind() == reflect.Slice && i < v.Cap() {
				ev = v.Slice(0, v.Cap()).Index(i)
			} else if v.IsValid() && t.Kind() == reflect.Array && i < v.Len() {
				ev = v.Index(i)
			}
			if !exactKeys(sub, t.Elem(), ev) {
				return false
			}
		}
	}
	return true
}

// jsonField is a field of a struct the decoder fills: its type, and its
// index for [reflect.Value.FieldByIndex].
type jsonField struct {
	t     reflect.Type
	index []int
}

// jsonFields is each field the standard decoder fills in struct type t, by
// its JSON name, chosen by the decoder's own rules (encoding/json's typeFields,
// and makeStructFields when it is built on json/v2): fields of embedded
// structs without a tag name are promoted, breadth first, with a type embedded
// twice at one depth giving its fields twice; of the fields of one name, the
// shallowest is taken, a tagged one before one that is not, and none if two
// tie, so that the name is not one the decoder knows. A tag name is the
// field's name only when the decoder takes it as one.
//
// It reports false, and the struct is refused, where the decoder's two builds
// (with and without GOEXPERIMENT=jsonv2) could choose differently: a tag name
// outside the plain set that each reads otherwise, the embed tag option of
// json/v2, and an embedded field of an unexported type, named by its tag, whose
// type has marshalling methods.
func jsonFields(t reflect.Type) (map[string]jsonField, bool) {
	type found struct {
		name   string
		tagged bool
		f      jsonField
	}
	type queued struct {
		t     reflect.Type
		index []int
	}
	var all []found
	next := []queued{{t: t}}
	visited := map[reflect.Type]bool{}
	var count map[reflect.Type]int // how many times each type of this depth was embedded
	for len(next) > 0 {
		current := next
		next = nil
		nextCount := map[reflect.Type]int{}
		for _, q := range current {
			if visited[q.t] {
				continue
			}
			visited[q.t] = true
			for i := 0; i < q.t.NumField(); i++ {
				sf := q.t.Field(i)
				et := sf.Type
				if et.Kind() == reflect.Pointer && et.Name() == "" {
					et = et.Elem()
				}
				if !sf.IsExported() && (!sf.Anonymous || et.Kind() != reflect.Struct) {
					continue
				}
				tag := sf.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, tagged, ok := fieldName(sf.Name, tag)
				if !ok {
					return nil, false
				}
				index := append(append([]int(nil), q.index...), i)
				if sf.Anonymous && !tagged && et.Kind() == reflect.Struct {
					nextCount[et]++
					if nextCount[et] == 1 {
						next = append(next, queued{et, index})
					}
					continue
				}
				if sf.Anonymous && !sf.IsExported() && hasCodecMethods(et) {
					return nil, false
				}
				f := found{name, tagged, jsonField{sf.Type, index}}
				all = append(all, f)
				if count[q.t] > 1 {
					all = append(all, f)
				}
			}
		}
		count = nextCount
	}
	byName := map[string][]found{}
	for _, f := range all {
		byName[f.name] = append(byName[f.name], f)
	}
	out := make(map[string]jsonField, len(byName))
	for name, fs := range byName {
		slices.SortStableFunc(fs, func(a, b found) int {
			if c := cmp.Compare(len(a.f.index), len(b.f.index)); c != 0 {
				return c
			}
			switch {
			case a.tagged == b.tagged:
				return 0
			case a.tagged:
				return -1
			}
			return 1
		})
		if len(fs) > 1 && len(fs[0].f.index) == len(fs[1].f.index) && fs[0].tagged == fs[1].tagged {
			continue
		}
		out[name] = fs[0].f
	}
	return out, true
}

// fieldName is the JSON name of a field, from its Go name and its json tag,
// and whether the tag gave it. A tag name of the plain set (letters, digits and
// the punctuation encoding/json allows, which json/v2 reads the same) is the
// name. Another one is not, and the field keeps its Go name, only where both
// builds of the decoder say so; elsewhere, and for the embed option of
// json/v2, ok is false.
func fieldName(goName, tag string) (name string, tagged, ok bool) {
	part, opts, _ := strings.Cut(tag, ",")
	for _, o := range strings.Split(opts, ",") {
		if strings.HasPrefix(o, "embed") {
			return "", false, false
		}
	}
	if part == "" {
		return goName, false, true
	}
	plain := utf8.ValidString(part)
	for _, r := range part {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", r) {
			plain = false
		}
	}
	if plain {
		return part, true, true
	}
	// encoding/json ignores a name that is not plain; json/v2 does too when
	// the name holds a quote, a backslash or a backquote and does not begin as
	// a Go identifier would, and else takes some of it.
	if first, _ := utf8.DecodeRuneInString(part); strings.ContainsAny(part, "\\'\"`") && first != '_' && !unicode.IsLetter(first) {
		return goName, false, true
	}
	return "", false, false
}

// hasCodecMethods reports whether t or *t has a method of the names of the
// marshalling interfaces of encoding/json and json/v2, or IsZero, which
// json/v2 counts with the omitzero option.
func hasCodecMethods(t reflect.Type) bool {
	pt := reflect.PointerTo(t)
	for _, m := range []string{"MarshalJSON", "MarshalJSONTo", "UnmarshalJSON", "UnmarshalJSONFrom", "MarshalText", "AppendText", "UnmarshalText", "IsZero"} {
		if _, ok := pt.MethodByName(m); ok {
			return true
		}
	}
	return false
}
