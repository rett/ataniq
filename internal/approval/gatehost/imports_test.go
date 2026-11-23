package gatehost

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The package imports, of this repository, only the owner channel's service,
// its words and its refusals, and the gate's transport; of the standard
// library, nothing that opens a socket or a file or runs a program. No file
// carries a build line: the package is untagged, and only tagged code imports
// it. Its tests may also use the network (the gate on the loopback address),
// and the console's client for its words.
func TestImportsAndNoBuildLine(t *testing.T) {
	allowed := map[string]bool{
		"ataniq/internal/approval/ownerchannel": true,
		"ataniq/internal/approval/ownerwire":    true,
		"ataniq/internal/approval/record":       true,
		"ataniq/internal/seatgate":              true,
	}
	testAllowed := map[string]bool{
		"ataniq/internal/consoleclient": true,
	}
	banned := map[string]bool{
		"net": true, "net/http": true, "os": true, "os/exec": true, "syscall": true,
		"unsafe": true, "plugin": true, "io/fs": true, "path/filepath": true,
	}
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
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly|parser.ParseComments)
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
				if !allowed[path] && !(isTest && testAllowed[path]) {
					t.Errorf("%s imports %s", name, path)
				}
			case strings.Contains(first, "."):
				t.Errorf("%s imports %s, which is not the standard library", name, path)
			case !isTest && banned[path]:
				t.Errorf("%s imports %s", name, path)
			}
		}
		for _, cg := range f.Comments {
			if cg.Pos() >= f.Package {
				break
			}
			for _, c := range cg.List {
				if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
					t.Errorf("%s has a build line", name)
				}
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go file read")
	}
}
