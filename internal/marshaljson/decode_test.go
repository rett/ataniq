package marshaljson

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEvidenceForActions(t *testing.T) {
	files := casesArchive(t)
	// Start each mutation with usable evidence. This isolates each signal
	// even when the upstream example has several reasons to withhold action.
	for _, tc := range []struct {
		name                       string
		kind                       Kind
		change                     func(map[string]any)
		stale, incomplete, unknown bool
	}{
		{"checked", Check, nil, false, false, false},
		{"local", Check, func(f map[string]any) { f["backend"], f["remote"] = "local", "none" }, false, false, false},
		{"not asked", Check, func(f map[string]any) { f["remote"] = "not-asked" }, true, false, false},
		{"unchecked", Check, func(f map[string]any) { f["remote"] = "unchecked" }, true, false, false},
		{"inconsistent backend", Check, func(f map[string]any) { f["remote"] = "none" }, false, true, false},
		{"free stale", Check, func(f map[string]any) {
			f["answer"] = "free-stale"
			f["lead"] = map[string]any{"id": "lead", "user": "user", "host": "host", "since": "now"}
		}, true, false, false},
		{"warning", Check, func(f map[string]any) { f["warnings"] = []string{"opaque text"} }, false, true, false},
		{"unnamed without inventory", Check, func(f map[string]any) { f["feature"], f["answer"] = nil, nil }, false, true, false},
		{"named without answer", Check, func(f map[string]any) { f["answer"] = nil }, false, true, false},
		{"stale without lead", Check, func(f map[string]any) { f["answer"] = "free-stale" }, true, true, false},
		{"extension", Check, func(f map[string]any) { f["Answer"] = "free" }, false, false, true},
		{"status", Status, nil, false, false, false},
		{"nested extension", Status, func(f map[string]any) { firstRow(f)["lead"].(map[string]any)["extra"] = true }, false, false, true},
		{"foreign row", Status, func(f map[string]any) { firstRow(f)["project"] = "other" }, false, true, false},
		{"map data", Status, func(f map[string]any) { firstRow(f)["overlap"] = map[string][]string{"truncated": {"file"}} }, false, false, false},
		{"handoff", Handoff, nil, false, false, false},
		{"fallback", Handoff, func(f map[string]any) { f["backend_fallback"] = true }, false, true, false},
		{"truncated", Handoff, func(f map[string]any) { f["truncated"] = true }, false, true, false},
		{"omitted despite flag", Handoff, func(f map[string]any) { f["lists_left_out"] = map[string]int{"gates": 1} }, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "valid-refs"
			if tc.kind == Check {
				name = "valid-refs-name"
			}
			var f map[string]any
			if err := json.Unmarshal(fixture(t, files, tc.kind, name), &f); err != nil {
				t.Fatal(err)
			}
			if tc.kind == Handoff {
				f["backend"], f["remote"] = "local", "none"
			}
			if tc.change != nil {
				tc.change(f)
			}
			d, err := Decode(tc.kind, mustJSON(t, f))
			if err != nil || d.Unknown() != tc.unknown || d.Stale() != tc.stale || d.Incomplete() != tc.incomplete ||
				d.ActionsAllowed("app", "r1") != !(tc.unknown || tc.stale || tc.incomplete) {
				t.Fatalf("unknown=%v stale=%v incomplete=%v allowed=%v err=%v", d.Unknown(), d.Stale(), d.Incomplete(), d.ActionsAllowed("app", "r1"), err)
			}
			if _, ok := d.Fields()["Answer"]; ok {
				t.Fatal("unknown field retained")
			}
		})
	}
}

func firstRow(f map[string]any) map[string]any {
	return f["features"].([]any)[0].(map[string]any)
}

func TestRevisionEqualityAndBounds(t *testing.T) {
	files := casesArchive(t)
	body := fixture(t, files, Status, "valid-refs")
	decode := func(n string) (Revision, error) {
		b := bytes.Replace(body, []byte(`"rev": 1`), []byte(`"rev": `+n), 1)
		d, err := Decode(Status, b)
		if err != nil {
			return Revision{}, err
		}
		return firstRow(d.Fields())["rev"].(Revision), nil
	}
	maximum, e1 := decode("9007199254740991")
	again, e2 := decode("9007199254740991")
	minimum, e3 := decode("-9007199254740991")
	if e1 != nil || e2 != nil || e3 != nil || maximum != again || maximum == minimum {
		t.Fatal("revision equality or inclusive bounds lost", e1, e2, e3)
	}
	for _, n := range []string{"9007199254740992", "-9007199254740992", "1e0", "1.0"} {
		if _, err := decode(n); err == nil {
			t.Errorf("accepted %s", n)
		}
	}
}

