// Package canon is the strict JSON reader and the canonical JSON writer of
// the genesis packages (the project brief and the question catalog).
//
// The reader takes exactly one JSON value under the same rules as the
// approval records: valid UTF-8, no float, no null, no control character in
// a string (U+0000 to U+001F, U+007F, U+0080 to U+009F), no key repeated in
// one object, a bounded nesting depth, and nothing after the value but
// whitespace. It does not use encoding/json, whose decoder matches keys
// without regard to case and keeps the last of a repeated key.
//
// The writer writes the canonical form: the keys of every object sorted by
// their UTF-8 bytes, no whitespace, integers in plain decimal, and strings
// with the two escapes \" and \\ and no other. Reading canonical bytes and
// writing the result gives the same bytes.
//
// Every error text is written here and never holds a byte of the data, so
// it can be shown as it is. The package imports the standard library only.
package canon

import (
	"errors"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Kind is the type of one JSON value. There is no kind for null or for a
// float: neither is allowed.
type Kind int

// The kinds of value.
const (
	String Kind = iota + 1
	Int
	Bool
	Array
	Object
)

// Value is one JSON value. Only the part its Kind names is used.
type Value struct {
	Kind Kind
	Str  string
	Num  int64
	B    bool
	Arr  []Value
	Obj  []Member
}

// Member is one key of an object with its value.
type Member struct {
	Key string
	Val Value
}

// Str returns a string value.
func Str(s string) Value { return Value{Kind: String, Str: s} }

// Num returns an integer value.
func Num(n int64) Value { return Value{Kind: Int, Num: n} }

// Boolean returns a true or false value.
func Boolean(b bool) Value { return Value{Kind: Bool, B: b} }

// List returns an array value. A nil list is written as [].
func List(vs ...Value) Value { return Value{Kind: Array, Arr: vs} }

// Strs returns an array of string values.
func Strs(ss []string) Value {
	vs := make([]Value, len(ss))
	for i, s := range ss {
		vs[i] = Str(s)
	}
	return List(vs...)
}

// Obj returns an object value. The writer sorts its keys.
func Obj(ms ...Member) Value { return Value{Kind: Object, Obj: ms} }

// M returns one member of an object.
func M(key string, v Value) Member { return Member{Key: key, Val: v} }

// IsControl reports whether r is one of the characters no string may hold:
// U+0000 to U+001F, U+007F and U+0080 to U+009F.
func IsControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

// Plain reports whether s can stand in the data as a JSON string: valid
// UTF-8 with no control character.
func Plain(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size <= 1) || IsControl(r) {
			return false
		}
		i += size
	}
	return true
}

// Encode writes v in the canonical form. It fails when a string or a key
// is not [Plain], when an object holds one key twice, or when a value has
// no kind.
func Encode(v Value) ([]byte, error) {
	return appendValue(nil, v)
}

func appendValue(out []byte, v Value) ([]byte, error) {
	var err error
	switch v.Kind {
	case String:
		if !Plain(v.Str) {
			return nil, errors.New("a string holds a control character or bytes that are not UTF-8")
		}
		out = appendString(out, v.Str)
	case Int:
		out = strconv.AppendInt(out, v.Num, 10)
	case Bool:
		out = strconv.AppendBool(out, v.B)
	case Array:
		out = append(out, '[')
		for i := range v.Arr {
			if i > 0 {
				out = append(out, ',')
			}
			if out, err = appendValue(out, v.Arr[i]); err != nil {
				return nil, err
			}
		}
		out = append(out, ']')
	case Object:
		ms := make([]Member, len(v.Obj))
		copy(ms, v.Obj)
		// Go compares strings by their bytes, which is the order of the
		// canonical form.
		sort.Slice(ms, func(i, j int) bool { return ms[i].Key < ms[j].Key })
		out = append(out, '{')
		for i, m := range ms {
			if i > 0 {
				if m.Key == ms[i-1].Key {
					return nil, errors.New("an object holds one key twice")
				}
				out = append(out, ',')
			}
			if !Plain(m.Key) {
				return nil, errors.New("a key holds a control character or bytes that are not UTF-8")
			}
			out = appendString(out, m.Key)
			out = append(out, ':')
			if out, err = appendValue(out, m.Val); err != nil {
				return nil, err
			}
		}
		out = append(out, '}')
	default:
		return nil, errors.New("a value has no kind")
	}
	return out, nil
}

// appendString writes s, which must be plain, as a canonical JSON string:
// only the quote and the backslash are escaped.
func appendString(out []byte, s string) []byte {
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return append(out, '"')
}

// Fields checks that v is an object whose keys are all among required and
// optional, and that every key in required is present, and returns its
// members by key. where names the place in the form for the error text; the
// text never holds a key the data brought.
func Fields(v Value, where string, required, optional []string) (map[string]Value, error) {
	if v.Kind != Object {
		return nil, errors.New(where + " is not an object")
	}
	out := make(map[string]Value, len(v.Obj))
	for _, m := range v.Obj {
		if !contains(required, m.Key) && !contains(optional, m.Key) {
			return nil, errors.New(where + " has a field the form does not name")
		}
		// The parser refused a repeated key.
		out[m.Key] = m.Val
	}
	for _, k := range required {
		if _, ok := out[k]; !ok {
			return nil, errors.New(where + "." + k + " is missing")
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// AsString returns the text of a string value.
func AsString(v Value, where string) (string, error) {
	if v.Kind != String {
		return "", errors.New(where + " is not a string")
	}
	return v.Str, nil
}

// AsInt returns an integer value that lies within [lo, hi].
func AsInt(v Value, where string, lo, hi int64) (int, error) {
	if v.Kind != Int {
		return 0, errors.New(where + " is not an integer")
	}
	if v.Num < lo || v.Num > hi {
		return 0, errors.New(where + " is out of range")
	}
	return int(v.Num), nil
}

// AsBool returns a true or false value.
func AsBool(v Value, where string) (bool, error) {
	if v.Kind != Bool {
		return false, errors.New(where + " is not true or false")
	}
	return v.B, nil
}

// AsStrings returns a list of strings of at most max items.
func AsStrings(v Value, where string, max int) ([]string, error) {
	if v.Kind != Array {
		return nil, errors.New(where + " is not a list")
	}
	if len(v.Arr) > max {
		return nil, errors.New(where + " has too many items")
	}
	out := make([]string, len(v.Arr))
	for i, e := range v.Arr {
		s, err := AsString(e, where+"["+strconv.Itoa(i)+"]")
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}
