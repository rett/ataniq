package ownerwire

import (
	"encoding/base64"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"ataniq/internal/approval/record"
)

// The payload types say their JSON names in struct tags, and this file
// turns a parsed value into such a type and back by those tags, as the
// record package does for its own forms. The tags are read here and not by
// encoding/json: a key must match its tag byte for byte, and a key the form
// does not name is refused.
//
// A field is optional when its tag says omitempty: a pointer that may be
// nil, or a string that may be empty. An optional field that is empty is
// left out on writing, and refused when it is present and empty on reading,
// so that a value has one form. Every other field must be present.

// field is one field of a payload type.
type field struct {
	name     string
	index    int
	optional bool
}

// fieldCache maps a struct type to its fields sorted by name, which is the
// order they are written in.
var fieldCache sync.Map

func fieldsOf(t reflect.Type) []field {
	if f, ok := fieldCache.Load(t); ok {
		return f.([]field)
	}
	fields := make([]field, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, opts, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" {
			panic("ownerwire: " + t.Name() + "." + t.Field(i).Name + " has no JSON name")
		}
		fields = append(fields, field{name: name, index: i, optional: opts == "omitempty"})
	}
	// Go compares strings by their bytes, which is the order of the form.
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	fieldCache.Store(t, fields)
	return fields
}

var (
	textType  = reflect.TypeOf(record.Text(""))
	bytesType = reflect.TypeOf([]byte(nil))
)

// fromValue fills dst from v. where names the place in the form for the
// error text: it is made of the form's own names and of list positions, and
// never of a key or a value the payload brought.
func fromValue(v value, dst reflect.Value, where string) error {
	switch dst.Type() {
	case textType:
		t, err := textFromValue(v)
		if err != nil {
			return errors.New(where + ": " + err.Error())
		}
		dst.SetString(string(t))
		return nil
	case bytesType:
		if v.kind != kindString {
			return errors.New(where + " is not a string of base64")
		}
		b, err := base64.StdEncoding.Strict().DecodeString(v.str)
		if err != nil {
			return errors.New(where + " is not standard base64")
		}
		dst.SetBytes(b)
		return nil
	}
	switch dst.Kind() {
	case reflect.String:
		if v.kind != kindString {
			return errors.New(where + " is not a string")
		}
		dst.SetString(v.str)
	case reflect.Int, reflect.Int64:
		if v.kind != kindInt {
			return errors.New(where + " is not an integer")
		}
		if dst.OverflowInt(v.num) {
			return errors.New(where + " is too large")
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
		panic("ownerwire: no decoding for " + dst.Type().String())
	}
	return nil
}

// appendValue writes src with its keys sorted, no whitespace, integers in
// plain decimal and strings with the shortest escapes. It refuses a plain
// string that holds a control character or bytes that are not UTF-8, since
// only a [record.Text] can carry those.
func appendValue(out []byte, src reflect.Value, where string) ([]byte, error) {
	switch src.Type() {
	case textType:
		return appendText(out, record.Text(src.String())), nil
	case bytesType:
		out = append(out, '"')
		out = base64.StdEncoding.AppendEncode(out, src.Bytes())
		return append(out, '"'), nil
	}
	var err error
	switch src.Kind() {
	case reflect.String:
		if !plain(src.String()) {
			return nil, errors.New(where + " holds a control character or bytes that are not UTF-8")
		}
		out = appendString(out, src.String())
	case reflect.Int, reflect.Int64:
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
		panic("ownerwire: no encoding for " + src.Type().String())
	}
	return out, nil
}

// The b64 form of a text is {"b64":"..."}: these are the bytes around the
// base64.
const (
	b64Open  = `{"b64":"`
	b64Close = `"}`
)

// appendText writes t in its one form: a JSON string when its bytes may
// stand in one, and the b64 object otherwise.
func appendText(out []byte, t record.Text) []byte {
	if t.Plain() {
		return appendString(out, string(t))
	}
	out = append(out, b64Open...)
	out = base64.StdEncoding.AppendEncode(out, []byte(t))
	return append(out, b64Close...)
}

// textLen is the length of what appendText writes for t.
func textLen(t record.Text) int {
	if !t.Plain() {
		return len(b64Open) + base64.StdEncoding.EncodedLen(len(t)) + len(b64Close)
	}
	n := len(t) + 2
	for i := 0; i < len(t); i++ {
		if t[i] == '"' || t[i] == '\\' {
			n++
		}
	}
	return n
}

// textFromValue reads a text from a parsed value: a string, or an object
// whose only key is b64. Bytes have one form, so a b64 that could have been
// a string is refused, as the record decoders refuse it.
func textFromValue(v value) (record.Text, error) {
	switch {
	case v.kind == kindString:
		return record.Text(v.str), nil
	case v.kind == kindObject && len(v.obj) == 1 && v.obj[0].key == "b64" && v.obj[0].val.kind == kindString:
		b, err := base64.StdEncoding.Strict().DecodeString(v.obj[0].val.str)
		if err != nil {
			return "", errors.New("a b64 text is not standard base64")
		}
		if record.Text(b).Plain() {
			return "", errors.New("a b64 text holds bytes that must be written as a string")
		}
		return record.Text(b), nil
	}
	return "", errors.New("a text is neither a string nor an object with only b64")
}

// decode parses data under the rules of a record and fills dst, a pointer
// to a payload type. limit is the cap of the payload, invalid the code for
// one that is not well formed, and name the payload's name for the detail.
func decode(data []byte, limit int, invalid record.Code, name string, dst any) error {
	if len(data) > limit {
		return tooLarge(name, limit)
	}
	v, err := parse(data)
	if err != nil {
		return &record.Error{Code: invalid, Detail: name + ": " + err.Error()}
	}
	if err := fromValue(v, reflect.ValueOf(dst).Elem(), name); err != nil {
		return &record.Error{Code: invalid, Detail: err.Error()}
	}
	return nil
}

// encode writes src, a pointer to a payload type, and holds the cap.
func encode(src any, limit int, invalid record.Code, name string) ([]byte, error) {
	out, err := appendValue(nil, reflect.ValueOf(src).Elem(), name)
	if err != nil {
		return nil, &record.Error{Code: invalid, Detail: err.Error()}
	}
	if len(out) > limit {
		return nil, tooLarge(name, limit)
	}
	return out, nil
}

func tooLarge(name string, limit int) *record.Error {
	return &record.Error{Code: record.CodeRecordTooLarge, Detail: name + " is larger than " + strconv.Itoa(limit) + " bytes"}
}