func TestFailedInput(t *testing.T) {
	files := casesArchive(t)
	body := fixture(t, files, Check, "valid-refs-name")
	for _, tc := range []struct {
		kind Kind
		body []byte
	}{
		{Status, body}, {"qargi-claim", body},
		{Refusal, bytes.Replace(fixture(t, files, Refusal, "valid"), []byte(`"exit":1`), []byte(`"exit":0`), 1)},
		{Status, bytes.Replace(fixture(t, files, Status, "valid-refs"), []byte(`"unsigned"`), []byte(`"future"`), 1)},
		{Check, bytes.Replace(body, []byte(`"r1"`), []byte(`"\ud800"`), 1)},
		{Check, bytes.Replace(body, []byte("r1"), []byte{0xff}, 1)},
		{Check, bytes.Replace(body, []byte(`"r1"`), []byte(`"r1","\u0066eature":"r2"`), 1)},
		{Check, bytes.Replace(body, []byte(`"r1"`), []byte(`"r1","extra":[1.5]`), 1)},
	} {
		d, err := Decode(tc.kind, tc.body)
		if err == nil || d.ActionsAllowed("app", "r1") {
			t.Fatal("invalid input enabled actions or decoded successfully")
		}
	}
}

func TestLargeStatusInventory(t *testing.T) {
	files := casesArchive(t)
	var fields map[string]any
	if err := json.Unmarshal(fixture(t, files, Status, "valid-refs"), &fields); err != nil {
		t.Fatal(err)
	}
	row := firstRow(fields)
	row["note"] = strings.Repeat("x", 64<<10) + "🌓"
	d, err := Decode(Status, mustJSON(t, fields))
	if err != nil || !d.ActionsAllowed("app", "r1") || firstRow(d.Fields())["note"] != row["note"] {
		t.Fatal("large valid status lost", err)
	}
}

func TestUnicodeEscapes(t *testing.T) {
	files := casesArchive(t)
	body := fixture(t, files, Check, "valid-refs-name")
	for _, tc := range []struct{ encoded, want string }{
		{`"\ud83c\udf13"`, "🌓"},
		{`"\\ud800"`, `\ud800`},
		{`"\u0061"`, "a"},
	} {
		d, err := Decode(Check, bytes.Replace(body, []byte(`"r1"`), []byte(tc.encoded), 1))
		if err != nil || d.Fields()["feature"] != tc.want {
			t.Errorf("%s: got %q, err %v", tc.encoded, d.Fields()["feature"], err)
		}
	}
}

func TestActionRequiresSameRecordRevision(t *testing.T) {
	files := casesArchive(t)
	for _, kind := range []Kind{Status, Check, Handoff} {
		var f map[string]any
		if err := json.Unmarshal(fixture(t, files, kind, "valid-refs"), &f); err != nil {
			t.Fatal(err)
		}
		if kind == Handoff {
			f["backend"], f["remote"] = "local", "none"
		}
		d, err := Decode(kind, mustJSON(t, f))
		if err != nil {
			t.Fatal(err)
		}
		if !d.AllowsRevision("app", "r1", Revision{value: 1, observed: true}) || d.AllowsRevision("app", "r1", Revision{value: 0, observed: true}) ||
			d.AllowsRevision("app", "r1", Revision{value: 2, observed: true}) || d.AllowsRevision("app", "missing", Revision{value: 1, observed: true}) ||
			d.AllowsRevision("other", "r1", Revision{value: 1, observed: true}) {
			t.Fatalf("revision check failed for %s", kind)
		}
		row := firstRow(f)
		if kind == Handoff {
			record := row["record"].(map[string]any)
			record["has_record"] = false
			d, err = Decode(kind, mustJSON(t, f))
			if err != nil || d.AllowsRevision("app", "r1", Revision{value: 1, observed: true}) {
				t.Fatalf("absent record: err %v", err)
			}
			record["has_record"] = true
		} else {
			status := row["status"]
			row["status"] = "unled"
			d, err = Decode(kind, mustJSON(t, f))
			if err != nil || d.AllowsRevision("app", "r1", Revision{value: 1, observed: true}) {
				t.Fatalf("unled feature: err %v", err)
			}
			row["status"] = status
		}
		f["features"] = []any{row, row}
		d, err = Decode(kind, mustJSON(t, f))
		if err != nil || d.AllowsRevision("app", "r1", Revision{value: 1, observed: true}) {
			t.Fatalf("ambiguous %s row: err %v", kind, err)
		}
		f["features"], f["extra"] = []any{row}, true
		d, err = Decode(kind, mustJSON(t, f))
		if err != nil || d.AllowsRevision("app", "r1", Revision{value: 1, observed: true}) {
			t.Fatalf("unknown %s with matching revision: err %v", kind, err)
		}
	}
}

