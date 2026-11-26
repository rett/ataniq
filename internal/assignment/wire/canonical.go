// Package wire encodes schema-1 assignment projections and their signatures.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"ataniq/internal/assignment"
)

const recordLimit = 64 << 10

var errJSON = errors.New("assignment wire: invalid JSON")

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Check them
// before decoding so different wire strings cannot acquire the same signature.
// The caller has already checked JSON syntax, including escape lengths.
func validUnicode(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for {
		i := bytes.IndexByte(b, '\\')
		if i < 0 {
			return true
		}
		b = b[i+1:]
		if b[0] != 'u' {
			b = b[1:]
			continue
		}
		n, _ := strconv.ParseUint(string(b[1:5]), 16, 16)
		b = b[5:]
		if !utf16.IsSurrogate(rune(n)) {
			continue
		}
		if n >= 0xdc00 || len(b) < 6 || b[0] != '\\' || b[1] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(b[2:6]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		b = b[6:]
	}
}

// Canonicalize returns RFC 8785 bytes for the assignment JSON subset.
// The only schema-1 number is the schema major. Numeric input is limited
// to interoperable exact integers. Assignment counters are strings.
func Canonicalize(b []byte) ([]byte, error) {
	if len(b) > recordLimit {
		return nil, assignment.ErrLimit
	}
	if !json.Valid(b) || !validUnicode(b) {
		return nil, errJSON
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := readValue(d)
	if err != nil {
		return nil, err
	}
	return appendValue(nil, v), nil
}

// Syntax has already been validated. Walk tokens to catch duplicate names
// after unescaping, before a map or a struct can discard either value.
func readValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		var value any
		if t == '{' {
			obj := make(map[string]any)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k := key.(string)
				if _, exists := obj[k]; exists {
					return nil, fmt.Errorf("%w: duplicate member %q", errJSON, k)
				}
				obj[k], err = readValue(d)
				if err != nil {
					return nil, err
				}
			}
			value = obj
		} else {
			var array []any
			for d.More() {
				item, err := readValue(d)
				if err != nil {
					return nil, err
				}
				array = append(array, item)
			}
			value = array
		}
		_, err := d.Token()
		return value, err
	case json.Number:
		n, err := t.Int64()
		const max = 1<<53 - 1
		if err != nil || n < -max || n > max {
			return nil, fmt.Errorf("%w: expected an exact integer", errJSON)
		}
		return n, nil
	default:
		return t, nil
	}
}

func appendValue(b []byte, value any) []byte {
	switch v := value.(type) {
	case map[string]any:
		keys := slices.Collect(maps.Keys(v))
		slices.SortFunc(keys, func(a, b string) int {
			return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
		})
		b = append(b, '{')
		for i, k := range keys {
			if i != 0 {
				b = append(b, ',')
			}
			b = appendString(b, k)
			b = appendValue(append(b, ':'), v[k])
		}
		return append(b, '}')
	case []any:
		b = append(b, '[')
		for i, item := range v {
			if i != 0 {
				b = append(b, ',')
			}
			b = appendValue(b, item)
		}
		return append(b, ']')
	case string:
		return appendString(b, v)
	case int64:
		return strconv.AppendInt(b, v, 10)
	case bool:
		return strconv.AppendBool(b, v)
	default: // JSON null
		return append(b, "null"...)
	}
}

func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	const hex = "0123456789abcdef"
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			b = append(b, '\\', c)
		case '\b':
			b = append(b, '\\', 'b')
		case '\t':
			b = append(b, '\\', 't')
		case '\n':
			b = append(b, '\\', 'n')
		case '\f':
			b = append(b, '\\', 'f')
		case '\r':
			b = append(b, '\\', 'r')
		default:
			if c < ' ' {
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			} else {
				b = append(b, c)
			}
		}
	}
	return append(b, '"')
}
