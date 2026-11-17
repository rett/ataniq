package assert

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

// maxJSONDepth is the deepest nesting checkKeys follows. A spec and a report
// are at most three levels deep.
const maxJSONDepth = 8

// keyShape says which keys an object of a decoded type may hold, and the shape
// of each value. A nil shape is a scalar or a list of scalars.
type keyShape struct {
	fields map[string]*keyShape
	elem   *keyShape // the shape of the elements of a list of objects
}

// shapeOf builds the shape of a type from its json tags, so the tags stay the
// one source of the names.
func shapeOf(t reflect.Type) *keyShape {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		sh := &keyShape{fields: map[string]*keyShape{}}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			sh.fields[name] = shapeOf(f.Type)
		}
		return sh
	case reflect.Slice:
		if e := shapeOf(t.Elem()); e != nil {
			return &keyShape{elem: e}
		}
	}
	return nil
}

// checkKeys refuses what encoding/json accepts and a strict reader must not:
// a key that is not exactly a field's name (the decoder matches names without
// regard to case), and a key that appears twice in one object (the decoder
// keeps the last). Other readers of the same bytes may take the first, or
// another spelling, so the bytes are refused. what names the input for the
// refusal. A syntax error is returned as is.
func checkKeys(data []byte, sh *keyShape, what string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return walkKeys(dec, sh, what, 0)
}

func walkKeys(dec *json.Decoder, sh *keyShape, path string, depth int) error {
	if depth > maxJSONDepth {
		return refuse(path, "is nested deeper than %d levels", maxJSONDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		if sh != nil && sh.fields == nil {
			return errors.New("an object where a list is expected")
		}
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			if seen[key] {
				return refuse(path, "key %s appears twice", clipQuote(key))
			}
			seen[key] = true
			var child *keyShape
			if sh != nil {
				c, known := sh.fields[key]
				if !known {
					return refuse(path, "key %s is not a field: keys are exact and case-sensitive", clipQuote(key))
				}
				child = c
			}
			if err := walkKeys(dec, child, path+"."+key, depth+1); err != nil {
				return err
			}
		}
	case '[':
		if sh != nil && sh.fields != nil {
			return errors.New("a list where an object is expected")
		}
		var elem *keyShape
		if sh != nil {
			elem = sh.elem
		}
		for i := 0; dec.More(); i++ {
			if err := walkKeys(dec, elem, path+"[]", depth+1); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}
