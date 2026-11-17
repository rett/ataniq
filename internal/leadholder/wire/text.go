package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Length caps of the text in structured payloads, in bytes.
const (
	// MaxClientName is the longest client name in a Hello and a Role.
	MaxClientName = 64
	// MaxNoticeText is the longest text of a Notice and of a refusal.
	MaxNoticeText = 256
	// MaxTitle is the longest title in a State frame.
	MaxTitle = 256
	// MaxLastScreen is the longest last screen in an Exit frame.
	MaxLastScreen = 256 << 10
	// MaxLead is the longest lead (role) name.
	MaxLead = 128
	// MaxRuntime is the longest runtime name and runtime version.
	MaxRuntime = 64
	// MaxToken is the longest code, signal or counter name.
	MaxToken = 64
	// MaxCommandID is the longest command id of a control request.
	MaxCommandID = 64
	// MaxModes is the most mode numbers in each list of a Modes frame.
	MaxModes = 64
	// MaxSignals is the most idle signals in a State frame.
	MaxSignals = 16
	// MaxCounters is the most counters in an Exit or a Status.
	MaxCounters = 256
	// MaxDimension is the largest column or row count that decodes. The holder
	// clamps what it is given to its own screen limits.
	MaxDimension = 65535
)

// ErrInvalid wraps every refusal of a structured payload: bad JSON, a field
// the struct lacks, a repeated key, trailing data, a value over its cap or
// out of its set. Test with [errors.Is].
var ErrInvalid = errors.New("wire: invalid payload")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// isControl reports a rune that must not reach a terminal or a log line: a
// C0 control, DEL or a C1 control.
func isControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r < 0xa0)
}

// forbidden reports a rune that a text field may not hold: a control
// character (a newline is let through when keepNewline is set), a Unicode
// format character (category Cf, which includes the bidirectional controls,
// the zero-width characters and the byte order mark) or a line or paragraph
// separator (U+2028, U+2029). A name or a title is shown beside other text in
// every attached pane, and these characters reorder, hide or break it.
func forbidden(r rune, keepNewline bool) bool {
	if r == '\n' && keepNewline {
		return false
	}
	return isControl(r) || unicode.Is(unicode.Cf, r) || unicode.In(r, unicode.Zl, unicode.Zp)
}

// checkText checks one text field: valid UTF-8, between min and max bytes and
// without control, format or line-separator characters (a newline is allowed
// only when keepNewline is set).
func checkText(field, s string, min, max int, keepNewline bool) error {
	if len(s) < min || len(s) > max {
		return invalidf("%s is %d bytes, want %d to %d", field, len(s), min, max)
	}
	if !utf8.ValidString(s) {
		return invalidf("%s is not valid UTF-8", field)
	}
	for _, r := range s {
		if forbidden(r, keepNewline) {
			return invalidf("%s has a control, format or line-separator character (U+%04X)", field, r)
		}
	}
	return nil
}

// checkToken checks a short machine word: lower-case letters, digits and the
// signs - _ . only, 1 to max bytes.
func checkToken(field, s string, max int) error {
	if len(s) < 1 || len(s) > max {
		return invalidf("%s is %d bytes, want 1 to %d", field, len(s), max)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return invalidf("%s has a character outside a-z 0-9 - _ .", field)
		}
	}
	return nil
}

// checkHex checks exactly n lower-case hexadecimal characters.
func checkHex(field, s string, n int) error {
	if len(s) != n {
		return invalidf("%s is %d characters, want exactly %d", field, len(s), n)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return invalidf("%s is not lower-case hexadecimal", field)
		}
	}
	return nil
}

func checkDimension(field string, n int) error {
	if n < 1 || n > MaxDimension {
		return invalidf("%s is %d, want 1 to %d", field, n, MaxDimension)
	}
	return nil
}

// Sanitize makes s fit a text field: each invalid byte, control character
// (a newline only when keepNewline is set), format character and line or
// paragraph separator becomes U+FFFD and the result is cut at a character
// boundary to at most max bytes. A holder uses it on text that
// came from the session (a title, the last screen) before it puts it in a
// frame.
func Sanitize(s string, max int, keepNewline bool) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError {
			// Either a real U+FFFD or one invalid byte; both become U+FFFD.
			if b.Len()+utf8.RuneLen(utf8.RuneError) > max {
				break
			}
			b.WriteRune(utf8.RuneError)
			continue
		}
		if forbidden(r, keepNewline) {
			r = utf8.RuneError
		}
		if b.Len()+utf8.RuneLen(r) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// message is a structured payload: a struct with JSON tags and a validity
// rule that applies to what it decodes from and what it encodes to.
type message interface{ validate() error }

// maxDepth is how deeply a structured payload may nest. The deepest message
// has three levels; the rest is slack.
const maxDepth = 6

