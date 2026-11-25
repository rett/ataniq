package pack

import (
	"encoding/json/jsontext"
	"fmt"
	"regexp"
)

type HookRow struct {
	ID        string      `json:"id"`
	Event     string      `json:"event"`
	Fields    []HookField `json:"fields,omitempty"`
	Digests   []HookField `json:"digests,omitempty"`
	Output    string      `json:"output"`
	Async     bool        `json:"async"`
	TimeoutMS int         `json:"timeout_ms"`
}

type HookField struct {
	Name    string            `json:"name"`
	Pointer *jsontext.Pointer `json:"pointer"` // the empty pointer selects the whole document
}

var (
	hookID    = regexp.MustCompile("^[a-z0-9.-]{1,40}$")
	fieldName = regexp.MustCompile("^[a-z_]{1,32}$")
)

func checkHooks(rows []HookRow) error {
	ids := make(map[string]bool)
	for _, row := range rows {
		if !hookID.MatchString(row.ID) || ids[row.ID] || row.Event == "" || row.TimeoutMS <= 0 {
			return fmt.Errorf("invalid or repeated hook row %q", row.ID)
		}
		ids[row.ID] = true
		switch row.Output {
		case "none", "continuation", "deny-from-table":
		default:
			return fmt.Errorf("row %q: invalid output %q", row.ID, row.Output)
		}
		if len(row.Fields) > 16 || len(row.Digests) > 4 {
			return fmt.Errorf("row %q: too many fields or digests", row.ID)
		}
		for _, list := range [][]HookField{row.Fields, row.Digests} {
			names := make(map[string]bool)
			for _, field := range list {
				if !fieldName.MatchString(field.Name) || names[field.Name] || field.Pointer == nil || !field.Pointer.IsValid() {
					return fmt.Errorf("row %q: invalid or repeated field %q", row.ID, field.Name)
				}
				names[field.Name] = true
			}
		}
	}
	return nil
}
