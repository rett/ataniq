package seatgate

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// checkObject holds data to the strict form of every frame and every payload in
// it, before anything is decoded into a type:
//
//   - it is valid UTF-8 and valid JSON, one value, and that value is an object,
//     with no byte before its opening brace or after its closing one (white
//     space included);
//   - objects and arrays nest at most maxDepth levels;
//   - no object has the same key twice, at any depth, where a key is compared
//     after its escapes are read (so "id" and "id" are the same key).
//
// The standard decoder takes the last of two equal keys and says nothing, which
// is how two readers of one message come to see two messages; this check is the
// reason a reader here cannot. A refusal is a violation with a fixed text.
func checkObject(data []byte, maxDepth int) error {
	if len(data) < 2 || data[0] != '{' || data[len(data)-1] != '}' {
		return violation("not a JSON object")
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		return violation("not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walk(dec, 1, maxDepth); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return violation("bytes after the object")
	}
	return nil
}

// walk reads one value's tokens, the value having depth levels around it, and
// reports a repeated key or a nesting beyond maxDepth.
func walk(dec *json.Decoder, depth, maxDepth int) error {
	tok, err := dec.Token()
	if err != nil {
		return violation("not valid JSON")
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth > maxDepth {
		return violation("nested too deeply")
	}
	switch d {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return violation("not valid JSON")
			}
			key, _ := k.(string)
			if _, dup := seen[key]; dup {
				return violation("repeated key")
			}
			seen[key] = struct{}{}
			if err := walk(dec, depth+1, maxDepth); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walk(dec, depth+1, maxDepth); err != nil {
				return err
			}
		}
	}
	if _, err := dec.Token(); err != nil { // the closing delimiter
		return violation("not valid JSON")
	}
	return nil
}

// fieldSpec names the keys an object may have, and which of them it must have.
type fieldSpec map[string]bool // key -> required

// splitObject checks data with [checkObject] and returns the raw value of each
// top-level key. A key not in spec is refused, whatever its case: the match is
// exact, unlike the standard decoder's. A required key that is absent is
// refused.
func splitObject(data []byte, spec fieldSpec) (map[string]json.RawMessage, error) {
	if err := checkObject(data, MaxDepth); err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, violation("not valid JSON")
	}
	for k := range m {
		if _, ok := spec[k]; !ok {
			return nil, violation("unknown field")
		}
	}
	for k, required := range spec {
		if _, ok := m[k]; required && !ok {
			return nil, violation("missing field")
		}
	}
	return m, nil
}

// DecodeStrict decodes raw, the params of a request or the result of a
// response, into dst, a pointer to a struct, under the rules of the wire: one
// object (an absent one reads as {}), no repeated key at any depth, no field
// the struct does not name, no byte after it. A handler decodes its params
// with it. Like the standard decoder, it matches a field name without regard
// to case, so a struct should have no two fields whose names differ only in
// case. The error is a violation and holds no byte of the data.
func DecodeStrict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := checkObject(raw, MaxDepth); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return violation("fields do not fit")
	}
	return nil
}
