//go:build linux && ataniq_canary

package sandboxstart

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// checkKeys walks a JSON document against a type and refuses what
// encoding/json accepts silently: a key in another case than the field's tag,
// and a key written twice. An unknown key is refused here too, so that the
// reason names where it is. It is the check the qualification harness makes of
// its own configuration, written again here because no production package may
// import the harness.
func checkKeys(data []byte, t reflect.Type) error {
	return walkKeys(json.NewDecoder(bytes.NewReader(data)), t, "")
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
		fields := taggedFields(t)
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
				return fmt.Errorf("%s has the unknown key %q (keys are exact, in lower case)", where(at), clipText(key))
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

// taggedFields maps the JSON names of a struct's fields to their types. A
// field tagged "-" has no name: a document cannot set it.
func taggedFields(t reflect.Type) map[string]reflect.Type {
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

// clipText cuts text a file gave before it goes into a message.
func clipText(s string) string {
	if len(s) > 48 {
		s = s[:48] + "..."
	}
	return s
}
