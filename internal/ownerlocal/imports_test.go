package ownerlocal

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestStandardLibraryOnly holds that the package, its tests included, imports
// no module and no other package of this repository.
func TestStandardLibraryOnly(t *testing.T) {
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			files++
			for _, imp := range f.Imports {
				path, _ := strconv.Unquote(imp.Path.Value)
				first, _, _ := strings.Cut(path, "/")
				if strings.Contains(first, ".") || first == "ataniq" || first == "C" {
					t.Errorf("%s imports %s, which is not the standard library", name, path)
				}
			}
		}
	}
	if files < 3 {
		t.Errorf("only %d files read", files)
	}
}
