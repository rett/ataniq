package sessionreplay_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The guard walks every directory under the package and checks the imports of
// its non-test files, so a package added later is checked without a change
// here. The format, the player, the generator and any other package are pure:
// no processes, network, system calls, cgo, files, unsafe, terminal emulator
// or pane. Only the generator's command may use os, to write files. A test
// file may use os.
const filesCommand = "cmd/genfixtures"

// importDenied reports whether a non-test file in dir (slash separated, "."
// for the package itself) may not import p.
func importDenied(dir, p string) bool {
	if deniedPure(p) {
		return true
	}
	return p == "os" && dir != filesCommand
}

func deniedPure(p string) bool {
	switch p {
	case "C", "os/exec", "net", "syscall", "unsafe":
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

func TestImportRules(t *testing.T) {
	for _, c := range []struct {
		dir, imp string
		denied   bool
	}{
		{".", "os", true},
		{"fixturegen", "os", true},
		{"newpkg", "os", true},
		{"cmd/other", "os", true},
		{filesCommand, "os", false},
		{filesCommand, "os/exec", true},
		{".", "C", true},
		{filesCommand, "C", true},
		{"newpkg", "unsafe", true},
		{"newpkg", "net/http", true},
		{"newpkg", "syscall", true},
		{"newpkg", "golang.org/x/sys/unix", true},
		{"newpkg", "charm.land/bubbletea/v2", true},
		{"newpkg", "ataniq/internal/terminalconsole/sessionpane", true},
		{".", "encoding/json", false},
		{".", "time", false},
	} {
		if got := importDenied(c.dir, c.imp); got != c.denied {
			t.Errorf("%s importing %q: denied %v, want %v", c.dir, c.imp, got, c.denied)
		}
	}
}

func TestImportGuard(t *testing.T) {
	dirs := map[string]int{}
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
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		dirs[dir]++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if importDenied(dir, p) {
				t.Errorf("%s imports %s", filepath.ToSlash(path), p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The walk must reach the packages that exist now.
	for _, want := range []string{".", "fixturegen", filesCommand} {
		if dirs[want] == 0 {
			t.Errorf("no Go files checked in %s", want)
		}
	}
}
