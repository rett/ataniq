package canon

import (
	"errors"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Parse reads exactly one JSON value from data: valid UTF-8, no float, no
// null, no control character in a string, no repeated key, no nesting of
// arrays and objects deeper than maxDepth, and nothing but whitespace after
// the value. The error text is this package's own and holds no byte of
// data. Parse holds no size cap: the caller checks the size before it reads.
func Parse(data []byte, maxDepth int) (Value, error) {
	if !utf8.Valid(data) {
		return Value{}, errors.New("the data is not valid UTF-8")
	}
	p := reader{data: data, maxDepth: maxDepth}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return Value{}, errors.New(err.Error() + " at byte " + strconv.Itoa(p.pos))
	}
	p.space()
	if p.pos != len(p.data) {
		return Value{}, errors.New("data after the JSON value at byte " + strconv.Itoa(p.pos))
	}
	return v, nil
}

// reader walks the bytes of one value.
type reader struct {
	data     []byte
	pos      int
	maxDepth int
}

func (p *reader) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

// peek returns the next byte, or 0 at the end. A NUL byte is not a valid
// start of anything, so 0 needs no case of its own.
func (p *reader) peek() byte {
	if p.pos < len(p.data) {
		return p.data[p.pos]
	}
	return 0
}

func (p *reader) value(depth int) (Value, error) {
	switch c := p.peek(); {
	case c == '"':
		s, err := p.string()
		return Str(s), err
	case c == '-' || (c >= '0' && c <= '9'):
		n, err := p.number()
		return Num(n), err
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case p.word("true"):
		return Boolean(true), nil
	case p.word("false"):
		return Boolean(false), nil
	case p.word("null"):
		return Value{}, errors.New("null is not allowed")
	case p.pos == len(p.data):
		return Value{}, errors.New("the JSON value ends early")
	default:
		return Value{}, errors.New("not a JSON value")
	}
}

// word consumes w when the data continues with it.
func (p *reader) word(w string) bool {
	if len(p.data)-p.pos >= len(w) && string(p.data[p.pos:p.pos+len(w)]) == w {
		p.pos += len(w)
		return true
	}
	return false
}

// number reads an integer in plain decimal: an optional minus, then 0 or
// digits that do not start with 0. A fraction or an exponent makes it a
// float, which is refused, and so is a value outside int64 and minus zero.
func (p *reader) number() (int64, error) {
	start := p.pos
	if p.peek() == '-' {
		p.pos++
	}
	digits := p.pos
	for c := p.peek(); c >= '0' && c <= '9'; c = p.peek() {
		p.pos++
	}
	switch {
	case p.pos == digits:
		return 0, errors.New("a number has no digits")
	case p.data[digits] == '0' && p.pos-digits > 1:
		return 0, errors.New("a number starts with a zero")
	}
	if c := p.peek(); c == '.' || c == 'e' || c == 'E' {
		return 0, errors.New("a number is not an integer")
	}
	text := string(p.data[start:p.pos])
	if text == "-0" {
		return 0, errors.New("a number is minus zero")
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, errors.New("a number is too large")
	}
	return n, nil
}

// string reads a JSON string and returns its text with the escapes
// resolved. A control character is refused whether it is written raw or as
// an escape.
func (p *reader) string() (string, error) {
	p.pos++ // the opening quote
	var out []byte
	for {
		if p.pos >= len(p.data) {
			return "", errors.New("a string does not end")
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			return string(out), nil
		case c == '\\':
			r, err := p.escape()
			if err != nil {
				return "", err
			}
			if IsControl(r) {
				return "", errors.New("a control character in a string")
			}
			out = utf8.AppendRune(out, r)
		default:
			// The data is valid UTF-8, so this decodes a whole rune.
			r, size := utf8.DecodeRune(p.data[p.pos:])
			if IsControl(r) {
				return "", errors.New("a control character in a string")
			}
			out = append(out, p.data[p.pos:p.pos+size]...)
			p.pos += size
		}
	}
}

// escape reads one escape sequence, the backslash included, and returns the
// character it stands for. A \u escape of a high surrogate must be followed
// by one of a low surrogate; a surrogate alone is refused.
func (p *reader) escape() (rune, error) {
	p.pos++ // the backslash
	if p.pos >= len(p.data) {
		return 0, errors.New("a string does not end")
	}
	c := p.data[p.pos]
	p.pos++
	switch c {
	case '"', '\\', '/':
		return rune(c), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		r, err := p.hex4()
		if err != nil {
			return 0, err
		}
		if !utf16.IsSurrogate(r) {
			return r, nil
		}
		if !p.word(`\u`) {
			return 0, errors.New("a surrogate escape stands alone")
		}
		low, err := p.hex4()
		if err != nil {
			return 0, err
		}
		if r >= 0xdc00 || low < 0xdc00 || low > 0xdfff {
			return 0, errors.New("a surrogate escape stands alone")
		}
		return utf16.DecodeRune(r, low), nil
	default:
		p.pos--
		return 0, errors.New("an unknown escape in a string")
	}
}

func (p *reader) hex4() (rune, error) {
	if len(p.data)-p.pos < 4 {
		return 0, errors.New("a \\u escape is cut short")
	}
	var r rune
	for _, c := range p.data[p.pos : p.pos+4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, errors.New("a \\u escape is not four hexadecimal digits")
		}
	}
	p.pos += 4
	return r, nil
}

func (p *reader) array(depth int) (Value, error) {
	if depth >= p.maxDepth {
		return Value{}, errors.New("the data nests too deep")
	}
	p.pos++ // the opening bracket
	v := Value{Kind: Array, Arr: []Value{}}
	p.space()
	if p.peek() == ']' {
		p.pos++
		return v, nil
	}
	for {
		p.space()
		e, err := p.value(depth + 1)
		if err != nil {
			return Value{}, err
		}
		v.Arr = append(v.Arr, e)
		p.space()
		switch p.peek() {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return v, nil
		default:
			return Value{}, errors.New("a list is not well formed")
		}
	}
}

func (p *reader) object(depth int) (Value, error) {
	if depth >= p.maxDepth {
		return Value{}, errors.New("the data nests too deep")
	}
	p.pos++ // the opening brace
	v := Value{Kind: Object}
	p.space()
	if p.peek() == '}' {
		p.pos++
		return v, nil
	}
	seen := make(map[string]struct{})
	for {
		p.space()
		if p.peek() != '"' {
			return Value{}, errors.New("an object key is not a string")
		}
		at := p.pos
		key, err := p.string()
		if err != nil {
			return Value{}, err
		}
		if _, dup := seen[key]; dup {
			p.pos = at
			return Value{}, errors.New("a key is repeated in one object")
		}
		seen[key] = struct{}{}
		p.space()
		if p.peek() != ':' {
			return Value{}, errors.New("an object key has no colon")
		}
		p.pos++
		p.space()
		e, err := p.value(depth + 1)
		if err != nil {
			return Value{}, err
		}
		v.Obj = append(v.Obj, Member{Key: key, Val: e})
		p.space()
		switch p.peek() {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return v, nil
		default:
			return Value{}, errors.New("an object is not well formed")
		}
	}
}
