package hook_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/hookwire"
	"ataniq/packs/codex"
	"ataniq/packs/codex/hook"
)

const build = "0.172.0"

func fixtureFrames(t testing.TB) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile("testdata/events.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Build, Evidence, Source string
		Cases                   []struct {
			Row   string
			State agentsession.State
			Input json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	p := codex.Pack{}
	d, err := p.Declaration()
	if err != nil {
		t.Fatal(err)
	}
	rows := hookwire.Table(d.Hooks[build])
	if fixture.Build != build || fixture.Evidence != "document-derived, not verified" || fixture.Source == "" ||
		len(d.Hooks) != 1 || len(rows) == 0 || len(rows) != len(fixture.Cases) {
		t.Fatal("fixture metadata or build coverage")
	}

	frames := make(map[string][]byte)
	for _, c := range fixture.Cases {
		row, ok := rows.Lookup(c.Row)
		if !ok || frames[c.Row] != nil || row.Output != "none" || row.Async || row.TimeoutMS != 300 {
			t.Fatalf("unexpected observation row %q: %+v", c.Row, row)
		}
		r, err := hookwire.Extract(row, bytes.NewReader(c.Input))
		if err != nil {
			t.Fatalf("%s: %v", c.Row, err)
		}
		frame, err := hookwire.EncodeRequest(*r)
		if err != nil {
			t.Fatal(err)
		}
		if r.Kind != hookwire.Event || bytes.Contains(frame, []byte("discard this text")) || bytes.Contains(frame, []byte("allow")) {
			t.Fatalf("%s: unexpected frame %s", c.Row, frame)
		}
		frames[c.Row] = frame
		got, err := p.DecodeHook(build, frame)
		if err != nil {
			t.Fatalf("%s: %v", c.Row, err)
		}
		want := hook.Observation{
			Row: c.Row, Report: agentsession.HookReport{Kind: "other", Session: "sample-session"},
			Claim: agentsession.Claim{State: c.State, Basis: "self", Source: "hook"},
			Model: "sample-model", PermissionMode: "sample-mode",
		}
		switch c.Row {
		case "session-start":
			want.Report.Kind = "session-start"
		case "permission-request", "pre-tool", "post-tool":
			want.Tool = "example"
			var tool struct {
				Input json.RawMessage `json:"tool_input"`
			}
			if err := json.Unmarshal(c.Input, &tool); err != nil {
				t.Fatal(err)
			}
			want.InputDigest = fmt.Sprintf("%x", sha256.Sum256(tool.Input))
		case "stop":
			active := true
			want.StopHookActive = &active
		}
		if !reflect.DeepEqual(*got, want) {
			t.Fatalf("%s: got %+v, want %+v", c.Row, *got, want)
		}
		checkObservation(t, got)
	}
	return frames
}

func checkObservation(t testing.TB, o *hook.Observation) {
	t.Helper()
	view, err := agentsession.NewHookView(0, "printable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentsession.Bind(view, o.Report); err != nil {
		t.Fatal(err)
	}
	result := agentsession.Compose(agentsession.Inputs{
		Transport: agentsession.Claim{State: "unknown", Basis: "transport"}, Hook: &o.Claim,
		Sight: "none", Policy: agentsession.Policy{Self: "checked"},
	})
	if o.Claim.Basis != "self" || o.Claim.Source != "hook" || len(result.Dropped) != 0 || result.Deliverable || result.Permit != "" {
		t.Fatalf("hook claim %+v composed to %+v", o.Claim, result)
	}
}

func TestDocumentaryRows(t *testing.T) {
	fixtureFrames(t)
}

func TestHookDecisionLimits(t *testing.T) {
	p := codex.Pack{}
	want := map[string]agentsession.DenyReason{
		"permission.deny": "pack-hooks-fail-open", "permission.route": "pack-hooks-fail-open",
		"permission.partial": "pack-hooks-fail-open",
	}
	delete(p.HookDecisionLimits(), "permission.route")
	if got := p.HookDecisionLimits(); !reflect.DeepEqual(got, want) {
		t.Fatalf("decision limits = %v", got)
	}
}

func TestRefusedFrames(t *testing.T) {
	frames := fixtureFrames(t)
	p := codex.Pack{}
	for _, version := range []string{"", "0.169", "0.169.0", "0.172", "0.172.1"} {
		if o, err := p.DecodeHook(version, frames["session-start"]); o != nil || err == nil {
			t.Errorf("accepted build %q", version)
		}
	}
	for name, edit := range map[string]func(*hookwire.Request){
		"unknown row":       func(r *hookwire.Request) { r.Row = "unknown" },
		"missing event":     func(r *hookwire.Request) { delete(r.Fields, "event") },
		"mismatched event":  func(r *hookwire.Request) { r.Fields["event"] = "SessionStart" },
		"missing session":   func(r *hookwire.Request) { delete(r.Fields, "session") },
		"long session":      func(r *hookwire.Request) { r.Fields["session"] = strings.Repeat("a", 129) },
		"non-ASCII session": func(r *hookwire.Request) { r.Fields["session"] = "snow☃" },
		"forged authority":  func(r *hookwire.Request) { r.Fields["decision"] = "allow" },
		"undeclared digest": func(r *hookwire.Request) { r.Digests["prompt"] = strings.Repeat("0", 64) },
		"cut":               func(r *hookwire.Request) { clear(r.Fields); clear(r.Digests); r.Cut = true },
		"fetch":             func(r *hookwire.Request) { r.Kind = hookwire.Fetch },
		"invalid stop flag": func(r *hookwire.Request) { r.Fields["stop_active"] = "1" },
	} {
		t.Run(name, func(t *testing.T) {
			r, err := hookwire.DecodeRequest(frames["stop"])
			if err != nil {
				t.Fatal(err)
			}
			edit(r)
			frame, err := hookwire.EncodeRequest(*r)
			if err != nil {
				t.Fatal(err)
			}
			if o, err := p.DecodeHook(build, frame); o != nil || err == nil {
				t.Fatalf("got %+v, %v", o, err)
			}
		})
	}
	for _, data := range [][]byte{nil, []byte("{}"), frames["stop"][4:], append(bytes.Clone(frames["stop"]), 0)} {
		if o, err := p.DecodeHook(build, data); o != nil || err == nil {
			t.Fatal("accepted malformed frame")
		}
	}
}

func TestOptionalFields(t *testing.T) {
	p := codex.Pack{}
	for _, flag := range []string{"", "false"} {
		fields := map[string]string{"event": "Stop", "session": strings.Repeat("s", 128)}
		if flag != "" {
			fields["stop_active"] = flag
		}
		frame, err := hookwire.EncodeRequest(hookwire.Request{Version: 1, Kind: hookwire.Event, Row: "stop", Fields: fields, Digests: map[string]string{}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.DecodeHook(build, frame)
		if err != nil {
			t.Fatal(err)
		}
		if got.Model != "" || got.PermissionMode != "" || (got.StopHookActive == nil) != (flag == "") || got.StopHookActive != nil && *got.StopHookActive {
			t.Fatalf("optional fields: %+v", got)
		}
	}
}

func FuzzDecodeHook(f *testing.F) {
	frames := fixtureFrames(f)
	for _, row := range slices.Sorted(maps.Keys(frames)) {
		f.Add(frames[row][4:])
	}
	f.Add([]byte(`{"v":1,"k":"fetch"}`))
	f.Add([]byte("null"))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > hookwire.MaxRequestBytes+1 {
			return
		}
		frame := append(binary.BigEndian.AppendUint32(nil, uint32(len(payload))), payload...)
		got, err := (codex.Pack{}).DecodeHook(build, frame)
		if err != nil {
			if got != nil {
				t.Fatal("refusal returned an observation")
			}
			return
		}
		checkObservation(t, got)
	})
}
