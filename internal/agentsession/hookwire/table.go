package hookwire

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"ataniq/internal/pack"
)

// Table is the ordered hook declaration for one exact provider build. A caller
// that already has a validated pack declaration can use Table(d.Hooks[build]).
type Table []pack.HookRow

// DecodeTable reads the standalone hook/rows.json companion. Matching its build
// keys to a manifest is part of pack.DecodeDeclaration.
func DecodeTable(data []byte) (map[string]Table, error) {
	if len(data) > pack.MaxDeclarationBytes {
		return nil, errors.New("hook table exceeds declaration bound")
	}
	// A synchronous row must explicitly say async:false.
	type wireRow struct {
		pack.HookRow
		Async *bool `json:"async"`
	}
	var doc struct {
		Schema string               `json:"schema"`
		Builds map[string][]wireRow `json:"builds"`
	}
	if err := decodeJSON(data, &doc); err != nil {
		return nil, err
	}
	if doc.Schema != pack.DeclarationSchema || doc.Builds == nil {
		return nil, errors.New("missing hook builds or wrong schema")
	}
	builds := make(map[string]Table, len(doc.Builds))
	for _, version := range slices.Sorted(maps.Keys(doc.Builds)) {
		rows := doc.Builds[version]
		if strings.TrimSpace(version) == "" {
			return nil, errors.New("missing hook build version")
		}
		table := make(Table, 0, len(rows))
		ids := make(map[string]bool, len(rows))
		for _, wire := range rows {
			row := wire.HookRow
			if wire.Async == nil || ids[row.ID] {
				return nil, fmt.Errorf("build %q: missing async or repeated row", version)
			}
			if err := validateRow(row); err != nil {
				return nil, fmt.Errorf("build %q: %w", version, err)
			}
			row.Async = *wire.Async
			ids[row.ID] = true
			table = append(table, row)
		}
		builds[version] = table
	}
	return builds, nil
}

func validateRow(row pack.HookRow) error {
	if !validRow(row.ID) || row.Event == "" || row.TimeoutMS < 1 || row.TimeoutMS > 3_600_000 {
		return errors.New("invalid hook row")
	}
	switch row.Output {
	case "none", "continuation", "deny-from-table":
	default:
		return errors.New("unknown hook output form")
	}
	if len(row.Fields) > MaxFields || len(row.Digests) > MaxDigests {
		return errors.New("too many hook row selections")
	}
	for _, list := range [][]pack.HookField{row.Fields, row.Digests} {
		names := make(map[string]bool, len(list))
		for _, field := range list {
			if !validName(field.Name) || names[field.Name] || field.Pointer == nil || !field.Pointer.IsValid() {
				return errors.New("invalid or repeated hook selection")
			}
			names[field.Name] = true
		}
	}
	return nil
}

// Lookup selects a row by its declared ID.
func (t Table) Lookup(id string) (pack.HookRow, bool) {
	for _, row := range t {
		if row.ID == id {
			return row, true
		}
	}
	return pack.HookRow{}, false
}

// DecodeRequest checks both the wire and the selected table. A fetch needs a
// continuation row. A missing selection produces no member on the wire.
func (t Table) DecodeRequest(data []byte) (*Request, error) {
	r, err := DecodeRequest(data)
	if err != nil {
		return nil, err
	}
	row, ok := t.Lookup(r.Row)
	if !ok || (r.Kind == Fetch && row.Output != "continuation") {
		return nil, errors.New("hook request not allowed for row")
	}
	if !declared(r.Fields, row.Fields) || !declared(r.Digests, row.Digests) {
		return nil, errors.New("undeclared hook selection")
	}
	return r, nil
}

func declared(values map[string]string, list []pack.HookField) bool {
	for name := range values {
		if !slices.ContainsFunc(list, func(f pack.HookField) bool { return f.Name == name }) {
			return false
		}
	}
	return true
}
