package switcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// maxJSONDepth bounds the nesting of a JSON file the switch reads.
const maxJSONDepth = 8

// decodeStrict decodes data, which must be exactly one JSON object, into v:
// a key that v does not have, a key given twice in any object, and anything
// after the object but white space are errors. The duplicate and trailing
// checks are a walk over the tokens before the decoding, since encoding/json
// keeps the last of two equal keys and stops reading after the first value.
func decodeStrict(data []byte, v any) error {
	if err := checkOneObject(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// checkOneObject reports whether data is one JSON object, with no key given
// twice in any object and nothing after it.
func checkOneObject(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("not a JSON object")
	}
	if err := walkObject(dec, 1); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the JSON object")
	}
	return nil
}

// walkObject reads the rest of an object whose { was read.
func walkObject(dec *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok && d == '}' {
			return nil
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("an object key is not a string")
		}
		if seen[key] {
			return errors.New("a key is given twice")
		}
		seen[key] = true
		if err := walkValue(dec, depth); err != nil {
			return err
		}
	}
}

// walkValue reads one value.
func walkValue(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= maxJSONDepth {
		return errors.New("nested too deeply")
	}
	switch d {
	case '{':
		return walkObject(dec, depth+1)
	case '[':
		for dec.More() {
			if err := walkValue(dec, depth+1); err != nil {
				return err
			}
		}
		_, err := dec.Token() // ]
		return err
	}
	return errors.New("unexpected delimiter")
}
