package marshaljson

import (
	"encoding/json"
	"testing"
)

func TestConditionalInventoryFields(t *testing.T) {
	files := casesArchive(t)
	for _, kind := range []Kind{Status, Check, Handoff} {
		test := func(name string, change func(map[string]any, map[string]any), usable bool) {
			t.Helper()
			t.Run(string(kind)+"/"+name, func(t *testing.T) {
				var f map[string]any
				if err := json.Unmarshal(fixture(t, files, kind, "valid-refs"), &f); err != nil {
					t.Fatal(err)
				}
				record := firstRow(f)
				if kind == Handoff {
					useLocalHandoff(f)
					record = record["record"].(map[string]any)
				}
				change(f, record)
				d, err := Decode(kind, mustJSON(t, f))
				if err != nil || d.UsableEvidence("app", "r1") != usable {
					t.Fatalf("want usable=%v, err=%v", usable, err)
				}
			})
		}
		for _, key := range []string{"integrity", "signer", "integrity_note"} {
			test("missing "+key, func(_, r map[string]any) { delete(r, key) }, false)
		}
		test("ambiguous worktree", func(_, r map[string]any) { delete(r["lead"].(map[string]any), "worktree") }, false)
		if kind == Status {
			for _, key := range []string{"tasks", "worktrees", "agent_branches", "overlap"} {
				test("missing "+key, func(_, r map[string]any) { delete(r, key) }, false)
			}
		}
		if kind == Handoff {
			test("provisional on local", func(_, r map[string]any) { r["provisional"] = false }, false)
			test("integrity off with fields", func(f, _ map[string]any) { f["integrity"] = "off" }, false)
		} else {
			test("missing provisional on refs", func(_, r map[string]any) { delete(r, "provisional") }, false)
		}
		test("no integrity fields", func(f, r map[string]any) {
			if kind == Handoff {
				f["integrity"] = "off"
			}
			delete(r, "integrity")
			delete(r, "integrity_note")
			delete(r, "signer")
		}, kind == Handoff)
	}
}

func TestHandoffSuitePresence(t *testing.T) {
	files := casesArchive(t)
	for _, tc := range []struct {
		state, missing string
		hasRun, usable bool
	}{
		{"at-tip", "", true, true}, {"older", "", true, true},
		{"other-command", "", true, true}, {"other-environment", "", true, true},
		{"at-tip", "sha", true, false}, {"older", "result", true, false},
		{"other-command", "at", true, false}, {"at-tip", "", false, false},
		{"not-run", "", false, true}, {"not-run", "", true, false},
		{"unknown", "", false, false},
	} {
		var f map[string]any
		if err := json.Unmarshal(fixture(t, files, Handoff, "valid-refs"), &f); err != nil {
			t.Fatal(err)
		}
		useLocalHandoff(f)
		suite := firstRow(f)["suite"].(map[string]any)
		suite["state"] = tc.state
		if tc.hasRun {
			suite["sha"], suite["result"], suite["at"] = "tip", "pass", ""
			delete(suite, tc.missing)
		}
		d, err := Decode(Handoff, mustJSON(t, f))
		if err != nil || d.UsableEvidence("app", "r1") != tc.usable {
			t.Errorf("suite %+v: err=%v", tc, err)
		}
	}
}

func TestHandoffRecordedEnvironment(t *testing.T) {
	files := casesArchive(t)
	for _, entry := range []string{"apply", "check"} {
		var f map[string]any
		if err := json.Unmarshal(fixture(t, files, Handoff, "valid-refs"), &f); err != nil {
			t.Fatal(err)
		}
		useLocalHandoff(f)
		env := f["environment"].(map[string]any)
		record := map[string]any{"head": "tip", "at_head": true, "exit": 0, "time": ""}
		if entry == "apply" {
			record["command"], record["allow_root"] = "", false
		} else {
			record["verdict"], record["missing"], record["expected"] = "", 0, 0
			record["doctor_command"], record["builtin_minimum"] = "", false
		}
		env["recorded"], env[entry] = true, record
		assert := func(incomplete bool) {
			t.Helper()
			d, err := Decode(Handoff, mustJSON(t, f))
			if err != nil || d.Incomplete() != incomplete || d.UsableEvidence("app", "r1") == incomplete {
				t.Errorf("%s: want incomplete=%v, err=%v", entry, incomplete, err)
			}
		}
		assert(false)
		delete(record, "time")
		assert(true)
	}
}
