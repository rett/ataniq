package ownerchannel

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The package imports, of this repository, only the packages the seams note
// lists for it, and nothing that opens a socket or runs a program. Its test
// files may also import the stand-in door.
func TestImportsOnlyWhatTheSeamsNoteAllows(t *testing.T) {
	allowed := map[string]bool{
		"ataniq/internal/approval/record":       true,
		"ataniq/internal/approval/card":         true,
		"ataniq/internal/approval/limits":       true,
		"ataniq/internal/approval/presence":     true,
		"ataniq/internal/approval/door":         true,
		"ataniq/internal/approval/ownerjournal": true,
		"ataniq/internal/approval/ownerwire":    true,
		"ataniq/internal/approval/seatpair":     true,
	}
	banned := map[string]bool{"net": true, "os/exec": true, "syscall": true, "os": true}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		files++
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		isTest := strings.HasSuffix(name, "_test.go")
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(path, "/")
			switch {
			case first == "ataniq":
				if !allowed[path] && !(isTest && path == "ataniq/internal/approval/door/doortest") {
					t.Errorf("%s imports %s", name, path)
				}
			case strings.Contains(first, "."):
				t.Errorf("%s imports %s, which is not the standard library", name, path)
			case !isTest && banned[path]:
				t.Errorf("%s imports %s", name, path)
			}
		}
		// No file carries a build line.
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "//go:"+"build") || strings.Contains(string(b), "// +"+"build") {
			t.Errorf("%s has a build line", name)
		}
	}
	if files < 10 {
		t.Fatalf("read %d files", files)
	}
}
