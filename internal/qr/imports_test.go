package qr

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
// p. The package is pure: it starts no program, opens no file and no socket
// and makes no system call. It imports the standard library only, and of that
// nothing that reaches the system, and nothing of this repository or any
// module. The test files may import more (os/exec runs the decoder).
func importDenied(p string) bool {
	switch p {
	case "C", "os", "net", "syscall", "unsafe", "plugin", "io/ioutil", "io/fs", "path/filepath", "runtime/debug":
		return true
	}
	if p == "ataniq" || strings.HasPrefix(p, "ataniq/") {
		return true
	}
	if first, _, _ := strings.Cut(p, "/"); strings.Contains(first, ".") {
		return true // a module, not the standard library
	}
	return strings.HasPrefix(p, "os/") ||
		strings.HasPrefix(p, "net/")
}

func TestImportGuardRules(t *testing.T) {
	for _, c := range []struct {
		imp    string
		denied bool
	}{
		{"os", true},
		{"os/exec", true},
		{"net/http", true},
		{"syscall", true},
		{"unsafe", true},
		{"ataniq", true},
		{"ataniq/internal/leadwire", true},
		{"io/fs", true},
		{"path/filepath", true},
		{"strings", false},
		{"bytes", false},
		{"errors", false},
		{"fmt", false},
		{"image", false},
		{"image/color", false},
		{"image/png", false},
		{"charm.land/bubbletea/v2", true},
		{"github.com/charmbracelet/x/ansi", true},
	} {
		if got := importDenied(c.imp); got != c.denied {
			t.Errorf("importing %q: denied %v, want %v", c.imp, got, c.denied)
		}
	}
}

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
