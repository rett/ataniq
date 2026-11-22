package main

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// allowedOwn lists, by name, the packages of this repository the command may
// import: the console, the attach client that is its live source, the
// read-only client of the daemon's view, the sample backend of --demo, and
// the approvals view's package, for the compile-time assertion that the
// sample backend satisfies the console's backend interface, and the leads
// view's package, for the adapter that gives it the daemon's lead client, and
// the owner-local settings reader (standard library only), for the origin of
// the pair-a-phone view.
var allowedOwn = map[string]bool{
	"ataniq/internal/terminalconsole":           true,
	"ataniq/internal/leadattach":                true,
	"ataniq/internal/consoleclient":             true,
	"ataniq/internal/approval/demo":             true,
	"ataniq/internal/terminalconsole/approvals": true,
	"ataniq/internal/terminalconsole/leadctl":   true,
	"ataniq/internal/ownerlocal":                true,
}

// importDenied reports whether a non-test file of the command may not import
// p: a process or network package, a terminal device library, a package of
// this repository that allowedOwn does not list. Package net is matched
// exactly, not as a prefix. The command may read its arguments and exit, so
// it may import os.
func importDenied(p string) bool {
	return p == "net" || p == "os/exec" || p == "syscall" || p == "unsafe" || p == "C" ||
		strings.HasPrefix(p, "net/") || strings.HasPrefix(p, "github.com/creack/pty") || strings.HasPrefix(p, "golang.org/x/sys/") ||
		((p == "ataniq" || strings.HasPrefix(p, "ataniq/")) && !allowedOwn[p])
}

func TestImportGuardRules(t *testing.T) {
	for _, c := range []struct {
		imp    string
		denied bool
	}{
		{"ataniq/internal/terminalconsole", false}, {"ataniq/internal/leadattach", false},
		{"ataniq/internal/consoleclient", false}, {"ataniq/internal/approval/demo", false},
		{"ataniq/internal/terminalconsole/approvals", false}, {"ataniq/internal/terminalconsole/leadctl", false},
		{"ataniq/internal/ownerlocal", false},
		// What the sample backend does not make available: the service and
		// what it uses, and the rest of the console's own packages.
		{"ataniq/internal/approval/door", true}, {"ataniq/internal/approval/door/doortest", true},
		{"ataniq/internal/approval/ownerchannel", true}, {"ataniq/internal/approval/ownerjournal", true},
		{"ataniq/internal/approval/mergebuild", true}, {"ataniq/internal/approval/ownerwire", true},
		{"ataniq/internal/approval/card", true}, {"ataniq/internal/approval/record", true},
		{"ataniq/internal/terminalconsole/sessionpane", true}, {"ataniq/internal/terminalconsole/pager", true},
		{"ataniq/internal/dispatch", true}, {"ataniq/internal/journal", true}, {"ataniq", true},
		{"net", true}, {"net/http", true}, {"os/exec", true}, {"syscall", true}, {"unsafe", true}, {"C", true},
		{"golang.org/x/sys/unix", true}, {"github.com/creack/pty", true},
		{"os", false}, {"network", false}, {"time", false}, {"charm.land/bubbletea/v2", false},
	} {
		if got := importDenied(c.imp); got != c.denied {
			t.Errorf("importing %q: denied %v, want %v", c.imp, got, c.denied)
		}
	}
}

// TestImportGuard keeps the command from importing a mutation package, a
// process or a network package, or a terminal device library. Of this
// repository it may import only the packages allowedOwn lists by name. The
// command itself still imports no net, no os/exec and no process or
// terminal-device library: the attach client and the daemon's read-only
// client are the packages that open a socket, each as a source of the
// console. It reads the direct imports of the command's non-test files.
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
			if importDenied(p) {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}
