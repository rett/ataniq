package standin

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The package imports the standard library and package ownerwire only, and of
// the standard library nothing that opens a file or a socket, runs a program
// or reads the environment: the stand-in holds its samples in memory, and
// reaches no lead, no other port of the daemon and nothing outside the
// process. No file carries a build line. Its tests may read the package's own
// files, to make this check.
func TestImportsTheStandardLibraryAndOwnerwireOnly(t *testing.T) {
	const allowed = "ataniq/internal/approval/ownerwire"
	banned := map[string]bool{
		"net": true, "net/http": true, "os": true, "os/exec": true, "syscall": true, "unsafe": true,
		"plugin": true, "io/fs": true, "path/filepath": true, "io/ioutil": true, "os/signal": true,
		"log": true, "runtime/debug": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files, usesWire := 0, false
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
			case path == allowed:
				usesWire = true
			case first == "ataniq" || strings.Contains(first, "."):
				t.Errorf("%s imports %s, which is neither the standard library nor %s", name, path, allowed)
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
	if files < 3 || !usesWire {
		t.Fatalf("read %d files, ownerwire used %v: the test did not read the package", files, usesWire)
	}
}
