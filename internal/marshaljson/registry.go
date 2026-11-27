package marshaljson

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

// Only the frozen schemas are linked into the reader. The case corpus is test-only.
//
//go:embed testdata/qargi-0.3.9/registries.zip
var registries []byte

// shape is the validation subset used by the four frozen J1 registries.
type shape struct {
	Type                 any
	Enum                 []any
	Pattern              *regexp.Regexp
	Required             []string
	Properties           map[string]*shape
	Items                *shape
	AdditionalProperties *shape
	Ref                  string            `json:"$ref"`
	Defs                 map[string]*shape `json:"$defs"`
	Reserved             bool              `json:"x-reserved"`
}

var schemas = loadSchemas()

func loadSchemas() map[Kind]*shape {
	z, err := zip.NewReader(bytes.NewReader(registries), int64(len(registries)))
	if err != nil {
		panic(err)
	}
	schemas := make(map[Kind]*shape)
	for _, kind := range []Kind{Status, Check, Handoff, Refusal} {
		b, err := fs.ReadFile(z, string(kind)+"/v1/registry.json")
		if err != nil {
			panic(err)
		}
		var registry struct{ Document *shape }
		if err := json.Unmarshal(b, &registry); err != nil {
			panic(err)
		}
		schemas[kind] = registry.Document
	}
	return schemas
}

func typeName(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case int64:
		return "integer"
	case bool:
		return "boolean"
	default:
		return "null"
	}
}

// check validates known values in place and removes unrecognized fields. Keys
// of maps such as overlap are data and follow their additionalProperties rule.
func (s *shape) check(v any, defs map[string]*shape, unknown *bool) error {
	if s.Ref != "" {
		return defs[strings.TrimPrefix(s.Ref, "#/$defs/")].check(v, defs, unknown)
	}
	actual := typeName(v)
	switch want := s.Type.(type) {
	case string:
		if actual != want {
			return fmt.Errorf("expected %s, got %s", want, actual)
		}
	case []any:
		if !slices.Contains(want, any(actual)) {
			return fmt.Errorf("expected one of %v, got %s", want, actual)
		}
	}
	if s.Enum != nil && !slices.Contains(s.Enum, v) {
		// The pinned handoff cases require rejection even for gates[].tier,
		// whose registry permits future versions to add words (x-open).
		return fmt.Errorf("value outside declared enum")
	}
	switch value := v.(type) {
	case string:
		if s.Pattern != nil && !s.Pattern.MatchString(value) {
			return fmt.Errorf("string does not match %s", s.Pattern)
		}
	case map[string]any:
		for _, key := range s.Required {
			if _, ok := value[key]; !ok {
				return fmt.Errorf("missing %s", key)
			}
		}
		for key, child := range value {
			field := s.Properties[key]
			if field == nil {
				field = s.AdditionalProperties
			}
			if field == nil || field.Reserved {
				if field == nil {
					*unknown = true
				}
				delete(value, key)
				continue
			}
			if err := field.check(child, defs, unknown); err != nil {
				return fmt.Errorf("%q: %w", key, err)
			}
		}
	case []any:
		for i, child := range value {
			if err := s.Items.check(child, defs, unknown); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
	}
	return nil
}
