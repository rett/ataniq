package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestImportGuard keeps the command from importing a mutation package, a
// process or a network package, or a terminal device library. Of this
// repository it may import only the packages allowedOwn lists by name. The
// command itself still imports no net, no os/exec and no process or
// terminal-device library: the attach client and the daemon's read-only
// client are the packages that open a socket, each as a source of the
// console. It reads the direct imports of the
// command's non-test files. Package net is matched exactly, not as a prefix.
// The command may read its arguments and exit, so it may import os.
func TestImportGuard(t *testing.T) {
	// allowedOwn lists, by name, the packages of this repository the command
	// may import: the console, the attach client that is its live source,
	// and the read-only client of the daemon's view.
	allowedOwn := map[string]bool{
		"ataniq/internal/terminalconsole": true,
		"ataniq/internal/leadattach":      true,
		"ataniq/internal/consoleclient":   true,
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
			denied := p == "net" || p == "os/exec" || p == "syscall" || p == "unsafe" || p == "C" ||
				strings.HasPrefix(p, "net/") || strings.HasPrefix(p, "github.com/creack/pty") || strings.HasPrefix(p, "golang.org/x/sys/") ||
				((p == "ataniq" || strings.HasPrefix(p, "ataniq/")) && !allowedOwn[p])
			if denied {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}
