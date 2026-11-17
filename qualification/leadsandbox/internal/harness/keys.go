//go:build linux && amd64

package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

func reflectType[T any]() reflect.Type { return reflect.TypeFor[T]() }

// checkKeys walks the JSON document against the type and refuses what
// encoding/json would accept silently: a key in another case than the field's
// tag, and a key written twice. (An unknown key is refused by the decoder's
// DisallowUnknownFields, and here too, so the reason names the path.)
func checkKeys(data []byte, t reflect.Type) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return walkKeys(dec, t, "")
}

func walkKeys(dec *json.Decoder, t reflect.Type, at string) error {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("is not JSON: %v", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		if t == nil || t.Kind() != reflect.Struct {
			return fmt.Errorf("%s holds an object where none is expected", where(at))
		}
		fields := tagged(t)
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return fmt.Errorf("is not JSON: %v", err)
			}
			key, _ := kt.(string)
			ft, known := fields[key]
			switch {
			case !known:
				return fmt.Errorf("%s has the unknown key %q (keys are exact, in lower case)", where(at), clip(key))
			case seen[key]:
				return fmt.Errorf("%s has the key %q twice", where(at), key)
			}
			seen[key] = true
			if err := walkKeys(dec, ft, at+"."+key); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	case '[':
		if t == nil || (t.Kind() != reflect.Slice && t.Kind() != reflect.Array) {
			return fmt.Errorf("%s holds a list where none is expected", where(at))
		}
		for dec.More() {
			if err := walkKeys(dec, t.Elem(), at+"[]"); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	}
	return nil
}

func where(at string) string {
	if at == "" {
		return "the document"
	}
	return strings.TrimPrefix(at, ".")
}

// tagged maps the JSON names of a struct's fields to their types.
func tagged(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}
