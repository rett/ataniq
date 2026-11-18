package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath    = "ataniq"
	systemdmgrPkg = "ataniq/internal/leadunit/systemdmgr"
	consolePkg    = "ataniq/internal/terminalconsole"
)

// importsOf returns the imports of the non-test Go files of a folder,
// whatever their build constraints.
func importsOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
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
			out = append(out, p)
		}
	}
	return out
}

// TestImportGuard: the command and everything of this module it reaches
// import no console and nothing under qualification/; the command imports the
// systemd manager directly and so does nothing else it reaches except the
// manager's own packages.
func TestImportGuard(t *testing.T) {
	root := filepath.Join("..", "..")
	direct := importsOf(t, ".")
	sawSystemd := false
	for _, p := range direct {
		if p == systemdmgrPkg {
			sawSystemd = true
		}
	}
	if !sawSystemd {
		t.Error("the command does not import the systemd manager")
	}
	seen := map[string]string{}
	var walk func(from, dir string)
	walk = func(from, dir string) {
		for _, p := range importsOf(t, dir) {
			if p != modulePath && !strings.HasPrefix(p, modulePath+"/") {
				continue
			}
			if _, done := seen[p]; done {
				continue
			}
			seen[p] = from
			walk(p, filepath.Join(root, strings.TrimPrefix(p, modulePath+"/")))
		}
	}
	walk("cmd/ataniqd", ".")
	for p, from := range seen {
		switch {
		case p == consolePkg || strings.HasPrefix(p, consolePkg+"/"):
			t.Errorf("%s reaches the terminal console through %s", p, from)
		case p == modulePath+"/qualification" || strings.HasPrefix(p, modulePath+"/qualification/"):
			t.Errorf("%s reaches qualification code through %s", p, from)
		}
	}
	if from, ok := seen[systemdmgrPkg]; !ok || from != "cmd/ataniqd" {
		t.Errorf("the systemd manager is imported by %q, want the command", from)
	}
	if _, ok := seen["ataniq/internal/leaddaemon"]; !ok {
		t.Error("the command does not import the daemon")
	}
}
