//go:build linux && ataniq_canary

package leadchat

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedImports are the only packages of the module that this package
// imports: of the approval packages only the wire types of ownerwire, the
// secret shape of the brief, and the layout's name rule.
var allowedImports = map[string]bool{
	"ataniq/internal/approval/ownerwire":  true,
	"ataniq/internal/genesis/brief":       true,
	"ataniq/internal/leadsandbox/profile": true,
}

func TestImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no files: %v", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(src), "//go:build linux && ataniq_canary\n") {
			t.Errorf("%s does not start with the canary build line", name)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(p, "ataniq/") {
				continue
			}
			if !allowedImports[p] {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
}
