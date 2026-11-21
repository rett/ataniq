package leadctl

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// importDenied reports whether a non-test file of this package may not
// import p. The package starts no program, opens no file and no socket, makes
// no system call and uses neither cgo nor unsafe. It imports nothing of this
// repository: the backend's types are its own, and a command adapts the
// daemon's client to them. Only direct imports are checked; test files may
// import what they need.
func importDenied(p string) bool {
	switch p {
	case "C", "os", "net", "syscall", "unsafe", "plugin", "io/ioutil", "path/filepath":
		return true
	}
	return p == "ataniq" || strings.HasPrefix(p, "ataniq/") ||
		strings.HasPrefix(p, "os/") ||
		strings.HasPrefix(p, "net/") ||
		strings.HasPrefix(p, "golang.org/x/sys/")
}

func TestImportGuardRules(t *testing.T) {
	for _, c := range []struct {
		imp    string
		denied bool
	}{
		{"os", true}, {"os/exec", true}, {"os/signal", true}, {"net", true}, {"net/http", true}, {"syscall", true},
		{"unsafe", true}, {"C", true}, {"plugin", true}, {"io/ioutil", true}, {"path/filepath", true},
		{"golang.org/x/sys/unix", true},
		{"ataniq", true}, {"ataniq/internal/terminalconsole", true}, {"ataniq/internal/consoleclient", true},
		{"ataniq/internal/leadwire", true}, {"ataniq/internal/approval/card", true},
		{"network", false}, {"errors", false}, {"strings", false}, {"time", false}, {"io", false},
		{"crypto/rand", false}, {"encoding/hex", false}, {"image/color", false},
		{"charm.land/bubbletea/v2", false}, {"github.com/charmbracelet/ultraviolet", false},
		{"github.com/charmbracelet/x/ansi", false},
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

// TestNoErrorTextIsDrawn holds that no non-test file of the package turns an
// error into text: the view's words for a failed call are fixed.
func TestNoErrorTextIsDrawn(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if text := string(src); strings.Contains(text, ".Error()") || strings.Contains(text, "%v") || strings.Contains(text, "Sprint") {
			t.Errorf("%s turns a value into text", name)
		}
	}
}
