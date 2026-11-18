//go:build linux && ataniq_canary

package injectbridge

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// goFiles returns the names of the package's Go files.
func goFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			names = append(names, e.Name())
		}
	}
	if len(names) < 5 {
		t.Fatalf("only %d Go files found: %v", len(names), names)
	}
	return names
}

// The package imports the injection library and the packages its interfaces
// name, the session, the wire, the pane and the standard library, and nothing
// else: not the control server, whose deliverer it is by its method set alone.
// Test files are not counted.
func TestPackageImportsOnlyWhatItAdapts(t *testing.T) {
	allowed := map[string]bool{
		"ataniq/internal/inject":                      true,
		"ataniq/internal/inject/osc7501":              true,
		"ataniq/internal/inject/screen":               true,
		"ataniq/internal/inject/idle":                 true,
		"ataniq/internal/leadholder/session":          true,
		"ataniq/internal/leadholder/wire":             true,
		"ataniq/internal/terminalconsole/sessionpane": true,
	}
	for _, name := range goFiles(t) {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			first, _, _ := strings.Cut(p, "/")
			if std := !strings.Contains(first, ".") && first != "ataniq"; std || allowed[p] {
				continue
			}
			t.Errorf("%s imports %s", name, p)
		}
	}
}

// Every Go file of the package, tests included, is compiled in canary mode
// only.
func TestEveryFileCarriesTheTag(t *testing.T) {
	for _, name := range goFiles(t) {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if first, _, _ := strings.Cut(string(data), "\n"); first != "//go:build linux && ataniq_canary" {
			t.Errorf("%s starts with %q", name, first)
		}
	}
}

// No Go file of the package names a runtime: the names are asked of the
// library's tested table, so this file names none either.
func TestNoFileNamesARuntime(t *testing.T) {
	var names []string
	for _, e := range testedPairs(t) {
		names = append(names, strings.ToLower(e.Runtime))
		// The app name of a runtime's reports starts with the runtime's name
		// or is a name of its own.
		if app, _, _ := strings.Cut(strings.ToLower(e.App), "-"); app != "" {
			names = append(names, app)
		}
	}
	for _, name := range goFiles(t) {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(data))
		for _, n := range names {
			if strings.Contains(text, n) {
				t.Errorf("%s names a runtime of the tested table", name)
			}
		}
	}
}
