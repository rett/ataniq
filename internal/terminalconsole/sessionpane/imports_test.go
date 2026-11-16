package sessionpane_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// importDenied reports whether a non-test file of the pane, or of any package
// under it, may not import p. The pane has no file, process or network
// access, no system calls, cgo or unsafe, and imports no other package of
// this repository: not the replay (the cockpit joins the two), not a client
// and nothing that changes state. Only direct imports are checked; test
// files may import what they need.
func importDenied(p string) bool {
	switch p {
	case "C", "os", "net", "syscall", "unsafe", "plugin", "io/ioutil", "path/filepath":
		return true
	}
	return strings.HasPrefix(p, "os/") ||
		strings.HasPrefix(p, "net/") ||
		strings.HasPrefix(p, "golang.org/x/sys/") ||
		p == "ataniq" || strings.HasPrefix(p, "ataniq/")
}

func TestImportGuardRules(t *testing.T) {
	for _, c := range []struct {
		imp    string
		denied bool
	}{
		{"os", true},
		{"os/exec", true},
		{"os/signal", true},
		{"net", true},
		{"net/http", true},
		{"net/netip", true},
		{"syscall", true},
		{"unsafe", true},
		{"C", true},
		{"plugin", true},
		{"io/ioutil", true},
		{"path/filepath", true},
		{"golang.org/x/sys/unix", true},
		{"ataniq/internal/terminalconsole/sessionreplay", true},
		{"ataniq/internal/operatorcli", true},
		{"ataniq/internal/dispatch", true},
		{"ataniq/internal/journal", true},
		{"ataniq/internal/client", true},
		{"network", false},
		{"io", false},
		{"sync", false},
		{"time", false},
		{"github.com/charmbracelet/x/vt", false},
		{"github.com/charmbracelet/ultraviolet", false},
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
