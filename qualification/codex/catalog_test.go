package codex

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogValidation(t *testing.T) {
	request := func() map[string]any {
		var catalog []any
		if err := json.Unmarshal(knownCatalog, &catalog); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"input": []any{map[string]any{"type": "additional_tools", "tools": catalog}}}
	}
	check := func(m map[string]any) error { b, _ := json.Marshal(m); return validateCatalog(b) }
	if err := check(request()); err != nil {
		t.Fatalf("observed catalog: %v", err)
	}
	for _, tc := range []struct {
		name string
		tool any
	}{
		{"function", map[string]any{"type": "function", "name": "unexpected"}},
		{"web search", map[string]any{"type": "web_search"}},
		{"mcp", map[string]any{"type": "mcp", "server_label": "unknown"}},
		{"namespace", map[string]any{"type": "namespace", "name": "unknown", "tools": []any{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := request()
			m["tools"] = []any{tc.tool}
			if check(m) == nil {
				t.Fatal("unknown direct entry accepted")
			}
		})
	}
	t.Run("nested name", func(t *testing.T) {
		m := request()
		ns := m["input"].([]any)[0].(map[string]any)["tools"].([]any)[0].(map[string]any)
		ns["tools"].([]any)[0].(map[string]any)["name"] = "unexpected"
		if check(m) == nil {
			t.Fatal("unknown nested name accepted")
		}
	})
	t.Run("schema", func(t *testing.T) {
		m := request()
		ns := m["input"].([]any)[0].(map[string]any)["tools"].([]any)[0].(map[string]any)
		ns["tools"].([]any)[0].(map[string]any)["format"] = map[string]any{"type": "other"}
		if check(m) == nil {
			t.Fatal("changed format accepted")
		}
	})
	t.Run("late addition", func(t *testing.T) {
		first := request()
		late := request()
		late["input"] = append(late["input"].([]any), map[string]any{"type": "additional_tools", "tools": []any{map[string]any{"type": "web_search"}}})
		if check(first) != nil || check(late) == nil {
			t.Fatal("late catalog change accepted")
		}
	})
}

func TestPatchScriptArgument(t *testing.T) {
	const prefix = `try { text(await tools.apply_patch(`
	const suffix = `)); } catch(e) { text(String(e)); }`
	if !strings.HasPrefix(patchScript, prefix) || !strings.HasSuffix(patchScript, suffix) {
		t.Fatal("unexpected wrapper")
	}
	var arg string
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(patchScript, prefix), suffix)), &arg); err != nil {
		t.Fatal(err)
	}
	if arg != "*** Begin Patch\n*** Add File: forbidden-patch\n+forbidden\n*** End Patch" {
		t.Fatalf("patch argument: %q", arg)
	}
}
