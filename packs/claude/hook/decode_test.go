package hook_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/hookwire"
	"ataniq/internal/pack"
	"ataniq/packs/claude"
	"ataniq/packs/claude/hook"
)

const session = "a0000000-0000-4000-8000-000000000001"

type examples struct {
	Source, Status, Build string
	VerifiedOn            *string `json:"verified_on"`
	Inputs                []json.RawMessage
}

func documented(t testing.TB) (examples, hookwire.Table) {
	t.Helper()
	raw, err := os.ReadFile("testdata/documented.json")
	if err != nil {
		t.Fatal(err)
	}
	var ex examples
	if err := json.Unmarshal(raw, &ex); err != nil {
		t.Fatal(err)
	}
	if ex.Source != "https://code.claude.com/docs/en/hooks" || ex.Status != "not verified" ||
		ex.VerifiedOn == nil || *ex.VerifiedOn != "" || ex.Build != "2.1.332" {
		t.Fatal("missing provenance for documentary examples")
	}
	table, ok := (claude.Pack{}).HookRows(ex.Build)
	if !ok {
		t.Fatal("fixture build has no rows")
	}
	return ex, table
}

func exampleRow(t testing.TB, table hookwire.Table, input []byte) pack.HookRow {
	t.Helper()
	var doc struct {
		Name string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(input, &doc); err != nil {
		t.Fatal(err)
	}
	for _, row := range table {
		if row.Event == doc.Name {
			return row
		}
	}
	t.Fatalf("no row for %q", doc.Name)
	return pack.HookRow{}
}

func packet(t testing.TB, row pack.HookRow, raw []byte) []byte {
	t.Helper()
	req, err := hookwire.Extract(row, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := hookwire.EncodeRequest(*req)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func claimed(state agentsession.State) *agentsession.Claim {
	return &agentsession.Claim{Basis: "self", Source: "hook", State: state}
}

func TestDocumentedEvents(t *testing.T) {
	ex, table := documented(t)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(`{"file_path":"sample.txt"}`)))
	want := map[string]hook.Event{
		"SessionStart":      {Kind: "session-start", Source: "startup", Model: "sample-model"},
		"UserPromptSubmit":  {Kind: "turn-start", Claim: claimed("busy")},
		"PreToolUse":        {Kind: "tool-start", Claim: claimed("busy"), Tool: "Read", InputDigest: digest},
		"PermissionRequest": {Kind: "permission", Claim: claimed("blocked"), Tool: "Read", InputDigest: digest},
		"Notification":      {Kind: "notification", Claim: claimed("blocked"), Reason: "permission_prompt"},
		"Stop":              {Kind: "turn-complete", Claim: claimed("idle")},
		"StopFailure":       {Kind: "turn-failed"},
		"SessionEnd":        {Kind: "session-end", Reason: "logout"},
		"ConfigChange":      {Kind: "config-change", Source: "project_settings"},
	}
	if len(table) != len(want) {
		t.Fatalf("got %d rows, want %d", len(table), len(want))
	}
	for _, raw := range ex.Inputs {
		row := exampleRow(t, table, raw)
		w, ok := want[row.Event]
		if !ok {
			t.Fatalf("unexpected or repeated %s", row.Event)
		}
		delete(want, row.Event)
		t.Run(row.Event, func(t *testing.T) {
			output, async := "none", true
			w.Report = agentsession.HookReport{Kind: "other", Session: session}
			w.Mode = "default"
			switch row.Event {
			case "ConfigChange":
				w.Report.Kind = "config-change"
				output, async = "deny-from-table", false
			case "SessionStart":
				w.Report.Kind = "session-start"
			}
			if row.Output != output || row.Async != async || row.TimeoutMS != 5000 {
				t.Fatalf("wrong output, asynchronous setting or deadline in %+v", row)
			}
			frame := packet(t, row, raw)
			for _, omitted := range []string{"sample.txt", "sample-settings.json", "sample prompt", "sample reply"} {
				if bytes.Contains(frame, []byte(omitted)) {
					t.Fatalf("wire retained %q", omitted)
				}
			}
			req, err := table.DecodeRequest(frame)
			if err != nil {
				t.Fatal(err)
			}
			got, err := hook.Decode(row, *req)
			if err != nil || !reflect.DeepEqual(got, w) {
				t.Fatalf("got %+v, %v, want %+v", got, err, w)
			}
			checkDelivery(t, got)
		})
	}
	if len(want) != 0 {
		t.Fatalf("missing examples for %v", want)
	}
}

func TestHookRowsByBuild(t *testing.T) {
	p := claude.Pack{}
	for _, version := range []string{"", "2.1", "2.1.325", "2.1.332-extra"} {
		if rows, ok := p.HookRows(version); ok || rows != nil {
			t.Fatalf("unexpected rows for %q", version)
		}
	}
	table, _ := p.HookRows("2.1.332")
	*table[0].Fields[0].Pointer = "/other"
	again, _ := p.HookRows("2.1.332")
	if *again[0].Fields[0].Pointer == "/other" {
		t.Fatal("callers share mutable rows")
	}
	raw, err := os.ReadFile("rows.json")
	if err != nil {
		t.Fatal(err)
	}
	tables, err := hookwire.DecodeTable(raw)
	if err != nil || len(tables) != 1 || !reflect.DeepEqual(tables["2.1.332"], again) {
		t.Fatalf("standalone and pack tables differ: %v", err)
	}
}

// V5 covers both permit sources, including a caller putting the hook in the
// wrong slot. An independent dialog veto must survive even an idle report.
func checkDelivery(t *testing.T, event hook.Event) {
	t.Helper()
	if event.Claim == nil {
		return
	}
	if c := event.Claim; c.Basis != "self" || c.Source != "hook" ||
		(c.State != "idle" && c.State != "busy" && c.State != "blocked") {
		t.Fatalf("invalid hook claim %+v", c)
	}
	in := agentsession.Inputs{Sight: "rules", Policy: agentsession.Policy{Self: "checked"}}
	for _, state := range []agentsession.State{"idle", "unknown", "blocked"} {
		in.Transport = agentsession.Claim{State: state, Basis: "transport"}
		for _, self := range []*agentsession.Claim{nil, {State: "idle", Basis: "self", Source: "client"}, event.Claim} {
			in.Self = self
			for _, veto := range []agentsession.Veto{nil, {"dialog"}} {
				in.Veto, in.Hook = veto, nil
				before := agentsession.Compose(in)
				in.Hook = event.Claim
				after := agentsession.Compose(in)
				if after.Deliverable && (!before.Deliverable || before.Permit != after.Permit) {
					t.Fatal("hook supplied a delivery permit")
				}
				shouldDeliver := before.Deliverable && event.Claim.State == "idle"
				if after.Deliverable != shouldDeliver {
					t.Fatalf("unexpected delivery with %s claim and %+v", event.Claim.State, in)
				}
			}
		}
	}
}

func TestOptionalFieldsAndSubtypes(t *testing.T) {
	_, table := documented(t)
	for _, tc := range []struct{ row, key, value, wantState string }{
		{"claude.notification", "notification_type", "idle_prompt", "idle"},
		{"claude.notification", "notification_type", "unknown_type", ""},
		{"claude.stop", "stop_hook_active", "true", "idle"},
		{"claude.session-start", "source", "resume", ""},
	} {
		row, ok := table.Lookup(tc.row)
		if !ok {
			t.Fatal(tc.row)
		}
		req := hookwire.Request{Version: hookwire.Version, Kind: hookwire.Event, Row: row.ID,
			Fields: map[string]string{"hook_event_name": row.Event, "session_id": session, tc.key: tc.value}}
		got, err := hook.Decode(row, req)
		if err != nil || got.Model != "" || got.Mode != "" || got.StopHookActive != (tc.key == "stop_hook_active") {
			t.Fatal(tc, got, err)
		}
		if tc.wantState == "" && got.Claim != nil || tc.wantState != "" && !reflect.DeepEqual(got.Claim, claimed(agentsession.State(tc.wantState))) {
			t.Fatal(tc, got.Claim)
		}
	}
}

func TestRefuseIncompleteOrMismatchedEvents(t *testing.T) {
	ex, table := documented(t)
	for _, raw := range ex.Inputs {
		row := exampleRow(t, table, raw)
		frame := packet(t, row, raw)
		edits := map[string]func(*hookwire.Request){
			"version":   func(r *hookwire.Request) { r.Version++ },
			"cut":       func(r *hookwire.Request) { r.Cut, r.Fields, r.Digests = true, nil, nil },
			"fetch":     func(r *hookwire.Request) { r.Kind = hookwire.Fetch },
			"wrong row": func(r *hookwire.Request) { r.Row = "other" },
			"event":     func(r *hookwire.Request) { r.Fields["hook_event_name"] = "other" },
			"no id":     func(r *hookwire.Request) { delete(r.Fields, "session_id") },
			"bad uuid":  func(r *hookwire.Request) { r.Fields["session_id"] = strings.ToUpper(session) },
		}
		switch row.Event {
		case "Stop":
			edits["flag"] = func(r *hookwire.Request) { r.Fields["stop_hook_active"] = "1" }
		case "PreToolUse", "PermissionRequest":
			edits["tool"] = func(r *hookwire.Request) { delete(r.Fields, "tool_name") }
			edits["digest"] = func(r *hookwire.Request) { delete(r.Digests, "tool_input") }
		}
		for name, edit := range edits {
			t.Run(row.Event+"/"+name, func(t *testing.T) {
				req, err := table.DecodeRequest(frame)
				if err != nil {
					t.Fatal(err)
				}
				edit(req)
				if event, err := hook.Decode(row, *req); err == nil || !reflect.DeepEqual(event, hook.Event{}) {
					t.Fatal("accepted invalid event", event)
				}
			})
		}
	}
	row := pack.HookRow{ID: "future", Event: "Future"}
	req := hookwire.Request{Version: hookwire.Version, Row: row.ID, Kind: hookwire.Event,
		Fields: map[string]string{"hook_event_name": row.Event, "session_id": session}}
	if event, err := hook.Decode(row, req); err == nil || !reflect.DeepEqual(event, hook.Event{}) {
		t.Fatal("accepted unknown event", event)
	}
}
