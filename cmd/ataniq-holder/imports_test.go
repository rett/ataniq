package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestImportGuard keeps the command small: of this repository it may import
// only the packages under internal/leadholder, and never the fake agent (which
// runs a script and may start processes: only tests import it); of anything
// else only the standard library and golang.org/x/sys/unix. It reads the direct
// imports of the command's non-test files.
func TestImportGuard(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(p, "/")
			switch {
			case p == "C", p == "unsafe":
				t.Errorf("%s imports %s", name, p)
			case p == "ataniq/internal/leadholder/fakeagent" || strings.HasPrefix(p, "ataniq/internal/leadholder/fakeagent/"):
				t.Errorf("%s imports %s, which only test files may do", name, p)
			case strings.HasPrefix(p, "ataniq/internal/leadholder/"), p == "golang.org/x/sys/unix":
			case strings.HasPrefix(p, "ataniq/") || p == "ataniq":
				t.Errorf("%s imports %s: of this repository only internal/leadholder packages are allowed", name, p)
			case strings.Contains(first, "."):
				t.Errorf("%s imports %s: only the standard library and golang.org/x/sys/unix are allowed", name, p)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}