func TestVerbResult(t *testing.T) {
	files := casesArchive(t)
	ok := fixture(t, files, Check, "valid-refs-name")
	line := string(fixture(t, files, Refusal, "valid"))
	remote := strings.NewReplacer("marshal-feature-held", "marshal-remote-unreachable", `"exit":1`, `"exit":3`).Replace(line)
	for _, tc := range []struct {
		name           string
		exit           int
		stdout         []byte
		stderr, reason string
		wantError      bool
	}{
		{"success", 0, ok, "", "", false},
		{"held", 1, nil, "warning\n" + line + "human block\n", "conflict", false},
		{"unknown feature", 1, nil, strings.Replace(line, "marshal-feature-held", "marshal-feature-unknown", 1), "not-found", false},
		{"unreachable", 3, nil, remote, "remote-unreachable", false},
		{"other code same exit", 3, nil, strings.Replace(remote, "marshal-remote-unreachable", "future-rule", 1), "refused", false},
		{"uncatalogued failure", 3, nil, "remote unreachable", "", true},
		{"defect", 70, ok, "write failed", "", true},
		{"status mismatch", 3, nil, line, "", true},
		{"ambiguous lines", 1, nil, line + line, "", true},
		{"mixed output", 1, ok, line, "", true},
		{"refusal with success", 0, ok, line, "", true},
		{"new prefix", 3, nil, strings.Replace(remote, "refusal/1", "refusal/2", 1), "", true},
		{"oversize line", 1, nil, strings.Replace(line, "feature=sha256", strings.Repeat("x", 4096), 1), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := DecodeResult(Check, tc.exit, tc.stdout, []byte(tc.stderr))
			if (err != nil) != tc.wantError || d.RefusalReason() != tc.reason || d.ActionsAllowed("app", "r1") != (tc.exit == 0 && !tc.wantError) {
				t.Errorf("reason=%q allowed=%v err=%v", d.RefusalReason(), d.ActionsAllowed("app", "r1"), err)
			}
		})
	}
	if _, err := DecodeResult(Refusal, 0, ok, nil); err == nil {
		t.Fatal("refusal accepted as a success kind")
	}
}

func TestReservedRefusalFields(t *testing.T) {
	files := casesArchive(t)
	line := fixture(t, files, Refusal, "valid")
	line = bytes.Replace(line, []byte(`"epoch":null`), []byte(`"epoch":{"future":42}`), 1)
	d, err := Decode(Refusal, line)
	if err != nil || d.Unknown() || d.ActionsAllowed("app", "r1") || d.RefusalReason() != "conflict" {
		t.Fatalf("reserved field changed refusal: %v", err)
	}
	for _, key := range []string{"epoch", "state", "tx"} {
		if _, exists := d.Fields()[key]; exists {
			t.Errorf("reserved field %s exposed", key)
		}
	}
}

func TestHandBuiltDocument(t *testing.T) {
	var rebuilt Document
	if err := json.Unmarshal([]byte(`{"Kind":"qargi-marshal-status","Fields":{"project":"app","features":[]}}`), &rebuilt); err != nil {
		t.Fatal(err)
	}
	for _, d := range []Document{{}, rebuilt} {
		if d.ActionsAllowed("app", "r1") || d.AllowsRevision("app", "r1", Revision{}) {
			t.Fatal("undecoded evidence passed a usability check")
		}
	}

	files := casesArchive(t)
	d, err := Decode(Status, fixture(t, files, Status, "valid-refs"))
	if err != nil {
		t.Fatal(err)
	}
	view := d.Fields()
	row := firstRow(view)
	rev := row["rev"].(Revision)
	row["feature"] = "other"
	view["features"].([]any)[0] = nil
	view["project"] = "elsewhere"
	if !d.AllowsRevision("app", "r1", rev) || d.AllowsRevision("elsewhere", "other", rev) {
		t.Fatal("editing displayed fields changed the decoded evidence")
	}
}

