package terminalconsole

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// importDenied reports whether a non-test file of the cockpit package may not
// import p. The cockpit shows a snapshot and replays a recording: it starts no
// process, opens no socket, makes no system call, and imports no client or
// mutation package of this repository (the operator CLI, the dispatcher, the
// journal and the shared client). Of this repository it may import only the
// pane and the replay, its own subpackages. Only direct imports are checked;
// test files may import what they need.
func importDenied(p string) bool {
	switch p {
	case "C", "os", "net", "syscall", "unsafe", "plugin", "io/ioutil", "os/exec", "os/signal", "path/filepath":
		return true
	}
	for _, prefix := range []string{
		"net/", "golang.org/x/sys/", "github.com/creack/pty", "github.com/aymanbagabas/go-pty",
		"github.com/charmbracelet/x/xpty", "github.com/charmbracelet/x/conpty", "github.com/UserExistsError/conpty",
		"modernc.org/sqlite",
	} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	if p == "ataniq" || strings.HasPrefix(p, "ataniq/") {
		return p != "ataniq/internal/terminalconsole/sessionpane" && p != "ataniq/internal/terminalconsole/sessionreplay"
	}
	return false
}

func TestImportGuardRules(t *testing.T) {
	for _, c := range []struct {
		imp    string
		denied bool
	}{
		{"os", true}, {"os/exec", true}, {"net", true}, {"net/http", true}, {"net/netip", true}, {"syscall", true},
		{"unsafe", true}, {"C", true}, {"golang.org/x/sys/unix", true}, {"github.com/creack/pty", true},
		{"ataniq/internal/operatorcli", true}, {"ataniq/internal/dispatch", true}, {"ataniq/internal/journal", true},
		{"ataniq/internal/client", true}, {"ataniq/internal/terminalconsole/sessionpane", false},
		{"ataniq/internal/terminalconsole/sessionreplay", false}, {"network", false}, {"netutil", false},
		{"time", false}, {"strings", false}, {"charm.land/bubbletea/v2", false}, {"github.com/charmbracelet/ultraviolet", false},
		{"github.com/charmbracelet/x/ansi", false},
	} {
		if got := importDenied(c.imp); got != c.denied {
			t.Errorf("importing %q: denied %v, want %v", c.imp, got, c.denied)
		}
	}
}

// TestImportGuard parses the imports of the package's own non-test Go files.
// The subpackages have guards of their own.
func TestImportGuard(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if importDenied(p) {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go files checked")
	}
}
