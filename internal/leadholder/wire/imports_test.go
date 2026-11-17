package wire_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// importDenied reports whether a non-test file of the package may not import
// p. The package is pure: it works on readers and writers, so it has no file,
// process, network or system-call access, no cgo and no unsafe, and it
// imports nothing that is not in the standard library (which includes this
// repository's other packages and golang.org/x). Only direct imports are
// checked; test files may import what they need.
func importDenied(p string) bool {
	switch p {
	case "C", "os", "net", "syscall", "unsafe", "plugin", "io/ioutil", "path/filepath", "log", "log/slog", "time":
		return true
	}
	if strings.HasPrefix(p, "os/") || strings.HasPrefix(p, "net/") {
		return true
	}
	// A standard-library path has no dot in its first element; the module of
	// this repository is named ataniq.
	first, _, _ := strings.Cut(p, "/")
	return strings.Contains(first, ".") || first == "ataniq"
}

func TestImportGuardRules(t *testing.T) {
	for _, c := range []struct {
		imp    string
		denied bool
	}{
		{"os", true},
		{"os/exec", true},
		{"net", true},
		{"net/http", true},
		{"syscall", true},
		{"unsafe", true},
		{"C", true},
		{"plugin", true},
		{"io/ioutil", true},
		{"path/filepath", true},
		{"time", true},
		{"golang.org/x/sys/unix", true},
		{"github.com/charmbracelet/x/vt", true},
		{"ataniq/internal/journal", true},
		{"ataniq", true},
		{"io", false},
		{"sync", false},
		{"bytes", false},
		{"encoding/json", false},
		{"encoding/binary", false},
		{"crypto/rand", false},
		{"crypto/sha256", false},
		{"unicode/utf8", false},
		{"network", false},
	} {
		if got := importDenied(c.imp); got != c.denied {
			t.Errorf("importing %q: denied %v, want %v", c.imp, got, c.denied)
		}
	}
}

// TestImportGuard parses the imports of every non-test Go file under the
// package directory, so a package added below it later is checked too.
func TestImportGuard(t *testing.T) {
	files := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if importDenied(p) {
				t.Errorf("%s imports %s", filepath.ToSlash(path), p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}
