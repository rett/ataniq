// Package qargiread interprets fixture bytes for the Qargi runtime crosswalk.
package qargiread

import (
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
)

// Pins uses the crosswalk's JSON pointers. Values are decoded strings, except
// for /schema, whose integer spelling is retained as a json.Number.
type Pins map[string]any

var errObject = errors.New("expected one JSON object without duplicate fields")

func object(data string) (map[string]any, error) {
	raw := jsontext.Value(data)
	if raw.Kind() != '{' || !raw.IsValid() {
		return nil, errObject
	}
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.UseNumber()
	var fields map[string]any
	err := decoder.Decode(&fields)
	return fields, err
}

// ParseDefinition extracts just the nine pinned values. Optional schema-2
// fields have to be present where the crosswalk pins them.
func ParseDefinition(data string) (Pins, error) {
	fields, err := object(data)
	if err != nil {
		return nil, err
	}
	schema, ok := fields["schema"].(json.Number)
	if !ok || strings.ContainsAny(string(schema), ".eE") {
		return nil, errors.New("/schema must be a JSON integer")
	}
	pins := Pins{"/schema": schema}
	for _, path := range []string{"kind", "provider", "family", "surface", "models/catalog", "models/use", "dispatch/mode", "barrier/kind"} {
		parent, key := fields, path
		if name, child, nested := strings.Cut(path, "/"); nested {
			parent, _ = fields[name].(map[string]any)
			key = child
		}
		value, ok := parent[key].(string)
		if !ok {
			return nil, fmt.Errorf("/%s must be a JSON string", path)
		}
		pins["/"+path] = value
	}
	return pins, nil
}
