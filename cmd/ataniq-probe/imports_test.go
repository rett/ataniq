package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestImportGuard keeps the command small: it reads the direct imports of its
// non-test files and refuses a process package, any network package, cgo and
// every package of this module except the two the probe is written against
// (assert and profile). golang.org/x/sys/unix is the one module outside the
// standard library it may use.
func TestImportGuard(t *testing.T) {
	allowedOwn := map[string]bool{
		"ataniq/internal/leadsandbox/assert":  true,
		"ataniq/internal/leadsandbox/profile": true,
	}
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
			var denied bool
			switch {
			case p == "C" || p == "unsafe" || p == "os/exec" || p == "plugin":
				denied = true
			case p == "net" || strings.HasPrefix(p, "net/"):
				denied = true // includes net/http
			case strings.HasPrefix(p, "ataniq/"):
				denied = !allowedOwn[p]
			case strings.Contains(first, "."):
				denied = p != "golang.org/x/sys/unix"
			}
			if denied {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}
