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

// allowedOwn lists, by name, the packages of this repository a non-test file
// of the cockpit package may import. Its own subpackages: the session pane and
// the replay, and the approvals pieces it places (the pager, the arrival
// strip, the approvals view and the limit's screen). And what those pieces
// speak in: the card and the record, the limit's rules, the presence tracker,
// and the text escaping of lead-written text. The sample backend and the
// daemon's client are not here: they reach the console as values, handed over
// by the command.
var allowedOwn = map[string]bool{
	"ataniq/internal/terminalconsole/sessionpane":   true,
	"ataniq/internal/terminalconsole/sessionreplay": true,
	"ataniq/internal/terminalconsole/pager":         true,
	"ataniq/internal/terminalconsole/arrival":       true,
	"ataniq/internal/terminalconsole/approvals":     true,
	"ataniq/internal/terminalconsole/limitview":     true,
	"ataniq/internal/approval/card":                 true,
	"ataniq/internal/approval/record":               true,
	"ataniq/internal/approval/presence":             true,
	"ataniq/internal/approval/limits":               true,
	"ataniq/internal/exchange/leadtext":             true,
}

// importDenied reports whether a non-test file of the cockpit package may not
// import p. The cockpit shows a snapshot and replays a recording: it starts no
// process, opens no socket, makes no system call, and imports no client or
// mutation package of this repository (the operator CLI, the dispatcher, the
// journal and the shared client), nor the door, the service, the journal of
// the owner channel, the merge builder, the sample backend or the daemon's
// client. Of this repository it may import only the packages allowedOwn
// lists by name. Only direct imports are checked; test files may import what
// they need.
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
		return !allowedOwn[p]
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
		{"ataniq/internal/terminalconsole/sessionreplay", false}, {"network", false},
		{"ataniq/internal/terminalconsole/pager", false}, {"ataniq/internal/terminalconsole/arrival", false},
		{"ataniq/internal/terminalconsole/approvals", false}, {"ataniq/internal/terminalconsole/limitview", false},
		{"ataniq/internal/approval/card", false}, {"ataniq/internal/approval/record", false},
		{"ataniq/internal/approval/presence", false}, {"ataniq/internal/approval/limits", false},
		{"ataniq/internal/exchange/leadtext", false},
		{"ataniq/internal/approval/demo", true}, {"ataniq/internal/consoleclient", true},
		{"ataniq/internal/approval/door", true}, {"ataniq/internal/approval/door/doortest", true},
		{"ataniq/internal/approval/ownerchannel", true}, {"ataniq/internal/approval/ownerjournal", true},
		{"ataniq/internal/approval/mergebuild", true}, {"ataniq/internal/approval/ownerwire", true},
		{"ataniq/internal/terminalconsole/sessionpane/extra", true}, {"ataniq/internal/approval", true},
		{"ataniq", true}, {"netutil", false},
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
