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
// repository it may import only the console and the attach client. The command
// itself still imports no net, no os/exec and no process or terminal-device
// library: the attach client is the one package that opens a socket, and it
// is the console's own live source. It reads the direct imports of the
// command's non-test files. Package net is matched exactly, not as a prefix.
// The command may read its arguments and exit, so it may import os.
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
				strings.HasPrefix(p, "net/") || strings.HasPrefix(p, "github.com/creack/pty") || strings.HasPrefix(p, "golang.org/x/sys/") ||
				(strings.HasPrefix(p, "ataniq/") && p != "ataniq/internal/terminalconsole" && p != "ataniq/internal/leadattach")
			if denied {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}