// decodeMessage decodes a payload strictly into the struct that p points to
// and validates it.
func decodeMessage(p message, data []byte) error {
	if !utf8.Valid(data) {
		return invalidf("payload is not valid UTF-8")
	}
	end, err := scanObject(data)
	if err != nil {
		return err
	}
	if err := checkSurrogates(data[:end]); err != nil {
		return err
	}
	for _, c := range data[end:] {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return invalidf("data after the JSON object")
		}
	}
	data = data[:end]
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p); err != nil {
		return invalidf("%v", err)
	}
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return invalidf("%v", err)
	}
	if err := checkKeys("", reflect.TypeOf(p), tree); err != nil {
		return err
	}
	return p.validate()
}

// scanObject checks that data starts with one JSON object (after white
// space), that no object in it repeats a key and that it nests at most
// maxDepth levels. It returns the offset just after the object.
func scanObject(data []byte) (int, error) {
	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == '\t' || data[start] == '\n' || data[start] == '\r') {
		start++
	}
	if start == len(data) || data[start] != '{' {
		return 0, invalidf("not a JSON object")
	}
	type level struct {
		obj     bool
		keyNext bool
		seen    map[string]struct{}
	}
	var stack []level
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return 0, invalidf("%v", err)
		}
		done := false // a value just ended
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				if len(stack) >= maxDepth {
					return 0, invalidf("nested deeper than %d levels", maxDepth)
				}
				l := level{obj: t == '{', keyNext: t == '{'}
				if l.obj {
					l.seen = map[string]struct{}{}
				}
				stack = append(stack, l)
			default:
				stack = stack[:len(stack)-1]
				done = true
			}
		case string:
			if top := len(stack) - 1; top >= 0 && stack[top].obj && stack[top].keyNext {
				if _, dup := stack[top].seen[t]; dup {
					return 0, invalidf("key %q repeated", t)
				}
				stack[top].seen[t] = struct{}{}
				stack[top].keyNext = false
			} else {
				done = true
			}
		default:
			done = true
		}
		if done {
			if len(stack) == 0 {
				return int(dec.InputOffset()), nil
			}
			if top := len(stack) - 1; stack[top].obj {
				stack[top].keyNext = true
			}
		}
	}
}

// hex4 returns the value of the four hexadecimal digits that start b.
func hex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			c = c - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(c)
	}
	return r, true
}

// checkSurrogates refuses a \uD800 to \uDFFF escape in a JSON string that is
// not half of a pair. encoding/json turns such an escape into U+FFFD without
// an error, which would let a payload say what its own encoder never writes.
// It reads the raw bytes of an object that scanObject has accepted, so every
// quote it finds is a real string delimiter.
func checkSurrogates(data []byte) error {
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if !inString {
			inString = c == '"'
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			i++
			if i >= len(data) || data[i] != 'u' {
				continue
			}
			r, ok := hex4(data[i+1:])
			if !ok {
				continue
			}
			i += 4
			switch {
			case r >= 0xd800 && r < 0xdc00:
				// A high half must be followed by a low half.
				if i+6 < len(data) && data[i+1] == '\\' && data[i+2] == 'u' {
					if low, ok := hex4(data[i+3:]); ok && low >= 0xdc00 && low < 0xe000 {
						i += 6
						continue
					}
				}
				return invalidf("a string has a lone surrogate escape")
			case r >= 0xdc00 && r < 0xe000:
				return invalidf("a string has a lone surrogate escape")
			}
		}
	}
	return nil
}

// jsonFields maps the JSON name of each field of a struct type to its type.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	m := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		m[name] = f.Type
	}
	return m
}

// requiredFields lists the JSON names of the fields of a struct type that
// carry the tag wire:"required": a payload that lacks one (or has it null) is
// refused, so that a missing field is never read as its zero value.
func requiredFields(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("wire") != "required" || !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" {
			name = f.Name
		}
		names = append(names, name)
	}
	return names
}

// checkKeys walks a decoded JSON tree beside the type it was decoded into and
// refuses a key that is not the exact, case-sensitive JSON name of a field
// (encoding/json would accept a different case).
func checkKeys(path string, t reflect.Type, node any) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		fields := jsonFields(t)
		for _, name := range requiredFields(t) {
			if v, ok := obj[name]; !ok || v == nil {
				return invalidf("missing required field %q%s", name, atPath(path))
			}
		}
		for k, v := range obj {
			ft, ok := fields[k]
			if !ok {
				return invalidf("unknown field %q%s", k, atPath(path))
			}
			if err := checkKeys(path+"."+k, ft, v); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		arr, _ := node.([]any)
		for _, v := range arr {
			if err := checkKeys(path+"[]", t.Elem(), v); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, _ := node.(map[string]any)
		for k, v := range obj {
			if err := checkKeys(path+"."+k, t.Elem(), v); err != nil {
				return err
			}
		}
	}
	return nil
}

func atPath(path string) string {
	if path == "" {
		return ""
	}
	return " in " + path
}

// encodeMessage validates p, encodes it as one JSON object without a trailing
// newline and checks the result against the cap of the frame type.
func encodeMessage(typ byte, p message) ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return nil, invalidf("%v", err)
	}
	out := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	if max, _ := Cap(typ); len(out) > max {
		return nil, fmt.Errorf("%w: %s payload is %d bytes, cap %d", ErrTooLarge, TypeName(typ), len(out), max)
	}
	return out, nil
}
