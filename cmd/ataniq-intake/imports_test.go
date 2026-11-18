package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// allowedInternal are the packages of the feature that the command may
// import. Process and system calls belong to them, not to the command.
var allowedInternal = map[string]bool{
	"ataniq/internal/mirror":                        true,
	"ataniq/internal/intake":                        true,
	"ataniq/internal/intake/verify":                 true,
	"ataniq/internal/intake/execconfig":             true,
	"ataniq/internal/exchange/boundedexec":          false, // run through the packages; see allowedIn
	"ataniq/internal/exchange/gitenv":               true,
	"ataniq/internal/exchange/leadtext":             true,
	"ataniq/internal/exchange/safefs":               true,
	"ataniq/internal/exchange/sandboxplan":          true,
	"ataniq/internal/exchange/sandboxplan/plantest": false, // for tests only
}

// allowedIn lets one file import a package that the rest of the command may
// not: main.go reads boundedexec.StopSignals, the list of signals on which a
// run is cancelled. It is the one call that the command makes into that
// package. The command itself starts no process, and the list is the only
// reason to import it.
var allowedIn = map[string]map[string]bool{
	"main.go": {"ataniq/internal/exchange/boundedexec": true},
}

// TestImportGuard keeps the command from starting processes or touching the
// system itself. It reads the direct imports of the command's non-test files.
// The command may import the standard library, except the network and the
// process and system packages, and the packages of the feature.
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
			denied := p == "net" || p == "os/exec" || p == "syscall" || p == "unsafe" || p == "C" ||
				strings.HasPrefix(p, "net/") || strings.HasPrefix(p, "golang.org/x/sys/") ||
				strings.Contains(strings.Split(p, "/")[0], ".") && !strings.HasPrefix(p, "ataniq/") ||
				(strings.HasPrefix(p, "ataniq/") && !allowedInternal[p] && !allowedIn[name][p])
			if denied {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}
