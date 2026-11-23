package switcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

// maxJSONDepth bounds the nesting of a JSON file the switch reads.
const maxJSONDepth = 8

// decodeStrict decodes data, which must be exactly one JSON object, into v,
// a pointer to a struct: a key of the object that is not byte for byte the
// JSON name of one of v's fields, a key given twice in any object, and
// anything after the object but white space are errors. The checks are a
// walk over the tokens before the decoding, since encoding/json matches a
// key to a field without regard to case (so "MIN_MODE" would set min_mode),
// keeps the last of two equal keys and stops reading after the first value.
func decodeStrict(data []byte, v any) error {
	if err := checkOneObject(data, jsonNames(v)); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// jsonNames returns the JSON names of the fields of the struct v points
// at, as their tags give them; nil when v is not a pointer to a struct.
func jsonNames(v any) map[string]bool {
	t := reflect.TypeOf(v)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return nil
	}
	t = t.Elem()
	names := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		names[name] = true
	}
	return names
}

// checkOneObject reports whether data is one JSON object, with no key given
// twice in any object and nothing after it. With names, every key of the
// object (not of the objects nested in it) must be one of them, byte for
// byte.
func checkOneObject(data []byte, names map[string]bool) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("not a JSON object")
	}
	if err := walkObject(dec, 1, names); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the JSON object")
	}
	return nil
}

// walkObject reads the rest of an object whose { was read; names, when
// given, are the keys it may hold.
func walkObject(dec *json.Decoder, depth int, names map[string]bool) error {
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
		if names != nil && !names[key] {
			return errors.New("a key is not one of the declared names")
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
		return walkObject(dec, depth+1, nil)
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
