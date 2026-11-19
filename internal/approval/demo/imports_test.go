package demo

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

// allowed is every package the non-test files may import: the standard
// packages the backend needs, and the approval packages of the seams note.
// door, doortest, the service and the journal are not on it, and neither are
// os, net or anything else that could change what is outside the process.
var allowed = map[string]bool{
	"errors":                            true,
	"fmt":                               true,
	"sort":                              true,
	"strings":                           true,
	"sync":                              true,
	"time":                              true,
	"unicode/utf8":                      true,
	"ataniq/internal/approval/card":     true,
	"ataniq/internal/approval/record":   true,
	"ataniq/internal/approval/limits":   true,
	"ataniq/internal/approval/presence": true,
	"ataniq/internal/exchange/leadtext": true,
}

func TestImportsAreHeld(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("files %v, %v", files, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range src.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			if !allowed[path] {
				t.Errorf("%s imports %s", f, path)
			}
		}
		for _, cg := range src.Comments {
			for _, c := range cg.List {
				if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
					t.Errorf("%s has a build line", f)
				}
			}
		}
	}
	if _, err := os.Stat("verdict.go"); err != nil {
		t.Error("verdict.go is missing")
	}
}

func TestNoExportedMethodBeyondTheInterface(t *testing.T) {
	want := []string{"Activity", "Decide", "Decider", "Limit", "List", "Looked", "Open", "Page", "SetLimit"}
	typ := reflect.TypeOf((*Backend)(nil))
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("methods %v, want %v", got, want)
	}
}

// The verdict hook holds the two decided samples: one the runs agreed with
// the owner on and one they did not, both already approved.
func TestVerdictSamplesAreTheTwoDecidedOnes(t *testing.T) {
	got := verdictSamples(newBackendTime())
	if len(got) != 2 {
		t.Fatalf("verdictSamples holds %d samples, want 2", len(got))
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.pending.ID)
		if s.state != record.StateApproved || s.verdicts == nil {
			t.Errorf("%s: state %q, verdicts %v", s.pending.ID, s.state, s.verdicts)
		}
	}
	if !slices.Equal(ids, []string{"ap-sample-verdict-agreed", "ap-sample-verdict-differed"}) {
		t.Errorf("ids %v", ids)
	}
	if !got[0].verdicts.Runs[0].Approve || !got[0].verdicts.Runs[1].Approve {
		t.Error("the first sample's runs do not both approve")
	}
	if got[1].verdicts.Runs[0].Approve == got[1].verdicts.Runs[1].Approve {
		t.Error("the second sample's runs agree with each other")
	}
}
