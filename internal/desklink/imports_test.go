package desklink

import (
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDeskLinkImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("listing Go files: %v", err)
	}
	for _, name := range files {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "ataniq/internal/seatgate" {
				continue
			}
			pkg, err := build.Default.Import(path, "", build.FindOnly)
			if err != nil || !pkg.Goroot {
				t.Errorf("%s imports %q outside the standard library and seatgate", name, path)
			}
		}
	}
}
