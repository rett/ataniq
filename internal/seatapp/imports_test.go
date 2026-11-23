package seatapp

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// TestStandardLibraryOnly holds the rule of the web stack: the app is the Go
// standard library only, so it imports no module and no other package of this
// repository (the daemon is reached through the Daemon port, which the wiring
// implements), and it uses no passkey library. The same rule holds for its
// test files, since the software authenticator is the standard library's too.
func TestStandardLibraryOnly(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
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
	if files < 5 {
		t.Errorf("only %d files read", files)
	}
}
