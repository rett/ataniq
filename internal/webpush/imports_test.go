package webpush

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The package is the standard library only, in its tests as well. It never
// starts a process, touches the file system in keyfile.go alone and the
// network in send.go alone. And the seat never imports it: only a separate
// notifier talks to a push service.

const thisPackage = "ataniq/internal/webpush"

// only maps an import to the non-test files that may hold it.
var only = map[string][]string{
	"os":       {"keyfile.go"},
	"syscall":  {"keyfile.go"},
	"net/http": {"send.go"},
}

// never lists imports no file of the package may hold.
var never = map[string]bool{
	"os/exec":     true,
	"os/user":     true,
	"os/signal":   true,
	"unsafe":      true,
	"plugin":      true,
	"net":         true,
	"net/rpc":     true,
	"runtime/cgo": true,
}

// fileImports returns the imports of one Go file.
func fileImports(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// goFiles lists the Go files of dir, test files included when tests is set.
func goFiles(t *testing.T, dir string, tests bool) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || (!tests && strings.HasSuffix(n, "_test.go")) {
			continue
		}
		out = append(out, n)
	}
	return out
}

func TestImportsStandardLibraryOnly(t *testing.T) {
	files := goFiles(t, ".", true)
	if len(files) < 5 {
		t.Fatalf("read only %d files", len(files))
	}
	for _, name := range files {
		isTest := strings.HasSuffix(name, "_test.go")
		for _, p := range fileImports(t, name) {
			first, _, _ := strings.Cut(p, "/")
			if strings.Contains(first, ".") || first == "ataniq" {
				t.Errorf("%s imports %s, which is not the standard library", name, p)
			}
			if never[p] {
				t.Errorf("%s imports %s, which this package never uses", name, p)
			}
			if allowed, ok := only[p]; ok && !isTest {
				found := false
				for _, a := range allowed {
					found = found || a == name
				}
				if !found {
					t.Errorf("%s imports %s, which only %v may", name, p, allowed)
				}
			}
		}
	}
}

// TestSeatNeverImports walks the module packages the seat's command reaches
// through its non-test files, build tags or not, and finds this package in
// none of them; nor does any file of the seat's own packages, tests included.
func TestSeatNeverImports(t *testing.T) {
	root := filepath.Join("..", "..")
	pkgDir := func(importPath string) string {
		return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(importPath, "ataniq/")))
	}
	seen := map[string]bool{}
	queue := []string{"ataniq/cmd/ataniq-seat"}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		for _, name := range goFiles(t, pkgDir(pkg), false) {
			for _, p := range fileImports(t, filepath.Join(pkgDir(pkg), name)) {
				if p == thisPackage {
					t.Errorf("%s/%s imports %s: the seat must not reach a push service", pkg, name, p)
				}
				if strings.HasPrefix(p, "ataniq/") {
					queue = append(queue, p)
				}
			}
		}
	}
	for _, must := range []string{"ataniq/internal/seatapp", "ataniq/internal/seatlink", "ataniq/internal/seatgate"} {
		if !seen[must] {
			t.Fatalf("the walk did not reach %s: it reads the wrong folders", must)
		}
	}
	for _, pkg := range []string{"ataniq/cmd/ataniq-seat", "ataniq/internal/seatapp", "ataniq/internal/seatlink"} {
		for _, name := range goFiles(t, pkgDir(pkg), true) {
			for _, p := range fileImports(t, filepath.Join(pkgDir(pkg), name)) {
				if p == thisPackage {
					t.Errorf("%s/%s imports %s", pkg, name, p)
				}
			}
		}
	}
}
