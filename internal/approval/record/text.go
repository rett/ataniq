package record

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Text is a string of raw bytes that survives JSON under the record rules.
// Every path, the plan text and the lead's note are a Text, because a lead
// can put any byte in them.
//
// Bytes that are valid UTF-8 and hold no control character (U+0000 to
// U+001F, U+007F, U+0080 to U+009F) are written as a JSON string. Anything
// else is written as {"b64": "..."} in standard base64 with padding. A b64
// form whose bytes could have been a JSON string is refused, so given bytes
// have exactly one form. A plan of several lines therefore travels as b64,
// since a line feed is a control character.
//
// This differs on purpose from the intake records' text, which writes every
// valid UTF-8 string as a JSON string, control characters included.
//
// A Text is lead-written: show it only through the lead-text escaping.
type Text string

// Format writes t quoted, with every byte outside printable ASCII escaped,
// whatever the verb is (%v, %+v, %#v, %s, %q, %x and %d alike). So printing
// a Text, or a record that holds one such as a SignOff, a PathChange or a
// Flag, never writes a raw byte of a lead: no control character, no
// bidirectional override and no invalid UTF-8 reaches a log line or an error
// message. string(t) is still the raw bytes, and it is the way to get them.
func (t Text) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, strconv.QuoteToASCII(string(t)))
}

// Plain reports whether t is written as a JSON string and not as b64.
func (t Text) Plain() bool { return plain(string(t)) }

// MarshalJSON writes t in its one form. It is here for packages that put a
// Text in a JSON document of their own with encoding/json. Such a caller
// must turn HTML escaping off if it needs the bytes this method returns.
func (t Text) MarshalJSON() ([]byte, error) {
	return appendText(nil, t), nil
}

// UnmarshalJSON reads t from its one form and refuses the other, as the
// record decoders do.
func (t *Text) UnmarshalJSON(data []byte) error {
	v, err := parse(data)
	if err != nil {
		return errors.New("record: a text: " + err.Error())
	}
	got, err := textFromValue(v)
	if err != nil {
		return errors.New("record: " + err.Error())
	}
	*t = got
	return nil
}

// appendText writes t as a JSON string or as the b64 object.
func appendText(out []byte, t Text) []byte {
	if t.Plain() {
		return appendString(out, string(t))
	}
	out = append(out, `{"b64":"`...)
	out = base64.StdEncoding.AppendEncode(out, []byte(t))
	return append(out, `"}`...)
}

// textFromValue reads a text from a parsed value: a string, or an object
// whose only key is b64.
func textFromValue(v value) (Text, error) {
	switch {
	case v.kind == kindString:
		return Text(v.str), nil
	case v.kind == kindObject && len(v.obj) == 1 && v.obj[0].key == "b64" && v.obj[0].val.kind == kindString:
		b, err := base64.StdEncoding.Strict().DecodeString(v.obj[0].val.str)
		if err != nil {
			return "", errors.New("a b64 text is not standard base64")
		}
		if plain(string(b)) {
			return "", errors.New("a b64 text holds bytes that must be written as a string")
		}
		return Text(b), nil
	}
	return "", errors.New("a text is neither a string nor an object with only b64")
}
