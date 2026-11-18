package record

import (
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The record types say their JSON names in struct tags, and this file turns
// a parsed value into such a type and back by those tags. So a form is
// written once, in types.go. The tags are read here and not by
// encoding/json: a key must match its tag byte for byte.
//
// A field is optional when its tag says omitempty: a pointer that may be
// nil, or a string that may be empty. An optional field that is empty is
// left out on writing, and refused when it is present and empty on reading,
// so that a value has one form. Every other field must be present.

// field is one field of a record type.
type field struct {
	name     string
	index    int
	optional bool
}

// fieldCache maps a struct type to its fields sorted by name, which is the
// order of the canonical form.
var fieldCache sync.Map

func fieldsOf(t reflect.Type) []field {
	if f, ok := fieldCache.Load(t); ok {
		return f.([]field)
	}
	fields := make([]field, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, opts, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" {
			panic("record: " + t.Name() + "." + t.Field(i).Name + " has no JSON name")
		}
		fields = append(fields, field{name: name, index: i, optional: opts == "omitempty"})
	}
	// Go compares strings by their bytes, which is the order of the form.
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	fieldCache.Store(t, fields)
	return fields
}

var textType = reflect.TypeOf(Text(""))

// fromValue fills dst from v. where names the place in the form for the
// error text: it is made of the form's own names and of list positions, and
// never of a key the record brought.
func fromValue(v value, dst reflect.Value, where string) error {
	if dst.Type() == textType {
		t, err := textFromValue(v)
		if err != nil {
			return errors.New(where + ": " + err.Error())
		}
		dst.SetString(string(t))
		return nil
	}
	switch dst.Kind() {
	case reflect.String:
		if v.kind != kindString {
			return errors.New(where + " is not a string")
		}
		dst.SetString(v.str)
	case reflect.Int64:
		if v.kind != kindInt {
			return errors.New(where + " is not an integer")
		}
		dst.SetInt(v.num)
	case reflect.Bool:
		if v.kind != kindBool {
			return errors.New(where + " is not true or false")
		}
		dst.SetBool(v.b)
	case reflect.Slice:
		if v.kind != kindArray {
			return errors.New(where + " is not a list")
		}
		dst.Set(reflect.MakeSlice(dst.Type(), len(v.arr), len(v.arr)))
		for i := range v.arr {
			if err := fromValue(v.arr[i], dst.Index(i), where+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
	case reflect.Pointer:
		dst.Set(reflect.New(dst.Type().Elem()))
		return fromValue(v, dst.Elem(), where)
	case reflect.Struct:
		if v.kind != kindObject {
			return errors.New(where + " is not an object")
		}
		fields := fieldsOf(dst.Type())
		seen := make([]bool, len(fields))
		for _, m := range v.obj {
			i := sort.Search(len(fields), func(i int) bool { return fields[i].name >= m.key })
			if i == len(fields) || fields[i].name != m.key {
				return errors.New(where + " has a field the form does not name")
			}
			// The parser refused a repeated key, so each field is
			// set at most once.
			seen[i] = true
			f := dst.Field(fields[i].index)
			name := where + "." + fields[i].name
			if err := fromValue(m.val, f, name); err != nil {
				return err
			}
			if fields[i].optional && f.Kind() == reflect.String && f.Len() == 0 {
				return errors.New(name + " is empty; an empty optional field is left out")
			}
		}
		for i, f := range fields {
			if !seen[i] && !f.optional {
				return errors.New(where + "." + f.name + " is missing")
			}
		}
	default:
		panic("record: no decoding for " + dst.Type().String())
	}
	return nil
}

// appendValue writes src in the canonical form: keys sorted, no whitespace,
// integers in plain decimal, strings with the shortest escapes. It refuses
// a plain string that holds a control character or bytes that are not
// UTF-8, since only a [Text] can carry those.
func appendValue(out []byte, src reflect.Value, where string) ([]byte, error) {
	if src.Type() == textType {
		return appendText(out, Text(src.String())), nil
	}
	var err error
	switch src.Kind() {
	case reflect.String:
		if !plain(src.String()) {
			return nil, errors.New(where + " holds a control character or bytes that are not UTF-8")
		}
		out = appendString(out, src.String())
	case reflect.Int64:
		out = strconv.AppendInt(out, src.Int(), 10)
	case reflect.Bool:
		out = strconv.AppendBool(out, src.Bool())
	case reflect.Slice:
		out = append(out, '[')
		for i := 0; i < src.Len(); i++ {
			if i > 0 {
				out = append(out, ',')
			}
			if out, err = appendValue(out, src.Index(i), where+"["+strconv.Itoa(i)+"]"); err != nil {
				return nil, err
			}
		}
		out = append(out, ']')
	case reflect.Pointer:
		return appendValue(out, src.Elem(), where)
	case reflect.Struct:
		out = append(out, '{')
		first := true
		for _, f := range fieldsOf(src.Type()) {
			v := src.Field(f.index)
			if f.optional && v.IsZero() {
				continue
			}
			if !first {
				out = append(out, ',')
			}
			first = false
			// A form's own names are ASCII letters, digits and
			// underscores: they need no escape.
			out = append(out, '"')
			out = append(out, f.name...)
			out = append(out, '"', ':')
			if out, err = appendValue(out, v, where+"."+f.name); err != nil {
				return nil, err
			}
		}
		out = append(out, '}')
	default:
		panic("record: no encoding for " + src.Type().String())
	}
	return out, nil
}
