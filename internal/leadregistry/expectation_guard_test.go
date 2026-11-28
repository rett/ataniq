package leadregistry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const registryImportPath = "ataniq/internal/leadregistry"

// unstatedExpectations returns the position and reason of every StateChange
// literal in f that does not state its expectation of the shown value: an
// unkeyed literal, a literal without the Expected key, and one whose Expected
// is the zero Expectation{} written out, which names nothing. A caller states
// ExpectShown(entry.Shown), or NoExpectation() with a comment saying why none
// applies. Test files are not checked: the zero value stays unchecked there.
func unstatedExpectations(fset *token.FileSet, f *ast.File) []string {
	local := f.Name.Name == "leadregistry"
	alias := ""
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != registryImportPath {
			continue
		}
		alias = "leadregistry"
		if imp.Name != nil {
			alias = imp.Name.Name
		}
	}
	isType := func(e ast.Expr, name string) bool {
		switch t := e.(type) {
		case *ast.Ident:
			return local && t.Name == name
		case *ast.SelectorExpr:
			x, ok := t.X.(*ast.Ident)
			return ok && alias != "" && x.Name == alias && t.Sel.Name == name
		}
		return false
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isType(lit.Type, "StateChange") {
			return true
		}
		var expected ast.Expr
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				out = append(out, fset.Position(lit.Pos()).String()+": unkeyed StateChange literal")
				return true
			}
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Expected" {
				expected = kv.Value
			}
		}
		switch v := expected.(type) {
		case nil:
			out = append(out, fset.Position(lit.Pos()).String()+": StateChange literal without Expected")
		case *ast.CompositeLit:
			if isType(v.Type, "Expectation") {
				out = append(out, fset.Position(v.Pos()).String()+": Expected is the zero Expectation{}; name ExpectShown or NoExpectation")
			}
		}
		return true
	})
	return out
}

func parseSnippet(t *testing.T, name, src string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fset, f
}

func TestStateChangeGuardFindsUnstatedExpectations(t *testing.T) {
	const caller = `package x

import lr "ataniq/internal/leadregistry"

var (
	a = lr.StateChange{Epoch: 1}
	b = lr.StateChange{Epoch: 1, Expected: lr.NoExpectation()}
	c = lr.StateChange{Epoch: 1, Expected: lr.ExpectShown(lr.Shown{})}
	d = lr.StateChange{1, lr.Expectation{}, "ready", "", lr.Reason{}, ""}
	e = lr.StateChange{Epoch: 1, Expected: lr.Expectation{}}
	f = other.StateChange{Epoch: 1}
)
`
	fset, f := parseSnippet(t, "caller.go", caller)
	got := unstatedExpectations(fset, f)
	if len(got) != 3 {
		t.Fatalf("flagged %d literals, want 3 (a, d, e): %q", len(got), got)
	}
	for i, want := range []string{"without Expected", "unkeyed", "zero Expectation{}"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("finding %d = %q, want it to say %q", i, got[i], want)
		}
	}
	const inPackage = `package leadregistry

var a = StateChange{Epoch: 1}
var b = StateChange{Epoch: 1, Expected: NoExpectation()}
`
	fset, f = parseSnippet(t, "inpackage.go", inPackage)
	if got := unstatedExpectations(fset, f); len(got) != 1 {
		t.Fatalf("in-package literals flagged %d, want 1: %q", len(got), got)
	}
}

// TestProductionStateChangesStateTheirExpectation walks the module's
// non-test Go files, whatever their build constraints, and requires every
// StateChange literal to say what the writer observed of the shown value, or
// that it observed nothing the registry could have changed. A new caller that
// leaves the field out would silently keep the unchecked behaviour.
func TestProductionStateChangesStateTheirExpectation(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		up := filepath.Dir(root)
		if up == root {
			t.Skip("no go.mod above the package")
		}
		root = up
	}
	var checked, literals int
	var bad []string
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "third_party", "build", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok {
				switch ty := lit.Type.(type) {
				case *ast.SelectorExpr:
					if ty.Sel.Name == "StateChange" {
						literals++
					}
				case *ast.Ident:
					if ty.Name == "StateChange" {
						literals++
					}
				}
			}
			return true
		})
		bad = append(bad, unstatedExpectations(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 || literals == 0 {
		t.Fatalf("the walk saw %d files and %d StateChange literals; it is not looking at the tree", checked, literals)
	}
	for _, b := range bad {
		rel := strings.TrimPrefix(b, root+string(filepath.Separator))
		t.Errorf("%s", rel)
	}
}
