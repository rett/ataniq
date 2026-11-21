package leadwire

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStandardLibraryOnly checks that no non-test file of the package imports
// anything outside the standard library (this repository's packages and
// modules with a dotted first path element are not), and that no file carries
// a build constraint, so the package is in every build.
func TestStandardLibraryOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//go:build") || strings.HasPrefix(l, "// +build") {
				t.Errorf("%s carries a build constraint: %s", name, l)
			}
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(p, "/")
			if p == "C" || p == "unsafe" || strings.Contains(first, ".") || first == "ataniq" {
				t.Errorf("%s imports %q, which is not allowed here", name, p)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source file was checked")
	}
}
