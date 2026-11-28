package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Check the built Linux command's whole import graph, including dependencies
// introduced indirectly by a shared checkpoint package.
func forbiddenDependencies(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOFLAGS=", "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	var forbidden []string
	for path := range strings.FieldsSeq(string(out)) {
		for _, p := range []string{"os/exec", "plugin", "ataniq/internal/leadregistry", "ataniq/internal/leaddaemon", "ataniq/internal/leadunit"} {
			if path == p || strings.HasPrefix(path, p+"/") {
				forbidden = append(forbidden, path)
			}
		}
	}
	return forbidden
}

func TestImports(t *testing.T) {
	if paths := forbiddenDependencies(t, "."); len(paths) != 0 {
		t.Errorf("home imports forbidden packages: %v", paths)
	}
}

func TestImportsDetectTransitiveLauncher(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "helper"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{
		"go.mod":           "module example.test/home\n",
		"home.go":          "package home\nimport _ \"example.test/home/helper\"\n",
		"helper/helper.go": "package helper\nimport _ \"os/exec\"\nimport _ \"plugin\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paths := forbiddenDependencies(t, dir)
	for _, want := range []string{"os/exec", "plugin"} {
		if !slices.Contains(paths, want) {
			t.Errorf("missed transitive import %s in %v", want, paths)
		}
	}
}

// Direct process starts live in packages also needed for ordinary file I/O.
// Resolve the imported name in each file, including aliases and dot imports.
func directStarts(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string][]string{
		"os":                    {"StartProcess"},
		"syscall":               {"ForkExec", "Exec"},
		"golang.org/x/sys/unix": {"Exec", "ForkExec"},
	}
	names := make(map[string]string)
	for _, imp := range file.Imports {
		pkg, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		alias := filepath.Base(pkg)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		for _, fn := range forbidden[pkg] {
			name := fn
			if alias != "." {
				name = alias + "." + fn
			}
			names[name] = pkg + "." + fn
		}
	}
	var starts []string
	for node := range ast.Preorder(file) {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			continue
		}
		var name string
		switch fn := ast.Unparen(call.Fun).(type) {
		case *ast.Ident:
			if fn.Obj == nil {
				name = fn.Name
			}
		case *ast.SelectorExpr:
			if qualifier, ok := ast.Unparen(fn.X).(*ast.Ident); ok && qualifier.Obj == nil {
				name = qualifier.Name + "." + fn.Sel.Name
			}
		}
		if target := names[name]; target != "" {
			starts = append(starts, target)
		}
	}
	return starts
}

func TestNoDirectProcessStarts(t *testing.T) {
	for _, root := range []string{".", "../../internal/enrollment"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".go") {
				if calls := directStarts(t, path); len(calls) != 0 {
					t.Errorf("%s calls %v", path, calls)
				}
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDirectStartsDetectsImportedCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "starts.go")
	const source = `package seeded
import (
	. "os"
	s "syscall"
	"golang.org/x/sys/unix"
)
func starts() {
	StartProcess()
	s.ForkExec()
	(s.Exec)()
	unix.Exec()
	unix.ForkExec()
	s := struct{ Exec func() }{}
	s.Exec()
}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	want := []string{"os.StartProcess", "syscall.ForkExec", "syscall.Exec", "golang.org/x/sys/unix.Exec", "golang.org/x/sys/unix.ForkExec"}
	if got := directStarts(t, path); !slices.Equal(got, want) {
		t.Fatalf("detected %v, want %v", got, want)
	}
}
