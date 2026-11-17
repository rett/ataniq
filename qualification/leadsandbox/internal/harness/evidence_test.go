//go:build linux && amd64

package harness_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
	"ataniq/qualification/leadsandbox/internal/harness"
	"ataniq/qualification/leadsandbox/internal/harnesstest"
)

// forbidden are the words no evidence key may contain: the evidence states
// facts, and nothing in it says the profile passed, is qualified or is
// supported.
var forbidden = []string{"pass", "qualified", "supported"}

func checkKey(t *testing.T, where, key string) {
	t.Helper()
	low := strings.ToLower(key)
	for _, w := range forbidden {
		if strings.Contains(low, w) {
			t.Errorf("%s: the key %q contains %q", where, key, w)
		}
	}
}

// walkKeys visits every object key of a decoded JSON value.
func walkKeys(t *testing.T, where string, v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			checkKey(t, where, k)
			walkKeys(t, where+"."+k, e)
		}
	case []any:
		for _, e := range x {
			walkKeys(t, where+"[]", e)
		}
	}
}

// typeKeys visits every JSON name a type can write, also those an omitempty
// field leaves out of a given file.
func typeKeys(t *testing.T, where string, ty reflect.Type, seen map[reflect.Type]bool) {
	for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice {
		ty = ty.Elem()
	}
	if ty.Kind() != reflect.Struct || seen[ty] {
		return
	}
	seen[ty] = true
	for i := 0; i < ty.NumField(); i++ {
		f := ty.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Errorf("%s.%s has no json name", ty, f.Name)
			continue
		}
		checkKey(t, where+"."+ty.Name(), name)
		typeKeys(t, where, f.Type, seen)
	}
}

func TestNoEvidenceKeySaysPassedQualifiedOrSupported(t *testing.T) {
	seen := map[reflect.Type]bool{}
	for _, v := range []any{harness.PlanEvidence{}, harness.ReportEvidence{}, harness.OutsideEvidence{}, harness.Summary{}} {
		typeKeys(t, "type", reflect.TypeOf(v), seen)
	}

	// And the keys of the files of real attempts: a full run, a failed one and a
	// dry run.
	var dirs []string
	for _, tc := range []struct {
		name     string
		behavior harness.FakeBehavior
		run      bool
	}{
		{"full", okProbe(assert.StatusOK, 0, 0), true},
		{"failed report", okProbe(assert.StatusOK, 2, 97), true},
		{"dry run", nil, false},
	} {
		f := harnesstest.New(t, profile.Claude)
		approved(f)
		var r *result
		if tc.run {
			r = execute(t, f, tc.behavior, nil)
		} else {
			r = executeWith(t, f, newFake(nil), false, nil)
		}
		mustNoErr(t, r)
		dirs = append(dirs, r.out.Dir)
	}
	files := 0
	for _, dir := range dirs {
		matches, _ := filepath.Glob(dir + "/*.json")
		for _, m := range matches {
			files++
			var v any
			data, err := os.ReadFile(m)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatalf("%s: %v", m, err)
			}
			walkKeys(t, filepath.Base(m), v)
		}
	}
	if files < 10 {
		t.Fatalf("only %d evidence files were looked at", files)
	}
}

func TestTerminalLogIsBounded(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	extra := 123
	r := execute(t, f, func(req harness.LaunchRequest, p *harness.FakeProcess) harness.Exit {
		_ = harness.WriteReportLine(req, harnesstest.ProbeLine(req.Plan.Digest, assert.StatusOK, 0))
		p.WriteTerminal(make([]byte, harness.MaxTerminalBytes+extra))
		return harness.Exit{}
	}, nil)
	mustNoErr(t, r)
	st, err := os.Stat(r.out.Dir + "/terminal.log")
	if err != nil {
		t.Fatal(err)
	}
	tm := r.out.Summary.Terminal
	if st.Size() != harness.MaxTerminalBytes || tm.CapturedBytes != harness.MaxTerminalBytes || tm.DroppedBytes != int64(extra) || tm.TotalBytes != int64(harness.MaxTerminalBytes+extra) {
		t.Errorf("log %d bytes, terminal %+v", st.Size(), tm)
	}
	if tm.MarkerSeen == nil || *tm.MarkerSeen {
		t.Errorf("marker seen: %v", tm.MarkerSeen)
	}
}

func TestCustomScratchCommandIsPassedAsGiven(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	f.Config.ScratchCommand = []string{"/usr/bin/env", "true"}
	r := execute(t, f, okProbe(assert.StatusOK, 0, 0), nil)
	mustNoErr(t, r)
	argv := r.fake.Requests()[0].Plan.Argv
	if n := len(argv); n < 2 || argv[n-2] != "/usr/bin/env" || argv[n-1] != "true" {
		t.Errorf("argv ends with %v", argv[len(argv)-3:])
	}
	if r.out.Summary.Terminal.MarkerSeen != nil {
		t.Error("the marker is only looked for with the default command")
	}
}

func TestEveryAttemptHasItsOwnRandomFolder(t *testing.T) {
	f := harnesstest.New(t, profile.Claude)
	approved(f)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		r := executeWith(t, f, newFake(okProbe(assert.StatusOK, 0, 0)), false, nil)
		mustNoErr(t, r)
		if seen[r.out.Attempt] {
			t.Fatalf("attempt name %s used twice", r.out.Attempt)
		}
		seen[r.out.Attempt] = true
	}
	if len(f.Attempts()) != 3 {
		t.Errorf("attempts: %v", f.Attempts())
	}
}
