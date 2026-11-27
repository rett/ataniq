package all_test

import (
	"crypto/sha256"
	"encoding/hex"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/pack"
	"ataniq/internal/pack/qargiread"
	"ataniq/packs/all"
)

func crosswalkFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "crosswalk", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func crosswalkJSON(t *testing.T, name string, dst any) {
	t.Helper()
	// Vectors forbid nulls. Preserve the schema number's spelling when
	// decoding the pointer-to-value map, as the definition reader does.
	values := json.UnmarshalFromFunc(func(d *jsontext.Decoder, dst any) error {
		if d.PeekKind() == 'n' {
			return errors.New("unexpected null in vector")
		}
		if v, ok := dst.(*any); ok && d.PeekKind() == '0' {
			raw, err := d.ReadValue()
			if err != nil {
				return err
			}
			if strings.ContainsAny(string(raw), ".eE") {
				return errors.New("expected integer in vector")
			}
			*v = jsonv1.Number(raw)
			return nil
		}
		return errors.ErrUnsupported
	})
	if err := json.Unmarshal(crosswalkFile(t, name), dst, json.RejectUnknownMembers(true), json.WithUnmarshalers(values)); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

type runtimeRow struct {
	Schema string `json:"schema"`
	Row    string `json:"row"`
	Link   string `json:"link"`
	Ataniq struct {
		Pack      pack.Name `json:"pack"`
		PackTier  string    `json:"pack_tier"`
		QargiName string    `json:"qargi_name"`
	} `json:"ataniq"`
	Qargi struct {
		Runtime  string   `json:"runtime"`
		Family   string   `json:"family"`
		Releases []string `json:"releases"`
	} `json:"qargi"`
	Pins  qargiread.Pins `json:"show_json"`
	Calls []struct {
		Argv   []string `json:"argv"`
		Exit   int      `json:"exit"`
		Stdout string   `json:"stdout"`
		Code   string   `json:"ataniq_code"`
	} `json:"calls"`
}

type exitCase struct {
	Surface       string `json:"surface"`
	Outcome       string `json:"outcome"`
	Status        int    `json:"status"`
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	Row           string `json:"row"`
	Step          string `json:"step"`
	Code          string `json:"code"`
	OriginUnknown bool   `json:"origin_unknown"`
	RefusalCode   string `json:"refusal_code"`
}

type exitVector struct {
	qargiread.ExitMap
	Cases map[string][]exitCase `json:"cases"`
}

func TestQargiCrosswalk(t *testing.T) {
	var sources struct {
		Tag       string `json:"tag"`
		Commit    string `json:"commit"`
		TagObject string `json:"tag_object"`
		Blobs     map[string]struct {
			Source string `json:"source"`
			SHA256 string `json:"sha256"`
		} `json:"blobs"`
	}
	crosswalkJSON(t, "sources.json", &sources)
	if sources.Tag != "0.3.5" || sources.TagObject != "642d3ccd982bcbad22b0de0ce51acfe44585d449" || sources.Commit != "09467e1207b5437838fb163a068d83502912a52d" {
		t.Fatal("unexpected Qargi fixture revision")
	}

	var rows []runtimeRow
	var vector exitVector
	crosswalkJSON(t, "rows.json", &rows)
	crosswalkJSON(t, "exit-map.json", &vector)

	registry := pack.New()
	if err := all.Register(registry); err != nil {
		t.Fatal(err)
	}
	registry.Freeze()
	wantLinks := map[string]string{"claude.claude-code": "qargi-name", "claude.claude-platform": "family", "codex.codex": "qargi-name"}
	names := make(map[pack.Name]string)
	pins := make(map[string]qargiread.Pins)
	for _, row := range rows {
		t.Run(row.Row, func(t *testing.T) {
			rt := row.Qargi.Runtime
			if row.Schema != "qargi-ataniq.runtime-crosswalk/1" || row.Link != wantLinks[row.Row] || row.Row != string(row.Ataniq.Pack)+"."+rt || !slices.Equal(row.Qargi.Releases, []string{sources.Tag}) {
				t.Fatalf("unexpected crosswalk row %q", row.Row)
			}
			delete(wantLinks, row.Row)
			p, ok := registry.Lookup(row.Ataniq.Pack)
			if !ok || row.Ataniq.PackTier != "built-in" {
				t.Fatalf("unknown built-in %q", row.Ataniq.Pack)
			}
			manifest := p.Manifest()
			switch row.Link {
			case "qargi-name":
				if row.Ataniq.QargiName != rt || manifest.QargiName != rt || row.Pins["/surface"] != "local" {
					t.Fatalf("manifest name %q, vector name %q, runtime %q", manifest.QargiName, row.Ataniq.QargiName, rt)
				}
				names[manifest.Name] = rt
			case "family":
				if row.Ataniq.QargiName != "" || rt == manifest.QargiName || row.Pins["/surface"] != "cloud" {
					t.Fatalf("%s must link only through its family", rt)
				}
			default:
				t.Fatalf("unknown link %q", row.Link)
			}
			if row.Pins["/provider"] != rt || row.Pins["/family"] != row.Qargi.Family {
				t.Fatal("row identifiers disagree with its pins")
			}
			blob := sources.Blobs[rt]
			if blob.Source != "lib/providers/"+rt+".json" {
				t.Fatalf("wrong blob source %q", blob.Source)
			}
			raw := crosswalkFile(t, rt+".json")
			sum := sha256.Sum256(raw)
			if hex.EncodeToString(sum[:]) != blob.SHA256 {
				t.Fatalf("raw %s SHA-256 = %x, want %s", blob.Source, sum, blob.SHA256)
			}
			delete(sources.Blobs, rt)
			got, err := qargiread.ParseDefinition(string(raw))
			if err != nil || !maps.Equal(got, row.Pins) {
				t.Fatalf("definition = %v, %v; want %v", got, err, row.Pins)
			}
			pins[row.Row] = row.Pins
			checkShowCalls(t, row, string(raw), vector.ExitMap)
		})
	}
	if len(wantLinks) != 0 || len(sources.Blobs) != 0 {
		t.Fatalf("unmatched rows %v or blobs %v", wantLinks, sources.Blobs)
	}
	for _, name := range registry.Names() {
		p, _ := registry.Lookup(name)
		if got := p.Manifest().QargiName; got != names[name] {
			t.Errorf("%s QargiName = %q, row gives %q", name, got, names[name])
		}
	}
	testExitCases(t, vector, pins)
}

func checkShowCalls(t *testing.T, row runtimeRow, raw string, vector qargiread.ExitMap) {
	t.Helper()
	wantCalls := map[string]bool{row.Qargi.Runtime: true}
	if row.Link == "qargi-name" {
		wantCalls[row.Qargi.Family] = false
	}
	for _, call := range row.Calls {
		if len(call.Argv) != 5 || !slices.Equal(call.Argv[:3], []string{"qargi", "provider", "show"}) || call.Argv[4] != "--json" {
			t.Fatalf("invalid show vector %q", call.Argv)
		}
		name := call.Argv[3]
		definition, ok := wantCalls[name]
		if !ok {
			t.Fatalf("unexpected show argument %q", name)
		}
		delete(wantCalls, name)
		input := qargiread.Call{Surface: "provider-show", Status: call.Exit, Want: row.Pins}
		wantCode := call.Code
		if definition {
			if call.Exit != 0 || call.Stdout != "definition" || call.Code != "" {
				t.Fatalf("invalid definition call %+v", call)
			}
			input.Stdout, wantCode = raw, "ok"
		} else if call.Exit != 1 || call.Stdout != "empty" || call.Code != "pack-qargi-no-definition" {
			t.Fatalf("invalid family call %+v", call)
		}
		result, err := vector.Select(input)
		if err != nil || result.Step != "status" || result.Code != wantCode {
			t.Errorf("show %s = %+v, %v; want status/%s", name, result, err, wantCode)
		}
	}
	if len(wantCalls) != 0 {
		t.Errorf("missing show calls %v", wantCalls)
	}
}

// The only adapter success verb in the vectors is barrier-check. Its response
// has two required fields and disallows additional properties.
func barrierResponse(obj map[string]any) bool {
	decision, _ := obj["decision"].(string)
	_, reason := obj["reason"].(string)
	return len(obj) == 2 && reason && (decision == "allow" || decision == "deny")
}

func testExitCases(t *testing.T, vector exitVector, pins map[string]qargiread.Pins) {
	t.Helper()
	eras := []string{"historical_0_3_5", "structured_from_0_3_6", "synthetic"}
	if vector.Schema != "qargi-ataniq.exit-map/1" || len(vector.Cases) != len(eras) {
		t.Fatal("unexpected exit vector schema or case groups")
	}
	for _, era := range eras {
		cases := vector.Cases[era]
		if len(cases) == 0 {
			t.Fatalf("missing %s exit cases", era)
		}
		for i, tc := range cases {
			t.Run(fmt.Sprintf("%s/%s/%d-%s-%d", era, tc.Surface, tc.Status, tc.Step, i), func(t *testing.T) {
				structured := era == "structured_from_0_3_6"
				if !structured && strings.Contains(tc.Stderr, "qargi-refusal/1 ") {
					t.Fatal("contract lines belong to the structured vectors")
				}
				if tc.Row != "" && pins[tc.Row] == nil {
					t.Fatalf("unknown row %q", tc.Row)
				}
				c := qargiread.Call{Surface: tc.Surface, Outcome: tc.Outcome, Status: tc.Status, Stdout: tc.Stdout, Stderr: tc.Stderr, Want: pins[tc.Row], StructuredRefusals: structured, FitsResponse: barrierResponse}
				want := qargiread.Result{Step: tc.Step, Code: tc.Code, OriginUnknown: tc.OriginUnknown, RefusalCode: tc.RefusalCode}
				if got, err := vector.Select(c); err != nil || got != want {
					t.Errorf("Select = %+v, %v; want %+v", got, err, want)
				}
			})
		}
	}
}

func TestQargiOutputLimits(t *testing.T) {
	var vector exitVector
	crosswalkJSON(t, "exit-map.json", &vector)
	const prefix = `qargi-refusal/1 {"exit":1,"padding":"`
	const suffix = `"}`
	line := func(n int) string {
		return prefix + strings.Repeat("x", n-len(prefix)-len(suffix)) + suffix + "\n"
	}
	for _, surface := range vector.QargiSurfaces {
		for _, tc := range []struct {
			stderr string
			step   string
		}{
			{line(4096), "line-agrees"},
			{line(4097), "refusal-line-unreadable"},
			{strings.Repeat("x", 16<<10), "status"},
			{strings.Repeat("x", 16<<10+1), "output-over-limit"},
		} {
			got, err := vector.Select(qargiread.Call{Surface: surface, Status: 1, Stderr: tc.stderr, StructuredRefusals: true})
			if err != nil || got.Step != tc.step {
				t.Errorf("%s with %d bytes of stderr: %+v, %v; want %s", surface, len(tc.stderr), got, err, tc.step)
			}
		}
	}
	for _, tc := range []struct {
		size int
		step string
	}{{256 << 10, "stdout-on-nonzero"}, {256<<10 + 1, "output-over-limit"}} {
		got, err := vector.Select(qargiread.Call{Surface: "provider-show", Status: 1, Stdout: strings.Repeat("x", tc.size)})
		if err != nil || got.Step != tc.step {
			t.Errorf("%d bytes of stdout: %+v, %v; want %s", tc.size, got, err, tc.step)
		}
	}
}
