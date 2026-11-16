package sessionreplay_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Each directory lists the imports its non-test files must not have. The
// format, the player and the generator are pure: no processes, network,
// system calls, files, unsafe, terminal emulator or pane. Only the generator's
// command may use os, to write files. A test file may use os.
var denied = map[string]func(path string) bool{
	".":               func(p string) bool { return deniedPure(p) || p == "os" },
	"fixturegen":      func(p string) bool { return deniedPure(p) || p == "os" },
	"cmd/genfixtures": deniedPure,
}

func deniedPure(p string) bool {
	switch p {
	case "os/exec", "net", "syscall", "unsafe":
		return true
	}
	return strings.HasPrefix(p, "net/") ||
		strings.HasPrefix(p, "github.com/charmbracelet/") ||
		strings.HasPrefix(p, "charm.land/") ||
		strings.HasPrefix(p, "golang.org/x/") ||
		strings.Contains(p, "/sessionpane") ||
		strings.HasPrefix(p, "ataniq/internal/dispatch") ||
		strings.HasPrefix(p, "ataniq/internal/journal") ||
		strings.HasPrefix(p, "ataniq/internal/operatorcli")
}

func TestImportGuard(t *testing.T) {
	for dir, bad := range denied {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if bad(p) {
					t.Errorf("%s imports %s", filepath.Join(dir, name), p)
				}
			}
		}
		if checked == 0 {
			t.Errorf("no Go files in %s", dir)
		}
	}
}
