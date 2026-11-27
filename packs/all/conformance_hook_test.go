package all_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/hookwire"
	"ataniq/internal/pack"
	"ataniq/packs/all"
	"ataniq/packs/claude"
	claudehook "ataniq/packs/claude/hook"
	"ataniq/packs/codex"
	codexhook "ataniq/packs/codex/hook"
)

func TestHookDeclarations(t *testing.T) {
	r := pack.New()
	if err := all.Register(r); err != nil {
		t.Fatal(err)
	}
	r.Freeze()
	for _, name := range r.Names() {
		t.Run(string(name), func(t *testing.T) {
			p, _ := r.Lookup(name)
			data, err := os.ReadFile(filepath.Join("..", string(name), pack.HooksPath))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := checkHookCompanion(p.Manifest().Builds, data); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func checkHookCompanion(builds []pack.Build, data []byte) error {
	var tables map[string]hookwire.Table
	if data != nil {
		// DecodeTable validates every field and digest pointer, including in
		// documentary tables for builds that do not yet advertise hooks.
		var err error
		tables, err = hookwire.DecodeTable(data)
		if err != nil {
			return err
		}
	}
	for _, b := range builds {
		rows := tables[b.Version]
		for _, iface := range b.Interfaces {
			if iface.Kind == "hooks" && len(rows) == 0 {
				return fmt.Errorf("build %s declares hooks but has no rows", b.Version)
			}
		}
		for _, row := range rows {
			// Keep the no-allow contract independent of the decoder's vocabulary.
			switch row.Output {
			case "none", "continuation", "deny-from-table":
			default:
				return fmt.Errorf("build %s row %s: unsafe output %q", b.Version, row.ID, row.Output)
			}
		}
		delete(tables, b.Version)
	}
	for version := range tables {
		return fmt.Errorf("hook rows for undeclared build %q", version)
	}
	return nil
}

func TestHookCompanionRefusals(t *testing.T) {
	builds := []pack.Build{{Version: "1", Interfaces: []pack.Interface{{Kind: "hooks"}}}}
	const valid = `{"schema":"ataniq.pack-declaration/1","builds":{"1":[{
		"id":"test","event":"Stop","async":false,"timeout_ms":1,"output":"none",
		"fields":[{"name":"field","pointer":"/value"}],
		"digests":[{"name":"hash","pointer":"/input"}]
	}]}}`
	if err := checkHookCompanion(builds, []byte(valid)); err != nil {
		t.Fatal(err)
	}
	edit := func(old, replacement string) []byte {
		return []byte(strings.Replace(valid, old, replacement, 1))
	}
	for name, data := range map[string][]byte{
		"missing companion": nil,
		"empty rows":        []byte(`{"schema":"ataniq.pack-declaration/1","builds":{"1":[]}}`),
		"other build only":  edit(`"1":`, `"2":`),
		"undeclared build":  edit(`"1":`, `"2":[],"1":`),
		"allow":             edit("none", "allow"),
		"field pointer":     edit("/value", "bad"),
		"digest pointer":    edit("/input", "/~2"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkHookCompanion(builds, data); err == nil {
				t.Fatal("accepted invalid hook companion")
			}
		})
	}
}

func hookFixtureFrame(t *testing.T, row pack.HookRow, input json.RawMessage) []byte {
	t.Helper()
	req, err := hookwire.Extract(row, bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := hookwire.EncodeRequest(*req)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestClaudeHookFixtureReplay(t *testing.T) {
	var fixture struct {
		Build  string
		Inputs []json.RawMessage
	}
	if err := json.Unmarshal(read(t, "../claude/hook/testdata/documented.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	table, ok := (claude.Pack{}).HookRows(fixture.Build)
	if !ok {
		t.Fatalf("no rows for fixture build %q", fixture.Build)
	}
	claim := func(state agentsession.State) *agentsession.Claim {
		return &agentsession.Claim{State: state, Source: "hook", Basis: "self"}
	}
	hash := digest([]byte(`{"file_path":"sample.txt"}`))
	events := map[string]claudehook.Event{
		"SessionStart":      {Kind: "session-start", Model: "sample-model", Source: "startup"},
		"UserPromptSubmit":  {Kind: "turn-start", Claim: claim("busy")},
		"PreToolUse":        {Kind: "tool-start", Claim: claim("busy"), Tool: "Read", InputDigest: hash},
		"PermissionRequest": {Kind: "permission", Claim: claim("blocked"), Tool: "Read", InputDigest: hash},
		"Notification":      {Kind: "notification", Claim: claim("blocked"), Reason: "permission_prompt"},
		"Stop":              {Kind: "turn-complete", Claim: claim("idle")},
		"StopFailure":       {Kind: "turn-failed"},
		"SessionEnd":        {Kind: "session-end", Reason: "logout"},
		"ConfigChange":      {Kind: "config-change", Source: "project_settings"},
	}
	if len(table) != len(events) {
		t.Fatalf("got %d rows for %d expected events", len(table), len(events))
	}
	for _, input := range fixture.Inputs {
		var header struct {
			Event string `json:"hook_event_name"`
		}
		if err := json.Unmarshal(input, &header); err != nil {
			t.Fatal(err)
		}
		want, ok := events[header.Event]
		if !ok {
			t.Fatalf("unexpected or repeated fixture event %q", header.Event)
		}
		delete(events, header.Event)
		t.Run(header.Event, func(t *testing.T) {
			index := slices.IndexFunc(table, func(r pack.HookRow) bool { return r.Event == header.Event })
			if index < 0 {
				t.Fatal("no row for fixture event")
			}
			row := table[index]
			want.Mode = "default"
			want.Report = agentsession.HookReport{Kind: "other", Session: "a0000000-0000-4000-8000-000000000001"}
			output := "none"
			switch header.Event {
			case "SessionStart":
				want.Report.Kind = "session-start"
			case "ConfigChange":
				want.Report.Kind = "config-change"
				output = "deny-from-table"
			}
			if row.Output != output {
				t.Errorf("output = %q, want %q", row.Output, output)
			}
			req, err := table.DecodeRequest(hookFixtureFrame(t, row, input))
			if err != nil {
				t.Fatal(err)
			}
			got, err := claudehook.Decode(row, *req)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("decoded %+v, %v; want %+v", got, err, want)
			}
		})
	}
	for event := range events {
		t.Errorf("missing fixture for %s", event)
	}
}

func TestCodexHookFixtureReplay(t *testing.T) {
	var fixture struct {
		Build string
		Cases []struct {
			Row   string
			State agentsession.State
			Input json.RawMessage
		}
	}
	if err := json.Unmarshal(read(t, "../codex/hook/testdata/events.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	p := codex.Pack{}
	d, err := p.Declaration()
	if err != nil {
		t.Fatal(err)
	}
	table := hookwire.Table(d.Hooks[fixture.Build])
	if len(table) == 0 {
		t.Fatalf("no rows for fixture build %q", fixture.Build)
	}
	seen := make(map[string]bool)
	for _, c := range fixture.Cases {
		t.Run(c.Row, func(t *testing.T) {
			row, ok := table.Lookup(c.Row)
			if !ok || seen[c.Row] {
				t.Fatalf("unexpected or repeated fixture row %q", c.Row)
			}
			seen[c.Row] = true
			if row.Output != "none" {
				t.Errorf("output = %q, want none", row.Output)
			}
			frame := hookFixtureFrame(t, row, c.Input)
			if c.Row == "permission-request" && bytes.Contains(frame, []byte(`"allow"`)) {
				t.Error("extracted frame retained the fixture's permission decision")
			}
			want := codexhook.Observation{
				Row: c.Row, Report: agentsession.HookReport{Kind: "other", Session: "sample-session"},
				Claim: agentsession.Claim{State: c.State, Basis: "self", Source: "hook"},
				Model: "sample-model", PermissionMode: "sample-mode",
			}
			switch c.Row {
			case "session-start":
				want.Report.Kind = "session-start"
			case "pre-tool", "post-tool", "permission-request":
				var tool struct {
					Input json.RawMessage `json:"tool_input"`
				}
				if err := json.Unmarshal(c.Input, &tool); err != nil {
					t.Fatal(err)
				}
				want.Tool, want.InputDigest = "example", digest(tool.Input)
			case "stop":
				active := true
				want.StopHookActive = &active
			}
			got, err := p.DecodeHook(fixture.Build, frame)
			if err != nil || !reflect.DeepEqual(got, &want) {
				t.Fatalf("decoded %+v, %v; want %+v", got, err, want)
			}
		})
	}
	for _, row := range table {
		if !seen[row.ID] {
			t.Errorf("missing fixture for %s", row.ID)
		}
	}
}
