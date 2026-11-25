package hookwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"ataniq/internal/pack"
)

// Extract reads at most MaxInputBytes+1 bytes. The helper supplies a reader
// with its input deadline already set. A timeout or excess byte produces just
// the row and size observation, even if the bytes so far look like valid JSON.
// Other read errors and malformed complete inputs return no request.
// Kept fields accept strings, booleans and numbers. Digests cover the selected
// JSON token's exact bytes, including quotes, escapes and internal whitespace.
func Extract(row pack.HookRow, in io.Reader) (*Request, error) {
	if err := validateRow(row); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(in, MaxInputBytes+1))
	var timeout interface{ Timeout() bool }
	cut := len(data) > MaxInputBytes || (errors.As(err, &timeout) && timeout.Timeout())
	if err != nil && !cut {
		return nil, err
	}
	r := &Request{
		Version: Version, Kind: Event, Row: row.ID,
		Fields: make(map[string]string), Digests: make(map[string]string),
		Size: min(len(data), MaxInputBytes), Cut: cut,
	}
	if row.Output == "continuation" {
		r.Kind = Fetch
	}
	if cut {
		return r, nil
	}
	// Check the entire document, including subtrees untouched by any pointer.
	if !jsontext.Value(data).IsValid() {
		return nil, errors.New("malformed hook input")
	}
	for _, field := range row.Fields {
		raw, err := selectValue(data, *field.Pointer)
		if err != nil {
			return nil, err
		}
		switch raw.Kind() {
		case '"':
			s, err := jsontext.AppendUnquote(nil, raw)
			if err != nil {
				return nil, err
			}
			r.Fields[field.Name] = keptText(string(s))
		case 't', 'f', '0':
			r.Fields[field.Name] = keptText(string(raw))
		}
	}
	for _, field := range row.Digests {
		raw, err := selectValue(data, *field.Pointer)
		if err != nil {
			return nil, err
		}
		if raw != nil {
			sum := sha256.Sum256(raw)
			r.Digests[field.Name] = hex.EncodeToString(sum[:])
		}
	}
	return r, nil
}

// selectValue walks a validated input along one RFC 6901 pointer. Skipping
// siblings avoids materializing unrelated values or re-parsing each ancestor.
// ReadValue preserves the selected token's spelling and internal whitespace.
func selectValue(data []byte, pointer jsontext.Pointer) (jsontext.Value, error) {
	d := jsontext.NewDecoder(bytes.NewBuffer(data))
	for key := range pointer.Tokens() {
		parent, err := d.ReadToken()
		if err != nil {
			return nil, err
		}
		switch parent.Kind() {
		case '{':
			for {
				name, err := d.ReadToken()
				if err != nil {
					return nil, err
				}
				if name.Kind() == '}' {
					return nil, nil
				}
				if name.String() == key {
					break
				}
				if err := d.SkipValue(); err != nil {
					return nil, err
				}
			}
		case '[':
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || strconv.Itoa(index) != key {
				return nil, nil
			}
			for range index {
				if d.PeekKind() == ']' {
					return nil, nil
				}
				if err := d.SkipValue(); err != nil {
					return nil, err
				}
			}
			if d.PeekKind() == ']' {
				return nil, nil
			}
		default:
			return nil, nil
		}
	}
	return d.ReadValue()
}

func keptText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsPrint(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > MaxFieldBytes {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