func TestSuccessfulResultWithStderr(t *testing.T) {
	files := casesArchive(t)
	for _, kind := range []Kind{Status, Handoff, Check} {
		var f map[string]any
		name := "valid-refs"
		if kind == Check {
			name = "valid-refs-name"
		}
		if err := json.Unmarshal(fixture(t, files, kind, name), &f); err != nil {
			t.Fatal(err)
		}
		if kind == Handoff {
			f["backend"], f["remote"] = "local", "none"
		}
		body := mustJSON(t, f)
		for _, stderr := range []string{"", "\n", "qargi: warning: damaged record\n", "diagnostic"} {
			d, err := DecodeResult(kind, 0, body, []byte(stderr))
			warned := len(strings.Trim(stderr, "\n")) != 0
			if err != nil || d.Incomplete() != warned || d.ActionsAllowed("app", "r1") == warned || d.RefusalReason() != "" {
				t.Fatalf("%s stderr %q: incomplete=%v err=%v", kind, stderr, d.Incomplete(), err)
			}
		}
	}
}

func TestZeroRevisionMatchesNothing(t *testing.T) {
	files := casesArchive(t)
	body := fixture(t, files, Status, "valid-refs")
	body = bytes.Replace(body, []byte(`"rev": 1`), []byte(`"rev": 0`), 1)
	d, err := Decode(Status, body)
	if err != nil {
		t.Fatal(err)
	}
	observed := firstRow(d.Fields())["rev"].(Revision)
	if observed == (Revision{}) || !d.AllowsRevision("app", "r1", observed) || d.AllowsRevision("app", "r1", Revision{}) {
		t.Fatal("an observed revision of zero must be distinct from an unobserved revision")
	}
}

func TestInventoryEvidenceByFeature(t *testing.T) {
	files := casesArchive(t)
	for _, tc := range []struct {
		name   string
		kind   Kind
		damage func(map[string]any)
	}{
		{"stale", Status, func(r map[string]any) { r["lease"] = "stale" }},
		{"provisional", Status, func(r map[string]any) { r["provisional"] = true }},
		{"invalid", Status, func(r map[string]any) { r["integrity"] = "INVALID" }},
		{"unverifiable", Check, func(r map[string]any) { r["integrity"] = "cannot verify" }},
		{"overlap warning", Check, func(r map[string]any) { r["warnings"] = []string{"another feature changes the same files"} }},
		{"truncated record", Handoff, func(r map[string]any) {
			r["record"].(map[string]any)["data"].(map[string]any)["truncated"] = []string{"goal"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fixture(t, files, tc.kind, "valid-refs")
			var f, other map[string]any
			for _, dst := range []*map[string]any{&f, &other} {
				if err := json.Unmarshal(body, dst); err != nil {
					t.Fatal(err)
				}
			}
			// Keep the real healthy and unled rows. Add a third, damaged row.
			damaged := firstRow(other)
			if tc.kind == Handoff {
				f["backend"], f["remote"] = "local", "none"
				damaged["name"] = "r3"
			} else {
				damaged["feature"] = "r3"
			}
			tc.damage(damaged)
			f["features"] = append(f["features"].([]any), damaged)
			body = mustJSON(t, f)
			for _, warn := range []string{"", "capture warning"} {
				d, err := DecodeResult(tc.kind, 0, body, []byte(warn))
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"r1", "r2", "r3", "absent"} {
					want := warn == "" && name == "r1"
					rev := Revision{value: 1, observed: true}
					if name == "r2" {
						rev.value = 0
					}
					if d.ActionsAllowed("app", name) != want || d.AllowsRevision("app", name, rev) != want {
						t.Errorf("feature %s, stderr %q: want usable and matching = %v", name, warn, want)
					}
				}
			}
		})
	}
}

func TestDocumentSizeLimit(t *testing.T) {
	files := casesArchive(t)
	body := fixture(t, files, Check, "valid-refs-name")
	atLimit := append(body, bytes.Repeat([]byte{' '}, MaxDocumentBytes-len(body))...)
	if d, err := DecodeResult(Check, 0, atLimit, nil); err != nil || !d.ActionsAllowed("app", "r1") {
		t.Fatalf("exactly MaxDocumentBytes: %v", err)
	}
	over := append(atLimit, '{') // A collector may retain just the first excess byte.
	check := func(d Document, err error) {
		t.Helper()
		if err != nil || !d.Incomplete() || d.Fields() != nil || d.RefusalReason() != "" || d.ActionsAllowed("app", "r1") {
			t.Fatalf("oversize capture: kind=%q, incomplete=%v, err=%v", d.Kind(), d.Incomplete(), err)
		}
	}
	check(Decode(Check, over))
	check(DecodeResult(Check, 0, over, nil))
	check(DecodeResult(Check, 0, body, over))
	check(DecodeResult(Check, 1, over, fixture(t, files, Refusal, "valid")))
	check(DecodeResult(Check, 1, nil, over))
	if _, err := Decode(Refusal, over); err == nil {
		t.Fatal("oversize refusal line accepted")
	}
}
