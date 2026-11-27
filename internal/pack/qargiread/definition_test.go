package qargiread

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

const definition = `{"schema":2,"kind":"provider","provider":"codex","family":"openai","surface":"local","models":{"catalog":"openai","use":"id"},"dispatch":{"mode":"command"},"barrier":{"kind":"sandbox-args"}}`

func TestPinnedFields(t *testing.T) {
	want := Pins{"/schema": json.Number("2"), "/kind": "provider", "/provider": "codex", "/family": "openai", "/surface": "local", "/models/catalog": "openai", "/models/use": "id", "/dispatch/mode": "command", "/barrier/kind": "sandbox-args"}
	for _, data := range []string{definition, strings.Replace(definition, `"codex"`, `"cod\u0065x"`, 1), strings.Replace(definition, `"models":{`, `"extra":[{"opaque":true}],"models":{"extra":5,`, 1)} {
		if got, err := ParseDefinition(data); err != nil || !maps.Equal(got, want) {
			t.Fatalf("pins = %v, %v; want %v", got, err, want)
		}
	}
	for pointer, original := range want {
		t.Run(pointer, func(t *testing.T) {
			path := strings.Split(pointer[1:], "/")
			for _, edit := range []string{"missing", "null", "wrong type", "different value"} {
				fields, _ := object(definition)
				parent := fields
				if len(path) == 2 {
					parent = fields[path[0]].(map[string]any)
				}
				key := path[len(path)-1]
				switch edit {
				case "missing":
					delete(parent, key)
				case "null":
					parent[key] = nil
				case "wrong type":
					parent[key] = false
				case "different value":
					if pointer == "/schema" {
						parent[key] = json.Number("2000000000000000000000000000")
					} else {
						parent[key] = ""
					}
				}
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				got, err := ParseDefinition(string(raw))
				if edit == "different value" {
					if err != nil || got[pointer] == original {
						t.Errorf("well-typed change = %v, %v", got, err)
					}
				} else if err == nil {
					t.Errorf("accepted %s", edit)
				}
			}
		})
	}
}

func TestMalformedDefinitions(t *testing.T) {
	for _, data := range []string{
		"", "null", "[]", definition + "{}", definition + "x",
		strings.Replace(definition, `"schema":2`, `"schema":"2"`, 1),
		strings.Replace(definition, `"schema":2`, `"schema":2.0`, 1),
		strings.Replace(definition, `"schema":2`, `"schema":2e0`, 1),
		strings.Replace(definition, `"family"`, `"Family"`, 1),
		strings.Replace(definition, `"dispatch":{"mode":"command"}`, `"dispatch":null`, 1),
		strings.Replace(definition, `"models":{"catalog":"openai","use":"id"}`, `"models":{"pattern":"x","tiers":{}}`, 1),
		strings.Replace(definition, `"barrier":{"kind":"sandbox-args"}`, `"extra":[{"a":1,"\u0061":2}],"barrier":{"kind":"sandbox-args"}`, 1),
		strings.Replace(definition, "openai", "\xff", 1),
	} {
		if _, err := ParseDefinition(data); err == nil {
			t.Errorf("accepted %q", data)
		}
	}
}
