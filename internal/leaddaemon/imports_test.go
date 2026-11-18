package leaddaemon

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath    = "ataniq"
	systemdmgrPkg = "ataniq/internal/leadunit/systemdmgr"
	consolePkg    = "ataniq/internal/terminalconsole"
)

// importsOf returns the imports of the non-test Go files of the folder dir,
// whatever their build constraints.
func importsOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			seen[p] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// closure returns every package of the module that the package in dir imports,
// directly or not, mapped to the package that imports it first.
func closure(t *testing.T, root, dir string) map[string]string {
	t.Helper()
	via := map[string]string{}
	var walk func(from, dir string)
	walk = func(from, dir string) {
		for _, p := range importsOf(t, dir) {
			if p != modulePath && !strings.HasPrefix(p, modulePath+"/") {
				continue
			}
			if _, done := via[p]; done {
				continue
			}
			via[p] = from
			walk(p, filepath.Join(root, strings.TrimPrefix(p, modulePath+"/")))
		}
	}
	walk(".", dir)
	return via
}

// TestImportGuard: the daemon's assembly imports no console and nothing under
// qualification/, and it does not import the systemd manager: the command
// wires that in.
func TestImportGuard(t *testing.T) {
	root := filepath.Join("..", "..")
	via := closure(t, root, ".")
	if len(via) == 0 {
		t.Fatal("no imports found")
	}
	for p, from := range via {
		switch {
		case p == consolePkg || strings.HasPrefix(p, consolePkg+"/"):
			t.Errorf("%s reaches the terminal console through %s", p, from)
		case strings.HasPrefix(p, modulePath+"/qualification/") || p == modulePath+"/qualification":
			t.Errorf("%s reaches qualification code through %s", p, from)
		case p == systemdmgrPkg:
			t.Errorf("%s imports the systemd manager; only the command does", from)
		}
	}
}
