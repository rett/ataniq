package marshaljson

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Validate the complete, size-checked input before walking
// tokens so truncation or a second document cannot yield a partial result.
func object(data []byte) (map[string]any, error) {
	if !json.Valid(data) || !validText(data) {
		return nil, errors.New("invalid J1 JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := readValue(d)
	if err != nil {
		return nil, err
	}
	fields, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("expected a J1 object")
	}
	return fields, nil
}

// Go replaces lone surrogate escapes during decoding. Check them while the
// original string bytes are still available. JSON syntax was checked above.
func validText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for {
		i := bytes.IndexByte(data, '\\')
		if i < 0 {
			return true
		}
		data = data[i+1:]
		if data[0] != 'u' {
			data = data[1:]
			continue
		}
		n, _ := strconv.ParseUint(string(data[1:5]), 16, 16)
		data = data[5:]
		if !utf16.IsSurrogate(rune(n)) {
			continue
		}
		if n >= 0xdc00 || len(data) < 6 || !bytes.HasPrefix(data, []byte(`\u`)) {
			return false
		}
		low, err := strconv.ParseUint(string(data[2:6]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		data = data[6:]
	}
}

func readValue(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch v := token.(type) {
	case json.Number:
		const max = 1<<53 - 1
		n, err := v.Int64()
		if err != nil || n < -max || n > max {
			return nil, errors.New("expected an integer within the J1 range")
		}
		return n, nil
	case json.Delim:
		if v == '{' {
			obj := make(map[string]any)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k := key.(string)
				if _, duplicate := obj[k]; duplicate {
					return nil, errors.New("duplicate J1 object key")
				}
				obj[k], err = readValue(d)
				if err != nil {
					return nil, err
				}
			}
			_, err := d.Token()
			return obj, err
		}
		items := []any{}
		for d.More() {
			item, err := readValue(d)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		_, err := d.Token()
		return items, err
	default:
		return token, nil
	}
}
