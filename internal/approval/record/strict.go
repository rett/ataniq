package record

import (
	"encoding/base64"
	"errors"
	"reflect"
)

// This file is the small reading API of the package, for another package
// that carries payloads under the same rules as a record (the owner
// connection's frames are one): the same strict JSON reader, the same tag
// codec and the same validators, so that two packages cannot drift apart on
// what they accept. The record decoders read and write with the same parts,
// and only their refusals are shaped by the record package itself.

// Reader is the strict JSON reader of the package, set for one kind of
// payload.
//
// It reads exactly one JSON value under the record rules: valid UTF-8, no
// float, no null, no control character in a string, no key that is repeated
// or differs from a name of the form by case, no field the form does not
// name, and nothing after the value but whitespace. The types it fills carry
// their JSON names in struct tags (as the record types do) and may hold
// string, [Text], int, int64, bool, []byte (standard base64 in a string),
// pointers, slices and structs of these. A field whose tag has ",omitempty"
// is optional: it may be absent, and it is left out on writing. Any other
// field must be present. A type it cannot read makes it panic, which is a
// mistake of the program and not of the data.
type Reader struct {
	// MaxDepth is how deep arrays and objects may nest. The records use 12.
	MaxDepth int
	// Noun is how the error texts call the data, such as "record" or
	// "payload".
	Noun string
}

// Unmarshal reads data into dst, a pointer to a struct. name is how the
// error texts call the form: a refusal for the JSON begins with it, and one
// for the form names the place in it. The error is plain text written here,
// and it holds no byte of data. Unmarshal does not hold a size cap: the caller
// checks the size before it reads.
func (r Reader) Unmarshal(data []byte, name string, dst any) error {
	v, err := parseDepth(data, r.MaxDepth, r.Noun)
	if err != nil {
		return errors.New(name + ": " + err.Error())
	}
	return fromValue(v, reflect.ValueOf(dst).Elem(), name)
}

// Marshal writes src, a pointer to a struct, in the canonical form: keys
// sorted by their UTF-8 bytes, no whitespace, integers in plain decimal and
// strings with only the two escapes \" and \\. It fails when a plain string
// holds a control character or bytes that are not UTF-8, since only a [Text]
// can carry those. name is how the error texts call the form.
func Marshal(src any, name string) ([]byte, error) {
	return AppendMarshal(nil, src, name)
}

// AppendMarshal is Marshal that appends to out.
func AppendMarshal(out []byte, src any, name string) ([]byte, error) {
	return appendValue(out, reflect.ValueOf(src).Elem(), name)
}

// B64FrameBytes is the number of bytes around the base64 in the b64 form of
// a [Text]: {"b64":"..."}.
const B64FrameBytes = len(`{"b64":""}`)

// TextLen is the number of bytes of the form of t in JSON, as [Text.MarshalJSON]
// writes it: a string with its quotes and escapes, or the b64 object.
func TextLen(t Text) int {
	if !t.Plain() {
		return B64FrameBytes + base64.StdEncoding.EncodedLen(len(t))
	}
	n := len(t) + 2
	for i := 0; i < len(t); i++ {
		if t[i] == '"' || t[i] == '\\' {
			n++
		}
	}
	return n
}

// IsControl reports whether r is one of the characters no string may hold:
// U+0000 to U+001F, U+007F and U+0080 to U+009F.
func IsControl(r rune) bool { return isControl(r) }

// IsLowerHex reports whether s is exactly n lower-case hexadecimal digits.
func IsLowerHex(s string, n int) bool { return len(s) == n && all(s, isHexByte) }

// ValidFlagKind reports whether k is one of the Flag constants.
func ValidFlagKind(k string) bool { return validFlagKind(k) }

// ValidMode reports whether s is a git file mode as six octal digits.
func ValidMode(s string) bool { return validMode(s) }
