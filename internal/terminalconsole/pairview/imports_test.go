package pairview

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
// p. It starts no program, opens no file and no socket and makes no system
// call. Of this repository it may import the QR encoder and nothing else.
func importDenied(p string) bool {
	switch p {
	case "C", "os", "net", "syscall", "unsafe", "plugin", "io/ioutil", "path/filepath":
		return true
	}
	if p == "ataniq" || strings.HasPrefix(p, "ataniq/") {
		return p != "ataniq/internal/qr"
	}
	return strings.HasPrefix(p, "os/") ||
		strings.HasPrefix(p, "net/") ||
		strings.HasPrefix(p, "golang.org/x/sys/")
}

func TestImportGuardRules(t *testing.T) {
	for _, c := range []struct {
		imp    string
		denied bool
	}{
		{"os", true},
		{"net/url", true},
		{"context", false},
		{"image/color", false},
		{"os/exec", true},
		{"net/http", true},
		{"syscall", true},
		{"unsafe", true},
		{"ataniq", true},
		{"ataniq/internal/terminalconsole", true},
		{"ataniq/internal/terminalconsole/pager", true},
		{"ataniq/internal/approval/card", true},
		{"ataniq/internal/approval/record", true},
		{"ataniq/internal/qr/other", true},
		{"ataniq/internal/approval/limits", true},
		{"ataniq/internal/exchange/leadtext", true},
		{"ataniq/internal/qr", false},
		{"strings", false},
		{"charm.land/bubbletea/v2", false},
		{"github.com/charmbracelet/x/ansi", false},
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
