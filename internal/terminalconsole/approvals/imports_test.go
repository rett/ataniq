package approvals_test

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

// importDenied reports whether a non-test file of this package, or of any
// package under it, may not import p. The package starts no program, opens
// no file and no socket, makes no system call and uses neither cgo nor
// unsafe. Of this repository it may import card, record, limits, leadtext and
// pager and nothing else: not the cockpit, not a backend, not the door. Only direct imports are checked;
// test files may import what they need.
func importDenied(p string) bool {
	switch p {
	case "C", "os", "net", "syscall", "unsafe", "plugin", "io/ioutil", "path/filepath":
		return true
	}
	if p == "ataniq" || strings.HasPrefix(p, "ataniq/") {
		switch p {
		case "ataniq/internal/approval/card", "ataniq/internal/approval/record",
			"ataniq/internal/approval/limits", "ataniq/internal/exchange/leadtext",
			"ataniq/internal/terminalconsole/pager":
			return false
		}
		return true
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
		{"ataniq", true},
		{"ataniq/internal/terminalconsole", true},
		{"ataniq/internal/terminalconsole/pager", false},
		{"ataniq/internal/terminalconsole/pager/other", true},
		{"ataniq/internal/terminalconsole/limitview", true},
		{"ataniq/internal/terminalconsole/arrival", true},
		{"ataniq/internal/terminalconsole/approvals", true},
		{"ataniq/internal/approval/card", false},
		{"ataniq/internal/approval/record", false},
		{"ataniq/internal/approval/limits", false},
		{"ataniq/internal/approval/card/other", true},
		{"ataniq/internal/approval/door", true},
		{"ataniq/internal/approval/door/doortest", true},
		{"ataniq/internal/approval/ownerchannel", true},
		{"ataniq/internal/approval/ownerjournal", true},
		{"ataniq/internal/approval/mergebuild", true},
		{"ataniq/internal/approval/demo", true},
		{"ataniq/internal/consoleclient", true},
		{"ataniq/internal/exchange/leadtext", false},
		{"ataniq/internal/exchange/leadtext/other", true},
		{"ataniq/internal/exchange/gitenv", true},
		{"ataniq/internal/approval/presence", true},
		{"network", false},
		{"errors", false},
		{"strings", false},
		{"time", false},
		{"image/color", false},
		{"charm.land/bubbletea/v2", false},
		{"github.com/charmbracelet/ultraviolet", false},
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

// TestLeadTextOnlyThroughTheCard holds that the package draws lead text only
// from card.LeadText, which escapes it: the package's own files use nothing
// of leadtext but its label.
func TestLeadTextOnlyThroughTheCard(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			if i := strings.Index(line, "leadtext."); i >= 0 && !strings.HasPrefix(line[i:], "leadtext.Label") && !strings.HasPrefix(strings.TrimSpace(line), "//") {
				t.Errorf("%s uses %s", name, strings.TrimSpace(line))
			}
		}
	}
}
